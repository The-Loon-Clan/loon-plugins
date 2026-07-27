package usenet

import "testing"

// Real subjects from four posters in alt.binaries.multimedia.anime.highspeed,
// taken from NZBs the operator pulled off another indexer after noticing those
// posters were never indexed here.
//
// Two of them put [i/j] at the START of the subject. There is no release name
// in front of it, so the base fell back to the per-file name — and because the
// yEnc byte count was never stripped, every file of a release derived a
// DIFFERENT base while still claiming total_files = j. Completeness needs j
// distinct file numbers under ONE base, so those sets never completed, never
// assembled, and expired out of staging. Nothing junked them and nothing
// logged it; the releases simply never appeared.
func TestBaseSubjectGroupsWholeRelease(t *testing.T) {
	groups := []struct {
		name     string
		wantBase string
		subjects []string
	}{
		{
			// [i/j] at the start, par2 volumes + the media file. The media
			// subject is constructed (the sample NZB held only the recovery
			// volumes) but is the same poster's form.
			name:     "TsukiHime — counter at start, size after yEnc",
			wantBase: "[Judas] Liar Game - S01E17",
			subjects: []string{
				`[1/6] - "[Judas] Liar Game - S01E17.mkv" yEnc  524288000 (1/700)`,
				`[3/6] - "[Judas] Liar Game - S01E17.vol00+01.par2" yEnc  1053788 (1/2)`,
				`[4/6] - "[Judas] Liar Game - S01E17.vol01+02.par2" yEnc  2102432 (1/3)`,
				`[5/6] - "[Judas] Liar Game - S01E17.vol03+04.par2" yEnc  4204744 (1/6)`,
				`[6/6] - "[Judas] Liar Game - S01E17.vol07+05.par2" yEnc  5253388 (1/8)`,
			},
		},
		{
			name:     "animetosho.xyz — counter at start, ten files",
			wantBase: "[SubsMix] The Ogre's Bride - 04 (S01E04) - (WEB 1080p AVC x264 AAC 2.0) _ Oni no Hanayome",
			subjects: []string{
				`[02/10] - "[SubsMix] The Ogre's Bride - 04 (S01E04) - (WEB 1080p AVC x264 AAC 2.0) _ Oni no Hanayome.mkv" yEnc  47208 (1/1)`,
				`[03/10] - "[SubsMix] The Ogre's Bride - 04 (S01E04) - (WEB 1080p AVC x264 AAC 2.0) _ Oni no Hanayome.vol000+001.par2" yEnc  687276 (1/2)`,
				`[05/10] - "[SubsMix] The Ogre's Bride - 04 (S01E04) - (WEB 1080p AVC x264 AAC 2.0) _ Oni no Hanayome.vol003+004.par2" yEnc  2654568 (1/5)`,
				`[10/10] - "[SubsMix] The Ogre's Bride - 04 (S01E04) - (WEB 1080p AVC x264 AAC 2.0) _ Oni no Hanayome.vol127+107.par2" yEnc  68817012 (1/108)`,
			},
		},
		{
			// Already worked — pinned so the fix above cannot regress the
			// common form, where the release name precedes [i/j].
			name:     "CrappySubs — release name before the counter",
			wantBase: "[CrappySubs] Sparks of Tomorrow - S01E04 (NF WEB 1080p H.264 AAC) [Dual Audio] [446E1F40]",
			subjects: []string{
				`[CrappySubs] Sparks of Tomorrow - S01E04 (NF WEB 1080p H.264 AAC) [Dual Audio] [446E1F40] [1/9] - "x.mkv" yEnc (1/1249)`,
				`[CrappySubs] Sparks of Tomorrow - S01E04 (NF WEB 1080p H.264 AAC) [Dual Audio] [446E1F40] [9/9] - "x.vol061+032.par2" yEnc (1/61)`,
			},
		},
	}

	for _, g := range groups {
		t.Run(g.name, func(t *testing.T) {
			seen := map[string]bool{}
			for _, s := range g.subjects {
				base, _, _, _, _, _, fp := parseSubject(s)
				if !fp {
					t.Errorf("expected fileParts for %q", s)
				}
				seen[base] = true
			}
			if len(seen) != 1 {
				t.Errorf("release split across %d bases, want 1:", len(seen))
				for b := range seen {
					t.Errorf("   %q", b)
				}
				return
			}
			for b := range seen {
				if b != g.wantBase {
					t.Errorf("base = %q, want %q", b, g.wantBase)
				}
			}
		})
	}
}

// The volume-suffix strip is scoped to the [i/j]-at-start branch. An ORPHAN
// par2 post has no file counter, keeps its .volNNN+NNN, and must still be
// caught by the par2_volume junk rule — otherwise stripping the suffix here
// would quietly disarm that rule and payload-free posts would index as
// releases.
func TestOrphanPar2StillJunked(t *testing.T) {
	for _, s := range []string{
		`sigma-sun.vol024+02`,
		`red-omega.vol031+14.par2`,
		`some.release.name.vol000+001.par2 yEnc (1/2)`,
	} {
		base, _, _, _, _, _, fp := parseSubject(s)
		if fp {
			t.Fatalf("%q parsed as multi-file; the orphan case needs no [i/j]", s)
		}
		if rule := whichJunkRule(base); rule != "par2_volume" {
			t.Errorf("orphan par2 %q -> base %q, junk rule %q, want par2_volume", s, base, rule)
		}
	}
}

// The byte count between the keyword and the counter must go, and only there.
func TestYencSizeStripped(t *testing.T) {
	base, part, total, _, _, _, _ := parseSubject(`[1/2] - "Show - 01.mkv" yEnc  524288000 (3/700)`)
	if base != "Show - 01" {
		t.Errorf("base = %q, want %q — the byte count must not survive", base, "Show - 01")
	}
	if part != 3 || total != 700 {
		t.Errorf("part = %d/%d, want 3/700 — the counter must still parse", part, total)
	}
	// A trailing number that is NOT a yEnc size stays: it is part of the title.
	if b, _, _, _, _, _, _ := parseSubject(`Some Release 2024 (1/5) yEnc`); b != "Some Release 2024" {
		t.Errorf("base = %q, want %q — only a post-yEnc number is a size", b, "Some Release 2024")
	}
}
