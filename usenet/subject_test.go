package usenet

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseSubject(t *testing.T) {
	tests := []struct {
		name      string
		subject   string
		wantBase  string // "" = don't assert
		wantPart  int
		wantTotal int
		wantFile  bool
	}{
		{
			name:      "single-file yEnc",
			subject:   `The.Release.Name.S01E05.1080p.WEB.mkv (12/45) yEnc`,
			wantBase:  "The.Release.Name.S01E05.1080p.WEB",
			wantPart:  12,
			wantTotal: 45,
		},
		{
			name:      "single part companion",
			subject:   `readme.nfo yEnc (1/1)`,
			wantPart:  1,
			wantTotal: 1,
		},
		{
			// The form that ate four days of the forward crawl. The scope for
			// the segment marker was everything BEFORE "yEnc", so this parsed
			// as 1/1; every segment then collided on staging key "0:1", 44 of
			// 45 articles were overwritten, and the survivor assembled as a
			// ~700 KB release the size catchalls threw away.
			name:      "single-file, segment marker AFTER yEnc",
			subject:   `[SubsPlease] Sekai Saikyou no Kouei - 03 (1080p) [E4261D3F].mkv yEnc (1/45)`,
			wantBase:  "[SubsPlease] Sekai Saikyou no Kouei - 03 (1080p) [E4261D3F]",
			wantPart:  1,
			wantTotal: 45,
		},
		{
			// Same form, mid-release, and brace-wrapped: proves the fix is not
			// keyed to part 1 and survives decoration around the name.
			name:      "single-file, marker after yEnc, mid part",
			subject:   `{Lioness.S02E05.Shatter.the.Moon.2160p.AMZN.WEB-DL.H.265-FLUX} yEnc (7/1200)`,
			wantPart:  7,
			wantTotal: 1200,
		},
		{
			// Precedence, and the reason the fix is a fallback rather than a
			// widened scope: when a counter exists BEFORE yEnc it is the
			// segment marker and anything after is a file count. Reading the
			// trailing pair here would report 1 of 12 segments instead of 12
			// of 45, and the set would never look complete.
			name:      "marker before yEnc wins over a trailing file count",
			subject:   `The.Release.Name.S01E05.1080p.WEB.mkv (12/45) yEnc (1/12)`,
			wantPart:  12,
			wantTotal: 45,
		},
		{
			name:      "multi-file, segment marker after yEnc",
			subject:   `Some Release [3/8] - "data.part03.rar" yEnc (5/20)`,
			wantPart:  5,
			wantTotal: 20,
			wantFile:  true,
		},
		{
			name:      "no markers",
			subject:   `just a plain subject`,
			wantBase:  "just a plain subject",
			wantPart:  1,
			wantTotal: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, part, total, seg, _, _, fileParts := parseSubject(tc.subject)
			if part != tc.wantPart || total != tc.wantTotal {
				t.Errorf("part/total = %d/%d, want %d/%d", part, total, tc.wantPart, tc.wantTotal)
			}
			if seg != total {
				t.Errorf("segTotal = %d, want == total %d", seg, total)
			}
			if fileParts != tc.wantFile {
				t.Errorf("fileParts = %v, want %v", fileParts, tc.wantFile)
			}
			if tc.wantBase != "" && base != tc.wantBase {
				t.Errorf("base = %q, want %q", base, tc.wantBase)
			}
		})
	}
}

func TestBuildNZBAndGzip(t *testing.T) {
	arts := []stagedArticle{
		{MessageID: "<a@x>", Subject: "rel (1/2) yEnc", Poster: "p", Bytes: 100, Group: "a.b", PartNum: 1},
		{MessageID: "<b@x>", Subject: "rel (2/2) yEnc", Poster: "p", Bytes: 120, Group: "a.b", PartNum: 2},
	}
	xmlBytes, err := buildNZB(arts)
	if err != nil || len(xmlBytes) == 0 {
		t.Fatalf("buildNZB: %v (len %d)", err, len(xmlBytes))
	}
	s := string(xmlBytes)
	for _, want := range []string{"<nzb", "a@x", "b@x", `number="1"`, `number="2"`, "a.b"} {
		if !contains(s, want) {
			t.Errorf("NZB XML missing %q", want)
		}
	}
	gz, err := gzipBytes(xmlBytes)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if len(gz) == 0 || len(gz) >= len(xmlBytes) {
		t.Errorf("gzip did not compress (%d -> %d)", len(xmlBytes), len(gz))
	}
}

