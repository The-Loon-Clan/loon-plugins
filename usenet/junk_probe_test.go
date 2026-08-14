package usenet

import "testing"

// The probe's entire finding rests on this one parse: if yEncName is wrong, the
// recovery rate it reports is wrong, and the decision it feeds — whether the
// crawler is discarding real releases on a scrambled subject — is made on a
// fabricated number.
func TestYEncNameReadsTheEncodersFilename(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			// The case the whole probe exists for: the subject was a random
			// token, the body was not.
			name: "obfuscated subject, real filename in the body",
			body: "=ybegin part=1 total=50 line=128 size=768000 name=Some.Real.Release-GROUP.part03.rar\r\n" +
				"=ypart begin=1 end=768000\r\nabcdef\r\n",
			want: "Some.Real.Release-GROUP.part03.rar",
		},
		{
			// name= runs to end of line precisely so this works; a parse that
			// splits on whitespace truncates every one of these.
			name: "filename containing spaces",
			body: "=ybegin line=128 size=100 name=My Show - 01 [1080p].mkv\r\n",
			want: "My Show - 01 [1080p].mkv",
		},
		{
			name: "bare LF, single-part header without part=",
			body: "=ybegin line=128 size=100 name=thing.nfo\ndata\n",
			want: "thing.nfo",
		},
		{
			// Some posters emit a blank line or a comment first, which is why
			// the pattern is per-line anchored rather than tied to byte 0.
			name: "header is not the first byte",
			body: "\r\nposted by a tool\r\n=ybegin line=128 size=100 name=real.mkv\r\n",
			want: "real.mkv",
		},
		{
			// The outcome that VINDICATES the drop: the body agrees the name is
			// junk. Parsed the same way — it is the junk rules, not this
			// function, that get to judge it.
			name: "body names another random token",
			body: "=ybegin line=128 size=100 name=541279675.bin\r\n",
			want: "541279675.bin",
		},
		{name: "no yEnc header at all", body: "just some text\r\nnothing here\r\n", want: ""},
		{name: "empty body", body: "", want: ""},
		{
			// =ypart carries begin=/end= and no name=; matching it would yield
			// nonsense, so the anchor is on =ybegin specifically.
			name: "ypart alone yields nothing",
			body: "=ypart begin=1 end=768000\r\n",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := yEncName([]byte(tc.body)); got != tc.want {
				t.Errorf("yEncName() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The probe classifies a recovered name by re-running the junk rules on it, and
// only the "real" bucket argues for changing the crawler. Pin that the two
// shapes actually land in different buckets — if every recovered name scored as
// junk, the probe would report 0% forever and look like a settled question.
func TestRecoveredNamesSplitIntoRealAndJunk(t *testing.T) {
	if r := whichJunkRule("Some.Real.Release-GROUP.part03.rar"); r != "" {
		t.Errorf("a normal release filename was judged junk by %q — the probe would under-report recovery", r)
	}
	if whichJunkRule("541279675.bin") == "" {
		t.Error("a bare numeric token was judged real — the probe would over-report recovery")
	}
}
