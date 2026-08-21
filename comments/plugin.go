// Package comments puts a conversation under whatever a page is about.
//
// It is the most-used social surface on a site of this kind and the release
// page had nowhere to say anything: is this the good encode, this one is
// missing subs, thanks. The forum exists, and a forum thread is the wrong
// shape — a comment is attached to a thing, not filed under a category, and
// nobody wants forty threads titled after release names in the index.
//
// KEYED BY SUBJECT, NOT BY RELEASE. A release here can also exist as a torrent
// on the tracker, and the two have different ids. The comment is about the
// RELEASE — the encode, the audio, whether the pack is complete — so keying on
// whichever id the page happened to hold would strand the conversation the day
// somebody mirrored it. (subject_kind, subject_id) is the fix, and it also
// means the next thing that wants comments is a new value in one column.
//
// RENDERED AS A WIDGET, which is what makes it work without the host knowing
// this plugin exists. The release page already declares what it is about
// through core.SetWidgetItem and already renders a "release" region; this
// plugin puts a widget there and reads the subject from the context. No host
// template names it, and the same widget works on any future page that
// declares a subject the same way.
package comments

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"

	"github.com/the-loon-clan/loon/core"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed templates/*.html
var tmplFS embed.FS

func init() {
	core.RegisterPlugin("comments", func() core.Plugin { return &Plugin{} })
}

// Route paths. Under /p/ because that is where plugin surfaces live on a loon
// host, and each is a POST because each writes.
const (
	postPath   = "/p/comments/post"
	editPath   = "/p/comments/edit"
	deletePath = "/p/comments/delete"
)

// bodyMax bounds a comment. Long enough for a real opinion about an encode,
// short enough that one comment cannot be the page.
const bodyMax = 4000

type Plugin struct {
	core *core.Core
	st   Store
	tmpl *template.Template

	// points pays the author of a thanked comment. Optional: without it thanks
	// still work as a signal and simply pay nothing, which is a poorer feature
	// but a working one.
	points core.PointsService
	// users resolves author names for a rendered list. Optional: without it
	// every comment renders as "a member", which is a worse page but a working
	// one — and far better than the plugin growing its own copy of usernames.
	users core.UsersService
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "comments",
		Version:     "0.1.0",
		Description: "Comments attached to whatever a page is about — releases today, anything that declares a subject tomorrow.",
		Migrations:  migrations,
		Processes:   []string{"web"},
		Flavours:    []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	db := c.Storage.SchemaDB(p.Metadata().Name)
	if db == nil {
		return fmt.Errorf("comments: Core.Storage.SchemaDB is nil")
	}
	p.st = NewPGStore(db)

	// Declared in Provision so the directory lists them at boot, whether or
	// not anybody has commented yet.
	if err := declareEvents(c); err != nil {
		return fmt.Errorf("comments: declaring events: %w", err)
	}

	tmpl, err := template.New("comments").Funcs(tmplFuncs()).ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("comments: parsing templates: %w", err)
	}
	p.tmpl = tmpl

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("comments: Core.Router.Engine() is nil")
	}
	// Behind the host's user gate: posting, editing and deleting all need to
	// know who you are, and the gate is the host's to run.
	authed := append(c.Auth.RequireUser(core.RoleUser), p.handlePost)
	engine.POST(postPath, authed...)
	engine.POST(editPath, append(c.Auth.RequireUser(core.RoleUser), p.handleEdit)...)
	engine.POST(deletePath, append(c.Auth.RequireUser(core.RoleUser), p.handleDelete)...)
	engine.POST(thanksPath, append(c.Auth.RequireUser(core.RoleUser), p.handleThanks)...)

	// Thanks is switchable on a running site. A SMALL feature — one button
	// inside a widget — and the reason the flag exists at all: an operator who
	// decides the economy does not need another faucet should not have to
	// choose between keeping it and losing the comments with it.
	if err := c.RegisterFeature(core.Feature{
		Key:   featureThanks,
		Title: "Thanks on comments",
		Description: "Members can thank a comment, and its author earns points. " +
			"Switched off, the button disappears and no more points are awarded — " +
			"the thanks already given are kept, and counts stop being shown.",
		Default: true,
	}); err != nil {
		return fmt.Errorf("comments: register thanks feature: %w", err)
	}

	return c.RegisterWidget(core.Widget{
		Slug:        "comments",
		Title:       "Comments",
		Description: "A conversation attached to whatever the page is about.",
		// Public so anonymous visitors can READ. Whether they may post is a
		// separate question the widget answers per viewer — a comment section
		// only members can see is a comment section nobody joins for.
		Public:  true,
		Regions: []string{"release-main"},
		Render:  p.widget,
	})
}

// Start resolves the user directory.
func (p *Plugin) Start(ctx context.Context) error {
	if p.core == nil {
		return nil
	}
	p.points = p.pointsFor(p.core)
	if p.points == nil {
		log.Printf("comments: no core.Points service — thanks will be recorded " +
			"and will pay nobody.")
	}
	p.users = p.core.Users
	if p.users == nil {
		log.Printf("comments: no core.Users service — every comment will render as " +
			"an unnamed member. The host should wire one.")
	}
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

// logf is the plugin's one log call site, so a caller does not have to reach
// for the standard logger in the middle of a handler.
func (p *Plugin) logf(format string, args ...any) { log.Printf(format, args...) }
