package usenet

import (
	"regexp"
	"testing"
)

// Fixtures for the Hentai (animated) / Porn (live-action) split.
//
// MIRROR: this file is duplicated as
// Indexer/indexer-site/pkg/services/nzb_tag_service_test.go. The two
// parseCategoryTag implementations are byte-identical on purpose (see the
// comment above the classifier in tags.go), so their fixtures are too — a test
// that only exists on one side stops proving the mirrors agree the moment the
// other side drifts.
//
// EVERY TITLE BELOW IS A REAL PRODUCTION TITLE unless marked synthetic. They
// were read out of the live index while the rules were being measured; inventing
// plausible-looking ones is how a rule set ends up passing its own tests and
// failing the corpus.

// isHiddenCategory mirrors storage.IsAdultCategory / storage.AdultCategories in
// the host repo, which the plugin cannot import. Keep the two in step: this is
// the list the NSFW gate hides, and the whole safety argument for the split is
// that a row can only ever move BETWEEN these two values, never out of them.
func isHiddenCategory(c string) bool { return c == "Hentai" || c == "Porn" }

type categoryCase struct {
	title string
	want  string
	why   string
}

var categorySplitCases = []categoryCase{
	// ── Porn: adult with no evidence of animation ────────────────────────
	{
		"Foxy.Lady.01.XXX.PAL.DVDR", "Porn",
		"scene XXX tag, no animation evidence anywhere in the title",
	},
	{
		"Debut.JAV.Uncensored.DVDRip.x264", "Porn",
		"live-action Japanese AV; 'Uncensored' is deliberately NOT a signal on its own",
	},
	{
		"1pondo.tv - Maki Hojo - I Love It, When A Lot Of Them (JAV) [SD, 540p]", "Porn",
		"JAV plus the [SD, 540p] quality tail",
	},
	{
		"Bargained.For.XXX.iMAGESET-YAPG", "Porn",
		"photo set",
	},
	{
		"12.Nora.Davis.iMAGESET-GGW", "Porn",
		"NEW COVERAGE: carries no adult keyword at all, so before the split it was " +
			"uncategorised and therefore VISIBLE",
	},
	{
		"Mila Jade - Amateur Fucking [Nubiles-Porn] (2015|FullHD|1920x1080)", "Porn",
		"the (year|quality|WxH) pipe tail",
	},
	{
		"Khaleesis Cunt A XXX Game Of Thrones Parody And Other Porn Parodies (2016/DVDRip)", "Porn",
		"the (year/quality) slash variant of the pipe tail",
	},
	{
		"MILF Pact (2017//FullHD)", "Porn",
		"the double-slash form of the same tail",
	},
	{
		"Agent Porn - Sarah Kay - Hardcore [FullHD ]", "Porn",
		"the BARE [FullHD ] tail, reached only because a porn token gates it",
	},
	{
		"1By-Day Porn - Eveline Dellai - The Prettiest Pink - Teens Beautiful Slit Requests Her [SD 540p]", "Porn",
		"the quality tail in its resolution form",
	},
	{
		"19 The Best Asian cumshot compilation (shemale) [HD, ]", "Porn",
		"keyword plus the comma tail",
	},
	{
		"1H4v34W1f3 - Jaclyn Taylor - Sexy Big Tits (Milf) [SD, 360p]", "Porn",
		"keyword plus the comma-and-resolution tail",
	},
	{
		"Linda Lay - First Porno Casting [CastingCouch-X] (2015|FullHD|1920x1080)", "Porn",
		"'porno' keyword and the pipe tail agree",
	},
	{
		"Gangbang Fantasies (2016//FullHD)", "Porn",
		"keyword and slash tail agree",
	},
	{"XXX Tribute Compilation 2024", "Porn", "synthetic; was tagged Hentai before the split"},
	{"Sextape Leak 2024", "Porn", "synthetic; was tagged Hentai before the split"},

	// ── Hentai: adult WITH evidence of animation ─────────────────────────
	{
		"Bible Black La Noche De Walpurgis HENTAi GERMAN.2001.DL. x264-GOREHOUNDS", "Hentai",
		"genuine hentai anime; the word clause is what keeps the German scene releases off the Porn shelf",
	},
	{
		"Discipline HENTAi GERMAN.2003.DL. x264-GOREHOUNDS", "Hentai",
		"same family, same clause",
	},
	{
		"Missbraucht.von.dreckigen.Haenden.German.Anime.XXX.DVDRiP.XViD-HENTAiRAPE", "Hentai",
		"carries the XXX scene tag like western porn; '.Anime.' is the only thing that distinguishes it",
	},
	{
		"Nikutai.Teni.Koerpertausch.E01.German.2004.HENTAi.XXX.DVDRiP.x264-3MiNA", "Hentai",
		"same shape as the western scene rips it was posted alongside",
	},
	{
		"Tsujimura Umeko, ms-pictures - Shoujo-tachi no Sadism The Animation - ep. 1 (Hentai) [SD, ]", "Hentai",
		"posted in the EXACT (Hentai) [SD, ] shape the flood spam uses; only the words separate them",
	},
	{
		"Amelialtie - Ano natsu kimi to pool de (3D Hentai) [HD, ]", "Hentai",
		"3D hentai, same tube-rip tail as the spam",
	},
	{
		"[Group] Some Hentai Title - 01 [1080p].mkv", "Hentai",
		"synthetic; the ordinary fansub shape",
	},
	{
		"Show OVA Hentai Special", "Hentai",
		"synthetic; adult outranks the Anime marker, so a title that is both stays behind the gate",
	},
	{
		"[JAV] Subtitle Pack", "Hentai",
		"ACCEPTED IMPERFECTION: a leading '[' is fansub evidence, so this lands on the wrong shelf. " +
			"Both shelves are hidden, so the cost is tidiness, not exposure",
	},
	{
		"My.Porn.Collection.S01E01.WEB-DL.mkv", "Hentai",
		"synthetic; ACCEPTED IMPERFECTION — the SxxExx clause outvotes the porn keyword " +
			"(same shape as 'Future Man S01E06 - A Blowjob Before Dying')",
	},
	{
		"Laid-Back Camp S03E08 The Porn Begins AAC2.0 H 264 DUAL-VARYG (Yuru Camp△ Season 2, Dual-Audio, Multi-Subs)", "Hentai",
		"UNCHANGED BY THE SPLIT: 'The Porn Begins' is a genuine episode title, and the frozen keyword " +
			"regex already filed it Hentai. The split may not narrow that regex, so all it can do is " +
			"keep the SxxExx clause pointed at the right shelf — see TestPornLexiconIsAGateNotARule",
	},

	// ── Anime: an explicit animated-format marker, nothing adult ─────────
	{
		"Riding.Bean.OVA.German.Anime.FS.DVDRip.x264.iNTERNAL-3MiNA", "Anime",
		"a real OVA posted by the same German group as the hentai releases above",
	},
	{"My.Hero.Academia.S03E01.OVA.1080p", "Anime", "synthetic; OVA marker, nothing adult"},
	{"[Group] Demon Slayer Gekijouban [1080p]", "Anime", "synthetic; Gekijouban marker"},
	{"S7rEx773 OVA", "Anime", "synthetic; the junk-engine bypass fixture"},

	// ── "": not classified. These are the measured false positives the ───
	// ── rules were shaped around; each one cost real content when the ────
	// ── obvious version of the rule was tried.
	{
		"Suzume no Tojimari (2022) [iTunes] [HD ]", "",
		"mehsubs ends titles with a bare [HD ]. Ungating the tail rule put 20 of these on the Porn shelf",
	},
	{
		"Black.Jack.1977.DVDRip.x264-HANDJOB", "",
		"-HANDJOB is a real RELEASE GROUP, which is why the porn lexicon is only ever a gate",
	},
	{
		"Animal.Farm.1999.DVDRip.x264-HANDJOB", "",
		"same release group, second title",
	},
	{
		"Return to Space.2022. WEB H265-JAVLAR", "",
		"-JAVLAR is a release group: \\bjav\\b needs the boundary that 'JAVLAR' does not give it",
	},
	{"[HorribleSubs] xxxHOLiC - 01 [1080p].mkv", "", "no boundary between 'xxx' and 'HOLiC'"},
	{"xxxHOLiC.S01E01.WEB-DL", "", "same, and the case-sensitive rule does not reach it either"},
	{"Java.Programming.Tutorial.S01E01", "", "'Java' is not \\bjav\\b"},
	{"[Group] Bleach S01E01 [1080p].mkv", "", "an ordinary release declares nothing"},
}

