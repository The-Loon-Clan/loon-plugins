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
	reExt    = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|mov|ts|nfo|sfv|par2|rar|r\d{2,3}|nzb|zip|7z|mp3|flac|iso|img|srt|ass|jpg|png)\b`)
	reWS     = regexp.MustCompile(`\s+`)
	reQuoted = regexp.MustCompile(`"([^"]+)"`) // "filename.ext" inside a subject
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
	s = reYenc.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, `"`, " ")
	s = reExt.ReplaceAllString(s, "")
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