func TestMultiFileGrouping(t *testing.T) {
	subs := []string{
		`Big.Release.2024 [1/2] - "big.part1.rar" yEnc (1/2)`,
		`Big.Release.2024 [1/2] - "big.part1.rar" yEnc (2/2)`,
		`Big.Release.2024 [2/2] - "big.part2.rar" yEnc (1/2)`,
		`Big.Release.2024 [2/2] - "big.part2.rar" yEnc (2/2)`,
	}
	var arts []stagedArticle
	bases := map[string]bool{}
	for i, s := range subs {
		base, part, total, seg, fn, tf, fp := parseSubject(s)
		bases[base] = true
		arts = append(arts, stagedArticle{
			MessageID: fmt.Sprintf("<%d@x>", i), Group: "a.b", BaseSubject: base,
			PartNum: part, TotalParts: total, SegTotal: seg,
			FileNum: fn, TotalFiles: tf, FileParts: fp,
		})
	}
	if len(bases) != 1 {
		t.Fatalf("all files should share one release base, got %d: %v", len(bases), bases)
	}
	if !isComplete(arts) {
		t.Error("2 files x 2 segments all present should be complete")
	}
	if isComplete(arts[:3]) {
		t.Error("dropping a segment should make it incomplete")
	}
	xmlOut, err := buildNZB(arts)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(xmlOut), "<file "); n != 2 {
		t.Errorf("multi-file NZB should have 2 <file> elements, got %d", n)
	}
}

// TestAgentPostedSubjectContract pins the round-trip with loon-agent: the
// canonical subject the agent posts (services/upload_usenet.go: UploadDirectory
// -> UploadToUsenet builds `<release> [i/F] - "name" yEnc (n/P)`) must parse
// back into the same shared release base + the right file/part indices for every
// part of every file, so the crawler groups the whole release into one NZB. If
// either side's format drifts, this fails.
func TestAgentPostedSubjectContract(t *testing.T) {
	// Exact mirror of the agent's fmt.Sprintf.
	agentSubject := func(release string, fileIdx, fileCount int, name string, part, parts int) string {
		return fmt.Sprintf(`%s [%d/%d] - "%s" yEnc (%d/%d)`, release, fileIdx, fileCount, name, part, parts)
	}

	for _, tc := range []struct {
		mode, release string
		names         []string
	}{
		{"real names", "My.Release.2024.1080p.BluRay.x264-GRP", []string{"grp-rel.mkv", "grp-rel.nfo", "grp-rel.sfv"}},
		{"obfuscated", "aB3xK9mQ2pL7wR4t", []string{"k7Rm2p.mkv", "p9XnQ4.nfo"}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			const parts = 12
			seenBase := map[string]bool{}
			for f, name := range tc.names {
				for p := 1; p <= parts; p++ {
					base, partNum, totalParts, seg, fileNum, totalFiles, fileParts := parseSubject(
						agentSubject(tc.release, f+1, len(tc.names), name, p, parts))
					seenBase[base] = true
					if !fileParts || fileNum != f+1 || totalFiles != len(tc.names) {
						t.Fatalf("file %d/%d part %d: file=(%d/%d,%v)", f+1, len(tc.names), p, fileNum, totalFiles, fileParts)
					}
					if partNum != p || totalParts != parts || seg != parts {
						t.Fatalf("file %d part %d: part=(%d/%d seg=%d), want (%d/%d/%d)", f+1, p, partNum, totalParts, seg, p, parts, parts)
					}
				}
			}
			// Every file of the release resolves to ONE shared base -> they group.
			if len(seenBase) != 1 {
				t.Fatalf("release must yield one base, got %d: %v", len(seenBase), seenBase)
			}
			if !seenBase[tc.release] {
				t.Fatalf("base != release %q; got %v", tc.release, seenBase)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