func TestParseCategoryTagSplitsAdultByAnimation(t *testing.T) {
	for _, c := range categorySplitCases {
		if got := parseCategoryTag(c.title); got != c.want {
			t.Errorf("parseCategoryTag(%q)\n  got  %q\n  want %q\n  why: %s", c.title, got, c.want, c.why)
		}
	}
}

// oldAdultPattern is the adult keyword regex EXACTLY as it stood before the
// Hentai/Porn split. Every title it matched was filed "Hentai" and hidden by the
// NSFW gate, so it is the definition of "what is hidden today".
const oldAdultPattern = `(?i)\b(porn|porno|hentai|jav|xxx|sextape|cumshot|gangbang|milf)\b`

// TestOldAdultTitlesStayHidden is THE test this change exists to satisfy.
//
// The split's only real hazard is not a wrong shelf, it is a row falling out of
// the adult branch entirely: "Hentai" and "Porn" are both hidden, "" is not. A
// narrowing bug therefore does not mis-file content, it PUBLISHES it — and
// nothing panics, nothing errors and nothing logs when that happens. The only
// way to notice is to assert it.
//
// Two halves, because either alone can be satisfied while the invariant breaks:
//
//   - the frozen regex is still the frozen regex (a keyword quietly dropped from
//     reTagAdult un-hides every title that carried only that keyword);
//   - every title the frozen regex matches still resolves to a HIDDEN category
//     (the union and the branch order still hold, whatever is added around them).
func TestOldAdultTitlesStayHidden(t *testing.T) {
	if reTagAdult.String() != oldAdultPattern {
		t.Fatalf("reTagAdult was changed:\n  now %s\n  was %s\n"+
			"It is frozen deliberately. Narrowing it does not relabel rows, it makes hidden "+
			"rows VISIBLE. New detection belongs in pornShape, which is unioned with this "+
			"and can only add.", reTagAdult.String(), oldAdultPattern)
	}
	old := regexp.MustCompile(oldAdultPattern)

	// One title per keyword, so a single keyword going missing is caught by
	// name rather than by luck of the fixture list.
	perKeyword := map[string]string{
		"porn":     "Agent Porn - Sarah Kay - Hardcore [FullHD ]",
		"porno":    "Linda Lay - First Porno Casting [CastingCouch-X] (2015|FullHD|1920x1080)",
		"hentai":   "Bible Black La Noche De Walpurgis HENTAi GERMAN.2001.DL. x264-GOREHOUNDS",
		"jav":      "Debut.JAV.Uncensored.DVDRip.x264",
		"xxx":      "Foxy.Lady.01.XXX.PAL.DVDR",
		"sextape":  "Sextape Leak 2024",
		"cumshot":  "19 The Best Asian cumshot compilation (shemale) [HD, ]",
		"gangbang": "Gangbang Fantasies (2016//FullHD)",
		"milf":     "1H4v34W1f3 - Jaclyn Taylor - Sexy Big Tits (Milf) [SD, 360p]",

		// The two spellings the shape rules deliberately do NOT claim. They are
		// in here precisely because the shape rules spare them: whatever hides
		// them today is the frozen keyword regex alone, so they are the fixtures
		// most exposed by a narrowing bug.
		"xXx (Vin Diesel)":    "xXx Triple X.2002.German DL. x264.iNTERNAL-1aQuali",
		"xXx (Xander Cage)":   "xXx.Die.Rueckkehr.des.Xander.Cage.2017.German.MD.x264-MULTiPLEX",
		"a real episode name": "Laid-Back Camp S03E08 The Porn Begins AAC2.0 H 264 DUAL-VARYG (Yuru Camp△ Season 2, Dual-Audio, Multi-Subs)",
	}
	for keyword, title := range perKeyword {
		if !old.MatchString(title) {
			t.Fatalf("fixture drift: %q no longer matches the OLD adult regex, so it proves "+
				"nothing about %s", title, keyword)
		}
		if got := parseCategoryTag(title); !isHiddenCategory(got) {
			t.Errorf("REGRESSION (%s): %q was hidden before the split and parseCategoryTag now "+
				"answers %q, which is NOT hidden. This publishes the row.", keyword, title, got)
		}
	}

	// And the same assertion over every shared fixture, so anything added to the
	// table above is covered by the invariant automatically.
	for _, c := range categorySplitCases {
		if !old.MatchString(c.title) {
			continue
		}
		if got := parseCategoryTag(c.title); !isHiddenCategory(got) {
			t.Errorf("REGRESSION: %q matched the OLD adult regex (so it is hidden today) but "+
				"parseCategoryTag now answers %q", c.title, got)
		}
	}
}

