package scraper

import (
	"context"
	"fmt"
	"time"

	"github.com/the-loon-clan/loon/catalog"
)

// runMatch enriches host-provided release candidates: for each, pick the source
// whose domain matches the release's category, match its title (Searcher.Search
// for API sources, TitleIndex/FindByTitle for local-index ones), upsert the
// entry, and link the release to its cover. Sources with no key/index simply
// return no match, so the job is a safe no-op until one is configured.
func (p *Plugin) runMatch(ctx context.Context) {
	if ctx == nil {
		return
	}
	p.matchJob.SetRunning()
	if deps.Candidates == nil || deps.Sink == nil {
		p.matchJob.Log("no candidate provider wired — skipping")
		p.matchJob.SetIdle(time.Now().Add(p.fillInterval()))
		return
	}
	cands, err := deps.Candidates(ctx)
	if err != nil {
		p.matchJob.SetError(err.Error())
		p.core.Errors.Report(ctx, "scraper/match-candidates", err)
		return
	}
	matched := 0
	for _, c := range cands {
		// Every early return after SetRunning must SetIdle, or the job
		// reads as running forever and stalls the host's shutdown drain.
		if ctx.Err() != nil {
			p.matchJob.SetIdle(time.Now().Add(p.fillInterval()))
			return
		}
		src := p.sourceForCategory(c.Category)
		if src == nil {
			continue
		}
		entry, ok := matchOne(ctx, src, c.Title)
		if !ok {
			continue
		}
		if err := deps.Sink.Upsert(ctx, entry); err != nil {
			p.core.Errors.Report(ctx, "scraper/match-upsert", fmt.Errorf("release %d: %w", c.ID, err))
			continue
		}
		if entry.CoverURL != "" && deps.Link != nil {
			_ = deps.Link(ctx, c.ID, entry.CoverURL)
		}
		matched++
	}
	p.matchJob.Log("catalog match: %d of %d candidate(s) enriched", matched, len(cands))
	p.matchJob.SetIdle(time.Now().Add(p.fillInterval()))
}

// sourceForCategory returns the enabled source whose domain covers a Newznab
// category, or nil.
func (p *Plugin) sourceForCategory(cat int) catalog.MetadataSource {
	key := domainForCategory(cat)
	if key == "" || !p.enabled(key) {
		return nil
	}
	s, _ := p.registry.ByKey(key)
	return s
}

// domainForCategory maps a Newznab category to a catalog domain key.
func domainForCategory(cat int) string {
	switch {
	case cat == 5070:
		return "anime"
	case cat/1000 == 6:
		return "xxx"
	case cat/1000 == 2:
		return "movie"
	case cat/1000 == 5:
		return "tv"
	// Books (7xxx). Everything in the tree is a book domain lookup: Mags,
	// Ebook, Comics and Technical all resolve against the same catalogue, and
	// splitting them would need three sources to answer one question.
	//
	// Added when the site started collecting more than video. Until then these
	// categories fell through to "" — no domain, so no match, so no cover, and
	// nothing anywhere said why. A release simply had no art forever.
	case cat/1000 == 7:
		return "book"
	// An audiobook IS a book, and the book source is the one that knows about
	// it. 3xxx had no domain at all, so every audio release fell through to ""
	// and matched nothing — 5,117 of them on the reference index, with a
	// keyless source already registered and idle.
	//
	// Only 3030. The rest of Audio is music, which Open Library does not
	// catalogue and would answer badly.
	case cat == 3030:
		return "book"
	}
	return ""
}

// matchOne resolves a release title to a catalog entry via the source's
// capabilities: Searcher (query) for API sources, else the normalized
// TitleIndex, else an optional live TitleFinder.
func matchOne(ctx context.Context, src catalog.MetadataSource, title string) (catalog.CatalogEntry, bool) {
	if searcher, ok := src.(Searcher); ok {
		e, found, err := searcher.Search(ctx, title)
		if err != nil || !found {
			return catalog.CatalogEntry{}, false
		}
		return e, true
	}
	norm := src.Normalize(title)
	if idx, err := src.TitleIndex(ctx); err == nil {
		if id, ok := idx[norm]; ok {
			if e, err := src.Fetch(ctx, id); err == nil {
				return e, true
			}
		}
	}
	if tf, ok := src.(catalog.TitleFinder); ok {
		if id, ok := tf.FindByTitle(title); ok {
			if e, err := src.Fetch(ctx, id); err == nil {
				return e, true
			}
		}
	}
	return catalog.CatalogEntry{}, false
}
