// Package feeds discovers releases on public torrent feeds and files
// community requests for them.
//
// Extracted from the ameNZB host's torrent_feed_service (see the site repo's
// docs/FEEDS-EXTRACTION.md). The file it came from was two unrelated things
// sharing a name that made both look like tracker code: a scheduled importer
// polling Nyaa, AniRena and Tokyo Toshokan, and an on-demand nekoBT Torznab
// search that other host services call. Both live here now — the importer as
// the worker job, the search as the published search.torznab capability.
//
// It is a host-data worker: it owns no tables. feed_items, nzb_requests, the
// release-group archive and the search analytics all belong to the host,
// which is why the importer takes narrow function seams via SetJobDeps
// rather than a store of its own.
package feeds

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"

	lpapi "github.com/the-loon-clan/loon-plugins/pluginapi"
)

func init() {
	core.RegisterPlugin("feeds", func() core.Plugin { return &Plugin{} })
}

const (
	// jobName is the HISTORICAL host name, kept on purpose: the admin's
	// interval override is stored under "job_interval:<name>", so renaming
	// the job would silently discard an operator-chosen interval.
	jobName         = "Torrent Feed Import"
	defaultInterval = 30 * time.Minute
	// bootDelay matches the host service: give the site (and the calendar
	// cache) a few minutes before the first poll.
	bootDelay = 3 * time.Minute
)

// Config is plugins.feeds.* in the host's config.
type Config struct {
	// NekoBTAPIKey enables the nekoBT Torznab source in the importer AND
	// backs the published search.torznab capability. Empty is ordinary: the
	// three public RSS sources still poll, and the capability reports itself
	// unavailable. (The ameNZB host copies its legacy app.nekobt_api_key in
	// here when this is unset, so existing deployments need no config edit.)
	NekoBTAPIKey string `json:"nekobt_api_key"`

	// SourceProxies routes individual sources' fetches through an HTTP
	// proxy, keyed by source name ("nyaa", "anirena", "tokyotosho",
	// "nekobt"):
	//
	//	plugins:
	//	  feeds:
	//	    source_proxies:
	//	      anirena: "http://egress-vpn:8888"
	//
	// This exists because upstreams IP-block servers: AniRena serves the
	// ameNZB VPS an anti-bot page while answering residential IPs normally,
	// which surfaced the day /ops/feeds first ran. A proxied source's
	// traffic exits via the named proxy (the site's VPN egress container);
	// unlisted sources fetch directly. The proxied client has no SSRF dial
	// guard — the proxy address is private by nature — so proxy URLs are
	// trusted operator config, and the .torrent fallback keeps its host pin
	// as a URL-level check instead.
	SourceProxies map[string]string `json:"source_proxies"`
}

