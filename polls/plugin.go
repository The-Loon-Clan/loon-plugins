// Package polls asks the members a question and counts the answers.
//
// Staff use them for rule changes, members for arguments, and neither is well
// served by a forum thread: a thread tells you who is loudest and a poll tells
// you what people think. Sites of this kind reach for one constantly — should
// we allow this source, which of these two banners, is the ratio too harsh —
// and without one every such question becomes forty replies and a guess.
//
// PLACED, NOT PAGED. There is no /polls page and no template anywhere names a
// poll, because a poll is never the destination: it belongs on the front page
// during a rule change, in the sidebar of the forum it concerns, in the body
// of the news post that argues for it. So the whole plugin is ONE widget that
// takes a poll's slug as its per-placement setting, which means an operator
// puts a poll wherever they want the day they write it —
//
//	[widget poll rule-change]
//
// in a page body, or the same widget dropped in a region from the widget
// editor. Two placements of one widget are two different polls, and neither
// needed a code change.
//
// WHAT IT DELIBERATELY DOES NOT DO. Voting pays nothing. Every points-bearing
// action on this site is one somebody could do MORE of usefully; a poll wants
// considered answers from people who care about the question, and paying for
// them buys the opposite — a room full of members clicking the first option to
// collect. The reward for voting is the result.
package polls

import (
	"context"
	"embed"
	"fmt"
	"html/template"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed templates/*.html
var tmplFS embed.FS

func init() {
	core.RegisterPlugin("polls", func() core.Plugin { return &Plugin{} })
}

// votePath is where a ballot posts. One route, because casting a vote and
// changing one are the same act — see poll_votes' primary key.
const votePath = "/p/polls/vote"

// featurePolls is the switch (core.RegisterFeature). The widget and the admin
// view name it, so core hides both; the vote handler checks it too, because a
// ballot in somebody's browser outlives the page it came from.
const featurePolls = "polls"

// Bounds. A poll that does not fit on a sidebar card is a forum thread
// wearing a ballot, and the widget is the only surface this has.
const (
	questionMax = 200
	labelMax    = 120
	optionsMax  = 12
	optionsMin  = 2
)

type Plugin struct {
	core *core.Core
	st   Store
	tmpl *template.Template
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "polls",
		Version:     "0.1.0",
		Description: "Ask the members a question. Placed anywhere as a widget, one vote each, changeable while it is open.",
		Migrations:  migrations,
		Processes:   []string{"web"},
		Flavours:    []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	db := c.Storage.SchemaDB(p.Metadata().Name)
	if db == nil {
		return fmt.Errorf("polls: Core.Storage.SchemaDB is nil")
	}
	p.st = NewPGStore(db)

	tmpl, err := template.New("polls").Funcs(tmplFuncs()).ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("polls: parsing templates: %w", err)
	}
	p.tmpl = tmpl

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("polls: Core.Router.Engine() is nil")
	}
	// Behind the host's user gate. Voting needs to know who you are — that is
	// what makes it one vote each rather than one vote per click.
	engine.POST(votePath, append(c.Auth.RequireUser(core.RoleUser), p.handleVote)...)

	// The whole plugin, as one switch. A BIG feature in the sense that matters:
	// it takes the widget and the admin page together, so an operator who does
	// not want polls on their site gets no poll anywhere and no page offering
	// to write one — without a restart, and without losing the polls already
	// written.
	if err := c.RegisterFeature(core.Feature{
		Key:   featurePolls,
		Title: "Polls",
		Description: "Questions placed on any page, and the admin screen for writing them. " +
			"Switched off, every placed poll renders nothing and the admin page is gone. " +
			"The polls and their votes are kept, and come back when it is switched on.",
		Default: true,
	}); err != nil {
		return fmt.Errorf("polls: register feature: %w", err)
	}

	if err := c.RegisterView(core.View{
		Slug:        "polls",
		Feature:     featurePolls,
		Title:       "Polls",
		Description: "Write a question, place it where it belongs, and read the answers.",
		Slot:        core.SlotAdminPage,
		MinRole:     core.RoleAdmin,
		Nav:         core.NavHint{Group: "Community"},
		Render:      p.adminPage,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"create": p.adminCreate,
			"close":  p.adminClose,
			"delete": p.adminDelete,
		},
	}); err != nil {
		return fmt.Errorf("polls: register admin view: %w", err)
	}

	return c.RegisterWidget(core.Widget{
		Slug:        "poll",
		Feature:     featurePolls,
		Title:       "Poll",
		Description: "One question and its answers. Name the poll in the setting below.",
		// Public so anonymous visitors can READ the question, and the result
		// where the poll says so. Whether they may VOTE is a separate answer
		// the widget gives per viewer.
		Public: true,
		// No Regions, which means every region. A poll is as at home in a
		// sidebar as it is across the front page, and the widget renders the
		// same either way — there is no wide variant to protect.
		ConfigLabel: "Poll",
		ConfigHint:  "The poll's name from Admin → Polls, e.g. rule-change. Blank shows nothing.",
		Render:      p.widget,
	})
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }
