package applications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// The public form is the one page in this plugin a stranger can POST to with no
// account and no invite, which makes its two helpers and its one handler worth
// pinning. Until now all three were verified by having submitted the form once.

func TestLooksLikeEmail(t *testing.T) {
	for _, in := range []string{
		"a@b.co",
		"someone@example.com",
		"first.last+tag@mail.example.co.uk",
		"UPPER@EXAMPLE.COM",
		"i@x.yz",
		// Deliberately accepted. This is a SHAPE check and every stricter rule
		// rejects somebody's real address; the mail server is the only
		// authority on whether one exists.
		"weird!#$%'*/?^_{|}~@example.com",
		"quoted\"thing@example.com",
	} {
		if !looksLikeEmail(in) {
			t.Errorf("looksLikeEmail(%q) = false, want true", in)
		}
	}

	for _, tc := range []struct{ in, why string }{
		{"", "empty"},
		{"nobody", "no @ at all"},
		{"@example.com", "nothing before the @"},
		{"someone@", "nothing after the @"},
		{"someone@example", "a domain with no dot cannot be delivered to"},
		{"someone@.com", "the dot leads the domain"},
		{"someone@example.", "the dot ends the domain"},
		{"a@b@c.com", "two @ signs"},
		{"some one@example.com", "a space, which is how a header injection starts"},
		{"someone@exam\tple.com", "a tab, likewise"},
	} {
		if looksLikeEmail(tc.in) {
			t.Errorf("looksLikeEmail(%q) = true, want false — %s", tc.in, tc.why)
		}
	}
}

// TestHashIPIsStableAndDistinct. The column exists so staff can see that six
// applications arrived from one place. It must therefore be stable for one
// address and distinct between two — and it must not be the address, because a
// moderator queue is not a place to keep one.
func TestHashIPIsStableAndDistinct(t *testing.T) {
	const ip = "203.0.113.9"
	first := hashIP(ip)
	if first != hashIP(ip) {
		t.Fatal("the same address hashed to two different values")
	}
	if first == hashIP("203.0.113.10") {
		t.Error("two addresses collided — the column cannot answer its one question")
	}
	if strings.Contains(first, ip) || len(first) != 16 {
		t.Errorf("hashIP(%q) = %q, want 16 hex characters that are not the address", ip, first)
	}
	if strings.TrimLeft(first, "0123456789abcdef") != "" {
		t.Errorf("hashIP produced %q, which is not hex", first)
	}
	// An unknown source hashes to nothing rather than to the hash of "" — a
	// stored value would make every unknown source look like the same one.
	if got := hashIP(""); got != "" {
		t.Errorf("hashIP(\"\") = %q, want empty", got)
	}
	// IPv6 goes through the same path; ClientIP hands over either family.
	if hashIP("2001:db8::1") == hashIP("2001:db8::2") {
		t.Error("two IPv6 addresses collided")
	}
}

// ── the handler ─────────────────────────────────────────────────────

type fakeStore struct {
	Store // never called; the four unused methods would panic loudly if they were

	pending map[string]bool
	created []Application
	err     error
}

func (f *fakeStore) PendingFor(_ context.Context, email string) (bool, error) {
	return f.pending[email], f.err
}

func (f *fakeStore) Create(_ context.Context, a Application) error {
	if f.err != nil {
		return f.err
	}
	f.created = append(f.created, a)
	return nil
}

// post drives submit exactly as the router does, and returns where it went.
func post(t *testing.T, st Store, ip string, form map[string]string) (string, int) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	values := url.Values{}
	for k, v := range form {
		values.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/p/apply/submit", strings.NewReader(values.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Request.RemoteAddr = ip + ":40000"

	p := &Plugin{st: st}
	if _, err := p.submit(c); err != nil {
		t.Fatalf("submit returned an error to the router: %v", err)
	}
	// gin buffers the status and flushes it when the engine finishes the
	// handler chain. A 303 to a POST writes no body, so nothing else triggers
	// the flush and the recorder would report 200 — this does what the engine
	// does, rather than the test asserting on a status the router never sent.
	c.Writer.WriteHeaderNow()
	return rec.Header().Get("Location"), rec.Code
}

// TestSubmitTellsAStrangerNothingAboutWhoAlreadyBelongs is the property this
// endpoint exists to preserve, and the reason it takes no credentials.
//
// Two addresses — one belonging to a member of the site, one to nobody — must
// produce the same answer. They do here structurally rather than by a check:
// the Store interface has no method that can answer "does this address have an
// account", so submit has nothing to leak. This pins the observable half, so a
// future revision that adds a friendly "you already have an account, sign in
// instead" fails here rather than in the wild.
func TestSubmitTellsAStrangerNothingAboutWhoAlreadyBelongs(t *testing.T) {
	st := &fakeStore{pending: map[string]bool{}}

	for _, email := range []string{"a-member@example.com", "a-total-stranger@example.com"} {
		loc, code := post(t, st, "203.0.113.9", map[string]string{
			"email": email, "body": "I have been reading for years.",
		})
		if loc != "/p/apply?sent=1" || code != http.StatusSeeOther {
			t.Errorf("%s: %d to %q — the two addresses must be indistinguishable", email, code, loc)
		}
	}
}

