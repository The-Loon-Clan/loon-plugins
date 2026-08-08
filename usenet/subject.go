package usenet

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	reFileOf = regexp.MustCompile(`\[(\d+)/(\d+)\]`) // [1/12] multi-file header
	// Two more file-counter forms, both verified against 430k production titles
	// before being trusted.
	//
	// The prose form ("092 of 209", "[684 of 842]", "File 42 of 52") appears in
	// 1,453 titles, every sampled one a counter; 815 of them are really 72
	// releases split per file.
	//
	// The bare slash form ("- 127/214 -") demands TWO digits on each side, and
	// that is not cosmetic: "Ranma 1/2" is a real anime with dozens of releases
	// in the catalogue from four different groups, and a one-digit-tolerant rule
	// mangles every one. At two digits the pattern matches 2,867 titles and zero
	// Ranma. Space-delimited so the "(1/2)" segment marker cannot be mistaken
	// for it.
	reFileOfWords = regexp.MustCompile(`\[?(\d{1,4})\s+of\s+(\d{1,4})\]?`)
	reFileOfBare  = regexp.MustCompile(`(?:^|\s)(\d{2,4})/(\d{2,4})(?:\s|$)`)
	rePartOf      = regexp.MustCompile(`\((\d+)/(\d+)\)`) // (1/45) yEnc segment marker
	reYenc        = regexp.MustCompile(`(?i)\byenc\b`)
	// The byte count some posters emit between the yEnc keyword and the segment
	// counter: `... yEnc  1053788 (1/2)`. It has to be removed BEFORE the
	// keyword itself, because afterwards there is nothing to anchor on and a
	// bare number is indistinguishable from part of a title. Left in, it lands
	// in the base subject — and since every file of a release has a different
	// size, every file gets a different base and the release never groups.
	// The optional "bytes" is one poster's spelling: "yEnc  24960000 bytes (1/100)".
	// Leaving it behind does not split a release (every file carries it), but it
	// lands in the title, so it comes off with the number it belongs to.
	reYencSize = regexp.MustCompile(`(?i)\byenc\b\s+\d+(\s+bytes)?`)
	// A par2 recovery-volume suffix, once the .par2 extension is already off.
	reVolSuffix = regexp.MustCompile(`(?i)\.vol\d+\+\d+$`)
	// A par2 recovery set named for the archive VOLUME it protects, matched as
	// one unit: ".part01.par2", ".part01.vol315+16.par2".
	//
	// Neither form reaches reArchivePart below, which requires a rar/7z/zip
	// immediately after the ".partNN". The ".par2" came off via reExt and the
	// ".vol315+16" via reVolSuffix, leaving ".partNN" welded to the base — so
	// the recovery files derived their own base, broke away into a release of
	// their own, and the set they protect lost its par data. Same failure the
	// reVolSuffix comment below describes, in a second naming convention that
	// fix did not reach; 64 releases on the demo index.
	//
	// Two properties here are load-bearing, and the first version of this fix
	// had neither. It set a "this is a par file" flag from the raw subject and
	// stripped a trailing ".partNN" off the FINISHED base, which broke two
	// classes of release that used to group correctly:
	//
	//	X.part01.mkv.par2   par2cmdline's default — the recovery file is named
	//	                    after the full data filename, extension and all. The
	//	                    ".part01" here belongs to the PAYLOAD's name, and the
	//	                    payload derives "X.part01"; stripping the end of the
	//	                    par's base moved one side of a matched pair.
	//	Film.Part2.par2     a real title whose own last token is "Part2". Both
	//	                    halves' recovery sets collapsed onto "Film", merging
	//	                    two releases' par data — the opposite mistake, and a
	//	                    quieter one.
	//
	// So: ADJACENT to the par extension, not trailing — ".part01" is only an
	// archive marker when the recovery extension follows it directly. And TWO
	// digits minimum, because real split archives are padded (".part01",
	// ".part001") while a title's own "Part2" is not. All 66 subjects this
	// changes on the demo index are ".partNN" with NN two digits.
	//
	// A title ending ".Part12" WITH a par set still collides — the two forms are
	// byte-identical at that width and no rule can separate them. Two-part
	// releases number Part1/Part2, so the overlap starts where the naming
	// convention has effectively stopped.
	rePartPar = regexp.MustCompile(`(?i)\.part\d{2,4}(\.vol\d+\+\d+)?\.par[23]?\b`)
	// Split-archive suffixes: ".part07.rar", ".7z.001". Each volume of a split
	// archive is one file of ONE release, so the suffix must come off or every
	// volume derives its own base, the set never satisfies completeness, and it
	// expires from staging unlogged.
	//
	// The archive extension is REQUIRED, and that is the whole safety argument.
	// Measured against 430k production titles, the bare forms are not
	// survivable: a bare ".partNN" folds 1,166 titles onto unrelated existing
	// ones, and of the 197 titles ending ".NNN", 120 end in "H.264" — a bare
	// numeric rule applied to a finished title truncates every one to "...H".
	//
	// (The H.264 case would not bite at THIS point in the pipeline, since this
	// runs before reExt and the codec is not yet at end-of-string. It is
	// recorded because it is the reason never to move a bare numeric strip
	// later, where it would.)
	//
	// No codec suffix is ever followed by ".rar" or ".7z", so anchoring on the
	// extension keeps the rule to real split archives.
	//
	// Unanchored on purpose: one poster writes
	// "Voltage Fighter … .part008.rar Choujin Gakuen Gowkaiser (1/40)", with the
	// release name continuing AFTER the archive name. The extension requirement
	// is what makes that safe to match mid-string.
	// The numeric-split half also allows a MEDIA extension before the number:
	// "[AnT]Tegami_Bachi_REVERSE_03_HD.mp4.001" is a raw split of an .mp4, not
	// an archive. Still an extension anchor, so "H.264" is untouched — nothing
	// precedes .264 there.
	reArchivePart = regexp.MustCompile(`(?i)\.part\d{1,3}\.(rar|7z|zip)\b|\.(7z|rar|zip|tar|mkv|mp4|avi|mov|ts|iso|img)\.\d{1,3}\b`)
	reExt         = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|mov|ts|nfo|sfv|par2|par3|rar|r\d{2,3}|nzb|zip|7z|mp3|flac|iso|img|srt|ass|jpg|png)\b`)
	reWS          = regexp.MustCompile(`\s+`)
	reQuoted      = regexp.MustCompile(`"([^"]+)"`) // "filename.ext" inside a subject
)

