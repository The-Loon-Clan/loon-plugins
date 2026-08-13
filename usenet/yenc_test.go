package usenet

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"strings"
	"testing"
)

// yencEncode is a test-only encoder, written independently of the decoder so a
// round-trip proves something. A decoder tested only against output from its own
// inverse agrees with itself and nothing else.
func yencEncode(data []byte, lineLen int) []byte {
	var b bytes.Buffer
	col := 0
	for _, d := range data {
		c := byte((int(d) + 42) & 0xFF)
		// The four values that must be escaped, per the yEnc spec: NUL, LF, CR
		// and '=' itself.
		if c == 0x00 || c == 0x0A || c == 0x0D || c == '=' {
			b.WriteByte('=')
			b.WriteByte(byte((int(c) + 64) & 0xFF))
			col += 2
		} else {
			b.WriteByte(c)
			col++
		}
		if col >= lineLen {
			b.WriteString("\r\n")
			col = 0
		}
	}
	if col > 0 {
		b.WriteString("\r\n")
	}
	return b.Bytes()
}

func singlePartBody(name string, data []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "=ybegin line=128 size=%d name=%s\r\n", len(data), name)
	b.Write(yencEncode(data, 128))
	fmt.Fprintf(&b, "=yend size=%d crc32=%08x\r\n", len(data), crc32.ChecksumIEEE(data))
	return b.Bytes()
}

func TestYencDecodeRoundTrip(t *testing.T) {
	cases := map[string][]byte{
		"text": []byte("NFO content, plain ASCII.\r\nSecond line.\r\n"),
		"all byte values": func() []byte {
			b := make([]byte, 256)
			for i := range b {
				b[i] = byte(i)
			}
			return b
		}(),
		"escapes": {0x00, 0x0A, 0x0D, '=' - 42, 0xD6, 0xE3, 0xE4, 0x13},
		"empty":   {},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := yencDecode(singlePartBody("x.bin", data))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !bytes.Equal(got, data) {
				t.Errorf("round trip lost data\n  in  %v\n  out %v", data, got)
			}
		})
	}
}

