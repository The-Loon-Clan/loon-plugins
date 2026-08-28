// script.go is how a plugin gets JavaScript onto a page without putting it in
// one. The stylesheet seam's twin, for the same three reasons and one more.
//
// THE PROBLEM IT REPLACES. A plugin renders a FRAGMENT, so any script it needs
// has travelled inside the fragment, in a <script> block:
//
//	UNCACHEABLE the code is in the DOCUMENT, re-sent on every view, for bytes
//	            that do not change between deploys. The agent dashboard alone
//	            carries six blocks.
//	INLINE      the host cannot drop script-src 'unsafe-inline' from its CSP
//	            while any plugin page needs it — and unlike a stylesheet, that
//	            is the directive that actually stops an injected <script> from
//	            running. This is the extra reason: the CSS version of this seam
//	            buys tidiness and caching; this one buys a real defence.
//	UNORDERED   a fragment's script runs where the fragment lands, which is
//	            mid-body, so it cannot assume the DOM below it exists. A
//	            registered file is linked once by the host, which decides
//	            defer/placement — and gets that decision right in one place
//	            rather than in every plugin.
//
// ABSENCE IS NORMAL, as everywhere else here: a host that registers no
// registrar leaves plugins to their <script> blocks, which still work. A
// plugin that wants to know may ask.
//
// WHAT THIS IS NOT. It is not a module loader and not a bundler. One file per
// plugin, served as-is, no dependency graph — because a plugin that needed a
// build step would be asking every host to run its toolchain. Anything richer
// than "one file of vanilla JS" belongs in the host.
package pluginapi

import (
	"github.com/the-loon-clan/loon/core"
)

// ScriptRegistrarName is where a host publishes its script sink.
const ScriptRegistrarName = "js.script"

// ScriptRegistrar takes one plugin script, once, at Provision.
//
// The host decides the URL, the caching and whether it is deferred; a plugin
// hands over bytes and a name and never sees any of it. Deliberate, and the
// same reasoning as the stylesheet seam: a plugin that built its own URL would
// be deciding the host's cache policy, and a plugin that chose its own
// placement would be deciding load order for a document it cannot see.
type ScriptRegistrar interface {
	// RegisterScript stores js under plugin, replacing any previous script for
	// that name. The name is the plugin's own (agent, forum), and the host
	// makes a URL of it.
	//
	// Called from Provision, BEFORE core.Boot serves anything. A script
	// registered later is a script no page has linked.
	RegisterScript(plugin, js string) error
}

// Scripts resolves the registered sink. Absent is normal.
func Scripts(c *core.Core) (ScriptRegistrar, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(ScriptRegistrarName)
	if !ok {
		return nil, false
	}
	r, ok := v.(ScriptRegistrar)
	return r, ok
}

// RegisterScript is the one-line form for a plugin that does not care whether
// the host offers the seam:
//
//	pluginapi.RegisterScript(c, "agent", agentJS)
//
// Reports whether it was taken, so a caller that wants to keep a <script>
// fallback can ask. Most should not: a fragment that ships the same code twice
// runs it twice, and the second run re-binds every handler.
func RegisterScript(c *core.Core, plugin, js string) bool {
	r, ok := Scripts(c)
	if !ok {
		return false
	}
	return r.RegisterScript(plugin, js) == nil
}
