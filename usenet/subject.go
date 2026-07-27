package usenet

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	reFileOf = regexp.MustCompile(`\[(\d+)/(\d+)\]`) // [1/12] multi-file header
	rePartOf = regexp.MustCompile(`\((\d+)/(\d+)\)`) // (1/45) yEnc segment marker
	reYenc   = regexp.MustCompile(`(?i)\byenc\b`)
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
// handles the two dominant yEnc forms:
//
//   - single-file:  Release.Name.ext (n/m) yEnc          -> base = release name
//   - multi-file:   Release Name [i/j] - "file" yEnc (n/m) -> base = release name
//     (the text before [i/j], shared by every file in the release, so they group
//     into ONE NZB)
//
// This is the multi-file-aware version: the base for an [i/j] release is the
// release name, not the per-file name, so completeness + assembly work at the
// release level (per-file segment counts, one <file> per file).
func parseSubject(subject string) (base string, partNum, totalParts, segTotal, fileNum, totalFiles int, fileParts bool) {
	partNum, totalParts = 1, 1

	fileLoc := reFileOf.FindStringSubmatchIndex(subject)
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
	// Prefer the one before yEnc, because for the first form anything after the
	// keyword is a file-count indicator rather than a segment counter. Fall back
	// to scanning the whole subject when there is nothing before it: that is the
	// second form, and reading it as 1/1 is not a cosmetic miss. Every segment
	// then parses as part 1 of 1, they collide on one staging key
	// (formatFieldKey(0,1) == "0:1"), 44 of 45 articles are overwritten, and the
	// survivor assembles as a ~700 KB "release" that the size catchalls junk.
	// That silently ate the whole forward crawl for four days.
	//
	// For [i/j] multi-file the counter legitimately follows yEnc, so the scope is
	// the whole subject either way.
	segScope := subject
	if !fileParts {
		if loc := reYenc.FindStringIndex(subject); loc != nil && rePartOf.MatchString(subject[:loc[0]]) {
			segScope = subject[:loc[0]]
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

	if fileParts {
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
	s = rePartOf.ReplaceAllString(s, " ")
	s = reArchivePart.ReplaceAllString(s, " ") // before reExt eats the .rar/.7z anchor
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
