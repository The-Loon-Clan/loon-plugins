package medals

import (
	"strings"
	"testing"
)

// html/template streams: a field the markup wants and the data lacks aborts
// the render part way through, and the caller gets a 200 carrying half a page
// with nothing logged. These templates had no execution test at all until the
// icon picker arrived — a partial called from three places with a dict of
// three keys is exactly the shape that fails that way.
//
// Rendered through the plugin's OWN template set (Provision builds it with the
// funcs the picker needs), so a helper missing from the FuncMap fails here.
func renderAdmin(t *testing.T, data map[string]any) string {
	t.Helper()
	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatalf("templates: %v", err)
	}
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "medals_admin.html", data); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	if strings.TrimSpace(out) == "" {
		t.Fatal("rendered empty")
	}
	return out
}

func TestAdminPageRendersWithThePicker(t *testing.T) {
	icons := []string{"bell", "check", "shield", "star"}
	out := renderAdmin(t, map[string]any{
		"Medals": []Medal{
			// A sprite, an image, and one that never chose — the three states
			// the picker has to round-trip.
			{ID: 1, Slug: "founder", Name: "Founder", Icon: "shield", Price: 3, BonusPct: 5, Enabled: true},
			{ID: 2, Slug: "veteran", Name: "Veteran", Icon: "/uploads/medals/v.png", Enabled: true},
			{ID: 3, Slug: "helper", Name: "Helper", Enabled: false},
		},
		"Icons":     icons,
		"L10nSlugs": []string{"medal.founder.desc"},
		"CSRFToken": "test-csrf",
	})

	// The picker offers what the site has, and the create form and every edit
	// row get one — the count is three edit rows plus the create form.
	if n := strings.Count(out, `name="icon"`); n != 4 {
		t.Errorf("%d icon fields, want 4 (one per medal + the create form)", n)
	}
	for _, want := range []string{
		`<option value="shield" selected>`, // the medal's own choice, kept
		`>&mdash; pick one from the slug &mdash;<`,
		`value="/uploads/medals/v.png" selected>/uploads/medals/v.png (image)`, // not silently replaced
		`action="/admin/p/medals/update"`,                                      // the edit action exists
		"test-csrf",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("admin page missing %q", want)
		}
	}
	// The slug is deliberately absent from the edit form: it is the id every
	// reward and store item names.
	edit := out[strings.Index(out, "Edit Founder"):]
	edit = edit[:strings.Index(edit, "</details>")]
	if strings.Contains(edit, `name="slug"`) {
		t.Error("the edit form offers the slug — renaming it breaks every grant that names it")
	}
	// Assert on the LAST thing the template writes, or a truncated render
	// passes every check above.
	if !strings.Contains(out, "on this site today it does not") {
		t.Error("the page stopped short of its closing note — the render truncated")
	}
}

// The empty state is what an operator sees on a fresh install, and a host with
// no icon catalogue still has to get a usable picker.
func TestAdminPageRendersEmptyAndWithoutAHostCatalogue(t *testing.T) {
	out := renderAdmin(t, map[string]any{
		"Medals": []Medal{}, "Icons": spritePalette, "CSRFToken": "x",
	})
	if !strings.Contains(out, "No medals yet") {
		t.Error("the empty state is missing")
	}
	if !strings.Contains(out, `value="star"`) {
		t.Error("the fallback palette did not reach the picker")
	}
}

func TestMemberPageRenders(t *testing.T) {
	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatalf("templates: %v", err)
	}
	var sb strings.Builder
	// The REAL view model (medalVM), not a map that happens to satisfy the
	// markup — a double looser than the thing it stands for is a test that
	// passes on code production rejects.
	if err := p.tmpl.ExecuteTemplate(&sb, "medals.html", map[string]any{
		"Medals": []medalVM{
			{Medal: Medal{ID: 1, Slug: "founder", Name: "Founder", Price: 3, BonusPct: 5},
				Description: "Here from the first boot.", Owned: true, Shown: true},
			// One nobody holds and one with an image, so the buy branch and
			// both icon branches execute.
			{Medal: Medal{ID: 2, Slug: "veteran", Name: "Veteran", Icon: "/uploads/m.png"},
				Description: "A year of service."},
		},
		"CSRFToken": "x",
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	for _, want := range []string{"Founder", "Veteran", "<use href=\"#", "<img src=\"/uploads/m.png\""} {
		if !strings.Contains(out, want) {
			t.Errorf("the member page is missing %q", want)
		}
	}
}