// The XXX rule enumerates seven spellings so it can omit exactly one. Losing
// that would delete a film trilogy from an anime index, which is why the
// enumeration is worth a test of its own rather than a comment.
func TestXXXRuleSparesTheVinDieselFilms(t *testing.T) {
	films := []string{
		"xXx 2002",
		"xXx Triple X.2002.German DL. x264.iNTERNAL-1aQuali",
		"xXx.2.The Next Level.2005.German DL. x264.iNTERNAL-1aQuali",
		"xXx.Die.Rueckkehr.des.Xander.Cage.2017.German.MD.x264-MULTiPLEX",
		"xXx Return of Xander Cage.2017. .10Bit X265.DD.5.1-Chivaman",
	}
	// The rule the enumeration replaced. If this does NOT match, the fixtures
	// have drifted and the test below is proving nothing.
	caseInsensitive := regexp.MustCompile(`(?i)\b(xxx)\b`)

	for _, title := range films {
		if !caseInsensitive.MatchString(title) {
			t.Fatalf("fixture drift: %q would not have been caught by a case-insensitive rule "+
				"either, so it does not demonstrate why the enumeration exists", title)
		}
		if pornShape(title) {
			t.Errorf("pornShape claimed the Vin Diesel film %q. The XXX rule lists seven "+
				"spellings so that 'xXx' is excluded; a case-insensitive \\bxxx\\b takes the "+
				"whole trilogy (26 titles / 236 rows on the spike day alone).", title)
		}
	}

	// What the classifier as a whole still answers, stated so nobody reads the
	// assertion above as "these are visible". They are not: the FROZEN keyword
	// regex is case-insensitive and has always matched 'xXx', so these films are
	// filed adult today and the invariant forbids changing that here — un-hiding
	// them would be a separate, deliberate change to reTagAdult, reviewed on its
	// own. What this change does buy is that the operator BACKFILL sweep uses the
	// case-sensitive rule, so it does not move these rows.
	for _, title := range films {
		if got := parseCategoryTag(title); !isHiddenCategory(got) {
			t.Errorf("parseCategoryTag(%q) = %q; the frozen keyword regex still matches 'xXx', "+
				"so this must stay in a hidden category until reTagAdult itself is changed", title, got)
		}
	}
}

