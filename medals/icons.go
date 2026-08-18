package medals

import (
	"hash/fnv"
	"strings"
)

// What a medal LOOKS like.
//
// The field has always been an image URL, which meant every medal needed an
// upload before it looked like anything — and one that never got a good file
// rendered as a plain coloured square, which is a worse badge than no badge.
//
// A medal's icon may now be either:
//
//	star, verified, shield …   a HOST sprite id, drawn as an <svg><use>
//	/uploads/medals/x.png      an image, drawn as an <img>
//	(blank)                    a sprite chosen from the slug — see defaultSprite
//
// The sprite ids are the host's, the same coupling the store's cards and the
// ranks groups widget already have: a host missing a symbol renders an empty
// <use>, so the cost of a wrong guess is a blank space rather than a broken
// page. It is documented in the README's Surface table.

// spritePalette is the set a medal falls back into when nothing is set.
//
// Curated rather than the whole sprite sheet: these are the symbols that read
// as an AWARD when they appear beside a member's name. #film or #server would
// each be a perfectly good icon for a specific medal an operator picked on
// purpose, and a confusing one to be handed by default.
//
// Order matters and must not be rearranged: defaultSprite indexes into it by a
// hash of the slug, so shuffling this list silently repaints every medal that
// never chose an icon.
var spritePalette = []string{
	"verified",
	"shield",
	"star",
	"coin",
	"check",
	"clock",
	"globe",
	"users",
}

// defaultSprite picks a medal's face from its slug.
//
// STABLE, not random: a medal that changed its icon on every page load would be
// unrecognisable, which is the one thing a badge cannot be. Hashing the slug
// gives a site a spread of faces across its catalogue while keeping each one
// fixed for as long as the medal exists — and an operator who wants a specific
// icon says so, which is what the field is for.
func defaultSprite(slug string) string {
	if slug == "" {
		return spritePalette[0]
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(slug))
	return spritePalette[int(h.Sum32())%len(spritePalette)]
}

// spriteOrImage splits an icon field into the two things a template can draw.
// Exactly one is ever non-empty.
func spriteOrImage(icon, slug string) (sprite, image string) {
	icon = strings.TrimSpace(icon)
	switch {
	case icon == "":
		return defaultSprite(slug), ""
	case strings.HasPrefix(icon, "/"), strings.HasPrefix(icon, "http://"), strings.HasPrefix(icon, "https://"):
		return "", icon
	case strings.ContainsAny(icon, "/\\. "):
		// Looks like a path but is not one this site can serve — a Windows
		// path, a bare filename, a mangled shell argument. Draw the fallback
		// rather than an <img> that will certainly break: the veteran medal
		// spent months as a broken-image icon holding
		// "C:/Program Files/Git/uploads/medals/founder.png".
		return defaultSprite(slug), ""
	}
	return icon, ""
}

// Sprite is the host sprite id to draw, empty when this medal has an image.
func (m Medal) Sprite() string { s, _ := spriteOrImage(m.Icon, m.Slug); return s }

// Image is the icon URL to draw, empty when this medal has a sprite.
func (m Medal) Image() string { _, i := spriteOrImage(m.Icon, m.Slug); return i }
