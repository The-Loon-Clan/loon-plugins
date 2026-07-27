package usenet

import (
	"strings"
	"testing"
)

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

// The volume suffix is now stripped universally, so an orphan par2 post no
// longer looks like one by NAME. The payload-free check moved to the assembled
// set, where the question is actually answerable: a release has media files
// alongside its par2s, an orphan post does not.
//
// Sizes here are deliberately LARGE. An orphan par2 set is usually small enough
// that under_1mib/under_5mib would catch it anyway, and a test that passes for
// that reason would not be testing allRecoveryVolumes at all.
func TestPayloadFreeRecoverySetIsJunked(t *testing.T) {
	recovery := []string{
		`sigma-sun.vol024+02.par2 yEnc (1/5)`,
		`sigma-sun.vol026+04.par2 yEnc (1/9)`,
		`sigma-sun.vol030+08.par2 yEnc (1/17)`,
	}
	var arts []stagedArticle
	for _, s := range recovery {
		base, pn, tp, seg, fn, tf, fp := parseSubject(s)
		arts = append(arts, stagedArticle{
			BaseSubject: base, Subject: s, Bytes: 400 << 20, // 400 MB/file: far past every size rule
			PartNum: pn, TotalParts: tp, SegTotal: seg, FileNum: fn, TotalFiles: tf, FileParts: fp,
		})
	}
	base, _, _, _, _, _, _ := parseSubject(recovery[0])
	_, _, rule, _ := classifyRelease(base, arts)
	if rule != "par2_volume" {
		t.Errorf("all-recovery set: rule = %q, want par2_volume (size cannot be what catches this)", rule)
	}

	// The case the old by-name rule got wrong: par2 volumes belonging to a real
	// release. They must group with the media AND the set must survive.
	full := []string{
		`[1/4] - "Some Anime - 01 [1080p].mkv" yEnc  524288000 (1/700)`,
		`[2/4] - "Some Anime - 01 [1080p].vol000+001.par2" yEnc  1053788 (1/2)`,
		`[3/4] - "Some Anime - 01 [1080p].vol001+002.par2" yEnc  2102432 (1/3)`,
		`[4/4] - "Some Anime - 01 [1080p].vol003+004.par2" yEnc  4204744 (1/6)`,
	}
	arts = nil
	seen := map[string]bool{}
	for _, s := range full {
		b, pn, tp, seg, fn, tf, fp := parseSubject(s)
		seen[b] = true
		arts = append(arts, stagedArticle{
			BaseSubject: b, Subject: s, Bytes: 200 << 20,
			PartNum: pn, TotalParts: tp, SegTotal: seg, FileNum: fn, TotalFiles: tf, FileParts: fp,
		})
	}
	if len(seen) != 1 {
		t.Errorf("release split across %d bases, want 1", len(seen))
	}
	base, _, _, _, _, _, _ = parseSubject(full[0])
	title, _, rule, _ := classifyRelease(base, arts)
	if rule != "" {
		t.Errorf("release WITH media junked as %q — this is the 1.4 GB failure", rule)
	}
	if title != "Some Anime - 01 [1080p]" {
		t.Errorf("title = %q", title)
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

// Split archives: every volume is one file of ONE release. The suffix must come
// off or each volume derives its own base and the set never completes.
//
// The archive extension is required, and that requirement is load-bearing —
// measured against 430k production titles, a bare ".partNN"/".NNN" rule is not
// survivable. See the reArchivePart comment.
func TestSplitArchiveVolumesShareOneBase(t *testing.T) {
	groups := []struct {
		name     string
		wantBase string
		subjects []string
	}{
		{
			name:     "rar parts, no file counter, name continues after the archive",
			wantBase: "Voltage Fighter Gowcaizer Movie Edition Choujin Gakuen Gowkaiser",
			subjects: []string{
				`Voltage Fighter Gowcaizer Movie Edition .part002.rar Choujin Gakuen Gowkaiser (1/40)`,
				`Voltage Fighter Gowcaizer Movie Edition .part008.rar Choujin Gakuen Gowkaiser (1/40)`,
				`Voltage Fighter Gowcaizer Movie Edition .part009.rar Choujin Gakuen Gowkaiser (1/40)`,
			},
		},
		{
			name:     "rar parts, single-file form",
			wantBase: "OSHI NO KO S03_E01-E11 - Dup By Gsinnicus 1080p AV1",
			subjects: []string{
				`OSHI NO KO S03_E01-E11 - Dup By Gsinnicus 1080p AV1.part01.rar (1/421)`,
				`OSHI NO KO S03_E01-E11 - Dup By Gsinnicus 1080p AV1.part02.rar (1/421)`,
				`OSHI NO KO S03_E01-E11 - Dup By Gsinnicus 1080p AV1.part13.rar (1/421)`,
			},
		},
		{
			// A raw split of a .mp4 — the numeric suffix follows a MEDIA
			// extension, not an archive one. Also the "bytes" spelling of the
			// yEnc size, which otherwise lands in the title.
			name:     "mp4 numeric split, yEnc size written with 'bytes'",
			wantBase: "Tegami Bachi Reverse 03 Hd ,720p| ~bY AnT - [AnT]Tegami_Bachi_REVERSE_03_HD",
			subjects: []string{
				`Tegami Bachi Reverse 03 Hd ,720p|.mp4 ~bY AnT - "[AnT]Tegami_Bachi_REVERSE_03_HD.mp4.001" yEnc  24960000 bytes (1/100)`,
				`Tegami Bachi Reverse 03 Hd ,720p|.mp4 ~bY AnT - "[AnT]Tegami_Bachi_REVERSE_03_HD.mp4.002" yEnc  24960000 bytes (1/100)`,
				`Tegami Bachi Reverse 03 Hd ,720p|.mp4 ~bY AnT - "[AnT]Tegami_Bachi_REVERSE_03_HD.mp4.003" yEnc  24960000 bytes (1/100)`,
			},
		},
		{
			// 7z volumes plus the .par3 recovery set. par3 was missing from the
			// extension list, so the volume suffix underneath it was never
			// exposed and every recovery file became its own base.
			name:     "7z volumes and par3 recovery, counter at start",
			wantBase: "[YE] LIAR GAME - 16 (TVO 1280x720 x265 10bit AAC)",
			subjects: []string{
				`[1/14] - "[YE] LIAR GAME - 16 (TVO 1280x720 x265 10bit AAC).7z.001" yEnc (1/82)`,
				`[6/14] - "[YE] LIAR GAME - 16 (TVO 1280x720 x265 10bit AAC).7z.006" yEnc (1/82)`,
				`[7/14] - "[YE] LIAR GAME - 16 (TVO 1280x720 x265 10bit AAC).par3" yEnc (1/1)`,
				`[8/14] - "[YE] LIAR GAME - 16 (TVO 1280x720 x265 10bit AAC).vol00+01.par3" yEnc (1/2)`,
			},
		},
	}
	for _, g := range groups {
		t.Run(g.name, func(t *testing.T) {
			seen := map[string]bool{}
			for _, s := range g.subjects {
				base, _, _, _, _, _, _ := parseSubject(s)
				seen[base] = true
			}
			if len(seen) != 1 {
				t.Errorf("split across %d bases, want 1:", len(seen))
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

// The measured red line for the archive-part rule.
//
// The .part02 case is the discriminating one: mutating reArchivePart to the
// bare form fails it, which is what the 1,166 measured folds onto unrelated
// production titles would look like in the wild.
//
// The H.264 cases do NOT currently discriminate — reArchivePart runs before
// reExt, so the codec is not at end-of-string when the rule is applied. They
// are kept as a guard against moving a bare numeric strip later in the
// pipeline, where 120 production titles would lose their codec suffix. Calling
// that out rather than leaving them looking load-bearing.
func TestCodecSuffixSurvives(t *testing.T) {
	for _, s := range []string{
		`[ToonsHub] One Piece EP1169 1080p TVER WEB-DL AAC2.0 H.264 (1/500) yEnc`,
		`[ToonsHub] LIAR GAME S01E06 1080p AMZN WEB-DL DDP2.0 H.264 (1/900) yEnc`,
	} {
		base, _, _, _, _, _, _ := parseSubject(s)
		if !strings.HasSuffix(base, "H.264") {
			t.Errorf("base = %q, want it to still end in H.264", base)
		}
	}
	// A bare ".partNN" with no archive extension is also left alone: 1,166
	// production titles would otherwise fold onto unrelated existing ones.
	base, _, _, _, _, _, _ := parseSubject(`Some Release Name.part02 (1/9) yEnc`)
	if base != "Some Release Name.part02" {
		t.Errorf("base = %q, want the bare .part02 kept", base)
	}
}

// template_token targets placeholders a poster's tooling failed to substitute
// ({total}, {{count}}). Braces also appear in real release names as operator
// tags — {REDO}, {DVD}, {RAW} — and the case-insensitive single-brace form was
// dropping a genuine 4xDVD9 Gundam post. Placeholders are lowercase
// identifiers; tags are upper. The {{...}} form needs no such care.
func TestTemplateTokenSparesUppercaseTags(t *testing.T) {
	junk := []string{
		`Some Release {total} something`,
		`Another Release {{count}} here`,
		`x {part_num} y`,
	}
	for _, s := range junk {
		if rule := whichJunkRule(s); rule != "template_token" {
			t.Errorf("%q -> %q, want template_token", s, rule)
		}
	}
	keep := []string{
		`Mobile Suit Gundam 0079 TV Anime Legends Collection 1 (1979) R1 NTSC 4xDVD9 - Disc 4 {REDO}`,
		`Some Anime - 01 {DVD}`,
		`Another Show {RAW} 1080p`,
	}
	for _, s := range keep {
		if rule := whichJunkRule(s); rule == "template_token" {
			t.Errorf("%q dropped as template_token — that is an operator tag, not a placeholder", s)
		}
	}
}
