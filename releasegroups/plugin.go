package releasegroups

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"
)

func init() {
	core.RegisterPlugin("release_groups", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	handlers *Handlers
	process  string
	scraper  *scraperService
	archive  *archiveService
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "release_groups",
		Version:     "1.0.0",
		Description: "Release-group directory — group pages, ownership claims, news + followers, archive mirror, plus the nekoBT scraper + archive-sweep jobs.",
		// web: the directory pages. worker: the two scraper/sweep loops.
		Processes: []string{"web", "worker"},
		Flavours:  []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.process = c.Process

	// Worker leg: the weekly nekoBT scraper + the daily archive sweep
	// (started in Start). Historical job names, so /admin/jobs history and
	// interval overrides carry over.
	if p.process == "worker" || p.process == "all" {
		if !jobDeps.ok() {
			return fmt.Errorf("release_groups: SetJobDeps was not called with a full JobDeps (Groups/FetchLogo/Slugify/ScrapeArchive) before core.Boot — wire it in the worker block")
		}
		p.scraper = newScraperService(*jobDeps, c.Errors)
		p.archive = newArchiveService(*jobDeps, c.Errors)
	}
	if p.process == "web" {
		// Split web process: the jobs run on the worker, but /admin/jobs is
		// served here — without a local registration the page cannot show
		// them at all, and an operator would conclude they had vanished.
		// Remote stubs need no deps: they are display rows whose triggers
		// forward over the Redis command channel.
		schedule.RegisterJob(scraperJobName, "Runs on the worker — see its /admin/jobs.").MarkRemote()
		schedule.RegisterJob(archiveJobName, "Runs on the worker — see its /admin/jobs.").MarkRemote()
	}
	if p.process == "worker" {
		return nil // headless worker: no routes
	}

	if !deps.ok() {
		return fmt.Errorf("release_groups: SetDeps was not called with a full Deps before core.Boot — wire it in the host's composition root (store + chrome + claim/scrape seams)")
	}
	if err := parseTemplates(); err != nil {
		return err
	}
	p.handlers = &Handlers{deps: *deps, errs: c.Errors}

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("release_groups: Core.Router.Engine() is nil")
	}
	g := engine.Group("/")
	g.Use(c.Auth.Authenticate()...)
	g.GET("/release-groups", p.handlers.ReleaseGroupsList)
	g.GET("/suggest-release-group", p.handlers.SuggestReleaseGroupForm)
	g.POST("/suggest-release-group", p.handlers.SubmitReleaseGroupSuggestion)
	g.GET("/release-groups/:slug", p.handlers.ReleaseGroupDetail)
	g.GET("/release-groups/:slug/suggest", p.handlers.SuggestReleaseGroupForm)
	g.POST("/release-groups/:slug/suggest", p.handlers.SubmitReleaseGroupSuggestion)
	g.POST("/release-groups/:slug/claim", p.handlers.ClaimReleaseGroup)
	g.POST("/release-groups/:slug/claim/verify", p.handlers.VerifyReleaseGroupClaim)
	g.GET("/release-groups/:slug/bio", p.handlers.EditReleaseGroupBio)
	g.POST("/release-groups/:slug/bio", p.handlers.SaveReleaseGroupBio)
	g.GET("/release-groups/:slug/archive", p.handlers.ReleaseGroupArchive)
	g.POST("/release-groups/:slug/archive/refresh", p.handlers.RefreshReleaseGroupArchive)
	g.POST("/release-groups/:slug/archive/request-missing", p.handlers.BulkRequestMissingFromArchive)
	g.POST("/release-groups/:slug/archive/torrents/:torrentID/hide", p.handlers.HideArchiveTorrent)
	g.POST("/release-groups/:slug/follow", p.handlers.ToggleReleaseGroupFollow)
	g.POST("/release-groups/:slug/news", p.handlers.PostReleaseGroupNews)
	g.POST("/release-groups/:slug/news/:id/delete", p.handlers.DeleteReleaseGroupNewsPost)

	// Public verification-token redirect (/v/<token> from claim
	// snippets on third-party pages) — deliberately outside the
	// Authenticate chain so anonymous visitors resolve too.
	engine.GET("/v/:token", p.handlers.VerifyTokenRedirect)

	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	// Post-response background work (news fan-out, on-demand scrape) binds
	// to the root context so a request cancellation doesn't kill it, and
	// SIGTERM does.
	if p.handlers != nil {
		p.handlers.bg = func() context.Context { return ctx }
	}
	// Worker legs only; nil on a web process. Bare ServiceLoop: the host's
	// globally-installed hooks provide the admin interval override and the
	// off-peak gate — and ctx is the root context, so both loops now die at
	// SIGTERM (the scraper's old hand-rolled loop never did).
	if p.scraper != nil {
		go schedule.ServiceLoop(ctx, p.scraper.job, scraperBootDelay, scraperDefaultInterval, p.scraper.run)
	}
	if p.archive != nil {
		go schedule.ServiceLoop(ctx, p.archive.job, archiveBootDelay, archiveDefaultInterval, p.archive.run)
	}
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

var _ core.Plugin = (*Plugin)(nil)
