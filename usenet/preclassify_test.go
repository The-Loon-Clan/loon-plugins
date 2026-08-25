package usenet

import "testing"

// The pre-filter decides junk from the subject line alone, BEFORE the builder
// pays 16.2 ms to load a set's articles. That makes it fast, and it makes a
// false positive more expensive than the full classifier's: the set is deleted
// from staging without ever being assembled, so a real release rejected here
// vanishes with no error anywhere.
//
// Two properties matter, and they are different questions.

// One: it must actually catch the volume. These subjects are real, taken from
// the live filter_hits table — the same shapes behind 3.8 BILLION long_alnum_run
// hits and 2.78 billion single_token_20 hits. If the pre-filter misses them it
// buys nothing, because they are essentially the whole queue.
func TestPreClassifyCatchesTheRealJunkVolume(t *testing.T) {
	// Title-shape rules, and between them the overwhelming majority of live junk:
	// long_alnum_run alone is past 3.8 billion hits and single_token_20 past
	// 2.78 billion.
	for _, subject := range []string{
		"717ca5652df75bcc7bc319a676970e11",            // long_alnum_run
		"PiTPW4vqdjdZ98k1YE9vZN",                      // single_token_20
		"Upload_83f2b96f-c121-4598-b16a-6cd9f5b5178b", // uuid
		"de6424f6cb66.raw",                            // alnum_blob_ext
		"Qpoz0PgLQ84hN9pv77E1.0v7j",                   // dot_sep_obfuscated
		"fast-fast",                                   // repeated_short_tok
		"IObit Uninstaller Pro 15.1.0.1 Multilingual", // software_warez
	} {
		_, rule, blocked := preClassify(subject)
		if rule == "" && !blocked {
			t.Errorf("preClassify passed %q — this shape is most of the live queue, so "+
				"missing it means the pre-filter saves nothing", subject)
		}
	}

	// And the other half of the split, which is the reason this is a three-stage
	// design rather than one cheap regex: these rules are SIZE-BANDED, so they
	// cannot fire before the articles are loaded and the pre-filter must let them
	// through. That is correct, not a miss — they are caught in stage 3 once the
	// size is known. Their combined live volume is under a million hits against
	// the billions above, which is why deferring them costs almost nothing.
	for _, subject := range []string{
		"sync_down_6e7776af.bin", // word_word_hex
		"MzFOvVwUlkvxudlzw",      // tiny_no_space
		"KwLqFgJOndQa",           // short_random_token
	} {
		title, rule, blocked := preClassify(subject)
		if rule != "" || blocked {
			t.Errorf("preClassify rejected %q as %q on an unknown size; size-banded rules must "+
				"wait for stage 3 or the verdict is a guess", subject, rule)
		}
		// Confirm stage 3 does catch them, so the deferral is a handoff and not
		// a hole in the pipeline.
		if got := whichJunkRuleSized(title, 512<<10); got == "" {
			t.Errorf("%q survived the SIZED check too — the pre-filter deferred it to nobody", subject)
		}
	}
}

// Blocked extensions are refused on the title alone, and must stay refused here
// now that this is the first code path to see a subject. These are executables
// and scripts — a release that is really an .exe must never be indexed, and the
// pre-filter is where it is now caught.
func TestPreClassifyRefusesBlockedExtensions(t *testing.T) {
	for _, subject := range []string{
		"Some.Release.1080p.exe",
		"Totally.Legit.Anime.Pack.bat",
		"setup.msi",
	} {
		title, _, blocked := preClassify(subject)
		if !blocked {
			t.Errorf("preClassify did not refuse %q (title=%q) — an executable would be assembled "+
				"and indexed as a release", subject, title)
		}
	}
	// And an ordinary release must not be caught by the extension check —
	// including BD ISOs, which are normal releases on an anime indexer (the
	// subject pipeline deliberately assembles .iso.001 splits; blocking the
	// bare extension contradicted it and depended on which split named the
	// title). Policy decided 2026-07: allow; operators junk-rule or blacklist
	// ISOs if unwanted.
	for _, subject := range []string{
		"[SubsPlease] Frieren - 12 (1080p) [B4F1A9C2].mkv",
		"Akira.1988.JPN.Blu-ray.AVC.TrueHD.5.1.REMUX.iso",
		// The Polish-language tag, title-final on the ordinary path (reExt
		// peels the media extension, exposing ".PL" at the end). The old
		// list read it as a Perl script and deleted the staged set — a
		// whole language's releases, counted only as blocked_ext.
		"Kler.2018.PL",
		"Nazwa.Filmu.2023.PL",
	} {
		if _, _, blocked := preClassify(subject); blocked {
			t.Errorf("%q was refused as a blocked extension — it is an ordinary release", subject)
		}
	}
}