// Plugin wires the importer job and the search capability.
type Plugin struct {
	core *core.Core
	cfg  Config

	// client fetches the trusted, hardcoded feed endpoints. torrentClient
	// fetches .torrent URLs that arrive IN feed content — host-pinned and
	// SSRF-guarded, because those URLs are upstream data, not ours.
	// proxied overrides both, per source, for operator-routed sources
	// (Config.SourceProxies).
	client        *http.Client
	torrentClient *http.Client
	proxied       map[string]*http.Client

	search *torznabSearch
	status *statusBook

	job   *schedule.JobInfo
	runMu sync.Mutex
	deps  JobDeps

	// botUserID is the request author, resolved lazily on the first run and
	// cached once non-zero. Written only under runMu.
	botUserID int
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "feeds",
		Version:     "0.1.0",
		Description: "Release-feed discovery: polls public torrent feeds (Nyaa, AniRena, Tokyo Toshokan, nekoBT) and auto-creates community requests; publishes the on-demand Torznab search capability.",
		// All three processes: the search.torznab capability has consumers on
		// web (request board), worker (resurrector sweep) and api
		// (download-triggered resurrection). The importer itself gates on the
		// worker below.
		Processes: []string{"web", "worker", "api"},
		Flavours:  []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	if err := c.Config.PluginInto("feeds", &p.cfg); err != nil {
		return fmt.Errorf("feeds: config: %w", err)
	}
	p.client = c.HTTPClient.Media()
	// The AniRena fallback fetches .torrent URLs from feed content; pin them
	// to the feed's own host and keep the SSRF dial guard.
	p.torrentClient = c.HTTPClient.Whitelisted(30*time.Second, anirenaHost)
	p.proxied = make(map[string]*http.Client, len(p.cfg.SourceProxies))
	for source, proxyURL := range p.cfg.SourceProxies {
		cl, err := c.HTTPClient.Proxied(30*time.Second, proxyURL)
		if err != nil {
			// A misconfigured proxy must refuse boot rather than silently
			// fetch direct — the operator routed this source for a reason.
			return fmt.Errorf("feeds: source_proxies[%q]: %w", source, err)
		}
		p.proxied[source] = cl
	}

	// The search capability follows the nekobt source's routing, so an
	// IP-blocked nekoBT would be one config line away from working too.
	p.search = &torznabSearch{key: p.cfg.NekoBTAPIKey, client: p.sourceClient("nekobt")}
	if err := c.RegisterDef(core.ExtensionDef{
		Name:    lpapi.TorznabSearchName,
		Kind:    core.ExtService,
		Stable:  true,
		Since:   "0.1.0",
		Summary: "on-demand nekoBT Torznab search; reports unavailable when no API key is configured",
	}, lpapi.TorznabSearch(p.search)); err != nil {
		return fmt.Errorf("feeds: register %s: %w", lpapi.TorznabSearchName, err)
	}

	// The importer runs where jobs run. Everything below is worker-only.
	if c.Process != "worker" && c.Process != "all" {
		return nil
	}
	if !jobDeps.ok() {
		return fmt.Errorf("feeds: SetJobDeps was not called with a full JobDeps before core.Boot — wire it in the worker block")
	}
	p.deps = *jobDeps

	p.status = newStatusBook(p.cfg.NekoBTAPIKey != "")
	if err := c.RegisterDef(core.ExtensionDef{
		Name:    lpapi.FeedsStatusName,
		Kind:    core.ExtData,
		Stable:  true,
		Since:   "0.1.0",
		Summary: "per-source poll health + last-run outcome counts for the feed importer (GET /ops/feeds)",
	}, lpapi.FeedsStatus(p.status)); err != nil {
		return fmt.Errorf("feeds: register %s: %w", lpapi.FeedsStatusName, err)
	}

	sources := "nyaa.si, anirena, tokyotosho"
	if p.cfg.NekoBTAPIKey != "" {
		sources += ", nekoBT"
	}
	p.job = schedule.RegisterJob(jobName,
		fmt.Sprintf("Polls RSS feeds (%s) for new anime torrents and auto-creates requests", sources)).
		MarkWrites()
	p.job.IntervalMin = int(defaultInterval.Minutes())
	// Background rather than the trigger's request context — the /admin/jobs
	// POST that fired it must not cancel the run. runImport's TryLock refuses
	// overlap with the scheduled loop.
	p.job.SetTrigger(func() { go p.runImport(context.Background()) })
	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	if p.job == nil {
		return nil // web/api process: capability only, no importer
	}
	// Bare ServiceLoop: the host installs the interval-override / off-peak /
	// panic hooks globally, so the admin's job_interval override keeps
	// working exactly as it did against the host service. ctx is the root
	// context — SIGTERM now interrupts both the inter-tick sleep and (via the
	// per-item SleepCtx) a run in flight, which the host service never did.
	go schedule.ServiceLoop(ctx, p.job, bootDelay, defaultInterval, p.runImport)
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

var _ core.Plugin = (*Plugin)(nil)
