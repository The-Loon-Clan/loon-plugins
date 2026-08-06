package messages

import (
	"html/template"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// Lifted markup, executed rather than only compiled: html/template streams, so
// a field the markup wants and the data lacks returns half a page silently.

// bind stands in for the seams the templates call. Markdown is deliberately
// NOT a real renderer here — these tests are about markup, and a second
// sanitiser is the very thing the seam exists to avoid.
func bind(t *testing.T) {
	t.Helper()
	SetDeps(Deps{
		Markdown:     func(s string) template.HTML { return template.HTML(s) },
		RelativeTime: func(any) string { return "2 hours ago" },
	})
	parseTemplates()
	t.Cleanup(func() { deps = nil; pageTmpl = nil })
}

func render1(t *testing.T, name string, data map[string]any) string {
	t.Helper()
	bind(t)
	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	s := sb.String()
	if strings.TrimSpace(s) == "" {
		t.Fatalf("%s rendered empty", name)
	}
	for _, unwanted := range []string{"<!DOCTYPE", "<html", `template "navbar"`, `template "footer"`} {
		if strings.Contains(s, unwanted) {
			t.Errorf("%s carries host chrome it should not: %q", name, unwanted)
		}
	}
	return s
}

// inboxData mirrors the keys the Inbox handler passes, read off the render
// call rather than invented — a test built from guessed keys proves the
// template renders SOMETHING, which is not the question.
func inboxData(extra map[string]any) map[string]any {
	d := map[string]any{
		"PageTitle": "Inbox", "ActiveNav": "inbox",
		"Items": []InboxItem{}, "ActiveThreadID": int64(0),
		"ActiveCpName": "", "ActiveCpID": 0,
		"ActiveMessages": []*DMMessageView{}, "ActiveBlocked": false,
		"ActiveMessage": nil, "ComposeMode": false, "CanSendDM": true,
		"ComposeOk": false, "ComposeError": "", "PrefillRecipient": "",
		// Injected by render() in production.
		"CSRFToken": "test-csrf", "ViewerID": 42,
		"EditorHTML": template.HTML(`<div id="md-ed"></div>`),
	}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

// The composer is gated behind ComposeMode, so the default view does not
// exercise it — both branches have to be rendered.
func TestInboxRendersBothModes(t *testing.T) {
	list := render1(t, "inbox.html", inboxData(nil))
	if strings.Contains(list, `<div id="md-ed">`) {
		t.Error("the list view drew a composer it should not")
	}

	compose := render1(t, "inbox.html", inboxData(map[string]any{
		"ComposeMode": true, "PrefillRecipient": "kirisame"}))
	for _, want := range []string{`<div id="md-ed">`, "kirisame"} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose view missing %q", want)
		}
	}
}

// Every POST form on this page must carry the CSRF field, because the site
// gate rejects a POST without it — and a missing hidden input is invisible on
// the rendered page, so the only symptom is a 403 on submit.
//
// The compose form shipped without one on 2026-05-21 and starting a new
// conversation has been impossible ever since: zero dm_threads rows existed
// when this lift was done. Counting forms is the assertion, so a form added
// later without a token fails here rather than in production.
func TestEveryPostFormCarriesTheCSRFField(t *testing.T) {
	for _, tc := range []struct {
		name string
		data map[string]any
	}{
		{"compose", inboxData(map[string]any{"ComposeMode": true})},
		{"thread", inboxData(map[string]any{
			"ActiveThreadID": int64(7), "ActiveCpID": 7, "ActiveCpName": "them",
			"ActiveMessages": []*DMMessageView{}})},
		{"announcement", inboxData(map[string]any{
			"ActiveMessage": &Announcement{ID: 3, FromName: "staff", Title: "hi", Body: "b"}})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := render1(t, "inbox.html", tc.data)
			forms := strings.Count(out, `method="POST"`)
			tokens := strings.Count(out, `name="_csrf" value="test-csrf"`)
			if forms == 0 {
				t.Fatalf("no POST form rendered — the branch under test did not open")
			}
			if tokens != forms {
				t.Errorf("%d POST forms but %d CSRF fields — a form would 403 on submit",
					forms, tokens)
			}
		})
	}
}

// The thread view is where $me decides which side each message sits on. It
// read .User.ID off the host's page data and now reads .ViewerID; a map miss
// would silently draw every message as the other person's.
func TestInboxThreadSidesOnViewerID(t *testing.T) {
	msgs := []*DMMessageView{
		{ID: 1, ThreadID: 7, SenderID: 42, SenderUsername: "me", Body: "mine"},
		{ID: 2, ThreadID: 7, SenderID: 7, SenderUsername: "them", Body: "theirs"},
	}
	data := func(viewer int) map[string]any {
		return inboxData(map[string]any{
			"ActiveThreadID": int64(7), "ActiveCpID": 7, "ActiveCpName": "them",
			"ActiveMessages": msgs, "ViewerID": viewer})
	}
	mine := render1(t, "inbox.html", data(42))
	flipped := render1(t, "inbox.html", data(7))
	if mine == flipped {
		t.Error("the thread rendered identically for both participants — " +
			"$me is not reading ViewerID, so every message shows as the other side's")
	}
	// Anonymous must be a stranger to the thread, not a panic.
	render1(t, "inbox.html", data(0))
}

func TestAdminMessagesRenders(t *testing.T) {
	got := render1(t, "admin_messages.html", map[string]any{
		"Messages": []*Announcement{{ID: 1, FromName: "staff", Title: "hello", Body: "body"}},
		"Users":    []UserOption{{ID: 7, Username: "them"}},
		"Total":    1, "Page": 1, "TotalPages": 1,
		"CSRFToken":      "test-csrf",
		"PaginationHTML": template.HTML(`<nav id="pg"></nav>`),
	})
	if !strings.Contains(got, `<nav id="pg">`) {
		t.Error("admin log did not place the host-rendered pager")
	}
}

// Provision must reject a host that wired none of the render seams, rather
// than leaving pageTmpl nil and panicking on the first page view.
func TestProvisionRequiresTheRenderSeams(t *testing.T) {
	t.Cleanup(func() { deps = nil; pageTmpl = nil })
	SetDeps(Deps{})
	if pageTmpl != nil {
		t.Fatal("templates parsed without the seams they depend on")
	}
	_ = gin.Mode()
}
