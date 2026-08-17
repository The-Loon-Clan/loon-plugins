package usenet

import "testing"

// The cracked_software rule, from three member malware reports.
//
// All three titles slipped past software_warez, which matches curated product
// NAMES and can therefore only ever catch software somebody already listed.
// This rule matches the SHAPE — keygen, patcher, "full game free", a named .exe
// — so it catches the next one without an operator adding it first.
//
// The file check that produced these: two of the three were 10 MB. A "full game
// free" that size is a dropper stub, not a game.
func TestJunkCatchesCrackedSoftware(t *testing.T) {
	junk := []string{
		// The reported ones, verbatim from production.
		"DEAD SPACE 2 ( full game free ) CRACK and keygen included",
		"Dead Space 2 Online Steam Patcher v 0.1 Dead Space 2 Online",
		"(Internet Download Manager 6.42 Build 34 Multilingual + Retail)",
		// The same shapes, other products.
		"Some.App.v3.2.1.Cracked.and.Keygen",
		"Photoshop 2026 activator",
		"GameName.RePack.by.SomeGuy",
		"Setup.exe",
		"installer.msi",
		"Origin Emu fix",
		"Total Commander serial key",
		// The 2026-08-17 report: repack-and-portable + N-bit arch markers,
		// which a 3-part version and no "by" slipped past everything.
		"MediaHuman YouTube Downloader 3.9.20 (0605) (Repack & Portable) 64-bit",
		"SomeApp 2.1 Repack and Portable 32bit",
	}
	for _, title := range junk {
		if got := whichJunkRule(title); got == "" {
			t.Errorf("NOT caught: %q", title)
		}
	}
}

// The expensive half. A false positive here deletes a real release, and these
// are the titles most likely to trip a careless crack/keygen pattern: a TV
// series literally called Cracker, a film with "Crack-Up" in the name, and the
// word "COMPLETE" which sits next to "crack" in a lot of naive rules.
func TestJunkDoesNotCatchRealTitlesThatLookLikeCracks(t *testing.T) {
	real := []string{
		"Cracker (1993) S01E01",
		"The Crack-Up 1936 DVDRip",
		"Jack Ryan S01 COMPLETE 1080p",
		"Call Of The Night (2022) S02 1080p BluRay REMUX FLAC 2 0 AVC-iVy",
		"[Erai-raws] Yofukashi no Uta - 01 [1080p][Multiple Subtitle]",
		"Supernatural.S12E21.720p.HDTV.x264-SVA[eztv].mkv",
		"Euphoria.US.S03E06.Stand.Still.and.See.1080p.WEB-DL.DDP5.H264-iND",
		"Kaiju No 8 S02E01 1080p WEB-DL AAC2 0 H 264",
		"Cowboy Bebop - Complete Series [1080p]",
		// Guards for the repack-and-portable + N-bit rules: scene REPACK
		// tags are legit re-releases, "Portable" is a real title word
		// (PSP-era adaptations), and anime bit-depth markers are 8/10-bit,
		// never 32/64.
		"Frieren S01E12 REPACK 1080p WEB H.264",
		"Persona 3 Portable The Movie 1080p BluRay",
		"[Group] Some Show - 05 (Hi10P 10-bit 1080p)",
	}
	for _, title := range real {
		if got := whichJunkRule(title); got == "cracked_software" {
			t.Errorf("FALSE POSITIVE — the cleanup job DELETES this: %q", title)
		}
	}
}

// The four releases members reported as malware that are NOT malware: ordinary
// TV episodes flagged with the wrong reason. Deleting them as cracked software
// would turn a mislabelling into data loss.
func TestJunkSparesTheMislabelledReports(t *testing.T) {
	for _, title := range []string{
		"Supernatural, Season 1 Supernatural S13E05.iNTERNAL. WEB x264",
		"Supernatural, Season 1 Supernatural S12E21. x264-SVA[eztv]",
		"Supernatural, Season 1 Supernatural S12E23. x264-AVS[eztv]",
		"Euphoria US S03E06.Stand Still and See. DDP5.H264-iND",
	} {
		if got := whichJunkRule(title); got == "cracked_software" {
			t.Errorf("a mislabelled-but-real release was caught as cracked software: %q", title)
		}
	}
}
