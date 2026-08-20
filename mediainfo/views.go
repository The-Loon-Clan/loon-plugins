package mediainfo

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

// reportVM is one member's report as the page shows it.
type reportVM struct {
	ID      int64
	Author  string
	When    time.Time
	Edited  bool
	Deleted bool
	Mine    bool
	// CanRemove is separate from Mine: an author may withdraw their own
	// report, a moderator may withhold anybody's.
	CanRemove bool

	Summary  string
	Video    []Track
	Audio    []Track
	Text     []Track
	General  []Field
	Chapters []Chapter
}

// shotVM is one screenshot.
type shotVM struct {
	ID  int64
	URL string
	// Source is where the member said it came from, shown as a caption rather
	// than as the image's address — see the plugin doc on why the two are not
	// the same thing.
	Source    string
	Author    string
	CanRemove bool
}

// widget renders everything contributed about whatever the page is about.
func (p *Plugin) widget(c *gin.Context) (template.HTML, error) {
	ref, ok := core.WidgetItem(c)
	if !ok || ref.Kind != "release" {
		// The host did not say what this page is about, or it is about
		// something that is not a release. Nothing rather than a form whose
		// posts would have no subject.
		return "", nil
	}
	ctx := c.Request.Context()
	reports, err := p.st.Reports(ctx, ref.ID)
	if err != nil {
		return "", err
	}
	// No screenshots at all when the feature is off: none shown, none
	// queried, and the form below not offered. The stored files stay where
	// they are and come back when it is switched on.
	shotsOn := core.FeatureOn(p.core, featureShots)
	var shots []Shot
	if shotsOn {
		shots, err = p.st.Shots(ctx, ref.ID)
		if err != nil {
			return "", err
		}
	}

	viewer, _ := p.core.Auth.CurrentUser(c)
	var viewerID int64
	staff := false
	if viewer != nil {
		viewerID, staff = viewer.ID, viewer.AtLeast(core.RoleMod)
	}

	// Every author's name in ONE call, across both lists. Resolving per row
	// would be a query per report on a page that already holds the ids.
	names := map[int64]string{}
	if p.users != nil {
		ids := make([]int64, 0, len(reports)+len(shots))
		for _, r := range reports {
			ids = append(ids, r.UserID)
		}
		for _, s := range shots {
			ids = append(ids, s.UserID)
		}
		if got, err := p.users.BulkDisplayNames(ctx, ids); err == nil {
			names = got
		}
	}
	who := func(id int64) string {
		if n := names[id]; n != "" {
			return n
		}
		return "a member"
	}

	vms := make([]reportVM, 0, len(reports))
	for _, r := range reports {
		// A withheld report is dropped for everybody except staff, who need to
		// see what was removed — the same rule the comments plugin follows.
		if r.Deleted() && !staff {
			continue
		}
		rep := r.Report()
		vm := reportVM{
			ID: r.ID, Author: who(r.UserID), When: r.CreatedAt,
			Edited: r.EditedAt != nil, Deleted: r.Deleted(),
			Mine:    viewerID != 0 && r.UserID == viewerID,
			Summary: rep.Summary(),
			Video:   rep.Of(KindVideo), Audio: rep.Of(KindAudio), Text: rep.Of(KindText),
			Chapters: rep.Chapters,
		}
		vm.CanRemove = vm.Mine || staff
		// A trimmed General section. The whole thing is forty lines of unique
		// ids and library versions; these five are the ones a reader choosing
		// between copies actually uses.
		if g := rep.Of(KindGeneral); len(g) > 0 {
			for _, name := range []string{"Format", "File size", "Duration", "Overall bit rate", "Writing application"} {
				if v := g[0].Get(name); v != "" {
					vm.General = append(vm.General, Field{Name: name, Value: v})
				}
			}
		}
		vms = append(vms, vm)
	}

	shotVMs := make([]shotVM, 0, len(shots))
	for _, s := range shots {
		shotVMs = append(shotVMs, shotVM{
			ID: s.ID, URL: s.CachePath, Source: s.SourceURL, Author: who(s.UserID),
			CanRemove: (viewerID != 0 && s.UserID == viewerID) || staff,
		})
	}

	// The member's own report, to pre-fill the box. Somebody correcting a
	// paste should not have to find it again.
	mine := ""
	if viewerID != 0 {
		if row, found, err := p.st.MineFor(ctx, ref.ID, viewerID); err == nil && found {
			mine = row.Raw
		}
	}
	shotsLeft := 0
	if shotsOn && viewerID != 0 && p.images != nil {
		if n, err := p.st.ShotCount(ctx, ref.ID, viewerID); err == nil {
			shotsLeft = shotsPerMember - n
		}
	}

	return p.render("mediainfo_widget.html", map[string]any{
		"CSRF":      pluginapi.CSRFToken(p.core, c),
		"ReleaseID": ref.ID,
		"Back":      c.Request.URL.RequestURI(),
		"Reports":   vms,
		"Shots":     shotVMs,
		"CanPost":   viewerID != 0,
		"Mine":      mine,
		"Max":       rawMax,
		"ShotsLeft": shotsLeft,
		"Err":       c.Query("mierr"),
	})
}

