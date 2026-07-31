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

// ackLine is one backup target's claim, turned into the sentence an operator
// actually wants: "complete until X", and how far the world has moved since.
//
// The percentage is content bytes of the acked generation over content bytes
// of the newest sealed one — both single indexed row reads, because this is a
// render path and a render path must never scan. It is deliberately a COVERAGE
// figure ("how much of what exists is in the last complete copy") rather than a
// transfer figure ("how much is left to move"): the two differ whenever pack
// identities churn, and the one that matters for a backup is coverage.
type ackLine struct {
	ackRow
	CompleteUntil string
	Pct           int
	HeldBytes     int64
	TotalBytes    int64
	GensBehind    int64
	FilesBehind   int64
	BytesBehind   int64
	Current       bool
	Known         bool // the acked generation's row still exists
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
	// index runs daily and the pull follows it — but how far behind it is, and
	// what "complete until" actually means in wall-clock terms, is the whole
	// question. Two O(1) row reads per target answers it.
	ackLines := make([]ackLine, 0, len(acks))
	for _, a := range acks {
		meta, err := p.st.generationMeta(ctx, a.Generation)
		ackLines = append(ackLines, ackCoverage(a, latest, meta, err == nil && meta.SealedAt != ""))
	}
	vm["Acks"] = ackLines

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

func fmtBytes2(v any) string {
	var b int64
	switch t := v.(type) {
	case int:
		b = int64(t)
	case int32:
		b = int64(t)
	case int64:
		b = t
	default:
		return fmt.Sprintf("%v", v)
	}
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

// fmtNum and fmtBytes2 take any, not int64.
//
// html/template fails the ENTIRE render on an argument type mismatch, so a
// single `{{num .Packs}}` against an int field — where every neighbouring
// field happened to be an int64 — turned the whole page into "this page failed
// to render", with the real cause only in a log. Accepting both is worth more
// than the type precision, because the cost of being wrong is the page.
func fmtNum(v any) string {
	var n int64
	switch t := v.(type) {
	case int:
		n = int64(t)
	case int32:
		n = int64(t)
	case int64:
		n = t
	default:
		return fmt.Sprintf("%v", v)
	}
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

// ackCoverage turns one target's claim into the bar.
//
// Every clamp here is load-bearing, because this widget's job is to be
// believed: a bar reading 103% is embarrassing, and one reading 100% while a
// third of the corpus is uncollected is dangerous. So a target holding a
// generation newer than the newest sealed one (it acked mid-index) reads 100
// rather than over, "behind" figures never go negative when the inventory
// SHRANK between the two generations, and a generation whose row has aged out
// of the index reports Known=false instead of a confidently wrong 0%.
func ackCoverage(a ackRow, latest genRow, meta genMeta, metaOK bool) ackLine {
	l := ackLine{ackRow: a, TotalBytes: latest.Bytes, HeldBytes: a.Bytes}
	if metaOK {
		l.Known = true
		l.CompleteUntil, l.HeldBytes = meta.SealedAt, meta.Bytes
		l.FilesBehind = maxInt64(latest.Files-meta.Files, 0)
		l.BytesBehind = maxInt64(latest.Bytes-meta.Bytes, 0)
	}
	l.GensBehind = maxInt64(latest.ID-a.Generation, 0)
	l.Current = latest.ID > 0 && a.Generation >= latest.ID
	switch {
	case l.TotalBytes > 0:
		l.Pct = int(min64(100*l.HeldBytes/l.TotalBytes, 100))
	case l.Current:
		// Nothing indexed yet but the target holds what exists: vacuously
		// complete, and better shown as such than as a 0% that reads as loss.
		l.Pct = 100
	}
	return l
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