// The header is the reason to fetch a body at all, so every field is asserted
// against a realistic multipart article.
func TestParseYencHeaderMultipart(t *testing.T) {
	body := "=ybegin part=1 total=45 line=128 size=734003200 name=Show.S02.part001.rar\r\n" +
		"=ypart begin=1 end=768000\r\n" +
		"abcdef\r\n" +
		"=yend size=768000 part=1 pcrc32=ABC12345 crc32=DEF67890\r\n"
	h, err := parseYencHeader([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if h.Name != "Show.S02.part001.rar" {
		t.Errorf("Name = %q", h.Name)
	}
	if h.Size != 734003200 {
		t.Errorf("Size = %d, want the WHOLE file's size not this part's", h.Size)
	}
	if h.Part != 1 || h.Total != 45 {
		t.Errorf("Part/Total = %d/%d, want 1/45", h.Part, h.Total)
	}
	if h.Begin != 1 || h.End != 768000 {
		t.Errorf("byte range = %d..%d, want 1..768000", h.Begin, h.End)
	}
	// Lowercased, because it becomes a lookup key and hex case is not
	// consistent between posting tools.
	if h.CRC32 != "def67890" {
		t.Errorf("CRC32 = %q, want def67890", h.CRC32)
	}
	if h.PartCRC32 != "abc12345" {
		t.Errorf("PartCRC32 = %q, want abc12345", h.PartCRC32)
	}
}

// crc32= and pcrc32= OVERLAP as substrings. A naive Index(line, "crc32=") finds
// the pcrc32 first, so the whole-file checksum silently becomes the part
// checksum — and since both are plausible hex, nothing downstream would notice
// a content key that is wrong for every multipart file.
func TestParseYencHeaderDoesNotConfusePcrc32WithCrc32(t *testing.T) {
	body := "=ybegin part=1 total=2 line=128 size=100 name=a.bin\r\n" +
		"=yend size=50 part=1 pcrc32=11111111 crc32=22222222\r\n"
	h, _ := parseYencHeader([]byte(body))
	if h.CRC32 == "11111111" {
		t.Fatal("crc32 was read from the pcrc32 field — the whole-file checksum " +
			"is now the part checksum, on every multipart file")
	}
	if h.CRC32 != "22222222" || h.PartCRC32 != "11111111" {
		t.Errorf("crc32=%q pcrc32=%q, want 22222222 / 11111111", h.CRC32, h.PartCRC32)
	}
}

// name= is last and unquoted by spec, so it runs to end of line. Splitting the
// line on whitespace truncates every filename containing a space, which is most
// scene-adjacent releases and nearly all music.
func TestParseYencHeaderNameMayContainSpaces(t *testing.T) {
	body := "=ybegin part=1 total=2 line=128 size=100 name=Some Show - S01E01 [1080p].mkv\r\n" +
		"=yend size=50\r\n"
	h, _ := parseYencHeader([]byte(body))
	if h.Name != "Some Show - S01E01 [1080p].mkv" {
		t.Errorf("Name = %q — a filename with spaces was truncated", h.Name)
	}
}

// A password protects the PAYLOAD, not the yEnc layer. The header is plaintext
// ASCII outside it, so name, size and crc32 all survive — which is why crc32
// dedup works on content we cannot otherwise identify.
func TestYencHeaderReadableForPasswordedPayload(t *testing.T) {
	encrypted := []byte{0x52, 0x61, 0x72, 0x21, 0xFF, 0x00, 0x9E, 0x7B, 0x11}
	h, err := parseYencHeader(singlePartBody("abc123.part01.rar", encrypted))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if h.Name != "abc123.part01.rar" || h.Size != int64(len(encrypted)) {
		t.Errorf("header unreadable for an encrypted payload: %+v", h)
	}
	if h.CRC32 == "" {
		t.Error("no crc32 — the key that works on payloads we cannot identify")
	}
}

// Control lines are structure. Decoding =ybegin as payload corrupts the first
// bytes of every file, and a plaintext preamble above it does the same.
func TestYencDecodeSkipsControlLinesAndPreamble(t *testing.T) {
	data := []byte("real payload")
	body := "Posted by some tool, ignore this line.\r\n" + string(singlePartBody("x.txt", data))
	got, err := yencDecode([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

// A body with no =ybegin is not a yEnc article. Returning empty with no error
// would read downstream as "an empty NFO", which is worse than a failure.
func TestYencRejectsNonYencBodies(t *testing.T) {
	if _, err := yencDecode([]byte("just some text\r\nno headers here\r\n")); err == nil {
		t.Error("a non-yEnc body decoded without error")
	}
	if _, err := parseYencHeader([]byte("nothing to see")); err == nil {
		t.Error("a non-yEnc body parsed a header without error")
	}
}

// A truncated escape must fail loudly. Dropping it shifts every subsequent byte,
// producing a file that decodes "successfully" and is wrong from that point on.
func TestYencDecodeRejectsTruncatedEscape(t *testing.T) {
	body := "=ybegin line=128 size=3 name=x.bin\r\nabc=\r\n=yend size=3\r\n"
	if _, err := yencDecode([]byte(body)); err == nil {
		t.Error("a line ending mid-escape decoded without error")
	}
}

// The decoded bytes must actually satisfy the checksum the poster published —
// the end-to-end statement that this decoder implements yEnc and not something
// that merely round-trips with its own encoder.
func TestYencDecodedPayloadMatchesPublishedCRC32(t *testing.T) {
	data := []byte(strings.Repeat("The quick brown fox. \x00\x0A\x0D=", 40))
	body := singlePartBody("sample.nfo", data)
	h, err := parseYencHeader(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := yencDecode(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sum := fmt.Sprintf("%08x", crc32.ChecksumIEEE(got)); sum != h.CRC32 {
		t.Errorf("decoded payload crc32 = %s, header says %s", sum, h.CRC32)
	}
}
