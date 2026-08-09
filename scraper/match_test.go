package scraper

import "testing"

// An audiobook is a book, and the book source is the one that knows about it.
//
// 3xxx had no domain at all, so every audio release fell through to "" and
// matched nothing — 5,117 of them on the reference index, with a keyless
// source already registered and idle.
func TestAudiobooksResolveToTheBookDomain(t *testing.T) {
	if got := domainForCategory(3030); got != "book" {
		t.Errorf("domainForCategory(3030 Audiobook) = %q, want book", got)
	}
	// The rest of Audio is music. Open Library does not catalogue it and would
	// answer badly, so it stays unmapped rather than being pointed somewhere
	// that will confidently return the wrong thing.
	for _, cat := range []int{3010, 3040, 3050} {
		if got := domainForCategory(cat); got != "" {
			t.Errorf("domainForCategory(%d) = %q, want no domain", cat, got)
		}
	}
}