// handlePost takes a member's MediaInfo paste.
func (p *Plugin) handlePost(c *gin.Context) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	back := backTo(c)
	releaseID := formID(c, "release")
	raw := strings.TrimSpace(c.PostForm("raw"))
	switch {
	case releaseID <= 0:
		c.Redirect(http.StatusSeeOther, back)
		return
	case raw == "":
		// An empty box is "withdraw mine", which is what somebody clearing the
		// field and pressing save means.
		if row, found, err := p.st.MineFor(c.Request.Context(), releaseID, u.ID); err == nil && found {
			_, _ = p.st.RemoveReport(c.Request.Context(), row.ID, u.ID, false)
		}
		c.Redirect(http.StatusSeeOther, back)
		return
	case len(raw) > rawMax:
		c.Redirect(http.StatusSeeOther, withErr(back, "toolong"))
		return
	}

	rep := Parse(raw)
	if !rep.Meaningful() {
		// The parse recognised nothing, which almost always means the wrong
		// thing was pasted. Refused with a sentence rather than stored: an
		// empty panel under somebody's name is worse than no panel.
		c.Redirect(http.StatusSeeOther, withErr(back, "unparsed"))
		return
	}
	if err := p.st.Upsert(c.Request.Context(), releaseID, u.ID, raw, rep); err != nil {
		c.Redirect(http.StatusSeeOther, withErr(back, "failed"))
		return
	}
	p.reportsPosted.Add(1)
	c.Redirect(http.StatusSeeOther, back)
}

// handleShot fetches and stores a screenshot.
func (p *Plugin) handleShot(c *gin.Context) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	back := backTo(c)
	releaseID := formID(c, "release")
	remote := strings.TrimSpace(c.PostForm("url"))
	if releaseID <= 0 || remote == "" {
		c.Redirect(http.StatusSeeOther, back)
		return
	}
	// No intake, or the feature is off, means this site did not offer the
	// field. Reaching here is a stale page or a forged post.
	if p.images == nil || !core.FeatureOn(p.core, featureShots) {
		c.Redirect(http.StatusSeeOther, withErr(back, "noshots"))
		return
	}
	ctx := c.Request.Context()
	// The cap is checked HERE and not only by hiding the form: a form in a
	// browser outlives the page it came from.
	if n, err := p.st.ShotCount(ctx, releaseID, u.ID); err != nil || n >= shotsPerMember {
		c.Redirect(http.StatusSeeOther, withErr(back, "toomany"))
		return
	}

	stored, err := p.images.FetchImage(ctx, shotDir, remote)
	if err != nil {
		p.shotFailures.Add(1)
		// The intake's own sentence, which is written to be shown: it says
		// what was wrong with the link and nothing about this site's network.
		c.Redirect(http.StatusSeeOther, withErr(back, "fetch")+"&miemsg="+urlSafe(err.Error()))
		return
	}
	if err := p.st.AddShot(ctx, Shot{
		ReleaseID: releaseID, UserID: u.ID,
		SourceURL: remote, CachePath: stored.URL, Bytes: stored.Bytes,
	}); err != nil {
		c.Redirect(http.StatusSeeOther, withErr(back, "failed"))
		return
	}
	p.shotsFetched.Add(1)
	c.Redirect(http.StatusSeeOther, back)
}

// backTo is where a write returns the viewer.
//
// Taken from the form and REFUSED unless it is a same-site path: a redirect
// target that came from a request is an open redirect the moment it is trusted.
// This widget is placed on pages it does not know the addresses of, which is
// exactly why the form carries the path and exactly why it cannot be believed.
func backTo(c *gin.Context) string {
	b := strings.TrimSpace(c.PostForm("back"))
	if strings.HasPrefix(b, "/") && !strings.HasPrefix(b, "//") {
		return b
	}
	return "/"
}

// withErr adds the outcome key, minding whether the path already has a query.
func withErr(back, key string) string {
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	return back + sep + "mierr=" + key
}

// urlSafe percent-encodes a message for a query string.
func urlSafe(s string) string {
	if len(s) > 200 {
		s = s[:200]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('+')
		default:
			b.WriteString("%" + strconv.FormatInt(int64(r), 16))
		}
	}
	return b.String()
}

// formID reads a positive id from a form field.
func formID(c *gin.Context, field string) int64 {
	id, _ := strconv.ParseInt(c.PostForm(field), 10, 64)
	if id <= 0 {
		return 0
	}
	return id
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
