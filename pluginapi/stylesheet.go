// stylesheet.go is how a plugin gets CSS onto a page without putting it in one.
//
// THE PROBLEM IT REPLACES. A plugin renders a FRAGMENT and the host wraps the
// chrome around it, so any CSS the fragment needs has travelled inside the
// fragment, in a <style> block. Three costs, and only the first is obvious:
//
//	INVALID     <style> is metadata content and does not belong in a <div>.
//	            The host now hoists these into the head, so the pages are
//	            valid -- but that fixed the markup, not the other two.
//	UNCACHEABLE the CSS is in the DOCUMENT, so it is re-sent on every view.
//	            news ships 90 lines across four pages and pays for them every
//	            time, for bytes that never change between deploys.
//	INLINE      the host cannot drop style-src 'unsafe-inline' from its CSP
//	            while every plugin page needs it.
//
// A registered sheet is served from a URL, so it is cacheable and it is not
// inline. See docs/BACKLOG.md #13 in loon-demo-site.
//
// ABSENCE IS NORMAL, as everywhere else here: a host that registers no
// registrar leaves plugins to their <style> blocks, which still work.
package pluginapi

import (
	"github.com/the-loon-clan/loon/core"
)

// StylesheetRegistrarName is where a host publishes its stylesheet sink.
const StylesheetRegistrarName = "css.stylesheet"

// StylesheetRegistrar takes one plugin stylesheet, once, at Provision.
//
// The host decides the URL and the caching; a plugin hands over bytes and a
// name and never sees either. That is deliberate -- a plugin that built its own
// URL would be deciding the host's cache policy, and the two would drift.
type StylesheetRegistrar interface {
	// RegisterStylesheet stores css under plugin, replacing any previous sheet
	// for that name. The name is the plugin's own (news, forum), and the host
	// makes a URL of it.
	//
	// Called from Provision, BEFORE core.Boot serves anything. A sheet
	// registered later is a sheet no page has linked.
	RegisterStylesheet(plugin, css string) error
}

// Stylesheets resolves the registered sink. Absent is normal.
func Stylesheets(c *core.Core) (StylesheetRegistrar, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(StylesheetRegistrarName)
	if !ok {
		return nil, false
	}
	r, ok := v.(StylesheetRegistrar)
	return r, ok
}

// RegisterStylesheet is the one-line form for a plugin that does not care
// whether the host offers the seam:
//
//	pluginapi.RegisterStylesheet(c, "news", newsCSS)
//
// Reports whether it was taken, so a caller that wants to keep its <style>
// fallback can ask. Most do not: the fragment simply stops carrying one.
func RegisterStylesheet(c *core.Core, plugin, css string) bool {
	r, ok := Stylesheets(c)
	if !ok {
		return false
	}
	return r.RegisterStylesheet(plugin, css) == nil
}
