package usenet

import (
	"strings"
	"testing"
)

// NFO decoding, which is the half of this feature that can silently put
// garbage on a release page.
//
// The message-id comes from a document walk that picked the file whose name
// ended ".nfo". If that walk was wrong -- or a poster named a video segment
// that way -- the job fetches a megabyte of binary. Storing it would render
// as a wall of noise on the release page, so the decode has to be able to say
// "this is not text" rather than doing its best with whatever arrived.

func TestDecodeNFOPlainText(t *testing.T) {
	const nfo = "  Release.Name-GROUP\n  Encoded with x264\n  Greets to everyone\n"
	got, ok := decodeNFO([]byte(nfo))
	if !ok {
		t.Fatal("a plain-text NFO was rejected")
	}
	if !strings.Contains(got, "Release.Name-GROUP") {
		t.Errorf("text mangled: %q", got)
	}
}

// NFO art is drawn in code page 437 -- the box-drawing characters live at
// 0xB0-0xDF. Read as UTF-8 those are invalid bytes, so without the mapping
// every border becomes a replacement character and a perfectly good NFO
// renders as a wall of question marks.
func TestDecodeNFOMapsCP437BoxArt(t *testing.T) {
	// 0xC9 0xCD 0xBB is the top-left, horizontal and top-right of a double
	// box -- the first line of a great many NFOs.
	raw := []byte{0xC9, 0xCD, 0xCD, 0xBB, '\n', 'h', 'i', '\n'}
	got, ok := decodeNFO(raw)
	if !ok {
		t.Fatal("CP437 box art was rejected as non-text")
	}
	for _, want := range []string{"╔", "═", "╗"} {
		if !strings.Contains(got, want) {
			t.Errorf("box character %q missing — the frame every NFO is drawn "+
				"with did not survive decoding: %q", want, got)
		}
	}
	if strings.Contains(got, "�") {
		t.Errorf("decoded to replacement characters: %q", got)
	}
}

// Valid UTF-8 must be left alone rather than run through the CP437 table,
// which would turn every multi-byte character into two wrong ones.
func TestDecodeNFOLeavesUTF8Alone(t *testing.T) {
	const nfo = "Release — “quoted” … ünïcode\n"
	got, ok := decodeNFO([]byte(nfo))
	if !ok {
		t.Fatal("a UTF-8 NFO was rejected")
	}
	if got != nfo {
		t.Errorf("UTF-8 was re-encoded through CP437:\n got %q\nwant %q", got, nfo)
	}
}

// The case this guard exists for: the id pointed at something that is not an
// NFO at all.
func TestDecodeNFORejectsBinary(t *testing.T) {
	bin := make([]byte, 4096)
	for i := range bin {
		bin[i] = byte(i % 7) // control bytes, nothing printable
	}
	if got, ok := decodeNFO(bin); ok {
		t.Errorf("binary was accepted as an NFO (%d chars) — this puts a "+
			"decoded video segment on the release page", len(got))
	}
}

func TestDecodeNFORejectsEmpty(t *testing.T) {
	if _, ok := decodeNFO(nil); ok {
		t.Error("nil body accepted")
	}
	if _, ok := decodeNFO([]byte("   \n\t\n  ")); ok {
		t.Error("whitespace-only body accepted — storing it would show an " +
			"empty NFO tab rather than none at all")
	}
}

// A yEnc-encoded NFO decodes through the same path, because both forms are
// common on usenet and the caller does not know which it has.
func TestDecodeNFOHandlesYenc(t *testing.T) {
	const plain = "Scene NFO body here\n"
	var enc []byte
	enc = append(enc, []byte("=ybegin line=128 size=20 name=release.nfo\r\n")...)
	for i := 0; i < len(plain); i++ {
		c := plain[i] + 42 // byte arithmetic already wraps at 256
		switch c {
		case 0x00, 0x0A, 0x0D, 0x3D:
			enc = append(enc, '=', c+64)
		default:
			enc = append(enc, c)
		}
	}
	enc = append(enc, []byte("\r\n=yend size=20\r\n")...)

	got, ok := decodeNFO(enc)
	if !ok {
		t.Fatal("a yEnc-encoded NFO was rejected")
	}
	if !strings.Contains(got, "Scene NFO body here") {
		t.Errorf("yEnc decode produced %q, want the plain text back", got)
	}
}

// Which errors mean "gone for good" and which mean "try again".
//
// The distinction decides whether a release is permanently marked as having
// no readable NFO. Getting it wrong in the pessimistic direction records a
// transient provider failure as a fact about the release, which nothing later
// would revisit.
func TestIsArticleGoneOnlyForServerRefusals(t *testing.T) {
	if isArticleGone(nil) {
		t.Error("nil error read as a missing article")
	}
	for _, e := range []error{errTestTimeout{}, errTestReset{}} {
		if isArticleGone(e) {
			t.Errorf("%v read as a missing article — a transport failure says "+
				"nothing about whether the server holds the article, and "+
				"recording it as absent is unrecoverable", e)
		}
	}
}

type errTestTimeout struct{}

func (errTestTimeout) Error() string { return "read tcp 10.0.0.1:119: i/o timeout" }

type errTestReset struct{}

func (errTestReset) Error() string { return "connection reset by peer" }

// ANSI colour codes are stripped.
//
// We render into a <pre>, which shows an escape sequence as the literal
// characters it is made of — so an ANSI-coloured NFO arrives as art
// interrupted every few characters by "[1;36m". The colour is a real loss;
// the art, which is the part people look at, survives either way.
func TestDecodeNFOStripsANSIColour(t *testing.T) {
	raw := "\x1b[1;36m╔══╗\x1b[0m\n\x1b[32mhello\x1b[0m\n"
	got, ok := decodeNFO([]byte(raw))
	if !ok {
		t.Fatal("an ANSI-coloured NFO was rejected")
	}
	if strings.Contains(got, "\x1b") || strings.Contains(got, "[1;36m") {
		t.Errorf("escape sequences survived: %q", got)
	}
	for _, want := range []string{"╔══╗", "hello"} {
		if !strings.Contains(got, want) {
			t.Errorf("stripping removed content as well as codes: %q missing from %q", want, got)
		}
	}
}

// The common case allocates nothing: most NFOs carry no escapes at all.
func TestStripANSILeavesPlainTextIdentical(t *testing.T) {
	const s = "plain ╔══╗ text\nwith lines\n"
	if got := stripANSI(s); got != s {
		t.Errorf("plain text was altered:\n got %q\nwant %q", got, s)
	}
}
