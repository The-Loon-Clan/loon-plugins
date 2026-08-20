// Package mediainfo carries what somebody who actually downloaded a release can
// tell everybody who has not: MediaInfo, screenshots, chapters.
//
// WHY IT IS CONTRIBUTED. A Usenet index holds pointers to articles, not the
// bytes. The host already reports everything the NZB's own file list proves —
// container, subtitle files and their languages, recovery share, how much of
// the download is not the film — and that is derived and therefore true.
// Bitrate, audio tracks, muxed subtitle tracks and chapters are simply not in
// an NZB. The only honest way to have them is for a member holding the file to
// say so, and for the page to be clear that is what it is.
//
// So the two are drawn as SEPARATE PANELS on purpose. One says "read from the
// NZB's own file list"; this one says who posted it and when. A reader must
// never have to guess whether a figure is a fact or a stranger's claim.
//
// SCREENSHOTS ARE FETCHED, NEVER HOTLINKED. A page that renders a remote image
// sends every one of its readers to a third party on load — handing that host a
// log of who reads what, and leaving it free to swap the picture afterwards. So
// the file is pulled once through the host's intake (pluginapi.ImageIntake),
// which owns the address rules, and served from here. This plugin never makes
// an outbound request itself; see that seam on why that is not its business.
package mediainfo

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed templates/*.html
var tmplFS embed.FS

func init() {
	core.RegisterPlugin("mediainfo", func() core.Plugin { return &Plugin{} })
}

// Route paths. Under /p/ because that is where plugin surfaces live, and each
// is a POST because each writes.
const (
	postPath   = "/p/mediainfo/post"
	removePath = "/p/mediainfo/remove"
	shotPath   = "/p/mediainfo/shot"
	unshotPath = "/p/mediainfo/unshot"
)

const (
	// rawMax bounds a paste. MediaInfo for a season pack is long; this is far
	// past any real one and stops the field being a way to store a novel.
	rawMax = 64 << 10
	// shotsPerMember is how many screenshots one member may add to one
	// release. Four frames is a set; twenty is somebody using a release page
	// as an image host.
	shotsPerMember = 4
	// shotDir is where intake stores them, under the host's uploads root.
	shotDir = "screenshots"
	// featureShots is the switch (core.RegisterFeature). Checked in the view
	// model and again in the handler, because a form outlives its page.
	featureShots = "mediainfo.screenshots"
)

type Plugin struct {
	core   *core.Core
	st     Store
	tmpl   *template.Template
	users  core.UsersService
	images pluginapi.ImageIntake
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "mediainfo",
		Version:     "0.1.0",
		Description: "MediaInfo, chapters and screenshots contributed by the members who downloaded a release — the things an NZB cannot tell you.",
		Migrations:  migrations,
		Processes:   []string{"web"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	db := c.Storage.SchemaDB(p.Metadata().Name)
	if db == nil {
		return fmt.Errorf("mediainfo: Core.Storage.SchemaDB is nil")
	}
	p.st = NewPGStore(db)

	tmpl, err := template.New("mediainfo").Funcs(tmplFuncs()).ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("mediainfo: parsing templates: %w", err)
	}
	p.tmpl = tmpl

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("mediainfo: Core.Router.Engine() is nil")
	}
	authed := c.Auth.RequireUser(core.RoleUser)
	engine.POST(postPath, append(authed, p.handlePost)...)
	engine.POST(removePath, append(authed, p.handleRemove)...)
	engine.POST(shotPath, append(authed, p.handleShot)...)
	engine.POST(unshotPath, append(authed, p.handleUnshot)...)

	// Screenshots, separately from the reports. A MEDIUM feature: half of one
	// widget. Worth its own switch because the two halves carry different
	// risk — a MediaInfo paste is technical text, a screenshot is an image
	// this site fetches from an address a member chose and then serves under
	// its own name.
	if err := c.RegisterFeature(core.Feature{
		Key:   featureShots,
		Title: "Screenshots on releases",
		Description: "Members can add screenshots to a release, which this site fetches once " +
			"and serves its own copy of. Switched off, the form is gone and existing " +
			"screenshots stop being shown — the stored files are kept.",
		Default: true,
	}); err != nil {
		return fmt.Errorf("mediainfo: register screenshots feature: %w", err)
	}

	return c.RegisterWidget(core.Widget{
		Slug:        "mediainfo",
		Title:       "Media details",
		Description: "MediaInfo, chapters and screenshots posted by members who downloaded this.",
		// Public so anonymous readers can SEE what is here — the whole value of
		// a contributed report is that it helps somebody choose before they
		// have an account. Whether they may POST is answered per viewer.
		Public:  true,
		Regions: []string{"release-main"},
		Render:  p.widget,
	})
}

// Start resolves the siblings.
//
// In Start rather than Provision because every Provision runs before any Start,
// and asking earlier is how a lookup comes back absent for a capability that is
// perfectly present.
func (p *Plugin) Start(ctx context.Context) error {
	if p.core == nil {
		return nil
	}
	p.users = p.core.Users
	if p.users == nil {
		log.Printf("mediainfo: no core.Users service — every report will render as " +
			"an unnamed member. The host should wire one.")
	}
	var ok bool
	if p.images, ok = pluginapi.Images(p.core); !ok {
		// Not fatal, and the difference matters: reports still work. Only
		// screenshots need intake, and the form for them is simply not offered
		// — which is better than offering a field that fails on submit.
		log.Printf("mediainfo: no %s capability — MediaInfo reports work, "+
			"screenshots are switched off. A plugin must not fetch a "+
			"member-supplied URL itself.", pluginapi.ImageIntakeName)
	}
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

// handleRemove withholds a report.
func (p *Plugin) handleRemove(c *gin.Context) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(303, "/login")
		return
	}
	if id := formID(c, "id"); id > 0 {
		_, _ = p.st.RemoveReport(c.Request.Context(), id, u.ID, u.AtLeast(core.RoleMod))
	}
	c.Redirect(303, backTo(c))
}

// handleUnshot withholds a screenshot.
func (p *Plugin) handleUnshot(c *gin.Context) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(303, "/login")
		return
	}
	if id := formID(c, "id"); id > 0 {
		_, _ = p.st.RemoveShot(c.Request.Context(), id, u.ID, u.AtLeast(core.RoleMod))
	}
	c.Redirect(303, backTo(c))
}
