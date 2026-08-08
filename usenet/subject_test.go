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
			// Two counters around yEnc: the one AFTER is the segment counter,
			// the one before the file counter. An earlier version of this row
			// pinned the opposite ("a counter before yEnc is the segment
			// marker and anything after is a file count") on this same
			// synthetic subject — a shape that occurs in zero of the 288
			// two-counter posts in a 7.6M-article index. The swap is measured,
			// unanimous, and validated over every distinct subject in staging;
			// see loon-demo-site docs/SUBJECT-PARSING-REVIEW.md.
			name:      "two counters: the after-yEnc pair is the segment counter",
			subject:   `The.Release.Name.S01E05.1080p.WEB.mkv (12/45) yEnc (1/12)`,
			wantPart:  1,
			wantTotal: 12,
			wantFile:  true,
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

// TestTwoCounterFamilies pins the counter swap against real-index fixtures,
// one per family that exists in production, with the FILE counter asserted —
// the table test above only checks the fileParts bool. Bases are asserted
// byte-for-byte where given: they must equal what the OLD parser derived, or
// the change re-keys live staging (the wanted values were read from staged
// base_subject rows, not computed by hand).
func TestTwoCounterFamilies(t *testing.T) {
	tests := []struct {
		name     string
		subject  string
		wantBase string // "" = don't assert (splinter-base warts, pinned elsewhere)
		pn, tp   int
		fn, tf   int
	}{
		{
			// The largest family by article count: counter at position 0, so a
			// release-name-before-the-counter base rule would derive "" here.
			name:     "leading counter",
			subject:  `(002/199) - - "3hehrnk86mlv.part001.rar" - 8,82 GB - yEnc (1/79)`,
			wantBase: "3hehrnk86mlv - 8,82 GB",
			pn:       1, tp: 79, fn: 2, tf: 199,
		},
		{
			// Mid-subject counter after a poster banner: the strongest
			// discriminator against the [i/j] base route, which would truncate
			// the base to the banner and merge every TOWN post into one.
			name:     "banner then counter",
			subject:  `<TOWN> www.town.ag > sponsored by www.ssl-news.info > (02/90) "flt-cry2.part01.rar" - 8,07 GB - yEnc (120/273)`,
			wantBase: "<TOWN> www.town.ag > sponsored by www.ssl-news.info > flt-cry2 - 8,07 GB",
			pn:       120, tp: 273, fn: 2, tf: 90,
		},
		{
			// Quoted filename FIRST, counter after the quote, .rNN volume
			// numbering (reExt strips it, so all volumes share the base). The
			// ground-truth measurement was made on this family: .r00 = file 03,
			// a fixed offset per post.
			name:     "realmom .rN form",
			subject:  `"Mac.OSX.Snow.Leopard.v10.6.7-HOTiSO__www.realmom.info__.r00" (03/80) 6,84 GB yEnc (1/261)`,
			wantBase: "Mac.OSX.Snow.Leopard.v10.6.7-HOTiSO__www.realmom.info__ 6,84 GB",
			pn:       1, tp: 261, fn: 3, tf: 80,
		},
		{
			// A par2 recovery volume of the same post. The counters swap like
			// every sibling; the base keeps its .vol000+01 splinter (the
			// trailing size text defeats the end-anchored reVolSuffix), which
			// is today's behaviour ON PURPOSE — unifying it would be a second,
			// unreviewed change that re-keys live staging.
			name:     "vol+par2 keeps its splinter base",
			subject:  `(169/199) - - "3hehrnk86mlv.vol000+01.PAR2" - 8,82 GB - yEnc (3/5)`,
			wantBase: "3hehrnk86mlv.vol000+01 - 8,82 GB",
			pn:       3, tp: 5, fn: 169, tf: 199,
		},
		{
			// One of exactly four real subjects whose two counters are
			// numerically EQUAL. They pin that the swap is positional, not
			// value-based: an implementation that "detects two distinct
			// counters" by comparing values passes every other fixture and
			// fails only here (fileParts would stay false).
			name:    "value-coincident counters still swap",
			subject: `(13/15) - Description - "Dean M. Cole - Multiversum-Raum 02 - Gefangene der Wiederkehr (Ungekuerzt).vol015+16.par2" - 683,12 MB - yEnc (13/15)`,
			pn:      13, tp: 15, fn: 13, tf: 15,
		},
		{
			// A trailing (1/1) after yEnc is still a real segment counter, and
			// with fileParts true the article is NOT residue (crawl.go's
			// pn==1 && tp==1 && !fp) — single-segment members of multi-file
			// posts must not be counted as unparseable.
			name:    "single-segment member of a multi-file post",
			subject: `(10/16) - Description - "Christian Knieps - Chaos Vater.vol000+01.par2" - 380,33 MB - yEnc (1/1)`,
			pn:      1, tp: 1, fn: 10, tf: 16,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, pn, tp, seg, fn, tf, fp := parseSubject(tc.subject)
			if pn != tc.pn || tp != tc.tp {
				t.Errorf("segment = %d/%d, want %d/%d", pn, tp, tc.pn, tc.tp)
			}
			if fn != tc.fn || tf != tc.tf || !fp {
				t.Errorf("file = %d/%d fp=%v, want %d/%d fp=true", fn, tf, fp, tc.fn, tc.tf)
			}
			if seg != tp {
				t.Errorf("segTotal = %d, want == totalParts %d", seg, tp)
			}
			if tc.wantBase != "" && base != tc.wantBase {
				t.Errorf("base = %q, want %q (must match the OLD parse byte-for-byte)", base, tc.wantBase)
			}
		})
	}
}