// TestSubmitRefusesADuplicateApplication. The single case where the answer
// differs is a duplicate, which is not an oracle: it reveals that an
// application exists to the person who made it.
func TestSubmitRefusesADuplicateApplication(t *testing.T) {
	st := &fakeStore{pending: map[string]bool{"twice@example.com": true}}
	loc, _ := post(t, st, "203.0.113.9", map[string]string{
		"email": "twice@example.com", "body": "asking again",
	})
	if loc != "/p/apply?err=already" {
		t.Errorf("redirected to %q, want the already-applied message", loc)
	}
	if len(st.created) != 0 {
		t.Error("stored a second application for an address already waiting")
	}
}

// TestSubmitStoresAHashNotAnAddress. The comment above Create says the raw
// address never reaches the table; this is what holds that true.
func TestSubmitStoresAHashNotAnAddress(t *testing.T) {
	st := &fakeStore{pending: map[string]bool{}}
	const ip = "198.51.100.7"
	post(t, st, ip, map[string]string{"email": "new@example.com", "body": "hello"})

	if len(st.created) != 1 {
		t.Fatalf("stored %d applications, want 1", len(st.created))
	}
	if got := st.created[0].IPHash; got == ip || strings.Contains(got, ip) {
		t.Errorf("IPHash = %q — that is the address", got)
	} else if got != hashIP(ip) {
		t.Errorf("IPHash = %q, want %q", got, hashIP(ip))
	}

	// The address is lowercased and trimmed on the way in, so PendingFor's
	// comparison and the unique index agree with each other.
	post(t, st, ip, map[string]string{"email": "  MiXeD@Example.COM  ", "body": "hello"})
	if len(st.created) != 2 {
		t.Fatal("the second application was not stored")
	}
	if e := st.created[1].Email; e != "mixed@example.com" {
		t.Errorf("stored %q, want it folded and trimmed", e)
	}
}

// TestSubmitBoundsWhatAStrangerCanStore. Every one of these is a row an
// unauthenticated POST would otherwise put in the database.
func TestSubmitBoundsWhatAStrangerCanStore(t *testing.T) {
	for _, tc := range []struct {
		name string
		form map[string]string
		want string
	}{
		{"no email", map[string]string{"body": "x"}, "/p/apply?err=bad-email"},
		{"unusable email", map[string]string{"email": "nobody", "body": "x"}, "/p/apply?err=bad-email"},
		{"no body", map[string]string{"email": "a@b.co"}, "/p/apply?err=no-body"},
		{"blank body", map[string]string{"email": "a@b.co", "body": "   "}, "/p/apply?err=no-body"},
		{"body over the cap", map[string]string{
			"email": "a@b.co", "body": strings.Repeat("x", bodyMax+1),
		}, "/p/apply?err=too-long"},
		{"username over the cap", map[string]string{
			"email": "a@b.co", "body": "x", "username": strings.Repeat("n", 65),
		}, "/p/apply?err=too-long"},
		{"email over the cap", map[string]string{
			"email": strings.Repeat("a", 250) + "@example.com", "body": "x",
		}, "/p/apply?err=too-long"},
	} {
		st := &fakeStore{pending: map[string]bool{}}
		loc, code := post(t, st, "203.0.113.9", tc.form)
		if loc != tc.want {
			t.Errorf("%s: redirected to %q, want %q", tc.name, loc, tc.want)
		}
		if code != http.StatusSeeOther {
			t.Errorf("%s: status %d, want 303", tc.name, code)
		}
		if len(st.created) != 0 {
			t.Errorf("%s: stored the row anyway", tc.name)
		}
	}

	// The cap is a boundary, so check the far side of it: exactly bodyMax is
	// accepted, and it is stored whole rather than truncated mid-sentence.
	st := &fakeStore{pending: map[string]bool{}}
	full := strings.Repeat("x", bodyMax)
	if loc, _ := post(t, st, "203.0.113.9", map[string]string{"email": "a@b.co", "body": full}); loc != "/p/apply?sent=1" {
		t.Errorf("a body of exactly bodyMax was refused: %q", loc)
	}
	if len(st.created) != 1 || st.created[0].Body != full {
		t.Error("the body was not stored whole")
	}
}

// TestSubmitSurvivesAStoreFailure — a failure must not read as a decision about
// the applicant, and must not vary by address either.
func TestSubmitSurvivesAStoreFailure(t *testing.T) {
	st := &fakeStore{pending: map[string]bool{}, err: context.DeadlineExceeded}
	loc, code := post(t, st, "203.0.113.9", map[string]string{"email": "a@b.co", "body": "x"})
	if loc != "/p/apply?err=failed" || code != http.StatusSeeOther {
		t.Errorf("got %d to %q, want a 303 to the generic failure", code, loc)
	}
}