// fileNameFromSubject pulls a display filename from an article subject: the
// quoted "file.ext" if present, else the subject with markers stripped.
func fileNameFromSubject(subject string) string {
	if m := reQuoted.FindStringSubmatch(subject); m != nil {
		if f := strings.TrimSpace(m[1]); f != "" {
			return f
		}
	}
	if b := cleanBase(stripAllMarkers(subject)); b != "" {
		return b
	}
	return strings.TrimSpace(subject)
}

// parseSubject parses a Usenet subject into the fields the crawler stages. It
// handles the three dominant forms:
//
//   - single-file:  Release.Name.ext (n/m) yEnc          -> base = release name
//   - multi-file:   Release Name [i/j] - "file" yEnc (n/m) -> base = release name
//     (the text before [i/j], shared by every file in the release, so they group
//     into ONE NZB)
//   - multi-file, parenthesized counters: "file.part01.rar" - (i/j) - yEnc (n/m)
//     and, without yEnc, [Title] (i/j) - "file.part01.rar" (n/m) — the counter
//     nearer the front is the file counter, the last one the segment counter
//     (see the measurement in the body)
//
// This is the multi-file-aware version: the base for an [i/j] release is the
// release name, not the per-file name, so completeness + assembly work at the
// release level (per-file segment counts, one <file> per file).
func parseSubject(subject string) (base string, partNum, totalParts, segTotal, fileNum, totalFiles int, fileParts bool) {
	partNum, totalParts = 1, 1

	// Bracketed form first — it is the least ambiguous. The others only get a
	// look when it is absent, so a subject carrying both cannot be misread.
	fileLoc := reFileOf.FindStringSubmatchIndex(subject)
	if fileLoc == nil {
		fileLoc = reFileOfWords.FindStringSubmatchIndex(subject)
	}
	if fileLoc == nil {
		fileLoc = reFileOfBare.FindStringSubmatchIndex(subject)
	}
	if fileLoc != nil {
		fileNum = atoi(subject[fileLoc[2]:fileLoc[3]])
		totalFiles = atoi(subject[fileLoc[4]:fileLoc[5]])
		fileParts = totalFiles > 0
	}

	// The segment marker is the last (a/b). Both placements are in the wild for
	// single-file posts:
	//
	//	Release.Name.mkv (1/45) yEnc     — counter BEFORE the keyword
	//	Release.Name.mkv yEnc (1/45)     — counter AFTER it (the common form)
	//
	// With only ONE counter, prefer the one before yEnc, and fall back to
	// scanning the whole subject when there is nothing before it: reading the
	// second form as 1/1 is not a cosmetic miss. Every segment then parses as
	// part 1 of 1, they collide on one staging key (formatFieldKey(0,1) ==
	// "0:1"), 44 of 45 articles are overwritten, and the survivor assembles as
	// a ~700 KB "release" that the size catchalls junk. That silently ate the
	// whole forward crawl for four days.
	//
	// With a counter on BOTH sides of yEnc, the one before is the FILE counter
	// and the one after the segment counter:
	//
	//	"BB520.part001.rar" - (001/225) - yEnc (100/391)
	//
	// This is measured, not assumed — an earlier comment here reasoned the
	// opposite ("anything after the keyword is a file-count indicator") and it
	// held for zero of the 288 two-counter posts in a 7.6M-article index. In
	// every measurable post the before-total is one constant, tracks the
	// .partNNN volume number with a fixed offset, and the after-totals vary
	// per file (rar vs par2) — the file-counter signature, unanimous across
	// 71k articles plus the under-20-article tail. Reading it the old way
	// staged every segment of a volume under that volume's file index: 98.1%
	// of these articles overwrote each other, and what assembled was one
	// segment per volume claiming the whole volume's size.
	//
	// The same swap applies WITHOUT yEnc when two or more counters are
	// present ([Superboys.of.Malegaon.2025] (06/23) - "….part04.rar"
	// (0683/1621)): first is the file counter, last the segment counter —
	// same evidence, its own population (9.4k articles, zero inversions).
	//
	// fromParens records that the file counter came from these rules rather
	// than a bracket match: the [i/j] base derivation below dereferences
	// fileLoc, which is nil here, and its release-name-before-the-marker rule
	// would be wrong anyway — for the leading-counter forms the text before
	// the counter is empty or a poster banner, not a release name. These
	// subjects keep the stripAllMarkers base they have always had, which is
	// what keeps their staged sets, junk memoisation and grouping history
	// stable across the change.
	//
	// For [i/j] multi-file the counter legitimately follows yEnc, so the scope
	// is the whole subject either way.
	fromParens := false
	segScope := subject
	if !fileParts {
		if loc := reYenc.FindStringIndex(subject); loc != nil {
			if rePartOf.MatchString(subject[:loc[0]]) {
				if after := subject[loc[1]:]; rePartOf.MatchString(after) {
					before := rePartOf.FindAllStringSubmatch(subject[:loc[0]], -1)
					last := before[len(before)-1]
					fileNum = atoi(last[1])
					totalFiles = atoi(last[2])
					fileParts = totalFiles > 0
					fromParens = fileParts
					segScope = after
				} else {
					segScope = subject[:loc[0]]
				}
			}
		} else if counters := rePartOf.FindAllStringSubmatch(subject, -1); len(counters) >= 2 {
			fileNum = atoi(counters[0][1])
			totalFiles = atoi(counters[0][2])
			fileParts = totalFiles > 0
			fromParens = fileParts
		}
	}
	if parts := rePartOf.FindAllStringSubmatch(segScope, -1); len(parts) > 0 {
		last := parts[len(parts)-1]
		partNum = atoi(last[1])
		totalParts = atoi(last[2])
	}
	if totalParts < 1 {
		totalParts = 1
	}
	segTotal = totalParts

	if fileParts && !fromParens {
		// Release name = the text before [i/j] (the "Title [i/j] - file" form),
		// shared by every file. Fall back to the per-file name if [i/j] is at the
		// very start.
		if release := cleanBase(subject[:fileLoc[0]]); release != "" {
			base = release
		} else {
			// [i/j] sits at the very start, so there is no release name in
			// front of it and the identity has to come from the per-file name.
			// stripAllMarkers takes the volume suffix off, so recovery volumes
			// land on the same base as the media file they protect.
			//
			// Without that, every file of the release derives a DIFFERENT base
			// — ".vol000+001", ".vol003+004", the .mkv — while each still
			// claims total_files = j. Completeness needs j distinct file
			// numbers under ONE base, so the set never completes, never
			// assembles, and expires out of staging. It is not junked and
			// nothing logs it; the release simply never appears.
			base = cleanBase(stripAllMarkers(subject))
		}
	} else {
		base = cleanBase(stripAllMarkers(subject))
	}
	if base == "" {
		base = strings.TrimSpace(subject)
	}
	return
}