// A bare [HD ] / [SD ] tail is a mehsubs convention, not a porn signal. Ungated
// it produced 20 false positives on a 117,955-title anime corpus.
func TestBareQualityTailSparesMehsubs(t *testing.T) {
	mehsubs := []string{
		"Suzume no Tojimari (2022) [iTunes] [HD ]",
		"Uzumaki (2024) [mehsubs] [Dual Audio - AACx2] [HD ]",
		"Papillon Rose: New Season [mehsubs] [SD ]",
	}
	for _, title := range mehsubs {
		if pornShape(title) {
			t.Errorf("pornShape claimed the mehsubs release %q — the bare tail rule must stay "+
				"gated on a porn token", title)
		}
	}
	// The gate is what makes the tail usable, so prove it still fires when the
	// token IS present. Otherwise this test passes just as well against a rule
	// that was deleted.
	if !pornShape("Agent Porn - Sarah Kay - Hardcore [FullHD ]") {
		t.Error("the gated bare-tail rule stopped firing; it is worth 3,567 rows and the " +
			"mehsubs test above no longer means anything without it")
	}
}

// The porn lexicon is a GATE inside the tail rule and never a rule of its own:
// standalone it matched 84 titles on the anime corpus, 9 of them real.
func TestPornLexiconIsAGateNotARule(t *testing.T) {
	// A release GROUP named after a lexicon word, and a real episode title.
	// Neither has a quality tail, so neither may reach pornShape.
	lexiconButNotPorn := []string{
		"Black.Jack.1977.DVDRip.x264-HANDJOB",
		"Animal.Farm.1999.DVDRip.x264-HANDJOB",
		"10.30.P.M.Summer.1966.DVDRip.x264-HANDJOB",
		"Laid-Back Camp S03E08 The Porn Begins AAC2.0 H 264 DUAL-VARYG (Yuru Camp△ Season 2, Dual-Audio, Multi-Subs)",
	}
	for _, title := range lexiconButNotPorn {
		if pornShape(title) {
			t.Errorf("pornShape claimed %q on a lexicon word alone — the lexicon is only "+
				"meaningful in front of a $-anchored quality tail", title)
		}
	}

	// Laid-Back Camp is the one that still comes back adult, and it is the
	// frozen keyword regex doing it, not anything added here. Assert the real
	// behaviour rather than the behaviour we would prefer: the split is not
	// allowed to narrow reTagAdult, so the most it can do is keep the episode
	// off the live-action shelf.
	const laidBackCamp = "Laid-Back Camp S03E08 The Porn Begins AAC2.0 H 264 DUAL-VARYG (Yuru Camp△ Season 2, Dual-Audio, Multi-Subs)"
	if got := parseCategoryTag(laidBackCamp); got != "Hentai" {
		t.Errorf("parseCategoryTag(%q) = %q, want \"Hentai\" — unchanged from before the split. "+
			"\"Porn\" would mean the SxxExx clause stopped working; \"\" would mean reTagAdult "+
			"was narrowed, which this change may not do.", laidBackCamp, got)
	}
}

