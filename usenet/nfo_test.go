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
	// box -- the first line of a great many NFOs. Run out past nfoMinSize,
	// because a 12-byte floor is part of what counts as an NFO and an
	// 8-byte fixture was never a realistic one.
	raw := []byte{0xC9}
	for i := 0; i < 20; i++ {
		raw = append(raw, 0xCD)
	}
	raw = append(raw, 0xBB, '\n')
	raw = append(raw, []byte("  Release.Name-GROUP\n")...)
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

// Size bounds, lifted from newznab's NfoService (MIN_NFO_SIZE 12,
// MAX_NFO_SIZE 65535).
//
// Distinct from nfoMaxBytes, which is a memory guard on the read. These are a
// judgement about the content: a 900 KB wall of text passes every "is this
// printable" check and is still not an NFO.
func TestDecodeNFOEnforcesNewznabSizeBounds(t *testing.T) {
	if _, ok := decodeNFO([]byte("tiny")); ok {
		t.Error("a 4-byte body was accepted; newznab's floor is 12")
	}
	if _, ok := decodeNFO([]byte(strings.Repeat("a", nfoMinSize))); !ok {
		t.Error("a body exactly at the floor was rejected")
	}
	big := strings.Repeat("scene notes ", 8000) // ~96 KB
	if _, ok := decodeNFO([]byte(big)); ok {
		t.Errorf("a %d-byte body was accepted; newznab's ceiling is %d", len(big), nfoMaxSize)
	}
}

// File signatures an NFO is not.
//
// The message-id was chosen because a filename ended ".nfo", so whatever a
// poster mislabelled can arrive. Several of these are partly printable and
// would otherwise survive the character test as mojibake on a release page.
func TestDecodeNFORejectsKnownNonNFOFormats(t *testing.T) {
	pad := strings.Repeat(" ", 64) // clear the size floor
	for name, head := range map[string]string{
		"png":  "\x89PNG\r\n\x1a\n",
		"gif":  "GIF89a",
		"pdf":  "%PDF-1.4",
		"zip":  "PK\x03\x04",
		"gzip": "\x1f\x8b\x08",
		"exe":  "MZ\x90\x00",
		"xml":  "<?xml version=\"1.0\"?>",
		"rar":  "RAR!\x1a\x07\x00",
		"riff": "RIFF\x00\x00\x00\x00WAVE",
		"nzb":  "=newz[NZB]=",
		"id3":  "ID3\x03\x00\x00\x00",
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := decodeNFO([]byte(head + pad)); ok {
				t.Errorf("%s content was accepted as an NFO", name)
			}
		})
	}
}

// And a real NFO that merely MENTIONS one of those words is still an NFO —
// the signatures are anchored to the start of the file, not searched for.
func TestDecodeNFOKeepsTextMentioningFormats(t *testing.T) {
	const nfo = "  Release notes\n  Encoded to MKV, cover art in PNG, packed with RAR\n  Enjoy\n"
	if _, ok := decodeNFO([]byte(nfo)); !ok {
		t.Error("an NFO describing its own formats was rejected — the signature " +
			"check must be anchored to the start, not a substring search")
	}
}

// The signature list must be matched as BYTES.
//
// This is the bug the format test caught. Written as one Go regexp, `\x89`
// means the rune U+0089 — 0xC2 0x89 in UTF-8, two bytes, neither of them the
// single 0x89 a PNG starts with. So the pattern silently matched nothing for
// precisely the binary signatures it existed to catch, while the ASCII ones
// (PDF, zip, GIF) passed and made the whole thing look correct.
func TestNonNFOSignaturesAreMatchedAsBytes(t *testing.T) {
	for name, head := range map[string][]byte{
		"png":  {0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		"gzip": {0x1f, 0x8b, 0x08, 0x00},
	} {
		t.Run(name, func(t *testing.T) {
			if !looksLikeAnotherFormat(head) {
				t.Errorf("%s signature not detected — a rune-escaped regex cannot "+
					"match a high byte, so this must be a byte comparison", name)
			}
			// And the UTF-8 encoding of the same code point must NOT match,
			// which is what the broken pattern was actually looking for.
			utf8Form := []byte("\u0089PNG")
			if name == "png" && looksLikeAnotherFormat(utf8Form) {
				t.Error("matched the UTF-8 encoding of U+0089 rather than the raw byte")
			}
		})
	}
}

// Message-ids are stored bare and NNTP wants them in angle brackets.
//
// An NZB carries a segment id without brackets, which is correct for the
// format. RFC 3977 §6.2.3 wants BODY <id@host>, and a server given a bare id
// cannot parse it as a message-id — so it answers 430, which is
// indistinguishable from "the article is gone". The first live pass therefore
// wrote off 96 releases as permanently missing when their articles were
// almost certainly present.
//
// The health checker already had this, commented "Stored bare; STAT wants the
// angle-bracket form". The lesson is not the brackets, it is that a sibling in
// the same file had solved it.
func TestMessageIDIsWrappedForNNTP(t *testing.T) {
	for in, want := range map[string]string{
		"abc123@JBinUp.local":   "<abc123@JBinUp.local>",
		"<abc123@JBinUp.local>": "<abc123@JBinUp.local>", // already wrapped: unchanged
		"  spaced@host  ":       "<spaced@host>",
	} {
		if got := wrapMessageID(in); got != want {
			t.Errorf("wrapMessageID(%q) = %q, want %q", in, got, want)
		}
	}
}