// stripAllMarkers removes segment/file markers, the yEnc keyword, quotes, and a
// trailing extension — used to derive a single-file base from the whole subject.
func stripAllMarkers(s string) string {
	s = reFileOf.ReplaceAllString(s, " ")
	s = reFileOfWords.ReplaceAllString(s, " ")
	s = reFileOfBare.ReplaceAllString(s, " ")
	s = rePartOf.ReplaceAllString(s, " ")
	s = reArchivePart.ReplaceAllString(s, " ") // before reExt eats the .rar/.7z anchor
	s = rePartPar.ReplaceAllString(s, " ")     // likewise: it needs the .par2 anchor
	s = reYencSize.ReplaceAllString(s, " ")    // before reYenc — it needs the keyword
	s = reYenc.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, `"`, " ")
	s = reExt.ReplaceAllString(s, "")
	// AFTER the extension: ".vol000+001.par2" only exposes its volume suffix
	// once the .par2/.par3 is off. Universal, not scoped to one branch — a
	// recovery volume belongs to the release it protects no matter which
	// subject form the poster used, and scoping it cost three real releases
	// (one 1.4 GB) whose par2 files broke away into their own bases and left
	// the set unable to complete.
	//
	// This does mean an ORPHAN par2 post no longer looks like one by name.
	// That check moved to where it can actually be answered: a set that is
	// ENTIRELY recovery volumes is payload-free, and only the assembled set
	// knows that. See allRecoveryVolumes.
	s = reVolSuffix.ReplaceAllString(cleanBase(s), "")
	return s
}

// cleanBase collapses whitespace and trims separator punctuation from the ends.
func cleanBase(s string) string {
	s = reWS.ReplaceAllString(s, " ")
	return strings.Trim(s, " -_:.")
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
