package polls

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

// pollVM is one poll as a placement draws it.
type pollVM struct {
	Slug     string
	Question string
	Closed   bool
	ClosesAt *time.Time
	Options  []optionVM
	Total    int

	// ShowResults and CanVote are independent, and every combination of them
	// is a real state: an open poll you have answered shows both, a closed one
	// shows results and no ballot, an 'on_close' poll you have answered shows
	// neither and says so.
	ShowResults bool
	CanVote     bool
	Voted       bool
	SignedIn    bool

	CSRF string
	Back string
}

type optionVM struct {
	ID      int64
	Label   string
	Votes   int
	Percent int
	// Mine marks the viewer's own answer, which is the one thing a results
	// bar cannot say with a number.
	Mine bool
}

// widget renders whichever poll this placement names.
func (p *Plugin) widget(c *gin.Context) (template.HTML, error) {
	slug := strings.TrimSpace(core.WidgetConfig(c))
	if slug == "" {
		// Not configured. Nothing, per core.WidgetConfig: an operator who
		// cleared the field meant to clear it, and a poll that fell back to
		// some default question would be one nobody could turn off.
		return "", nil
	}
	ctx := c.Request.Context()
	poll, opts, found, err := p.st.BySlug(ctx, slug)
	if err != nil {
		return "", err
	}
	viewer, _ := p.core.Auth.CurrentUser(c)
	if !found {
		// The same bargain the shortcode expander makes with an unknown slug:
		// an admin is told, because a typo that renders nothing is otherwise
		// indistinguishable from a poll that has not been written yet, and the
		// person who can fix it is exactly the one who should know. Everybody
		// else gets an empty space where a poll they never knew about was.
		if viewer != nil && viewer.AtLeast(core.RoleAdmin) {
			return p.render("poll_missing.html", map[string]any{"Slug": slug})
		}
		return "", nil
	}

	var viewerID int64
	if viewer != nil {
		viewerID = viewer.ID
	}
	var mine int64
	voted := false
	if viewerID != 0 {
		mine, voted, _ = p.st.VoteOf(ctx, poll.ID, viewerID)
	}

	closed := poll.Closed(time.Now())
	vm := pollVM{
		Slug:        poll.Slug,
		Question:    poll.Question,
		Closed:      closed,
		ClosesAt:    poll.ClosesAt,
		Voted:       voted,
		SignedIn:    viewerID != 0,
		CanVote:     viewerID != 0 && !closed,
		ShowResults: showResults(poll.Results, voted, closed),
		CSRF:        pluginapi.CSRFToken(p.core, c),
		Back:        c.Request.URL.RequestURI(),
	}
	for _, o := range opts {
		vm.Total += o.Votes
	}
	for _, o := range opts {
		vm.Options = append(vm.Options, optionVM{
			ID: o.ID, Label: o.Label, Votes: o.Votes,
			Percent: percent(o.Votes, vm.Total),
			Mine:    voted && o.ID == mine,
		})
	}
	return p.render("poll_widget.html", vm)
}

// showResults answers the one editorial question this plugin has.
//
// A closed poll shows its results under every policy, including 'after_vote'
// for somebody who never voted: withholding the answer once the question is
// over serves nobody, and the reason to withhold it — not moving the vote —
// stopped applying when the last vote was cast.
func showResults(policy string, voted, closed bool) bool {
	switch policy {
	case ResultsAlways:
		return true
	case ResultsOnClose:
		return closed
	default: // ResultsAfterVote
		return voted || closed
	}
}

// percent rounds a share to whole numbers for a bar's width.
func percent(n, total int) int {
	if total <= 0 {
		return 0
	}
	return int((float64(n)/float64(total))*100 + 0.5)
}

// handleVote takes one answer.
func (p *Plugin) handleVote(c *gin.Context) {
	back := backTo(c)
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	// Switched off since the ballot was drawn. Silent — the form is not
	// offered, so reaching here is a stale page rather than a member to
	// explain something to.
	if !core.FeatureOn(p.core, featurePolls) {
		c.Redirect(http.StatusSeeOther, back)
		return
	}
	slug := strings.TrimSpace(c.PostForm("poll"))
	optionID, _ := strconv.ParseInt(c.PostForm("option"), 10, 64)
	if slug == "" || optionID <= 0 {
		c.Redirect(http.StatusSeeOther, back)
		return
	}
	ctx := c.Request.Context()

	// Resolved by SLUG rather than trusting a poll id in the form, so the only
	// thing a forged post can name is a poll that exists — and the option is
	// then checked against it in the statement (see PGStore.Vote).
	poll, _, found, err := p.st.BySlug(ctx, slug)
	if err != nil || !found {
		c.Redirect(http.StatusSeeOther, back)
		return
	}
	// A closed poll takes no votes. Checked HERE and not only in the template,
	// because the ballot a member had open when the poll closed is still a
	// working form in their browser.
	if poll.Closed(time.Now()) {
		c.Redirect(http.StatusSeeOther, back)
		return
	}
	_, _ = p.st.Vote(ctx, poll.ID, u.ID, optionID)
	c.Redirect(http.StatusSeeOther, back)
}

// backTo is where a vote returns the viewer.
//
// Taken from the form and REFUSED unless it is a same-site path: a redirect
// target that came from a request is an open redirect the moment it is
// trusted. A poll is placed on arbitrary pages, so it genuinely does not know
// where it was rendered — which is exactly why the form carries it and exactly
// why the value cannot be believed without checking.
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
		"date": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.Format("2 Jan 2006 15:04")
		},
	}
}
