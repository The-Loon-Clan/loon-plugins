package wiki

import (
	"strings"
	"testing"
)

// Topic icon and colour are admin-editable, and both are decided by TEMPLATE
// logic with a fallback: an unset icon must still draw the glyph the topic's
// slug has always given it, and an unset colour must leave the CSS rules in
// charge. Neither path is reachable from a unit test of Go code — it is all in
// wiki.html — and a mistake shows up as a page that renders wrong, or aborts,
// only when somebody visits it.
//
// These moved here with the markup. They lived in the host's web/handlers and
// used a locally-declared struct, because a host package must not import a
// plugin; on this side the real Topic can be used, so the test now pins the
// template against the type it will actually receive.

func renderWikiIndex(t *testing.T, topics []*Topic) string {
	t.Helper()
	var sb strings.Builder
	err := pageTmpl.ExecuteTemplate(&sb, "wiki.html", map[string]any{
		"Topics": topics, "RecentPosts": []*RecentPost{},
		"PopularPosts": []*RecentPost{}, "PostsByTopic": map[int][]*Post{},
	})
	if err != nil {
		t.Fatalf("execute wiki.html: %v", err)
	}
	return sb.String()
}

// A topic with no icon and no colour must render exactly as before: the
// slug-derived glyph and no inline style. Every existing topic is in this
// state, so a regression here changes the whole page at once.
func TestWikiTopicIcon_UnsetKeepsTheSlugDefault(t *testing.T) {
	out := renderWikiIndex(t, []*Topic{{Slug: "api", Name: "API"}})

	if strings.Contains(out, `style="color:`) {
		t.Error("a topic with no colour emitted an inline colour style")
	}
	// The slug map's api glyph is the code chevrons; the generic folder is the
	// fallback. Seeing the folder here means the slug layer stopped running.
	if !strings.Contains(out, `polyline points="16 18 22 12 16 6"`) {
		t.Error("the api slug did not draw its own glyph — the default layer is broken")
	}
}

func TestWikiTopicIcon_ExplicitIconOverridesTheSlug(t *testing.T) {
	out := renderWikiIndex(t, []*Topic{{Slug: "api", Name: "API", Icon: "star"}})

	if !strings.Contains(out, `polygon points="12 2 15.09 8.26`) {
		t.Error("the chosen star glyph was not drawn")
	}
	if strings.Contains(out, `polyline points="16 18 22 12 16 6"`) {
		t.Error("the slug's glyph still rendered alongside the explicit icon")
	}
}

// An explicit colour must reach the grid tile — the one icon surface left
// after the 2026-08-17 declutter removed the explorer rail — and the tile
// must tint its background to match rather than keeping the default blue
// behind a custom-coloured glyph.
func TestWikiTopicIcon_ExplicitColourStylesTheGridTile(t *testing.T) {
	out := renderWikiIndex(t, []*Topic{{Slug: "guides", Name: "Guides", Color: "#ff8800"}})

	if !strings.Contains(out, "color:#ff8800") {
		t.Error("the chosen colour did not reach the grid tile")
	}
	if !strings.Contains(out, "color-mix(in srgb, #ff8800 18%, transparent)") {
		t.Error("the grid tile did not tint its background from the chosen colour")
	}
}

// An unknown key must fall through to the folder rather than rendering
// nothing. The handler validates against the closed set, so this only happens
// if a key is removed from the template while rows still reference it —
// exactly the case where a blank icon would be worst.
func TestWikiTopicIcon_UnknownKeyFallsBackToAGlyph(t *testing.T) {
	out := renderWikiIndex(t, []*Topic{{Slug: "whatever", Name: "X", Icon: "retired-key"}})

	if !strings.Contains(out, "<svg") {
		t.Error("an unknown icon key rendered no glyph at all")
	}
}

// The admin form is the other half: it must offer the icon list and prefill
// both fields, and keep working when creating a topic (no .Topic at all).
func TestWikiTopicForm_RendersForCreateAndEdit(t *testing.T) {
	for _, tc := range []struct {
		name string
		data map[string]any
		want []string
	}{
		{
			name: "create",
			data: map[string]any{"Action": "Create", "Icons": TopicIcons},
			want: []string{`name="icon"`, `name="color"`, "Folder"},
		},
		{
			name: "edit",
			data: map[string]any{"Action": "Edit", "Icons": TopicIcons,
				"Topic": &Topic{ID: 7, Slug: "api", Name: "API", Icon: "star", Color: "#ff8800"}},
			// The stored icon must come back selected and the colour
			// prefilled, or editing a topic silently resets its appearance.
			want: []string{`value="star" selected`, `value="#ff8800"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sb strings.Builder
			if err := pageTmpl.ExecuteTemplate(&sb, "admin_wiki_topic_form.html", tc.data); err != nil {
				t.Fatalf("execute form: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(sb.String(), w) {
					t.Errorf("form is missing %q", w)
				}
			}
		})
	}
}
