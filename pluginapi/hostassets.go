// hostassets.go declares the two things a host LENDS a plugin for its own
// admin surfaces: the icons this site can draw, and somewhere to put an upload.
//
// Both were bare-string conventions until now, and both show the same failure
// in different stages. The icon list was agreed twice — `icons.catalogue` with
// medals and `achievements.icons` with achievements — for one list, with two
// different types: a `func() []string` on one side and a plain `[]string` on
// the other. The func is the right answer, because a sprite added to the site
// changes the list and a snapshot taken at Provision never notices; the
// snapshot version was simply the second author's guess, and nothing could have
// told them.
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
