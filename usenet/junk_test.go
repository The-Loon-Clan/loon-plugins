package usenet

import "testing"

func TestIsJunkTitle(t *testing.T) {
	junk := []string{
		"Pzz8CzBPoBNsCu8oRPpDYwESRkpq5UU3jGlzo8f7poeLWCLmU596hqnS0SA6eGPW", // the reported one
		"f2c8b393559540cfb9e33471cfda340c.par2",                            // hash + ext
		"550e8400-e29b-41d4-a716-446655440000",                             // UUID
		"OCazHDgoZua22m9UAFIHwxyz.part01.rar",                              // 24-char token + compound ext
		"n9pmKSuLKSyOP5wcDMLmnv_66qv9uJneqQusjTH4NZx_EY89VIWnGO_33zhz",     // underscore chaos
		"'qsptYFQA73GXgLh9IabcdEFGH12345678'",                              // quote-wrapped hash
		"season pack {total} files",                                        // template token mid-string
		"QTVxBgZmUbZnAJFWgJq6",                                             // 20-char bare mixed-case token (under 24)
		"OF6OfeYgrXyHQjpiLstb",                                             // 20-char bare token w/ digit
		"sigma-sun.vol024+02",                                              // orphan PAR2 recovery volume, no .par2 tail
		"",                                                                 // empty
		// Desktop-app releases: a 3-4 component version string running into
		// Multilingual/Portable, or the pair without a version. Reported
		// 2026-08-24 (release 176408827) after slipping the name list.
		"4K.Video.Downloader.v4.23.1.5220.Multilingual.Portable",
		"Wondershare Filmora 13.5.2.4444 Multilingual",
		"SomeApp Multilingual Portable x86",
	}
	for _, s := range junk {
		if !isJunkTitle(s) {
			t.Errorf("isJunkTitle(%q) = false, want true", s)
		}
	}

	legit := []string{
		"[SubsPlease] Frieren - 12 (1080p) [ABCD1234].mkv",
		"Spy x Family S02E05 1080p WEB H264-Group",
		"Kaguya-sama.Love.is.War.S03.1080p.BluRay.x265-RARBG",
		"One Piece 1085 [720p]",
		"My Hero Academia - 138 VOSTFR",
		"Macross Frontier Vol.02 1080p BluRay", // Vol.NN disc numbering is not a .volNNN+NN recovery marker
		"Neon Genesis Evangelion vol1",         // bare volN without +blocks
		"Persona 3 Portable [PSP]",             // Portable in a game NAME: no version adjacency, no Multilingual pairing
		"[Judas] Frieren - 01v2 (1080p)",       // anime vN markers never grow three dots
		"Initial D 5.1.2ch DTS BluRay",         // two-dot audio layout is not a version string
	}
	for _, s := range legit {
		if isJunkTitle(s) {
			t.Errorf("isJunkTitle(%q) = true, want false (legit release)", s)
		}
	}
}