// TestTwoCounterGrouping is TestMultiFileGrouping for the parenthesized form:
// volumes of one post share a base, complete per-file, and emit one <file>
// per volume — each carrying its own quoted filename, where the broken parse
// produced a single bucket under one arbitrary volume's name.
func TestTwoCounterGrouping(t *testing.T) {
	subs := []string{
		`"tiny.part01.rar" - (1/2) - yEnc (1/2)`,
		`"tiny.part01.rar" - (1/2) - yEnc (2/2)`,
		`"tiny.part02.rar" - (2/2) - yEnc (1/2)`,
		`"tiny.part02.rar" - (2/2) - yEnc (2/2)`,
	}
	var arts []stagedArticle
	bases := map[string]bool{}
	keys := map[string]bool{}
	for i, s := range subs {
		base, part, total, seg, fn, tf, fp := parseSubject(s)
		bases[base] = true
		keys[formatFieldKey(fn, part)] = true
		arts = append(arts, stagedArticle{
			MessageID: fmt.Sprintf("<%d@x>", i), Group: "a.b", BaseSubject: base,
			Subject: s, PartNum: part, TotalParts: total, SegTotal: seg,
			FileNum: fn, TotalFiles: tf, FileParts: fp,
		})
	}
	if len(bases) != 1 {
		t.Fatalf("volumes should share one base, got %d: %v", len(bases), bases)
	}
	if len(keys) != len(subs) {
		t.Fatalf("4 articles should hold 4 distinct staging keys, got %d", len(keys))
	}
	if !isComplete(arts) {
		t.Error("2 volumes x 2 segments all present should be complete")
	}
	if isComplete(arts[:3]) {
		t.Error("dropping a segment should make it incomplete")
	}
	xmlOut, _, err := buildNZB(arts)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(xmlOut), "<file "); n != 2 {
		t.Errorf("NZB should have one <file> per volume, got %d", n)
	}
	for _, name := range []string{"tiny.part01.rar", "tiny.part02.rar"} {
		if !strings.Contains(string(xmlOut), name) {
			t.Errorf("NZB lost volume filename %q", name)
		}
	}
}

