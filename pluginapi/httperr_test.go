package pluginapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestTheLeakThisExistsToStop reproduces the actual failure: a real
// *http.Client, a host that cannot resolve, and the credential appearing in
// the error a plugin would report.
//
// Against a live client rather than a hand-built *url.Error, because the point
// is what net/http does — and "the transport embeds the URL" is the part that
// would otherwise be an assumption.
func TestTheLeakThisExistsToStop(t *testing.T) {
	const secret = "SECRET_KEY_abc123"
	c := &http.Client{Timeout: 2 * time.Second}
	target := "https://api.example.invalid/3/search/movie?api_key=" + secret + "&query=the+office"

	_, err := c.Get(target)
	if err == nil {
		t.Skip("the unresolvable host resolved; a DNS wildcard is in play")
	}

	// Unredacted, this is what would reach error_logs.
	if !strings.Contains(err.Error(), secret) {
		t.Fatalf("net/http no longer embeds the URL; this helper may be unnecessary now: %v", err)
	}

	safe := RedactURLError(err)
	if strings.Contains(safe.Error(), secret) {
		t.Errorf("the key survived redaction: %v", safe)
	}
	if !strings.Contains(safe.Error(), "REDACTED") {
		t.Errorf("nothing was marked redacted: %v", safe)
	}
	// The searchable half must survive, or nobody can debug from the log.
	if !strings.Contains(safe.Error(), "the+office") && !strings.Contains(safe.Error(), "the%2Boffice") {
		t.Errorf("the query text was lost, which is most of the diagnostic value: %v", safe)
	}
	// And wrapping it the way a plugin does must not reintroduce it.
	wrapped := fmt.Errorf("tmdb request: %w", safe)
	if strings.Contains(wrapped.Error(), secret) {
		t.Errorf("the key came back when wrapped: %v", wrapped)
	}
}

func TestRedactSecretsCoversTheUsualNames(t *testing.T) {
	for _, raw := range []string{
		"https://x.test/a?api_key=SECRET",
		"https://x.test/a?apikey=SECRET",
		"https://x.test/a?API_KEY=SECRET",
		"https://x.test/a?access_token=SECRET",
		"https://x.test/a?token=SECRET",
		"https://x.test/a?client=SECRET",
		"https://x.test/a?client_secret=SECRET",
		"https://x.test/a?password=SECRET",
		"https://x.test/a?auth=SECRET",
		"https://x.test/a?sig=SECRET",
		"https://x.test/a?sessionid=SECRET",
	} {
		if got := RedactSecrets(raw); strings.Contains(got, "SECRET") {
			t.Errorf("RedactSecrets(%q) = %q — the credential survived", raw, got)
		}
	}
}

// TestRedactSecretsKeepsWhatIsUseful. Over-redaction would make this helper
// something people route around.
func TestRedactSecretsKeepsWhatIsUseful(t *testing.T) {
	got := RedactSecrets("https://api.example.test/3/search/movie?api_key=SECRET&query=the+office&page=1")
	for _, want := range []string{"api.example.test", "/3/search/movie", "page=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is missing from %q", want, got)
		}
	}
	if !strings.Contains(got, "query=the") {
		t.Errorf("the search term was redacted: %q", got)
	}
}

func TestRedactSecretsLeavesACleanURLAlone(t *testing.T) {
	const clean = "https://api.example.test/v1/shows?q=hello&page=2"
	if got := RedactSecrets(clean); got != clean {
		t.Errorf("RedactSecrets rewrote a URL with nothing to hide:\n got %q\nwant %q", got, clean)
	}
}

// TestRedactSecretsCoversUserinfo — credentials in the authority are easy to
// forget precisely because they are not query parameters.
func TestRedactSecretsCoversUserinfo(t *testing.T) {
	got := RedactSecrets("https://alice:hunter2@api.example.test/v1/x")
	if strings.Contains(got, "hunter2") || strings.Contains(got, "alice") {
		t.Errorf("userinfo survived: %q", got)
	}
}

// TestRedactURLErrorKeepsTheCauseInspectable. Callers test the WRAPPED cause —
// a timeout, a cancelled context — and a redaction that broke errors.Is would
// change behaviour well away from where it was applied.
func TestRedactURLErrorKeepsTheCauseInspectable(t *testing.T) {
	orig := &url.Error{
		Op:  "Get",
		URL: "https://x.test/a?api_key=SECRET",
		Err: context.DeadlineExceeded,
	}
	safe := RedactURLError(orig)

	if !errors.Is(safe, context.DeadlineExceeded) {
		t.Error("errors.Is no longer finds the cause")
	}
	var ue *url.Error
	if !errors.As(safe, &ue) {
		t.Fatal("errors.As no longer yields a *url.Error")
	}
	if ue.Op != "Get" {
		t.Errorf("Op = %q, want Get", ue.Op)
	}
	if strings.Contains(safe.Error(), "SECRET") {
		t.Errorf("the key survived: %v", safe)
	}
}

func TestRedactURLErrorPassesThroughWhatItCannotHelp(t *testing.T) {
	if RedactURLError(nil) != nil {
		t.Error("nil became non-nil")
	}
	plain := errors.New("something else entirely")
	if got := RedactURLError(plain); got != plain {
		t.Errorf("a non-URL error was rewritten: %v", got)
	}
	// Nothing to redact: the SAME error comes back, so no information about
	// the original is lost by a needless rebuild.
	clean := &url.Error{Op: "Get", URL: "https://x.test/a?page=1", Err: errors.New("boom")}
	if got := RedactURLError(clean); got != error(clean) {
		t.Errorf("a clean URL error was rebuilt: %v", got)
	}
}
