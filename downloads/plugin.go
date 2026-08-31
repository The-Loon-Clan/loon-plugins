// Package downloads closes the loop the other way: the member's download
// client telling the SITE what happened.
//
// An indexer publishes an NZB and never learns whether it worked. The health
// sweep STATs articles on a schedule, which catches expiry eventually and
// catches nothing that fails for a reason the articles do not show — a bad
// PAR2 set, a truncated post, an unpack that dies on a corrupt volume. The
// member's downloader knows within minutes and has had nowhere to say so.
// That is the gap this fills, and it is the one members actually ask for:
//
//	"Does anyone of you have a call back script for SABnzbd with the indexer?
//	 if there is a prebuilt one, it will be very helpful."
//
// So the plugin ships the script as well as the endpoint. A member opens one
// page, downloads a file with this site's URL and their own key already in it,
// drops it in their scripts folder and selects it in their client. Anything
// harder than that is a feature nobody turns on.
//
// A REPORT IS A SIGNAL, NOT A VERDICT. Nothing here marks a release broken. A
// failure flags the row for the health sweep through
// pluginapi.ReleaseRecheckRequester and the sweep decides from the articles
// themselves — because "it failed for me" has a dozen causes on the member's
// side (thin retention, a full disk, the wrong file) and only one of them is
// the release being bad. Writing a verdict from a report would let one broken
// seedbox condemn a healthy release.
package downloads

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed templates/*.html
var tmplFS embed.FS

//go:embed scripts/report.py
var scriptFS embed.FS

func init() {
	core.RegisterPlugin("downloads", func() core.Plugin { return &Plugin{} })
}

// ReportPath is where a download client posts an outcome.
//
// Under /api because that is where this site's machine surface lives, and
// because a host's page policy already treats /api as key-authenticated rather
// than session-authenticated — a script carries a key, not a cookie.
const ReportPath = "/api/downloads/report"

type Plugin struct {
	core *core.Core
	st   Store
	tmpl *template.Template

	// keys identifies the member behind a machine request. REQUIRED: without
	// it the endpoint cannot tell one member from anybody on the internet, so
	// it refuses rather than accepting.
	keys pluginapi.APIKeyResolver
	// issuer is the same registry entry when the host's key store can also
	// hand a member's key back. Optional: without it the setup page cannot
	// pre-fill the script and says so, rather than offering a download that
	// arrives unconfigured.
	issuer pluginapi.APIKeyIssuer
	// recheck asks the health sweep to look at a release again. Optional — a
	// host without a sweep still gets reports recorded, which is the useful
	// half on its own.
	recheck pluginapi.ReleaseRecheckRequester
	// grabs resolves the reports that arrive naming a job rather than an id.
	// Optional; see resolve.go for why it is worth having.
	grabs pluginapi.DownloadGrabLookup
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "downloads",
		Version:     "0.1.0",
		Description: "Download-client callback: a member's SABnzbd or NZBGet reports each job's outcome back to the index.",
		Migrations:  migrations,
		Processes:   []string{"web"},
		Flavours:    []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	// The admin page's CSS. See stylesheet.go.
	pluginapi.RegisterStylesheet(c, "downloads", downloadsCSS)
	p.core = c
	db := c.Storage.SchemaDB(p.Metadata().Name)
	if db == nil {
		return fmt.Errorf("downloads: Core.Storage.SchemaDB is nil")
	}
	p.st = NewPGStore(db)

	tmpl, err := template.New("downloads").ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("downloads: parsing templates: %w", err)
	}
	p.tmpl = tmpl

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("downloads: Core.Router.Engine() is nil")
	}
	// Mounted HERE rather than in Start, because routes belong to Provision by
	// the Plugin contract. The capability it needs is resolved in Start, and
	// until it is the handler refuses — see handleReport. An endpoint that
	// answers "not configured" is honest; one that appears later depending on
	// boot order is not.
	engine.POST(ReportPath, p.handleReport)
	// The generated script. A GET because it is a file download, and behind
	// the host's user gate because it carries the member's own key. One
	// registration with the gate chain and the handler appended — gin panics
	// on a duplicate method+path, so the middleware cannot be a second call.
	engine.GET(scriptRoute, append(c.Auth.RequireUser(core.RoleUser), p.serveScript)...)

	return p.registerViews(c)
}

// Start resolves the sibling capabilities.
//
// In Start, not Provision: every Provision runs before any Start, and the host
// registers its own capabilities during that window — looking them up earlier
// finds nothing on a host that is wired correctly.
func (p *Plugin) Start(ctx context.Context) error {
	if p.core == nil {
		return nil
	}
	if v, ok := p.core.Lookup(pluginapi.APIKeyResolverName); ok {
		p.keys, _ = v.(pluginapi.APIKeyResolver)
		// The richer form, if the host's key store offers it. Same entry, one
		// type assertion — see the seam's own note on why it is not a second
		// registration.
		p.issuer, _ = v.(pluginapi.APIKeyIssuer)
	}
	if r, ok := pluginapi.LookupReleaseRecheckRequester(p.core); ok {
		p.recheck = r
	}
	if g, ok := pluginapi.LookupDownloadGrabLookup(p.core); ok {
		p.grabs = g
	}

	// Both of these are LOUD, for the same reason the tracker's Redis notice
	// is: a capability that quietly is not there is indistinguishable from a
	// bug, and the member-visible symptom of both is "my reports do nothing".
	if p.keys == nil {
		log.Printf("downloads: no %s capability — %s will refuse every report. "+
			"The host must register an API-key resolver before a download client can report anything.",
			pluginapi.APIKeyResolverName, ReportPath)
	}
	if p.recheck == nil {
		log.Printf("downloads: no %s capability — failures will be recorded but will not "+
			"request a health re-check.", pluginapi.ReleaseRecheckName)
	}
	if p.grabs == nil {
		log.Printf("downloads: no %s capability — only reports carrying a release id or an "+
			"NZB URL can be matched; ones naming just a job will be answered as unmatched.",
			pluginapi.DownloadGrabLookupName)
	}
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }
