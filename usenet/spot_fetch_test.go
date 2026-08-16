package usenet

import (
	"bytes"
	"compress/flate"
	"strings"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// specialZipStr is the ENCODER, written only so the tests can round-trip.
// Production never posts spots, so this has no place in the package itself.
func specialZipStr(s string) string {
	s = strings.ReplaceAll(s, "=", "=D")
	s = strings.ReplaceAll(s, "\x00", "=A")
	s = strings.ReplaceAll(s, "\r", "=B")
	return strings.ReplaceAll(s, "\n", "=C")
}

// THE ORDER IS THE WHOLE TEST. Spotweb undoes '=D' LAST, so an escaped '='
// cannot be re-read as the introducer of the escape that follows it. Undoing it
// first turns the encoded form of "=C" into a newline — silently corrupting
// every payload containing a literal '=', which for base64-adjacent data is
// most of them.
func TestUnspecialZipStrOrdering(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"newline", "a=Cb", "a\nb"},
		{"carriage return", "a=Bb", "a\rb"},
		{"nul", "a=Ab", "a\x00b"},
		{"literal equals", "a=Db", "a=b"},
		// The trap: the payload contained the two characters '=' and 'C'.
		// Decoding must yield them back, NOT a newline.
		{"escaped equals followed by C", "a=DCb", "a=Cb"},
		{"escaped equals followed by D", "a=DDb", "a=Db"},
		{"run of escaped equals", "=D=D=D", "==="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unspecialZipStr(tc.in); got != tc.want {
				t.Errorf("unspecialZipStr(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Round-trip through the real encoder, over bytes chosen to hit every escape
// at once — including the sequences that only break under the wrong order.
func TestSpecialZipRoundTrip(t *testing.T) {
	for _, payload := range []string{
		"plain",
		"<?xml version=\"1.0\"?>\n<nzb>\r\n</nzb>\n",
		"=C=B=A=D",
		"a=b=c\nd\re\x00f",
		strings.Repeat("=DC\n", 50),
	} {
		if got := unspecialZipStr(specialZipStr(payload)); got != payload {
			t.Errorf("round trip of %q gave %q", payload, got)
		}
	}
}

// The full wire path: DEFLATE, escape, split into body lines. Lines join with
// NO separator — the newlines that mattered were escaped before posting, so
// re-adding the transport's own line breaks injects bytes that were never in
// the payload.
func TestDecodeSpotBinary(t *testing.T) {
	const nzb = `<?xml version="1.0" encoding="iso-8859-1" ?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="x@y.z" date="1786812549" subject="Some.Release (1/2)">
    <segments><segment bytes="500000" number="1">abc@news</segment></segments>
  </file>
</nzb>`

	var deflated bytes.Buffer
	w, _ := flate.NewWriter(&deflated, flate.BestCompression)
	if _, err := w.Write([]byte(nzb)); err != nil {
		t.Fatal(err)
	}
	w.Close()

	wire := specialZipStr(deflated.String())
	// Split at an arbitrary offset, as a real posting does — the boundary can
	// land in the middle of an escape sequence.
	var lines []string
	for s := wire; len(s) > 0; {
		n := 37
		if n > len(s) {
			n = len(s)
		}
		lines = append(lines, s[:n])
		s = s[n:]
	}
	if len(lines) < 3 {
		t.Fatalf("fixture split into only %d lines", len(lines))
	}

	got, err := decodeSpotBinary(lines, true)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(got) != nzb {
		t.Errorf("decoded %d bytes, want %d\ngot:  %.80q\nwant: %.80q", len(got), len(nzb), got, nzb)
	}
}

func TestDecodeSpotBinaryUncompressed(t *testing.T) {
	got, err := decodeSpotBinary([]string{"he", "llo=Cwor", "ld"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\nworld" {
		t.Errorf("got %q", got)
	}
}

func TestDecodeSpotBinaryRejectsRubbish(t *testing.T) {
	if _, err := decodeSpotBinary([]string{"not deflate at all, definitely not"}, true); err == nil {
		t.Error("undecodable body was accepted")
	}
}

// Repeated headers must survive as a SLICE. Collapsing them is the failure
// ParseSpotXML exists to catch, reintroduced one layer up where the parser can
// no longer see it: the joined document would be missing its middle and the NZB
// pointer with it.
func TestReadSpotHeadersMultiKeepsRepeats(t *testing.T) {
	raw := "Message-ID: <a@spot.net>\r\n" +
		"X-Xml: <Spotnet><Post\r\n" +
		"X-Xml: ing><Title>T</Title>\r\n" +
		"X-Xml: </Posting></Spotnet>\r\n" +
		"X-User-Key: <RSAKeyValue><Modulus>abc\r\n" +
		"\tdef</Modulus></RSAKeyValue>\r\n"
	h := readSpotHeadersMulti(strings.NewReader(raw))

	if got := len(h.all("x-xml")); got != 3 {
		t.Fatalf("x-xml kept %d values, want 3", got)
	}
	doc, err := ParseSpotXML(h.all("x-xml"))
	if err != nil {
		t.Fatalf("the joined document did not parse: %v", err)
	}
	if doc.Posting.Title != "T" {
		t.Errorf("title = %q", doc.Posting.Title)
	}
	// A folded continuation belongs to the header above it, joined without the
	// fold whitespace.
	if got := h.get("x-user-key"); got != "<RSAKeyValue><Modulus>abcdef</Modulus></RSAKeyValue>" {
		t.Errorf("folded header = %q", got)
	}
	if h.get("nope") != "" {
		t.Error("a missing header returned content")
	}
}

// One spot is one posting, so re-fetching it must not create a second release.
func TestSpotContentHashIsStableAndDistinct(t *testing.T) {
	a := spotContentHash("<x@spot.net>")
	if a != spotContentHash("<x@spot.net>") {
		t.Error("hash is not stable — a re-fetch would duplicate the release")
	}
	if a == spotContentHash("<y@spot.net>") {
		t.Error("two different spots collide")
	}
	if len(a) != 64 {
		t.Errorf("hash is %d chars, want a 64-char sha256 hex", len(a))
	}
}

func TestSpotReleaseCarriesProvenance(t *testing.T) {
	s := spotWork{
		MessageID: "<a@spot.net>", GroupName: SpotGroup, Poster: "Paaldanser",
		SizeBytes: 100, PostedAt: time.Unix(1000, 0).UTC(),
	}
	d := spotDocument{Title: "Some Release", Trust: SpotTrustWeakKey}
	doc := &SpotXML{}
	doc.Posting.Size = 3365188124 // past int4, as real spots are
	doc.Posting.Created = 1786812549

	rel := spotRelease(s, d, doc, []byte("gz"))
	if rel.Origin != "spot" {
		t.Errorf("Origin = %q, want spot", rel.Origin)
	}
	// The label must reach the sink. A weak-key spot stored as if verified is
	// the exact laundering the trust column exists to prevent.
	if rel.OriginTrust != SpotTrustWeakKey {
		t.Errorf("OriginTrust = %q, want %q", rel.OriginTrust, SpotTrustWeakKey)
	}
	if rel.Title != "Some Release" || rel.Poster != "Paaldanser" {
		t.Errorf("title/poster = %q / %q", rel.Title, rel.Poster)
	}
	// The document's own size wins over the header's, and survives int4.
	if rel.SizeBytes != 3365188124 {
		t.Errorf("size = %d", rel.SizeBytes)
	}
	if rel.PostedAt.Unix() != 1786812549 {
		t.Errorf("posted = %v — the document's Created should win", rel.PostedAt)
	}
	if len(rel.Groups) != 1 || rel.Groups[0] != SpotGroup {
		t.Errorf("groups = %v", rel.Groups)
	}
}

// A spot whose document carries no size falls back to the header's, rather
// than storing a zero-byte release.
func TestSpotReleaseFallsBackToTheHeaderSize(t *testing.T) {
	s := spotWork{MessageID: "<a@spot.net>", SizeBytes: 4242, PostedAt: time.Unix(99, 0).UTC()}
	rel := spotRelease(s, spotDocument{Title: "T"}, &SpotXML{}, nil)
	if rel.SizeBytes != 4242 {
		t.Errorf("size = %d, want the header's 4242", rel.SizeBytes)
	}
	if rel.PostedAt.Unix() != 99 {
		t.Errorf("posted = %v, want the header's", rel.PostedAt)
	}
}

// The sink contract: an unset Origin must still describe itself honestly.
func TestAssembledReleaseOriginIsOptional(t *testing.T) {
	var rel pluginapi.AssembledRelease
	if rel.Origin != "" || rel.OriginTrust != "" {
		t.Error("zero value carries a provenance claim")
	}
}

// A truncated DEFLATE stream must FAIL, not hand back what it managed.
//
// This is the bug that shipped an 89GB release with about a tenth of its
// segments. The decoder kept partial output whenever any had been produced, so
// reading one article of a multi-article NZB inflated cleanly enough to parse,
// published as a working release, and the only outward sign was the file list
// failing to load. Nothing downstream can tell "the first 9GB of an 89GB
// release" from a small release — the judgement has to be made here.
func TestDecodeSpotBinaryRejectsATruncatedStream(t *testing.T) {
	nzb := strings.Repeat(`<file subject="x"><segments><segment number="1">abc</segment></segments></file>`, 400)
	var deflated bytes.Buffer
	w, _ := flate.NewWriter(&deflated, flate.DefaultCompression)
	_, _ = w.Write([]byte(nzb))
	w.Close()

	full := specialZipStr(deflated.String())
	// Keep the first fifth, the way fetching segment 1 of 5 would.
	cut := len(full) / 5
	if cut < 32 {
		t.Fatalf("fixture too small to truncate meaningfully (%d bytes)", len(full))
	}

	got, err := decodeSpotBinary([]string{full[:cut]}, true)
	if err == nil {
		t.Fatalf("a truncated stream decoded without error into %d bytes — it would have been published", len(got))
	}
	if got != nil {
		t.Errorf("returned %d bytes alongside the error; a caller reading both would publish them", len(got))
	}

	// And the whole thing still decodes, so the check is not simply refusing
	// everything.
	if _, err := decodeSpotBinary([]string{full}, true); err != nil {
		t.Errorf("the complete stream was rejected too: %v", err)
	}
}
