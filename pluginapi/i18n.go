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

import "context"

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