// The point of pornShape is coverage the keyword regex never had: image sets and
// site rips announce nothing, so they sat in the unclassified bucket, which is
// VISIBLE.
func TestPornShapeAddsCoverageOverTheKeywordRegex(t *testing.T) {
	old := regexp.MustCompile(oldAdultPattern)
	newlyHidden := []string{
		"12.Nora.Davis.iMAGESET-GGW",
		"BD25.eu Siterip - Complete Collectie - BD25 - Dump",
	}
	for _, title := range newlyHidden {
		if old.MatchString(title) {
			t.Fatalf("fixture drift: %q already matched the keyword regex, so it does not "+
				"demonstrate added coverage", title)
		}
		if got := parseCategoryTag(title); !isHiddenCategory(got) {
			t.Errorf("parseCategoryTag(%q) = %q; this row was visible before the split and "+
				"the shape rules are what should hide it", title, got)
		}
	}
}

// animeEvidence decides a shelf, never visibility. Pinned because the tempting
// "optimisation" — hoisting it out to skip work on non-adult titles — turns a
// shelf choice into a visibility decision.
func TestAnimeEvidenceOnlyEverChoosesAShelf(t *testing.T) {
	for _, c := range categorySplitCases {
		adult := reTagAdult.MatchString(c.title) || pornShape(c.title)
		got := parseCategoryTag(c.title)
		if adult && !isHiddenCategory(got) {
			t.Errorf("%q is adult by the union but parseCategoryTag answered %q", c.title, got)
		}
		if !adult && isHiddenCategory(got) {
			t.Errorf("%q is not adult by the union but parseCategoryTag answered %q", c.title, got)
		}
	}
}
