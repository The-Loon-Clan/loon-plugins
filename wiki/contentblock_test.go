package wiki

import (
	"context"
	"errors"
	"html/template"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

func blockCore(t *testing.T, name string, blk pluginapi.ContentBlock) *core.Core {
	t.Helper()
	c := &core.Core{}
	if err := c.Register(pluginapi.ContentBlockKey(name), blk); err != nil {
		t.Fatalf("register: %v", err)
	}
	return c
}

func table(html string) pluginapi.ContentBlockFunc {
	return func(context.Context) (template.HTML, error) { return template.HTML(html), nil }
}

// A token alone in a paragraph consumes the PARAGRAPH, not just the token.
// Markdown wraps it in <p>, and a <table> inside a <p> is invalid HTML that
// browsers repair by hoisting the table out and leaving an empty paragraph.
func TestTokenAloneInAParagraphReplacesTheParagraph(t *testing.T) {
	c := blockCore(t, "achievements", table("<table><tr><td>x</td></tr></table>"))
	got := string(expandBlocks(context.Background(), c,
		template.HTML("<p>Intro.</p>\n<p>{{achievements}}</p>\n<p>Outro.</p>")))

	if strings.Contains(got, "<p><table") {
		t.Error("the table was left inside a paragraph")
	}
	if !strings.Contains(got, "<table>") {
		t.Fatalf("the block did not render: %s", got)
	}
	for _, keep := range []string{"Intro.", "Outro."} {
		if !strings.Contains(got, keep) {
			t.Errorf("expansion ate surrounding prose (%q missing): %s", keep, got)
		}
	}
}

// An unregistered token is LEFT AS WRITTEN. An editor who mistypes must see the
// mistake; silently deleting part of their page is the worse failure.
func TestUnknownTokenIsLeftAlone(t *testing.T) {
	c := blockCore(t, "achievements", table("<table></table>"))
	src := template.HTML("<p>{{achievemnets}}</p>") // typo
	got := string(expandBlocks(context.Background(), c, src))
	if got != string(src) {
		t.Errorf("a mistyped token was altered: %q", got)
	}
}

// A block that errors leaves its token in place and does not take the page with
// it. A help page missing one table still helps.
func TestBlockErrorLeavesTheTokenAndTheRestOfThePage(t *testing.T) {
	c := blockCore(t, "boom", pluginapi.ContentBlockFunc(
		func(context.Context) (template.HTML, error) { return "", errors.New("nope") }))
	got := string(expandBlocks(context.Background(), c,
		template.HTML("<p>Before.</p>\n<p>{{boom}}</p>\n<p>After.</p>")))

	if !strings.Contains(got, "{{boom}}") {
		t.Error("the token was consumed by a failing block")
	}
	for _, keep := range []string{"Before.", "After."} {
		if !strings.Contains(got, keep) {
			t.Errorf("%q was lost", keep)
		}
	}
}

// Content with no tokens is returned untouched, and cheaply — this runs on every
// wiki page render.
func TestContentWithoutTokensIsUntouched(t *testing.T) {
	c := blockCore(t, "achievements", table("<table></table>"))
	src := template.HTML("<h1>Help</h1><p>Nothing dynamic here.</p>")
	if got := expandBlocks(context.Background(), c, src); got != src {
		t.Errorf("plain content was rewritten: %q", got)
	}
	// And with no Core at all, which is how the handler runs in tests.
	if got := expandBlocks(context.Background(), nil, src); got != src {
		t.Errorf("nil core rewrote content: %q", got)
	}
}

// A registration under the right key with the wrong TYPE must not be treated as
// a block. It is a wiring bug, and rendering the token unchanged keeps the page
// working while the log says what is wrong.
func TestWronglyTypedRegistrationIsIgnored(t *testing.T) {
	c := &core.Core{}
	if err := c.Register(pluginapi.ContentBlockKey("achievements"), "not a block"); err != nil {
		t.Fatal(err)
	}
	src := template.HTML("<p>{{achievements}}</p>")
	if got := expandBlocks(context.Background(), c, src); got != src {
		t.Errorf("a string registration was expanded: %q", got)
	}
}

// Only lowercase/digit/dash names match. A permissive pattern could capture
// across markup it should not.
func TestTokenNamesAreRestricted(t *testing.T) {
	c := blockCore(t, "achievements", table("<table></table>"))
	for _, src := range []string{
		"<p>{{ACHIEVEMENTS}}</p>",
		"<p>{{achievements!}}</p>",
		"<p>{{ach ievements}}</p>",
	} {
		if got := string(expandBlocks(context.Background(), c, template.HTML(src))); got != src {
			t.Errorf("%q was expanded to %q", src, got)
		}
	}
	// Surrounding spaces inside the braces ARE tolerated — an editor typing
	// {{ achievements }} means the same thing.
	spaced := template.HTML("<p>{{ achievements }}</p>")
	if got := string(expandBlocks(context.Background(), c, spaced)); !strings.Contains(got, "<table>") {
		t.Errorf("a spaced token did not expand: %q", got)
	}
}
