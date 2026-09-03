package usenet

import "testing"

// The studio rule exists because the token rules could not see this naming
// convention at all: of 248,761 uncategorised free.pt rows, reTagAdult and
// pornShape together caught 4,851 -- under two percent. Everything below is a
// real production title.

// The list is the measured HEAD, not the whole tail, and that boundary is
// deliberate rather than forgotten. Studios below the >=120-row cut are NOT
// covered -- "TrueAnal" (80 rows), "Facials4K" (96), "HotGirlsRaw" (82) still
// classify as empty, and no token rule can reach them either: a
// word-bounded "porn" finds no boundary inside "GirlsOnlyPorn", and an
// UNbounded "anal" would match "Canal" and "Analysis". Extending the list
// means curating a few hundred more names
// by hand, which is a decision about effort, not a bug to fix silently.
//
// No test pins the tail as empty on purpose: doing so would lock in the gap
// and fail the day somebody closes it.
func TestAdultStudioPrefixIsCategorised(t *testing.T) {
	// Titles that carry NO adult word for a token rule to find. Each one was
	// sitting on public browse uncategorised, because an empty category
	// passes the NSFW gate.
	for _, title := range []string{
		"EvilAngel - Alexis Tae",
		"FakeTaxi - Maddy Black",
		"RKPrime - Eveline Dellai Sauna Slut",
		"NebraskaCoeds - Kapri",
		"Cosmid - Gabbie Drake 1080p",
		"XES - Wendy Marvell POLISH",
		"Private - Alexa Flexy Date At The Mansion",
		"Hentaied - Emiri Momota Prison XviD",
	} {
		if got := parseCategoryTag(title); got != "Porn" {
			t.Errorf("parseCategoryTag(%q) = %q, want Porn", title, got)
		}
	}
}

// Animated adult is Hentai and live-action is Porn. They are separate
// categories on purpose, so the studio's medium decides -- not the fact that
// it is adult.
func TestAnimatedAdultStudioIsHentaiNotPorn(t *testing.T) {
	for _, title := range []string{
		"Futanari - Doe Candy",
		"Futanari - The Bordello - Darcy",
	} {
		if got := parseCategoryTag(title); got != "Hentai" {
			t.Errorf("parseCategoryTag(%q) = %q, want Hentai", title, got)
		}
	}
}

// The rule's real risk is not missing porn, it is HIDING legitimate content:
// an adult category is removed from public browse, so a false positive costs
// a member the release. These seven publishers share the "Publisher - Title"
// shape and were excluded by hand after reading their titles. If one ever
// creeps into the list, this fails.
func TestPublishersSharingTheShapeAreNotAdult(t *testing.T) {
	for _, title := range []string{
		"Elvis Presley - Another Day In Paradise (1989)(Cdm)",
		"VA - Original Album Series Punk USA (2014) 5cd",
		"Android - Week 06 2023 - APP MISC",
		"Sky Sports Formule 1 - Race Belgie",
		"Top 40 - Week 34",
		"Diverse Artiesten - Hollandse Hits",
		"MasterChef Australia - S18E34",
	} {
		if got := parseCategoryTag(title); got == "Porn" || got == "Hentai" {
			t.Errorf("parseCategoryTag(%q) = %q — a non-adult publisher was "+
				"classified adult, which HIDES it from browse", title, got)
		}
	}
}

// " - " with the spaces, not a bare prefix. Without the separator the rule
// would claim any title merely beginning with a studio's name, and "Blacked"
// would match inside a hyphenated word.
func TestStudioNeedsTheFullSeparator(t *testing.T) {
	for _, title := range []string{
		"PrivateEye Investigations S01E01 1080p",
		"Vixen-Adjacent Documentary 2024",
		"Blacked-Out Windows (2019) 1080p WEB-DL",
	} {
		if _, ok := adultStudioOf(title); ok {
			t.Errorf("adultStudioOf(%q) matched — a bare prefix is not a studio", title)
		}
	}
}

// Case is not part of the signal: posters vary it freely.
func TestStudioMatchIsCaseInsensitive(t *testing.T) {
	for _, title := range []string{
		"evilangel - alexis tae",
		"EVILANGEL - ALEXIS TAE",
		"EvilAngel - Alexis Tae",
	} {
		if got := parseCategoryTag(title); got != "Porn" {
			t.Errorf("parseCategoryTag(%q) = %q, want Porn", title, got)
		}
	}
}

// The list has to be big enough to be the thing that was measured. A
// truncated generation would still compile and still pass every case above.
func TestAdultStudioListIsComplete(t *testing.T) {
	if len(adultStudios) < 190 {
		t.Errorf("adultStudios has %d entries, want the measured ~193 — "+
			"a short list means the generator lost names", len(adultStudios))
	}
	if animated, ok := adultStudios["futanari"]; !ok || !animated {
		t.Error("futanari missing or not marked animated: Hentai would be filed as Porn")
	}
	for _, notAdult := range []string{
		"elvis presley", "va", "android", "top 40", "masterchef australia",
	} {
		if _, ok := adultStudios[notAdult]; ok {
			t.Errorf("%q is in adultStudios — it is a legitimate publisher", notAdult)
		}
	}
}