func TestBuildNZBAndGzip(t *testing.T) {
	arts := []stagedArticle{
		{MessageID: "<a@x>", Subject: "rel (1/2) yEnc", Poster: "p", Bytes: 100, Group: "a.b", PartNum: 1},
		{MessageID: "<b@x>", Subject: "rel (2/2) yEnc", Poster: "p", Bytes: 120, Group: "a.b", PartNum: 2},
	}
	xmlBytes, _, err := buildNZB(arts)
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
	xmlOut, _, err := buildNZB(arts)
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

// A par2 set named for the archive volume it protects must land on the SAME
// base as that archive.
//
// Found on the demo index: one release arrived as two, a 194 MB payload and a
// 20 MB splinter, from these two subjects:
//
//	"…Big Boobs.part11.rar"  yEnc (1/10)   -> "…Big Boobs"
//	"…Big Boobs.part01.par2" yEnc (1/1)    -> "…Big Boobs.part01"
//
// reArchivePart requires a rar/7z/zip immediately after the ".partNN", so
// neither par form reaches it: ".par2" comes off via reExt and ".vol315+16"
// via reVolSuffix, leaving ".partNN" welded to the base. The par files break
// away and the set they protect loses its recovery data — the same failure the
// reVolSuffix fix addressed for the ".volNNN+NN.par2" naming, in a second
// convention that fix did not reach.
func TestParVolumeNamedForItsArchivePartSharesTheBase(t *testing.T) {
	const want = "Some.Release.Name"

	for _, subject := range []string{
		// The payload, which was always right — pinned so a change to the
		// archive rule cannot quietly move the target the par files aim at.
		`"Some.Release.Name.part11.rar" yEnc (1/10)`,
		`"Some.Release.Name.part01.rar" yEnc (1/10)`,
		// The two par namings that were not.
		`"Some.Release.Name.part01.par2" yEnc (1/1)`,
		`"Some.Release.Name.part01.vol315+16.par2" yEnc (1/9)`,
		`"Some.Release.Name.part007.vol000+001.par2" yEnc (1/2)`,
		// The plain recovery forms, already covered by reVolSuffix. Here so
		// this test states the whole rule: every par file of a release lands
		// on the release's base, whatever the poster called it.
		`"Some.Release.Name.par2" yEnc (1/1)`,
		`"Some.Release.Name.vol000+001.par2" yEnc (1/2)`,
	} {
		if got, _, _, _, _, _, _ := parseSubject(subject); got != want {
			t.Errorf("parseSubject(%s)\n  base = %q\n  want = %q", subject, got, want)
		}
	}
}

// The scoping is the risky half, so it is asserted directly: a title whose own
// last token looks like an archive marker must survive. Stripping it merges two
// halves of a film into one base — the opposite mistake, and a quieter one.
func TestPartSuffixIsNotStrippedFromNonParSubjects(t *testing.T) {
	for _, tc := range []struct{ subject, want string }{
		{`"Nymphomaniac.2013.Part2.1080p.BluRay" yEnc (1/40)`, "Nymphomaniac.2013.Part2.1080p.BluRay"},
		{`"Some.Documentary.Part2" yEnc (1/9)`, "Some.Documentary.Part2"},
		{`"Some.Release.Name.part01.mkv" yEnc (1/40)`, "Some.Release.Name.part01"},
	} {
		if got, _, _, _, _, _, _ := parseSubject(tc.subject); got != tc.want {
			t.Errorf("parseSubject(%s)\n  base = %q\n  want = %q", tc.subject, got, tc.want)
		}
	}
}

// The other half of that argument, and the one the first version of this fix
// got wrong: a par file must land on its PAYLOAD's base, which is not always
// the payload's base with ".partNN" removed.
//
// Both groups below used to group correctly and were split by a rule that took
// a trailing ".partNN" off any base derived from a subject mentioning ".par2".
// Each pair is asserted as a pair — payload and recovery deriving one base —
// because that, not the spelling of either base, is what decides whether the
// release assembles.
func TestParLandsOnItsPayloadBaseWhicheverEndsInPartNN(t *testing.T) {
	for _, group := range []struct {
		name     string
		want     string
		subjects []string
	}{
		{
			// par2cmdline and QuickPar name recovery files after the full data
			// filename, extension included. The ".part01" is the PAYLOAD's, so
			// it stays on both sides rather than coming off one.
			name: "recovery named for a .partNN media payload",
			want: "Some.Release.Name.part01",
			subjects: []string{
				`"Some.Release.Name.part01.mkv" yEnc (1/40)`,
				`"Some.Release.Name.part01.mkv.par2" yEnc (1/1)`,
				`"Some.Release.Name.part01.mkv.vol000+001.par2" yEnc (1/2)`,
			},
		},
		{
			// A real two-part release. Its ".Part2" is a title token, and the
			// recovery set of Part2 must not collapse onto Part1's base.
			name: "release whose own last token is PartN",
			want: "Some.Documentary.Part2",
			subjects: []string{
				`"Some.Documentary.Part2.mkv" yEnc (1/40)`,
				`"Some.Documentary.Part2.par2" yEnc (1/1)`,
				`"Some.Documentary.Part2.vol000+001.par2" yEnc (1/2)`,
			},
		},
		{
			// The same title in the multi-file form, where a disagreement is
			// not merely cosmetic: completeness needs j distinct file numbers
			// under ONE base, so a par on its own base means the set never
			// completes, never assembles, and expires from staging unlogged.
			name: "multi-file form of the same",
			want: "Some.Documentary.Part2",
			subjects: []string{
				`Some.Documentary.Part2 [01/10] - "video.mkv" yEnc (1/40)`,
				`Some.Documentary.Part2 [10/10] - "video.par2" yEnc (1/1)`,
			},
		},
	} {
		for _, subject := range group.subjects {
			if got, _, _, _, _, _, _ := parseSubject(subject); got != group.want {
				t.Errorf("%s\n  parseSubject(%s)\n  base = %q\n  want = %q",
					group.name, subject, got, group.want)
			}
		}
	}
}

// Part1 and Part2 of the same release must NOT share a base. Stated separately
// from the pairing test above because it is a different failure: not a release
// that fails to assemble, but two releases merged into one, with half the
// recovery data attributed to the wrong film.
func TestTwoHalvesOfAReleaseKeepSeparateBases(t *testing.T) {
	one, _, _, _, _, _, _ := parseSubject(`"Some.Documentary.Part1.par2" yEnc (1/1)`)
	two, _, _, _, _, _, _ := parseSubject(`"Some.Documentary.Part2.par2" yEnc (1/1)`)
	if one == two {
		t.Errorf("Part1 and Part2 recovery sets share base %q — two releases merged into one", one)
	}
}
