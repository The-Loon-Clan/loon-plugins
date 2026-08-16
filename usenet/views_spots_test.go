package usenet

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"
)

// spotsData builds the template's data map the way renderSpots does. Kept in
// one place so a field added to the view cannot be forgotten in the tests.
func spotsData(probe spotProbe, have, haveProvider bool) map[string]any {
	return map[string]any{
		"Group": SpotGroup, "MinKeyBits": MinSpotKeyBits,
		"Probe": probe, "HaveProbe": have, "ProbeAge": humanSince(probe.At),
		"HaveProvider": haveProvider, "ImportWired": false,
	}
}

func renderSpotsTemplate(t *testing.T, data map[string]any) string {
	t.Helper()
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "spots.html", data); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

// html/template STREAMS, so a field the data map does not carry aborts the
// render partway and the page silently shows only what was emitted before the
// bad reference. That failure has shipped here before, so every state the tab
// can be in gets rendered.
func TestSpotsTabRendersInEveryState(t *testing.T) {
	full := spotProbe{
		At: time.Now().Add(-90 * time.Minute), Server: "news.example.com", Carried: true,
		Articles: 5903617, Low: 3118, High: 5906734,
		Sampled: 25, Spots: 21, NotSpots: 4, Fetched: 21,
		Verified: 10, WeakKey: 9, Unsigned: 1, BadSig: 1, Malformed: 2,
		SmallestK: 384, LargestK: 1024,
	}
	for _, tc := range []struct {
		name string
		data map[string]any
		want []string
	}{
		{"never probed", spotsData(spotProbe{}, false, true),
			[]string{"No probe has been run yet", "Probe " + SpotGroup}},
		{"probe errored", spotsData(spotProbe{At: time.Now(), Err: "connect: refused"}, true, true),
			[]string{"connect: refused"}},
		{"group not carried", spotsData(spotProbe{At: time.Now(), Server: "news.x.com"}, true, true),
			[]string{"does not carry"}},
		{"full result", spotsData(full, true, true),
			[]string{"5903617", "verified", "weak key", "bad signature", "384", "1024", "1h ago"}},
		{"no provider disables the button", spotsData(spotProbe{}, false, false),
			[]string{"needs an enabled provider", "disabled"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := renderSpotsTemplate(t, tc.data)
			// The closing form is the LAST thing in the template, so its
			// presence proves the render did not abort partway.
			if !strings.Contains(out, "</form>") {
				t.Fatal("render stopped before the end of the template")
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q", want)
				}
			}
		})
	}
}

// The status table must never claim the importer works while it does not.
func TestSpotsTabSaysImportIsNotBuilt(t *testing.T) {
	out := renderSpotsTemplate(t, spotsData(spotProbe{}, false, true))
	if !strings.Contains(out, "not built") {
		t.Error("the tab does not say the importer is missing")
	}
	if strings.Contains(out, "spots imported") {
		t.Error("the tab shows an imported count, which can only be a zero that reads as broken")
	}
}

// A probe's action must return to its own tab, or the operator lands on
// Providers and thinks nothing happened.
func TestSpotProbeRedirectsToTheSpotsTab(t *testing.T) {
	if got := tabForAction("/admin/p/usenet/spot-probe"); got != "spots" {
		t.Errorf("tabForAction = %q, want spots", got)
	}
}

func TestForgeableShare(t *testing.T) {
	for _, tc := range []struct {
		name  string
		probe spotProbe
		want  int
	}{
		{"no sample yet does not divide by zero", spotProbe{}, 0},
		{"half the feed", spotProbe{Fetched: 12, WeakKey: 6}, 50},
		{"all provable", spotProbe{Fetched: 10, Verified: 10}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.probe.ForgeableShare(); got != tc.want {
				t.Errorf("ForgeableShare = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestHumanSince(t *testing.T) {
	if got := humanSince(time.Time{}); got != "never" {
		t.Errorf("zero time = %q, want never", got)
	}
	if got := humanSince(time.Now().Add(-3 * time.Hour)); got != "3h ago" {
		t.Errorf("3h = %q", got)
	}
}

// X-Xml arrives as SEVERAL headers whose concatenation is the document. A map
// that overwrote on repeat would keep only the last fragment — the exact bug
// the document parser exists to prevent, reintroduced one layer up.
func TestReadSpotHeadersJoinsRepeatsAndFolds(t *testing.T) {
	raw := "Message-ID: <a@spot.net>\r\n" +
		"X-Xml: <Spotnet><Post\r\n" +
		"X-Xml: ing><Title>T</Title>\r\n" +
		"X-Xml: </Posting></Spotnet>\r\n" +
		"X-User-Key: <RSAKeyValue><Modulus>abc\r\n" +
		"\tdef</Modulus></RSAKeyValue>\r\n"
	h := readSpotHeaders(strings.NewReader(raw))

	if got := h["x-xml"]; got != "<Spotnet><Posting><Title>T</Title></Posting></Spotnet>" {
		t.Errorf("repeated headers joined to %q", got)
	}
	// A folded continuation belongs to the header above it, and the fold
	// whitespace is not part of the value.
	if got := h["x-user-key"]; got != "<RSAKeyValue><Modulus>abcdef</Modulus></RSAKeyValue>" {
		t.Errorf("folded header = %q", got)
	}
	if h["message-id"] != "<a@spot.net>" {
		t.Errorf("message-id = %q", h["message-id"])
	}
}

func TestSpotProbeSummary(t *testing.T) {
	if got := (spotProbe{Err: "boom"}).Summary(); got != "boom" {
		t.Errorf("error summary = %q", got)
	}
	if got := (spotProbe{}).Summary(); !strings.Contains(got, "not carried") {
		t.Errorf("uncarried summary = %q", got)
	}
	got := (spotProbe{Carried: true, Articles: 10, Sampled: 5, Spots: 4, Verified: 2, WeakKey: 2}).Summary()
	for _, want := range []string{SpotGroup, "10 articles", "2 verified"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q missing %q", got, want)
		}
	}
}
