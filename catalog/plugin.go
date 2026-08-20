// Package catalog is the content-taxonomy plugin: it owns the standard Newznab
// category tree (Console/Movies/Audio/PC/TV/XXX/Books/Other) and which
// top-level categories an admin has enabled ("list everything, pick what to
// index"). It publishes a Catalog capability that indexer plugins (usenet) read
// for Newznab caps + release categorization, and contributes a settings section
// to the host's /admin/settings page.
//
// Metadata scraping (TMDB/IMDB/… enrichment) is a separate, pluggable
// MetadataSource layer to be built on top — see SCRAPER-ARCHITECTURE.md.
package catalog

import (
	"context"
	"embed"
	"html/template"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed templates/*.html
var viewFS embed.FS

func init() {
	core.RegisterPlugin("catalog", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	core *core.Core
	st   Store
	svc  *service
	tmpl *template.Template
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "catalog",
		Version:     "0.1.0",
		Description: "Content taxonomy: the Newznab category tree + which categories the indexer surfaces.",
		Processes:   []string{"web", "worker"},
		Flavours:    []string{core.FlavourAny},
		Migrations:  migrations,
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	p.st = NewPGStore(c.Storage.SchemaDB("catalog"))
	p.svc = &service{store: p.st}

	// Publish the taxonomy capability in every process (the worker categorizes
	// on build; the web process reads enabled cats for Newznab caps).
	if err := c.Register(pluginapi.CatalogName, p.svc); err != nil {
		return err
	}

	// The "pick what to index" settings section (web/all only).
	if c.Process == "web" || c.Process == "all" {
		if err := p.registerViews(c); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }
