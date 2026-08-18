package medals

import "testing"

// What an operator typed decides what gets drawn, and the awkward inputs are
// the point: a blank field, and the several ways a "URL" turns out not to be
// one. Nothing here may return both a sprite and an image — a template picks
// with an {{if}} and would draw two icons.
func TestSpriteOrImage(t *testing.T) {
	for _, tc := range []struct {
		name, icon, slug string
		wantSprite       string // "" means "any default sprite"
		wantImage        string
	}{
		{name: "a bare name is a sprite", icon: "star", slug: "founder", wantSprite: "star"},
		{name: "a rooted path is an image", icon: "/uploads/medals/x.png", slug: "founder",
			wantImage: "/uploads/medals/x.png"},
		{name: "an absolute url is an image", icon: "https://example.test/m.png", slug: "founder",
			wantImage: "https://example.test/m.png"},
		{name: "blank falls back to a sprite", icon: "", slug: "founder"},
		{name: "whitespace is blank", icon: "   ", slug: "founder"},
		// The real one. This exact string sat in the demo database for months,
		// rendering a broken image on every page that drew the medal.
		{name: "an MSYS-mangled path is not an image", slug: "veteran",
			icon: `C:/Program Files/Git/uploads/medals/founder.png`},
		{name: "a bare filename is not an image either", icon: "founder.png", slug: "founder"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sprite, image := spriteOrImage(tc.icon, tc.slug)
			if sprite != "" && image != "" {
				t.Fatalf("both set: sprite=%q image=%q", sprite, image)
			}
			if tc.wantImage != "" {
				if image != tc.wantImage {
					t.Errorf("image = %q, want %q", image, tc.wantImage)
				}
				return
			}
			if image != "" {
				t.Fatalf("image = %q, want a sprite", image)
			}
			if tc.wantSprite != "" && sprite != tc.wantSprite {
				t.Errorf("sprite = %q, want %q", sprite, tc.wantSprite)
			}
			if sprite == "" {
				t.Error("no sprite and no image — the medal would draw nothing")
			}
		})
	}
}

// A badge that changed its face between page loads would be unrecognisable,
// which is the one thing a badge cannot be.
func TestDefaultSpriteIsStableAndInThePalette(t *testing.T) {
	inPalette := func(s string) bool {
		for _, p := range spritePalette {
			if p == s {
				return true
			}
		}
		return false
	}
	seen := map[string]int{}
	for _, slug := range []string{"founder", "veteran", "uploader", "donor", "beta", "helper", "archivist", "legend"} {
		first := defaultSprite(slug)
		if !inPalette(first) {
			t.Errorf("%s got %q, which is not in the palette", slug, first)
		}
		for i := 0; i < 5; i++ {
			if again := defaultSprite(slug); again != first {
				t.Fatalf("%s drew %q then %q — a medal must keep its face", slug, first, again)
			}
		}
		seen[first]++
	}
	// Not a distribution test, just the failure that would make the feature
	// pointless: every medal on a site landing on one icon.
	if len(seen) < 3 {
		t.Errorf("eight slugs produced %d distinct icons %v — the spread is too narrow to tell medals apart",
			len(seen), seen)
	}
	if defaultSprite("") == "" {
		t.Error("a medal with no slug draws nothing")
	}
}

// The Medal methods are what the templates call, and each must answer for the
// other's absence.
func TestMedalIconAccessors(t *testing.T) {
	img := Medal{Slug: "founder", Icon: "/uploads/medals/founder.png"}
	if img.Image() == "" || img.Sprite() != "" {
		t.Errorf("image medal: sprite=%q image=%q", img.Sprite(), img.Image())
	}
	blank := Medal{Slug: "veteran"}
	if blank.Sprite() == "" || blank.Image() != "" {
		t.Errorf("blank medal: sprite=%q image=%q", blank.Sprite(), blank.Image())
	}
}
