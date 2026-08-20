// hostassets.go declares the two things a host LENDS a plugin for its own
// admin surfaces: the icons this site can draw, and somewhere to put an upload.
//
// Both were bare-string conventions until now.
//
// The icon keys looked like a duplicate and are not, which is worth recording
// because the first attempt at this file collapsed them and would have shipped
// a regression. `icons.catalogue` is everything the site can draw — 114 sprite
// ids — and medals resolves it. `achievements.icons` was a CURATED fourteen,
// because, as the host's own comment puts it, offering the whole sheet would
// put #logo and #chevron-down in a picker where they mean nothing. Two
// questions, not one: *what can this site draw* and *what should a badge picker
// offer*.
//
// So there are two contracts. The types did genuinely disagree — a
// `func() []string` on one side and a plain `[]string` on the other — and the
// func is right, because a sprite added to the site changes the answer and a
// snapshot taken at Provision never notices.
//
// The file store never got a second consumer, which is the other way this goes
// wrong: it looks fine right up until medals wants badge uploads too, and then
// there is a `medals.files` beside `achievements.files` holding the same value.
package pluginapi

import (
	"github.com/the-loon-clan/loon/blob"
	"github.com/the-loon-clan/loon/core"
)

// IconCatalogueName is the Core extension-registry key under which a host
// publishes the sprite ids it can render.
//
// Absent means the host does not publish one, and a plugin should offer a
// free-text field rather than an empty dropdown — an operator can still type an
// id that works, and a picker with nothing in it reads as a broken page.
const IconCatalogueName = "icons.catalogue"

// IconCatalogue returns the sprite ids this site can draw.
//
// A FUNC rather than a slice, and this is the part worth keeping: the answer
// changes when a sprite is added, and a value read once at Provision would go
// on offering yesterday's icons for the life of the process. Called per render
// of whatever picker needs it.
type IconCatalogue func() []string

// Icons resolves the registered catalogue.
//
// Tolerates the bare `func() []string` a host may have registered before this
// contract existed, because that is what both existing hosts do — a Lookup
// asserts on the exact type, so a named type and its underlying type are two
// different registrations, and refusing the untyped one would have broken every
// icon picker on the day this file landed.
func Icons(c *core.Core) (IconCatalogue, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(IconCatalogueName)
	if !ok {
		return nil, false
	}
	switch fn := v.(type) {
	case IconCatalogue:
		return fn, true
	case func() []string:
		return IconCatalogue(fn), true
	}
	return nil, false
}

// IconSetPrefix is where a host publishes CURATED icon lists for a purpose:
// "icons.set.<purpose>", e.g. icons.set.achievement-badge.
//
// A prefix because the purposes multiply — badges, medals, category glyphs —
// and each wants a different subset of the same sheet. The curation is host
// work and cannot be a plugin's: only the host knows which of its sprites read
// as a badge.
const IconSetPrefix = "icons.set."

// IconSet returns the curated list for a purpose, falling back to the full
// catalogue when the host has curated nothing for it.
//
// The fallback is deliberate and is the difference between a useful default and
// a broken page: a host that has not thought about a purpose should offer a
// picker containing too much rather than a picker containing nothing.
func IconSet(c *core.Core, purpose string) ([]string, bool) {
	if c == nil {
		return nil, false
	}
	if purpose != "" {
		if fn, ok := Contributed[IconCatalogue](c, IconSetPrefix, purpose); ok {
			return fn(), true
		}
		// Tolerating the bare func for the same reason Icons does.
		if v, ok := c.Lookup(IconSetPrefix + purpose); ok {
			if fn, ok := v.(func() []string); ok {
				return fn(), true
			}
			if list, ok := v.([]string); ok {
				return list, true
			}
		}
	}
	if fn, ok := Icons(c); ok {
		return fn(), true
	}
	return nil, false
}

// FileStoreName is the Core extension-registry key under which a host offers a
// place to store plugin-uploaded files — a badge image, a medal icon.
//
// ONE key for every plugin rather than one per plugin. The value is the host's
// own store and the same for all of them; the plugin decides the name it saves
// under, which is where the separation actually belongs (`badges/12.png`).
//
// Absent means this host takes no uploads. A plugin must then HIDE its upload
// control rather than offering one that fails on submit — the achievements
// admin page has done that from the start and it is the behaviour to copy.
const FileStoreName = "files.store"

// Files resolves the registered store.
func Files(c *core.Core) (blob.Store, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(FileStoreName)
	if !ok {
		return nil, false
	}
	s, ok := v.(blob.Store)
	return s, ok
}
