// i18n.go declares the contract a host with a message catalogue publishes so
// plugins can SEED it: declare the slugs they render and a default text for
// each, so an operator opening the catalogue finds the plugin's vocabulary
// already listed and awaiting translation instead of reverse-engineering slug
// names from templates.
//
// The direction matters. The catalogue is host-owned operator content —
// plugins never write it directly, and a plugin's declaration is a STARTING
// POINT, not a source of truth: the host inserts a declared pair only when
// the cell does not exist yet, so a string the operator has touched (or even
// merely created) is never overwritten, including by a plugin update that
// changed its shipped default. A plugin removed later leaves its slugs
// behind; they are operator content now, and rows that resolve for nobody
// are inert.
package pluginapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// I18nDeclarerName is the Core extension-registry key under which a host with
// a message catalogue publishes its I18nDeclarer. Absent means the host has no
// catalogue, which is a legitimate host, not a broken one — a plugin's own
// fallback texts simply ARE its content there, and Provision must treat the
// missing key as normal rather than an error.
const I18nDeclarerName = "i18n.declare"

// I18nDeclarer records default texts for catalogue slugs, seed-only: a pair
// whose cell already exists is skipped silently, whatever its text.
//
// Call it during Provision, once, with every slug the plugin renders. The
// text is in the HOST'S DEFAULT LANGUAGE by convention (the language the rest
// of the site falls back to) — a declarer has no say in which locale it seeds,
// because the fallback column is the host's to define.
//
// Slugs are dotted lowercase (`ach.night-owl.title`); the host validates and
// returns an error naming the first offender, refusing the whole batch, so a
// malformed declaration is a loud Provision-time failure rather than a slug
// that silently never matches the catalogue's vocabulary.
type I18nDeclarer func(ctx context.Context, defaults map[string]string) error

// ---------------------------------------------------------------------------
// Reading the catalogue back
//
// The seeding half above has been a declared contract since it was built. The
// READING half was not, and it is the cautionary tale this file now carries.
//
// Two plugins wanted the same two things — the slug list, for the dropdowns on
// their definition forms, and slug-to-text for the current viewer — and each
// agreed a private pair of keys with the host. So the host registered THE SAME
// TWO CLOSURES four times, under achievements.l10n.slugs, achievements.l10n
// .resolve, medals.l10n.slugs and medals.l10n.resolve, with a comment saying
// "one key per consumer, the same closures". A third plugin would have made it
// six, and the fifth would have got the type subtly wrong, because nothing
// declared what the type was.
//
// One key, one interface, any number of consumers.

// MessageCatalogueName is the Core extension-registry key under which a host
// with a message catalogue publishes it for READING.
//
// Absent is normal and is not an error: a host without a catalogue is a host
// where a plugin's own text columns ARE the content, which is every
// pre-catalogue site. A plugin must treat the missing key that way rather than
// refusing to provision.
const MessageCatalogueName = "i18n.catalogue"

// MessageCatalogue is how a plugin reads host-owned translated text.
type MessageCatalogue interface {
	// Slugs is every slug the catalogue knows, for the dropdowns on a
	// definition form — an operator picking which string a badge uses should
	// choose from what exists rather than typing a slug and hoping.
	Slugs(ctx context.Context) ([]string, error)

	// Resolve turns a slug into text for THIS VIEWER's locale, reporting
	// whether the catalogue had anything to say.
	//
	// The bool is what keeps a fallback honest: a missing slug and a slug
	// deliberately set to an empty string are different, and a resolver that
	// returned "" for both would make a plugin fall back to its shipped text
	// for a string an operator had chosen to blank.
	//
	// It takes a *gin.Context rather than a context.Context because the answer
	// depends on the REQUEST — the viewer's locale comes from their session
	// and headers — which is also why this cannot be resolved once at boot.
	Resolve(gc *gin.Context, slug string) (string, bool)
}

// Messages resolves the registered catalogue. Absent is normal; see the name
// constant.
func Messages(c *core.Core) (MessageCatalogue, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(MessageCatalogueName)
	if !ok {
		return nil, false
	}
	m, ok := v.(MessageCatalogue)
	return m, ok
}
