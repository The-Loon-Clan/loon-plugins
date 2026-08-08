package wiki

import (
	"context"
	"html/template"
	"log"
	"regexp"
	"strings"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// Live blocks embedded in wiki content.
//
// A page keeps its hand-written prose and defers one part to a plugin:
//
//	{{achievements}}
//
// expands to whatever registered pluginapi.ContentBlockKey("achievements").
// The wiki knows nothing about achievements and the rewards plugin knows nothing
// about the wiki; the registry is the only thing between them.
//
// WHY NOT a job that rewrites the page body. It was the obvious alternative and
// it is worse in two ways that compound: the table is stale between ticks, and
// every rewrite is a wiki REVISION, so the page history fills with machine edits
// and the human ones become impossible to find. Rendering live has neither
// problem and no schedule to get wrong.

// blockToken matches a token and, when it is alone in a paragraph, the paragraph
// wrapping it.
//
// Both forms are needed and the paragraph one matters more. Markdown puts a
// token on its own line inside <p>...</p>, and a <table> inside a <p> is invalid
// HTML that browsers "fix" by hoisting the table out and leaving an empty
// paragraph behind — so the common case has to consume the wrapper. The bare
// form covers a token used mid-sentence, where a block small enough to be inline
// is the author's problem rather than ours.
//
// \{\{ *([a-z0-9-]+) *\}\} — names are lowercase, digits and dashes only. Not
// `.+`: a permissive name class would let a token capture across HTML it should
// not, and the registry keys this resolves against are all of this shape.
var blockToken = regexp.MustCompile(`(?s)<p>\s*\{\{ *([a-z0-9-]+) *\}\}\s*</p>|\{\{ *([a-z0-9-]+) *\}\}`)

// expandBlocks replaces block tokens in ALREADY-SANITISED html.
//
// Called after deps.Markdown, which converts and sanitises. That order is the
// whole security argument (see pluginapi.ContentBlock): a block's HTML is
// plugin-generated and must not be sanitised, and an editor's text has already
// been through the sanitiser by the time this sees it.
//
// A token whose block is not registered, or whose block errors, is LEFT AS
// WRITTEN. An editor who mistypes should see their mistake; silently deleting
// part of somebody's page is the worse failure, and a help page missing one
// table still helps.
func expandBlocks(ctx context.Context, c *core.Core, html template.HTML) template.HTML {
	s := string(html)
	if c == nil || !strings.Contains(s, "{{") {
		// The overwhelmingly common case: no tokens, so no registry lookups and
		// no regex scan of every wiki page render.
		return html
	}

	out := blockToken.ReplaceAllStringFunc(s, func(match string) string {
		m := blockToken.FindStringSubmatch(match)
		name := m[1]
		if name == "" {
			name = m[2]
		}
		v, ok := c.Lookup(pluginapi.ContentBlockKey(name))
		if !ok {
			return match
		}
		blk, ok := v.(pluginapi.ContentBlock)
		if !ok {
			// Registered under the right key with the wrong shape. Loud, because
			// it is a wiring bug in whoever registered it and the page would
			// otherwise just look like the token was mistyped.
			log.Printf("wiki: %s is registered but does not implement pluginapi.ContentBlock (%T)",
				pluginapi.ContentBlockKey(name), v)
			return match
		}
		frag, err := blk.Render(ctx)
		if err != nil {
			log.Printf("wiki: content block %q: %v", name, err)
			return match
		}
		return string(frag)
	})
	return template.HTML(out)
}

// renderContent is the one place wiki content becomes HTML: markdown + sanitise
// (the host's Markdown seam), then block expansion.
//
// Every caller goes through here so a new surface cannot accidentally render a
// post without blocks — or, worse, expand before sanitising.
func (h *Handlers) renderContent(ctx context.Context, src string) template.HTML {
	return expandBlocks(ctx, h.core, deps.Markdown(src))
}
