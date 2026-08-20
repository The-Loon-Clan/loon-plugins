package polls

import (
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// adminPath is where every action returns.
const adminPath = "/admin/p/polls"

// ttlChoices are the run lengths offered, in hours, with 0 meaning "until
// somebody closes it".
//
// Presets rather than a date picker: an operator writing a poll is thinking
// "about a week", not "the 27th at 14:00", and a free-text date is a field
// that can be typed wrong in a dozen ways for no gain.
var ttlChoices = []struct {
	Hours int
	Label string
}{
	{0, "Until I close it"},
	{24, "24 hours"},
	{72, "3 days"},
	{168, "7 days"},
	{336, "14 days"},
	{720, "30 days"},
}

// resultsChoices are the three moments a tally can become readable.
var resultsChoices = []struct {
	Key, Label, Hint string
}{
	{ResultsAfterVote, "After voting", "The tally arrives once somebody has committed to an answer. The safe default."},
	{ResultsAlways, "Always", "Anyone can see the numbers, voted or not. For a temperature check where the tally is the point."},
	{ResultsOnClose, "When it closes", "Nobody sees anything until it is over. For a vote where an early lead would campaign for one side."},
}

// pollRowVM is one poll in the admin list.
type pollRowVM struct {
	ID       int64
	Slug     string
	Question string
	Results  string
	Votes    int
	Closed   bool
	ClosesAt *time.Time
	ClosedAt *time.Time
	// Shortcode is the thing an operator actually came here for: the string to
	// paste into a page to make this poll appear.
	Shortcode string
}

func (p *Plugin) adminPage(c *gin.Context) (template.HTML, error) {
	polls, votes, err := p.st.List(c.Request.Context())
	if err != nil {
		return "", err
	}
	now := time.Now()
	rows := make([]pollRowVM, 0, len(polls))
	for i, poll := range polls {
		rows = append(rows, pollRowVM{
			ID: poll.ID, Slug: poll.Slug, Question: poll.Question,
			Results: resultsLabel(poll.Results), Votes: votes[i],
			Closed: poll.Closed(now), ClosesAt: poll.ClosesAt, ClosedAt: poll.ClosedAt,
			Shortcode: "[widget poll " + poll.Slug + "]",
		})
	}
	return p.render("polls_admin.html", map[string]any{
		"CSRF":    pluginapi.CSRFToken(p.core, c),
		"Polls":   rows,
		"TTLs":    ttlChoices,
		"Results": resultsChoices,
		"Err":     c.Query("err"),
		"Made":    c.Query("made"),
	})
}

func resultsLabel(key string) string {
	for _, r := range resultsChoices {
		if r.Key == key {
			return r.Label
		}
	}
	return key
}

// adminCreate writes a poll.
func (p *Plugin) adminCreate(c *gin.Context) (template.HTML, error) {
	fail := func(key string) (template.HTML, error) {
		c.Redirect(303, adminPath+"?err="+key)
		return "", nil
	}
	question := strings.TrimSpace(c.PostForm("question"))
	if question == "" || len(question) > questionMax {
		return fail("bad-question")
	}

	// One option per LINE, which is how somebody writing a ballot already
	// thinks about it — and it needs no add-another-row JavaScript on a page
	// that otherwise needs none.
	var labels []string
	for _, line := range strings.Split(c.PostForm("options"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue // a stray blank line is not an option
		}
		if len(line) > labelMax {
			return fail("long-option")
		}
		labels = append(labels, line)
	}
	if len(labels) < optionsMin {
		return fail("few-options")
	}
	if len(labels) > optionsMax {
		return fail("many-options")
	}

	// The slug is optional and derived when blank, so an operator who does not
	// care about slugs never types one — but it is theirs to set, because it
	// is the name they will type in a shortcode.
	slug := slugify(c.PostForm("slug"))
	if slug == "" {
		slug = slugify(question)
	}
	if slug == "" {
		return fail("bad-slug")
	}

	results := ResultsAfterVote
	for _, r := range resultsChoices {
		if r.Key == c.PostForm("results") {
			results = r.Key
		}
	}

	var closesAt *time.Time
	if hours, _ := strconv.Atoi(c.PostForm("ttl")); hours > 0 {
		t := time.Now().Add(time.Duration(hours) * time.Hour)
		closesAt = &t
	}

	var by *int64
	if u, ok := p.core.Auth.CurrentUser(c); ok && u != nil {
		by = &u.ID
	}
	if err := p.st.Create(c.Request.Context(), Poll{
		Slug: slug, Question: question, Results: results,
		CreatedBy: by, ClosesAt: closesAt,
	}, labels); err != nil {
		// Overwhelmingly the unique index on slug, which is the one failure an
		// operator can act on: two polls cannot share a name because a
		// shortcode names exactly one.
		return fail("taken")
	}
	// The new poll's name is handed straight back, because the next thing to
	// do with a poll is place it and placing it means typing this.
	c.Redirect(303, adminPath+"?made="+slug)
	return "", nil
}

func (p *Plugin) adminClose(c *gin.Context) (template.HTML, error) {
	id, _ := strconv.ParseInt(c.PostForm("id"), 10, 64)
	if id > 0 {
		_ = p.st.SetClosed(c.Request.Context(), id, c.PostForm("open") != "1")
	}
	c.Redirect(303, adminPath)
	return "", nil
}

// adminDelete removes a poll and every vote in it.
//
// Guarded by a typed confirmation in the form rather than a dialog, because
// this is the one irreversible thing on the page: closing a poll keeps the
// answers, deleting it throws away what people said.
func (p *Plugin) adminDelete(c *gin.Context) (template.HTML, error) {
	id, _ := strconv.ParseInt(c.PostForm("id"), 10, 64)
	if id > 0 && strings.EqualFold(strings.TrimSpace(c.PostForm("confirm")), c.PostForm("slug")) {
		_ = p.st.Delete(c.Request.Context(), id)
	}
	c.Redirect(303, adminPath)
	return "", nil
}
