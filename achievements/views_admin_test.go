package achievements

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// The create form's refusals. Pure over url.Values, so every one of them is
// checked here rather than through a database round trip.
func TestParseNewAchievement(t *testing.T) {
	good := func() url.Values {
		return url.Values{
			"slug": {"centurion"}, "name": {"Centurion"}, "metric": {"forum.post.created"},
			"threshold": {"100"},
		}
	}

	t.Run("a complete metric form parses", func(t *testing.T) {
		a, err := parseNewAchievement(good())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if a.Slug != "centurion" || a.Threshold != 100 || a.Metric != "forum.post.created" {
			t.Errorf("got %+v", a)
		}
		if a.RewardSlug != "" {
			t.Errorf("no reward_slug submitted but got %q", a.RewardSlug)
		}
	})

	// The reward is OPTIONAL — a blank reward_slug is a pure badge, and the
	// form must accept it without complaint.
	t.Run("reward_slug is optional and carried when present", func(t *testing.T) {
		f := good()
		f.Set("reward_slug", " badge-bonus ")
		a, err := parseNewAchievement(f)
		if err != nil {
			t.Fatal(err)
		}
		if a.RewardSlug != "badge-bonus" {
			t.Errorf("reward_slug = %q, want trimmed badge-bonus", a.RewardSlug)
		}
	})

	// A trigger is the other criterion shape: no metric, no threshold.
	t.Run("a trigger form parses with no threshold", func(t *testing.T) {
		f := url.Values{"slug": {"first-login"}, "trigger": {"auth.login"}}
		a, err := parseNewAchievement(f)
		if err != nil {
			t.Fatal(err)
		}
		if a.Trigger != "auth.login" || a.Metric != "" || a.Threshold != 0 {
			t.Errorf("got %+v", a)
		}
	})

	// A threshold typed alongside a trigger is ignored, not stored: nothing
	// would ever read it, and a number that looks configured but does nothing
	// is the failure this form exists to refuse.
	t.Run("a trigger form drops a stray threshold", func(t *testing.T) {
		f := url.Values{"slug": {"first-login"}, "trigger": {"auth.login"}, "threshold": {"100"}}
		a, err := parseNewAchievement(f)
		if err != nil {
			t.Fatal(err)
		}
		if a.Threshold != 0 {
			t.Errorf("threshold = %d on a trigger achievement, want 0", a.Threshold)
		}
	})

	t.Run("metric and trigger together are refused", func(t *testing.T) {
		f := good()
		f.Set("trigger", "auth.login")
		_, err := parseNewAchievement(f)
		if err == nil {
			t.Fatal("accepted a form with both criteria")
		}
		if !strings.Contains(err.Error(), "not both") {
			t.Errorf("the refusal does not say why: %v", err)
		}
	})

	t.Run("neither criterion is refused", func(t *testing.T) {
		f := url.Values{"slug": {"empty"}}
		_, err := parseNewAchievement(f)
		if err == nil {
			t.Fatal("accepted a form with no criterion — nothing could ever score it")
		}
	})

	t.Run("whitespace is trimmed", func(t *testing.T) {
		f := good()
		f.Set("slug", "  centurion  ")
		f.Set("metric", " forum.post.created ")
		a, err := parseNewAchievement(f)
		if err != nil {
			t.Fatal(err)
		}
		if a.Slug != "centurion" || a.Metric != "forum.post.created" {
			t.Errorf("not trimmed: %q / %q", a.Slug, a.Metric)
		}
	})

	// A blank name is filled from the slug rather than refused: an
	// achievement with no name renders as an empty badge, and the slug is a
	// usable label.
	t.Run("a missing name falls back to the slug", func(t *testing.T) {
		f := good()
		f.Del("name")
		a, err := parseNewAchievement(f)
		if err != nil {
			t.Fatal(err)
		}
		if a.Name != "centurion" {
			t.Errorf("name = %q, want the slug", a.Name)
		}
	})

	// THE one that matters. A threshold of 0 completes for every member on
	// the first pass, and a completion is irreversible.
	t.Run("a zero threshold is refused", func(t *testing.T) {
		f := good()
		f.Set("threshold", "0")
		_, err := parseNewAchievement(f)
		if err == nil {
			t.Fatal("accepted threshold 0 — this awards the badge to the whole membership")
		}
		if !strings.Contains(err.Error(), "every member") {
			t.Errorf("the refusal does not say why: %v", err)
		}
	})

	for name, mutate := range map[string]func(url.Values){
		"no slug":            func(f url.Values) { f.Del("slug") },
		"threshold garbage":  func(f url.Values) { f.Set("threshold", "lots") },
		"negative threshold": func(f url.Values) { f.Set("threshold", "-5") },
		"ordinal garbage":    func(f url.Values) { f.Set("ordinal", "first") },
	} {
		t.Run(name+" is refused", func(t *testing.T) {
			f := good()
			mutate(f)
			if _, err := parseNewAchievement(f); err == nil {
				t.Errorf("accepted a form with %s", name)
			}
		})
	}
}

