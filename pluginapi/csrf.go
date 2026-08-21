// csrf.go is the one place a plugin gets the host's CSRF token.
//
// Every plugin that renders a POST form needs it, because a host mounts CSRF
// middleware over the whole engine: a form without the hidden `_csrf` input is
// refused with 403 for every human who clicks it, and the failure is INVISIBLE
// to the access audit, which probes destructive POSTs WITH a valid token by
// design (it tests the gate, not the form).
//
// That is not hypothetical. A sweep on 18 Aug 2026 found 58 tokenless POST
// forms across nine plugins — every admin action in usenet, ranks, events,
// achievements, messages and lists, plus the rewards page's own toggle and
// create. All of them had been answering 403 to every operator who tried.
//
// ONE key, not one per plugin. The first three plugins to need a token each got
// their own ("games.csrf", "medals.csrf", "magic.csrf") and a host registration
// each; at nine plugins that is nine identical registrations whose only
// difference is a string, and the tenth plugin's omission looks exactly like a
// host that chose not to wire it. The per-plugin keys still work — a host that
// registers them keeps whatever it had — but nothing new should add one.
package pluginapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// MOVED TO core, AND ALIASED HERE. The definitions below now live in
// loon/core/csrf.go, because plugins stopped being the only thing that renders
// a form: loon-baseline supplies the account, API-key, inbox, admin-users and
// maintenance pages, and it depends on loon but must not depend on this module.
//
// While the seam lived here, those eight forms had no token and answered 403
// to everyone who clicked them.
//
// ALIASES, NOT DEFINITIONS. `type X = core.Y` makes the two names denote the
// SAME type, so the host's existing pluginapi.CSRFTokenFunc registration
// produces a value that core's resolver and this one both recognise. A
// `type X core.Y` definition would create a DISTINCT type and every
// registration in the tree would quietly stop resolving — the failure being,
// again, a form field that renders empty.

// CSRFTokenName is where a host publishes its per-request token minter, as
// CSRFTokenFunc. Registered BEFORE Boot, like every seam a plugin resolves at
// Provision.
const CSRFTokenName = core.CSRFTokenName

// CSRFTokenFunc mints the token for one request. Registered AS this type — a
// bare func never survives the registry's type assertion.
type CSRFTokenFunc = core.CSRFTokenFunc

// CSRFToken resolves the token for this request, empty when no host published
// one.
//
// Empty is deliberately not an error: a host with no CSRF middleware is a
// legitimate host, and its forms work with an empty hidden field. What must
// never happen is the field being ABSENT, which is why every caller puts the
// result in its view model unconditionally rather than gating the markup on it.
//
// Both key shapes are tried — the shared one first, then the plugin's own
// legacy key — so a host that wired only "medals.csrf" keeps working while a
// host that wired the shared key serves every plugin at once.
func CSRFToken(c *core.Core, gc *gin.Context, legacyKeys ...string) string {
	return core.CSRFToken(c, gc, legacyKeys...)
}

// ── carrying the token through a context ────────────────────────────────────
//
// Several plugins render through helpers that take a context.Context rather
// than the gin one — usenet's six page builders, for instance. Threading a
// *gin.Context through them to reach a hidden form field would be a worse
// change than the bug: it couples six signatures to the web layer so that one
// string can reach the bottom.
//
// So the token rides in the context, put there once at the view boundary where
// the gin context exists. WithCSRF at the top, FromCSRF at the render.

type csrfCtxKey struct{}

// WithCSRF returns ctx carrying the token.
func WithCSRF(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, csrfCtxKey{}, token)
}

// CSRFFrom reads the token back, empty when none was put in. Empty is not an
// error — see CSRFToken — but an ABSENT form field is, so callers put the
// result in their view model unconditionally.
func CSRFFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(csrfCtxKey{}).(string)
	return s
}
