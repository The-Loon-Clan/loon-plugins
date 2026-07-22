package usenet

import (
	"errors"
	"fmt"
	"testing"

	"github.com/the-loon-clan/loon/nntp"
)

// TestHealthVerdict pins the scoring rule: missing data is survivable exactly as
// far as the SURVIVING par2 blocks can rebuild it.
func TestHealthVerdict(t *testing.T) {
	cases := []struct {
		name                                string
		missingData, par2Total, par2Missing int
		want                                string
	}{
		{"nothing missing", 0, 100, 0, healthHealthy},
		{"nothing missing, no par2 either", 0, 0, 0, healthHealthy},
		{"healthy even with par2 gone", 0, 50, 50, healthHealthy},
		{"repairable: fewer missing than par2", 10, 50, 0, healthBroken},
		{"repairable: exactly as many as par2", 50, 50, 0, healthBroken},
		{"unrepairable: one more than par2", 51, 50, 0, healthDead},
		{"par2 losses shrink the budget", 30, 50, 40, healthDead}, // only 10 survive
		{"par2 losses still leave enough", 10, 50, 40, healthBroken},
		{"no par2 at all: any loss is fatal", 1, 0, 0, healthDead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := healthVerdict(tc.missingData, tc.par2Total, tc.par2Missing)
			if got != tc.want {
				t.Errorf("healthVerdict(%d, %d, %d) = %q, want %q",
					tc.missingData, tc.par2Total, tc.par2Missing, got, tc.want)
			}
		})
	}
}

// TestClassifyStat: ONLY a server-issued 430 means "missing". Everything else is
// inconclusive — treating a timeout as a missing article is how a healthy
// archive gets wrongly condemned.
func TestClassifyStat(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want statResult
	}{
		{"no error = present", nil, statPresent},
		{"430 = definitively missing", nntp.Error{Code: 430, Msg: "no such article"}, statMissing},
		{"423 = inconclusive", nntp.Error{Code: 423, Msg: "no such article number"}, statUnknown},
		{"400 = inconclusive", nntp.Error{Code: 400, Msg: "service discontinued"}, statUnknown},
		{"timeout = inconclusive, NOT missing", errors.New("i/o timeout"), statUnknown},
		{"wrapped 430 still counts", fmt.Errorf("stat: %w", nntp.Error{Code: 430}), statMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyStat(tc.err); got != tc.want {
				t.Errorf("classifyStat(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsProtocolError decides whether a failed STAT costs us the connection. A
// 430 is a normal answer and must not; an i/o error means the connection is
// finished. Getting this backwards would either destroy the pool one missing
// article at a time, or keep using a broken socket.
func TestIsProtocolError(t *testing.T) {
	if !isProtocolError(nntp.Error{Code: 430}) {
		t.Error("430 should be a protocol error (keep the connection)")
	}
	if !isProtocolError(fmt.Errorf("wrapped: %w", nntp.Error{Code: 423})) {
		t.Error("wrapped protocol errors should still be recognised")
	}
	if isProtocolError(errors.New("connection reset by peer")) {
		t.Error("transport failures must NOT be treated as protocol errors")
	}
}

func TestIsPar2Subject(t *testing.T) {
	cases := map[string]bool{
		`Some.Release.vol000+01.par2`: true,
		`Some.Release.PAR2`:           true,
		`"Some.Release.par2" yEnc`:    true,
		`Some.Release.part01.rar`:     false,
		`Some.Release.mkv`:            false,
	}
	for subject, want := range cases {
		if got := isPar2Subject(subject); got != want {
			t.Errorf("isPar2Subject(%q) = %v, want %v", subject, got, want)
		}
	}
}

// TestParseNzbSegments checks the split that the whole verdict rests on, plus
// message-id normalisation (stored bare, STAT wants angle brackets).
func TestParseNzbSegments(t *testing.T) {
	raw := `<?xml version="1.0" encoding="iso-8859-1" ?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p" date="1" subject="Show.S01E01.mkv (1/2)">
    <groups><group>alt.test</group></groups>
    <segments>
      <segment bytes="100" number="1">data1@example.com</segment>
      <segment bytes="100" number="2">&lt;data2@example.com&gt;</segment>
    </segments>
  </file>
  <file poster="p" date="1" subject="Show.S01E01.vol000+01.par2 (1/1)">
    <groups><group>alt.test</group></groups>
    <segments>
      <segment bytes="50" number="1">par1@example.com</segment>
    </segments>
  </file>
</nzb>`
	gz, err := gzipBytes([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	segs, err := parseNzbSegments(gz)
	if err != nil {
		t.Fatalf("parseNzbSegments: %v", err)
	}
	if len(segs.Data) != 2 {
		t.Errorf("data segments = %d, want 2 (%v)", len(segs.Data), segs.Data)
	}
	if len(segs.Par2) != 1 {
		t.Errorf("par2 segments = %d, want 1 (%v)", len(segs.Par2), segs.Par2)
	}
	for _, id := range append(append([]string{}, segs.Data...), segs.Par2...) {
		if len(id) < 2 || id[0] != '<' || id[len(id)-1] != '>' {
			t.Errorf("message-id %q is not in angle-bracket form", id)
		}
	}
	if segs.Data[0] != "<data1@example.com>" {
		t.Errorf("bare id was not normalised: %q", segs.Data[0])
	}
	if segs.Data[1] != "<data2@example.com>" {
		t.Errorf("already-bracketed id was double-wrapped: %q", segs.Data[1])
	}
}

func TestParseNzbSegmentsRejectsGarbage(t *testing.T) {
	if _, err := parseNzbSegments([]byte("not gzip")); err == nil {
		t.Error("expected an error for a non-gzip blob")
	}
	gz, err := gzipBytes([]byte("<nzb><unclosed>"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseNzbSegments(gz); err == nil {
		t.Error("expected an error for malformed XML")
	}
}

// TestParseNzbSegmentsLegacyCharset is a regression guard for a real failure:
// the standard NZB preamble declares iso-8859-1, and Go's XML decoder refuses
// any non-UTF-8 declaration unless a CharsetReader is supplied. Our own NZBs are
// written UTF-8, so only IMPORTED files hit this — and a parse failure means the
// release's health can never be determined.
func TestParseNzbSegmentsLegacyCharset(t *testing.T) {
	for _, charset := range []string{"iso-8859-1", "ISO-8859-1", "windows-1252", "us-ascii", "UTF-8"} {
		raw := `<?xml version="1.0" encoding="` + charset + `" ?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p" date="1" subject="Caf&#233;.S01E01.mkv (1/1)">
    <segments><segment bytes="1" number="1">a@b</segment></segments>
  </file>
</nzb>`
		gz, err := gzipBytes([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		segs, err := parseNzbSegments(gz)
		if err != nil {
			t.Errorf("charset %s: %v", charset, err)
			continue
		}
		if len(segs.Data) != 1 {
			t.Errorf("charset %s: got %d data segments, want 1", charset, len(segs.Data))
		}
	}
}

func TestNzbCharsetReaderRejectsUnknown(t *testing.T) {
	if _, err := nzbCharsetReader("shift_jis", nil); err == nil {
		t.Error("expected an unsupported charset to be reported, not silently mangled")
	}
}