// Two, and this is the one that could lose data: the pre-filter must never
// reject something the FULL classifier would have kept.
//
// It holds by construction rather than by luck — whichJunkRule passes size 0 and
// the matcher skips every size-banded rule on an unknown size, so a verdict here
// can only come from a title-shape rule that fires at any size. This pins it,
// because the property is invisible in the code and one rule gaining a size band
// would break it silently.
func TestPreClassifyNeverRejectsWhatTheFullCheckKeeps(t *testing.T) {
	subjects := []string{
		"Sousou.no.Frieren.S01E12.1080p.WEB-DL.AAC2.0.H.264-VARYG",
		"[SubsPlease] Frieren - 12 (1080p) [B4F1A9C2]",
		"[Erai-raws] Kusuriya no Hitorigoto - 24 [1080p][Multiple Subtitle]",
		"Bocchi.the.Rock.S01.1080p.BluRay.FLAC2.0.x265-ZR",
		"Attack on Titan Final Season THE FINAL CHAPTERS Special 2",
		"717ca5652df75bcc7bc319a676970e11",
		"PiTPW4vqdjdZ98k1YE9vZN",
		"de6424f6cb66.raw",
		"fast-fast",
	}
	// Sizes spanning the bands the sized rules use, so a rule that only fires in
	// one band cannot hide.
	sizes := []int64{0, 1 << 10, 1 << 20, 700 << 20, 8 << 30}

	for _, subject := range subjects {
		title, rule, blocked := preClassify(subject)
		if blocked || rule == "" {
			continue // no junk verdict to cross-check
		}
		for _, size := range sizes {
			if got := whichJunkRuleSized(title, size); got == "" {
				t.Errorf("preClassify rejected %q as %q, but the full check at size %d KEEPS it — "+
					"the pre-filter would delete a set the builder would have assembled",
					subject, rule, size)
			}
		}
	}
}

// And real releases must survive the pre-filter itself. Same guarantee
// TestJunkRules_RealReleaseNamesSurvive makes for the full engine, restated
// because this is now the code path that sees them first.
func TestPreClassifyKeepsRealReleases(t *testing.T) {
	for _, subject := range []string{
		"Sousou.no.Frieren.S01E12.1080p.WEB-DL.AAC2.0.H.264-VARYG",
		"[SubsPlease] Frieren - 12 (1080p) [B4F1A9C2]",
		"[Erai-raws] Kusuriya no Hitorigoto - 24 [1080p][Multiple Subtitle]",
		"One.Piece.1071.1080p.CR.WEB-DL.AAC2.0.H.264-VARYG",
		"Mushoku Tensei Jobless Reincarnation Season 2 Part 2",
		"Spy.x.Family.S02E12.1080p.WEB.H264-SenpaiSubs",
	} {
		_, rule, blocked := preClassify(subject)
		if rule != "" || blocked {
			t.Errorf("preClassify REJECTED the real release %q (rule=%q blocked=%v) — it would be "+
				"deleted from staging and never indexed, with no error anywhere", subject, rule, blocked)
		}
	}
}

