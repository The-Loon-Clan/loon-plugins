package backup

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

//go:embed templates/*.html
var viewFS embed.FS

// The admin page. Until it existed the only window onto this plugin was three
// entries in /admin/jobs and their log rings — so "is the backup working?" was
// answerable only by reading job logs and knowing what to look for.
//
// The page is built around the two questions in order of how badly a wrong
// answer hurts:
//
//  1. Is anything actually TAKING it? (the acks — a pull that stopped a month
//     ago looks exactly like one that ran last night, from here.)
//  2. Is what we would hand over complete and trustworthy? (generations, the
//     per-class table, suspects, the database dump.)

func (p *Plugin) registerViews(c *core.Core) error {
	t, err := template.New("").Funcs(template.FuncMap{
		"bytes": fmtBytes2,
		"num":   fmtNum,
		"ago":   fmtAgo,
	}).ParseFS(viewFS, "templates/*.html")
	if err != nil {
		return err
	}
	p.tmpl = t
	return c.RegisterView(core.View{
		Slug: "backup", Title: "Backup", Slot: core.SlotAdminPage,
		Description: "What is inventoried, what a puller would take, and whether anything took it.",
		Nav:         core.NavHint{Group: "Operations"},
		Render: func(gc *gin.Context) (template.HTML, error) {
			return p.renderBackup(gc.Request.Context())
		},
	})
}

type classLine struct {
	Class     string
	Files     int64
	Bytes     int64
	Rotates   bool
	Empty     bool
	Delta     int64 // files, against the previous sealed generation
	PrevKnown bool
	Packs     int
	PackBytes int64
}

type dumpLine struct {
	Stamp   string
	Files   int
	Bytes   int64
	Tables  int
	Version string
	Newest  bool
}

func (p *Plugin) renderBackup(ctx context.Context) (template.HTML, error) {
	vm := map[string]any{}

	acks, err := p.st.latestAcks(ctx)
	if err != nil {
		return "", err
	}
	vm["Acks"] = acks
	vm["NoAck"] = len(acks) == 0

	gens, err := p.st.recentGenerations(ctx, 8)
	if err != nil {
		return "", err
	}
	vm["Generations"] = gens
	var latest genRow
	for _, g := range gens {
		if g.SealedAt.Valid {
			latest = g
			break
		}
	}
	vm["Latest"] = latest
	vm["HasSealed"] = latest.ID != 0
	// An ack older than the newest sealed generation is not a failure — the
	// puller runs daily and the index hourly — but the LAG is the number that
	// tells an operator how much of today is not yet off-site.
	for _, a := range acks {
		if latest.ID > 0 && a.Generation < latest.ID {
			vm["AckLag"] = latest.ID - a.Generation
			break
		}
	}

	// Per-class coverage, with the previous sealed generation as the baseline
	// so a class that lost files is visible without reading two tables.
	cur, err := p.st.classTotals(ctx, latest.ID)
	if err != nil {
		return "", err
	}
	var prev map[string]classTotal
	for _, g := range gens {
		if g.SealedAt.Valid && g.ID < latest.ID {
			prev, _ = p.st.classTotals(ctx, g.ID)
			break
		}
	}
	packsByClass, packBytes := map[string]int{}, map[string]int64{}
	if man, err := p.BuildManifest(ctx); err == nil {
		vm["PackCount"] = len(man.Packs)
		var tot int64
		for _, pk := range man.Packs {
			packsByClass[pk.Class]++
			packBytes[pk.Class] += pk.Bytes
			tot += pk.Bytes
		}
		vm["PackBytes"] = tot
		vm["PackGen"] = man.Generation
	}
	var lines []classLine
	for _, c := range orderedClasses(deps.Classes) {
		t := cur[c.Slug]
		l := classLine{
			Class: c.Slug, Files: t.Files, Bytes: t.Bytes,
			Rotates: c.Rotates, Empty: t.Files == 0,
			Packs: packsByClass[c.Slug], PackBytes: packBytes[c.Slug],
		}
		if prev != nil {
			if pt, ok := prev[c.Slug]; ok {
				l.PrevKnown, l.Delta = true, t.Files-pt.Files
			}
		}
		lines = append(lines, l)
	}
	vm["Classes"] = lines

	sus, err := p.st.suspects(ctx, 12)
	if err != nil {
		return "", err
	}
	vm["Suspects"] = sus

	vm["Dumps"] = p.dumpLines()
	vm["DumpDir"] = deps.DBDumpDir
	vm["ShrinkPct"] = maxClassShrinkPct
	vm["RehashDenom"] = rehashDenominator

	var sb strings.Builder
	if err := p.tmpl.ExecuteTemplate(&sb, "backup.html", vm); err != nil {
		return "", err
	}
	return template.HTML(sb.String()), nil
}

// dumpLines reads the published database dumps off disk rather than from a
// table: the directory IS the record (same principle as the puller's array),
// so this cannot drift from what would actually be restored.
func (p *Plugin) dumpLines() []dumpLine {
	if deps == nil || deps.DBDumpDir == "" {
		return nil
	}
	stamps := publishedDumps(deps.DBDumpDir)
	out := make([]dumpLine, 0, len(stamps))
	for i, s := range stamps {
		dir := filepath.Join(deps.DBDumpDir, s)
		files, bytes := dirTotals(dir)
		l := dumpLine{Stamp: s, Files: files, Bytes: bytes, Newest: i == 0}
		if blob, err := os.ReadFile(filepath.Join(dir, dumpManifestName)); err == nil {
			var m dumpManifest
			if json.Unmarshal(blob, &m) == nil {
				l.Tables, l.Version = len(m.Tables), m.ServerVersion
			}
		}
		out = append(out, l)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Stamp > out[j].Stamp })
	return out
}

func fmtBytes2(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for v := b / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func fmtNum(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

func fmtAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
