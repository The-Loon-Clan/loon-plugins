package comments

import (
	"bytes"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// commentVM is one comment as the page shows it.
type commentVM struct {
	ID     int64
	Author string
	// AuthorFX is the name-effect class for this author, or "".
	//
	// Resolved HERE rather than left to the host, because this widget draws its
	// own author markup instead of calling the host's user-tag — so an effect
	// that reaches every listing on the site would stop dead at the comment
	// thread, which is the one place a name is attached to something the person
	// actually said.
	AuthorFX string
	Body    string
	When    time.Time
	Edited  bool
	Deleted bool
	// Mine and CanDelete drive the controls. Separate because they are
	// different permissions: an author may edit their own words, a moderator
	// may only remove them — nobody edits somebody else's comment, which would
	// put words in their mouth under their name.
	Mine      bool
	CanDelete bool

	// Thanks is how many members found this useful, and Thanked whether the
	// viewer is one of them. CanThank is separate from both: you cannot thank
	// your own comment, a removed one, or anything at all while signed out.
	Thanks   int
	Thanked  bool
	CanThank bool
}

// widget renders the conversation for whatever the page is about.
func (p *Plugin) widget(c *gin.Context) (template.HTML, error) {
	ref, ok := core.WidgetItem(c)
	if !ok {
		// The host did not say what this page is about, so there is nothing to
		// attach a conversation to. Render nothing rather than a comment box
		// whose posts would have no subject.
		return "", nil
	}
	ctx := c.Request.Context()
	rows, err := p.st.List(ctx, ref.Kind, ref.ID, 0)
	if err != nil {
		return "", err
	}

	viewer, _ := p.core.Auth.CurrentUser(c)
	var viewerID int64
	staff := false
	if viewer != nil {
		viewerID = viewer.ID
		staff = viewer.AtLeast(core.RoleMod)
	}

	// Every author's name in ONE call. A list of forty comments by six people
	// is six names, and resolving them per row would be forty queries for a
	// page that already has the ids.
	names := map[int64]string{}
	if p.users != nil {
		ids := make([]int64, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.UserID)
		}
		if got, err := p.users.BulkDisplayNames(ctx, ids); err == nil {
			names = got
		}
	}

	// Thanks for the whole thread in one call — see ThanksStore. A per-row
	// lookup would be two more queries per comment on a page that already has
	// every id it needs.
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	thanks, mine, err := p.st.ThanksFor(ctx, ids, viewerID)
	if err != nil {
		// A thanks count that could not be read must not take the conversation
		// down with it: render the thread with no counts rather than an error
		// page where the comments were.
		thanks, mine = map[int64]int{}, map[int64]bool{}
	}

	out := make([]commentVM, 0, len(rows))
	for _, r := range rows {
		vm := commentVM{
			ID: r.ID, When: r.CreatedAt, Edited: r.Edited(), Deleted: r.Deleted(),
			Author: names[r.UserID],
		}
		if vm.Author == "" {
			vm.Author = "a member"
		}
		vm.AuthorFX = pluginapi.NameClass(p.core, vm.Author)
		vm.Thanks, vm.Thanked = thanks[r.ID], mine[r.ID]
		switch {
		case !r.Deleted():
			vm.Body = r.Body
			vm.Mine = viewerID != 0 && r.UserID == viewerID
			vm.CanDelete = vm.Mine || staff
			// Offered to a signed-in member on somebody ELSE's live comment.
			// Thanking your own is the first thing anybody tries and would pay
			// you for commenting.
			vm.CanThank = viewerID != 0 && !vm.Mine
		case staff:
			// Staff see what was said. A moderator asked "why did you remove
			// this" cannot answer from a row that shows nothing.
			vm.Body = r.Body
		}
		out = append(out, vm)
	}

	return p.render("comments_widget.html", map[string]any{
		"Comments":  out,
		"Kind":      ref.Kind,
		"SubjectID": ref.ID,
		"CanPost":   viewerID != 0,
		"CSRF":      pluginapi.CSRFToken(p.core, c),
		"Max":       bodyMax,
		// Where to come back to. The handlers redirect here rather than to a
		// path they construct, because this plugin does not know the host's
		// routes — the release page's URL is the host's to choose.
		"Back": c.Request.URL.Path,
	})
}

// handlePost adds a comment.
func (p *Plugin) handlePost(c *gin.Context) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	kind := strings.TrimSpace(c.PostForm("kind"))
	id, _ := strconv.ParseInt(c.PostForm("subject"), 10, 64)
	body := strings.TrimSpace(c.PostForm("body"))
	back := backTo(c)

	// An empty comment is a mis-click, not an error worth a page: send them
	// back to where they were with nothing changed.
	if kind == "" || id <= 0 || body == "" {
		c.Redirect(http.StatusSeeOther, back)
		return
	}
	if len(body) > bodyMax {
		body = body[:bodyMax]
	}
	if _, err := p.st.Add(c.Request.Context(), Comment{
		SubjectKind: kind, SubjectID: id, UserID: u.ID, Body: body,
	}); err != nil {
		c.Redirect(http.StatusSeeOther, back)
		return
	}
	c.Redirect(http.StatusSeeOther, back)
}

// handleEdit rewrites the viewer's own comment.
func (p *Plugin) handleEdit(c *gin.Context) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	id, _ := strconv.ParseInt(c.PostForm("id"), 10, 64)
	body := strings.TrimSpace(c.PostForm("body"))
	if len(body) > bodyMax {
		body = body[:bodyMax]
	}
	if id > 0 && body != "" {
		// Ownership is checked in the statement — see Store.Edit. A false is
		// "not yours, or already removed", and both send the viewer back
		// unchanged rather than explaining which.
		_, _ = p.st.Edit(c.Request.Context(), id, u.ID, body)
	}
	c.Redirect(http.StatusSeeOther, backTo(c))
}

// handleDelete withholds a comment.
func (p *Plugin) handleDelete(c *gin.Context) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	id, _ := strconv.ParseInt(c.PostForm("id"), 10, 64)
	if id > 0 {
		_, _ = p.st.Delete(c.Request.Context(), id, u.ID, u.AtLeast(core.RoleMod))
	}
	c.Redirect(http.StatusSeeOther, backTo(c))
}

// backTo is where a write returns the viewer.
//
// Taken from the form and REFUSED unless it is a same-site path. A redirect
// target that came from a request is an open redirect the moment it is trusted
// — somebody posts a link to a real comment form with a foreign "back", and
// the site bounces its own members off it wearing its own domain.
func backTo(c *gin.Context) string {
	b := strings.TrimSpace(c.PostForm("back"))
	if strings.HasPrefix(b, "/") && !strings.HasPrefix(b, "//") {
		return b
	}
	return "/"
}

func (p *Plugin) render(name string, data any) (template.HTML, error) {
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func tmplFuncs() template.FuncMap {
	return template.FuncMap{
		// The site's own relative-time helper is not reachable from a plugin's
		// own template set, so this is the small version: enough for "2 hours
		// ago" and honest about anything older by showing the date.
		"ago": func(t time.Time) string {
			d := time.Since(t)
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				return strconv.Itoa(int(d.Minutes())) + "m ago"
			case d < 24*time.Hour:
				return strconv.Itoa(int(d.Hours())) + "h ago"
			case d < 7*24*time.Hour:
				return strconv.Itoa(int(d.Hours()/24)) + "d ago"
			default:
				return t.Format("2 Jan 2006")
			}
		},
	}
}