// The admin page executes against the real view model. html/template streams,
// so a field the model lacks truncates the page mid-render with a 200 and no
// error — which is why every assertion set below includes content from the
// LAST section of the page (the create form's help text).
func TestAdminPageRenders(t *testing.T) {
	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatalf("parse: %v", err)
	}
	backfilled := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	vm := achAdminVM{
		Msg: "Created centurion",
		Achievements: []AchievementDef{
			{ID: 1, Slug: "centurion", Name: "Centurion", Metric: "forum.post.created",
				Threshold: 100, RewardSlug: "badge-bonus", Enabled: true,
				BackfilledAt: &backfilled},
			// A pure badge, disabled and hidden: the row that must say
			// "badge only" rather than an empty cell.
			{ID: 2, Slug: "quiet-one", Name: "Quiet One", Trigger: "auth.first_login",
				Hidden: true},
		},
		MetricOptions:  []string{"forum.post.created", "uploads"},
		TriggerOptions: []string{"auth.first_login", "auth.login"},
		IconOptions:    []string{"star"},
		CanUpload:      true,
	}

	out := renderAdmin(t, p, vm)
	for _, want := range []string{
		"Created centurion",
		"centurion", "badge-bonus",
		"quiet-one", "badge only", "on event", "hidden",
		"2026-08-01", // the backfilled stamp, proving the *time.Time renders
		"forum.post.created", "auth.first_login",
		"a one_off reward's slug from the Rewards page; blank = a pure badge",
		"localization-slug", // last element of the page
	} {
		if !strings.Contains(out, want) {
			t.Errorf("admin page is missing %q — likely truncated mid-render", want)
		}
	}
}

// The empty state has to render too: a fresh install has no achievements, and
// that is the first page an operator sees.
func TestAdminPageRendersEmpty(t *testing.T) {
	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := renderAdmin(t, p, achAdminVM{Err: "bad slug"})
	for _, want := range []string{"No achievements defined yet", "bad slug"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty admin page is missing %q", want)
		}
	}
}

// Every POST form on the page targets this plugin's own admin actions —
// where the host's CSRF story applies (the chrome injects the _csrf field on
// submit, and the middleware refuses a tokenless POST). A form posting
// anywhere else would silently sit outside that protection, so the count of
// forms and the count of same-page actions must match.
func TestAdminFormsAllPostToTheAdminActions(t *testing.T) {
	p := &Plugin{}
	if err := p.parseTemplates(); err != nil {
		t.Fatalf("parse: %v", err)
	}
	vm := achAdminVM{Achievements: []AchievementDef{
		{ID: 1, Slug: "a", Metric: "m", Threshold: 1},
		{ID: 2, Slug: "b", Trigger: "t"},
	}}
	out := renderAdmin(t, p, vm)

	forms := strings.Count(out, `<form method="post"`)
	toSelf := strings.Count(out, `action="/admin/p/achievements/`)
	if forms == 0 {
		t.Fatal("no POST forms rendered; the assertion below would pass vacuously")
	}
	if forms != toSelf {
		t.Errorf("%d POST forms but %d post to /admin/p/achievements/* — a stray form "+
			"sits outside the host's CSRF handling", forms, toSelf)
	}
	// Two toggle rows plus the create form.
	if forms != 3 {
		t.Errorf("got %d forms for 2 rows, want 3 (one toggle each + create)", forms)
	}
}

func renderAdmin(t *testing.T, p *Plugin, vm achAdminVM) string {
	t.Helper()
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "achievements_admin.html", vm); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return sb.String()
}

// The page renders achievements through the Store interface, so it works
// against the in-memory store with no database.
func TestAdminPageReadsDefsFromTheStore(t *testing.T) {
	m := NewMemStore()
	m.SeedAchievement(AchievementDef{
		Slug: "centurion", Name: "Centurion", Metric: "forum.post.created",
		Threshold: 100, RewardSlug: "badge-bonus", Enabled: true,
	})
	p := &Plugin{store: m}
	if err := p.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	out, err := p.renderAdminPage(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "centurion") {
		t.Error("a seeded def did not reach the page")
	}
}
