package pluginapi

import (
	"html/template"
	"strings"
	"testing"
)

// No host renderer, no panic, and never nothing. A plugin that trusted this
// seam and got "" back would silently drop the author's name off its own page
// -- a worse failure than a missing colour, and one nobody would attribute to
// a cosmetics contract.
func TestRenderUserTagFallsBackToAPlainLink(t *testing.T) {
	out := string(RenderUserTag(nil, "alice"))
	if !strings.Contains(out, `href="/u/alice"`) || !strings.Contains(out, ">alice<") {
		t.Fatalf("nil core produced %q, want a plain link to the profile", out)
	}
	if RenderUserTag(nil, "") != "" {
		t.Error("an empty name produced markup; there is nobody to link to")
	}
	if RenderUserTag(nil, "   ") != "" {
		t.Error("a whitespace name produced markup")
	}
}

// A username is member-controlled input and this returns template.HTML, which
// tells the caller's template not to escape it. So the escaping has to happen
// here or every plugin that adopts the seam gains an XSS.
func TestPlainUserTagEscapes(t *testing.T) {
	out := string(RenderUserTag(nil, `bob"><script>alert(1)</script>`))
	for _, bad := range []string{"<script", `"><`} {
		if strings.Contains(out, bad) {
			t.Fatalf("unescaped %q survived into %q", bad, out)
		}
	}
	if !strings.Contains(out, "&lt;script") {
		t.Errorf("expected the tag escaped into text, got %q", out)
	}
}

// A host whose renderer returns nothing -- a template error, a name it cannot
// resolve -- must still leave the reader something to click.
func TestAnEmptyHostRenderIsNotTheAnswer(t *testing.T) {
	var fn UserTag = func(string) template.HTML { return "" }
	if got := renderWith(fn, "carol"); !strings.Contains(string(got), `href="/u/carol"`) {
		t.Fatalf("an empty host render produced %q, want the plain fallback", got)
	}
}

// renderWith is RenderUserTag's tail, exercised without a Core: constructing a
// real registry here would test core.Lookup rather than this file.
func renderWith(fn UserTag, name string) template.HTML {
	if fn != nil {
		if out := fn(name); out != "" {
			return out
		}
	}
	return plainUserTag(name)
}