// An explicit category tag bypasses the junk engine, exactly as prod's
// assembler does. Without this a legitimately tagged release whose name happens
// to look machine-generated would be deleted before anyone saw it.
func TestCategoryTagBypassesTheJunkEngine(t *testing.T) {
	// A 32-character hex run carrying an OVA keyword. The keyword is what
	// parseCategoryTag recognises; the hex run is what long_alnum_run objects to.
	tagged := "717ca5652df75bcc7bc319a676970e11 OVA"

	// The fixture only means something if the shape is STILL junk with the
	// keyword present — otherwise the bypass is never exercised and this test
	// passes for the wrong reason.
	title, rule, blocked := preClassify(tagged)
	if got := whichJunkRule(title); got == "" {
		t.Fatalf("fixture is tautological: %q is not junk by shape, so nothing is being bypassed", tagged)
	}
	if parseCategoryTag(title) == "" {
		t.Fatalf("fixture carries no recognised category keyword: %q", tagged)
	}
	if rule != "" || blocked {
		t.Errorf("preClassify rejected %q as %q despite its category keyword, even though the tag "+
			"is meant to bypass the junk engine — an operator's explicit categorisation must win "+
			"over a shape heuristic", tagged, rule)
	}
}

// The pre-filter's two load-bearing judgements, out of the middle of a pass that
// needs Redis, a fleet and a lease to run.
func TestSplitByTitleDropsJunkAndHonoursTheWatch(t *testing.T) {
	keys := []groupKey{
		{Group: "a.b.anime", Base: "[SubsPlease] Frieren - 12 (1080p) [B4F1A9C2]"},
		{Group: "a.b.anime", Base: "717ca5652df75bcc7bc319a676970e11"},
		{Group: "a.b.anime", Base: "One.Piece.1071.1080p.CR.WEB-DL.AAC2.0.H.264-VARYG"},
		{Group: "a.b.anime", Base: "PiTPW4vqdjdZ98k1YE9vZN"},
	}

	kept, rejects := splitByTitle(append([]groupKey(nil), keys...), false)
	if len(kept) != 2 || len(rejects) != 2 {
		t.Fatalf("kept=%d rejected=%d, want 2/2", len(kept), len(rejects))
	}
	for _, k := range kept {
		if k.Base == "717ca5652df75bcc7bc319a676970e11" || k.Base == "PiTPW4vqdjdZ98k1YE9vZN" {
			t.Errorf("junk survived into the expensive path: %q", k.Base)
		}
	}
	for _, r := range rejects {
		if r.junkRule == "" && !r.blockedExt {
			t.Errorf("%q was rejected with no reason recorded — the outcome would be unattributable", r.key.Base)
		}
		if r.title == "" {
			t.Errorf("%q rejected without a title, so filter_hits gets no sample", r.key.Base)
		}
	}

	// With a poster watch active, NOTHING may be short-circuited: attribution
	// needs the articles, and the watch exists precisely to explain why a known
	// poster's releases did not appear.
	kept, rejects = splitByTitle(append([]groupKey(nil), keys...), true)
	if len(kept) != len(keys) || len(rejects) != 0 {
		t.Errorf("with a watch active: kept=%d rejected=%d, want %d/0 — skipping the article "+
			"load here silently disables poster attribution", len(kept), len(rejects), len(keys))
	}

	if kept, rejects := splitByTitle(nil, false); len(kept) != 0 || len(rejects) != 0 {
		t.Error("an empty draw should produce nothing")
	}
}

// The fast path and the slow path must agree about what they are looking at.
// classifyRelease delegates to preClassify; this pins that it keeps delegating
// rather than growing a second copy — this package has already had three drifted
// copies of a single comparison.
func TestPreClassifyAndFullClassifyAgree(t *testing.T) {
	for _, subject := range []string{
		"[SubsPlease] Frieren - 12 (1080p) [B4F1A9C2]",
		"717ca5652df75bcc7bc319a676970e11",
		"Some.Release.With.An.Exe.exe",
		"",
		"   ",
	} {
		pTitle, _, pBlocked := preClassify(subject)
		cTitle, _, _, cBlocked := classifyRelease(subject, nil)
		if pTitle != cTitle {
			t.Errorf("%q: title differs — pre %q, full %q", subject, pTitle, cTitle)
		}
		if pBlocked != cBlocked {
			t.Errorf("%q: blocked-extension verdict differs — pre %v, full %v", subject, pBlocked, cBlocked)
		}
	}
}
