package tracker

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

//go:embed templates/*.html
var viewFS embed.FS

// The admin oversight page, as a SlotAdminPage view at /admin/p/tracker.
//
// The host had four separate routes for this — /admin/tracker, /tracker/torrents,
// /tracker/users, /tracker/users/:id — and the survey that started this extraction
// flagged three of them as unreachable from the admin hub, linked only from each
// other. One page with tabs replaces them, and it appears in the hub
// automatically because the hub ranges over registered plugin views.

type adminVM struct {
	Now      time.Time
	Msg      string
	Err      string
	Idle     bool
	Members  []memberVM
	Torrents []*Torrent
	Total    int
}

// memberVM is one oversight row: the plugin's arithmetic, plus the name and
// entitlement the plugin had to ask elsewhere for.
//
// The host got Username and TrackerAccess from a join on `users`. This plugin
// cannot join another schema, and the split turns out to be the honest shape —
// the name comes from core.Users and access is an entitlement, so neither was ever
// really the tracker's data.
type memberVM struct {
	*Aggregate
	Username string
	Entitled bool
	Ratio    float64
}

func (p *Plugin) registerViews(c *core.Core) error {
	if err := p.parseTemplates(); err != nil {
		return err
	}
	return c.RegisterView(core.View{
		Slug: "tracker", Title: "Tracker", Slot: core.SlotAdminPage,
		Description: "Swarm oversight: torrents, and every member's ratio accounting.",
		MinRole:     core.RoleMod,
		Nav:         core.NavHint{Group: "Operations"},
		Render: func(gc *gin.Context) (template.HTML, error) {
			return p.renderAdmin(gc.Request.Context(), gc.Query("sort"), gc.Query("msg"), gc.Query("err"))
		},
	})
}

func (p *Plugin) parseTemplates() error {
	t, err := template.New("").Funcs(template.FuncMap{
		"ts": func(t *time.Time) string {
			if t == nil || t.IsZero() {
				return "—"
			}
			return t.UTC().Format("2006-01-02 15:04")
		},
		// Bytes as a human figure. A tracker page is entirely about byte counts,
		// and raw integers in the billions are unreadable at a glance.
		"bytes": humanBytes,
		// ts takes a *time.Time (the nullable last_seen); ts_at takes a value.
		// Two helpers rather than one that accepts any, because a template that
		// passes the wrong shape should fail at parse rather than print an
		// address.
		"ts_at": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.UTC().Format("2006-01-02 15:04")
		},
		"ratio": func(r float64) string {
			if r == 0 {
				return "—"
			}
			return fmt.Sprintf("%.2f", r)
		},
	}).ParseFS(viewFS, "templates/*.html")
	if err != nil {
		return err
	}
	p.tmpl = t
	return nil
}

func (p *Plugin) renderAdmin(ctx context.Context, sort, msg, errMsg string) (template.HTML, error) {
	vm := adminVM{Now: time.Now(), Msg: msg, Err: errMsg}

	// The idle case has to be SAID. Provision returns early with no routes when
	// there is no Redis, so without this the page would render an empty table and
	// look like a tracker nobody uses rather than one that cannot run.
	if p.store == nil {
		vm.Idle = true
		return p.exec(vm)
	}

	rows, total, err := p.store.ListTorrents(ctx, 100, 0)
	if err != nil {
		return "", err
	}
	vm.Torrents, vm.Total = rows, total

	aggs, _, err := p.store.ListAggregates(ctx, sort, 100, 0)
	if err != nil {
		return "", err
	}
	for _, a := range aggs {
		row := memberVM{Aggregate: a, Username: fmt.Sprintf("#%d", a.UserID)}
		// Name and entitlement resolved per row, which is an N+1 this page can
		// afford: it is a mod-only oversight table capped at 100, and core.Users
		// caches DisplayName. The alternative — a batch lookup seam — would be
		// machinery for one admin page.
		if p.core.Users != nil {
			// The error is ignored deliberately rather than by accident: a name
			// this page cannot resolve falls back to "#<id>", which is still a
			// usable oversight row. Failing the whole table because one member
			// lookup hiccuped would be the wrong trade.
			if name, err := p.core.Users.DisplayName(ctx, a.UserID); err == nil && name != "" {
				row.Username = name
			}
		}
		row.Entitled = p.h.gate.Entitled(ctx, a.UserID)
		row.Ratio = Totals{Uploaded: a.Uploaded, Downloaded: a.Downloaded}.Ratio()
		vm.Members = append(vm.Members, row)
	}
	return p.exec(vm)
}

func (p *Plugin) exec(vm adminVM) (template.HTML, error) {
	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "tracker_admin.html", vm); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// humanBytes renders a byte count at the largest unit that keeps it under 1024.
func humanBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", n, units[i])
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
