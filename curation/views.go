package curation

import (
	"embed"
	"fmt"
	"html/template"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed templates/curation.html
var pageFS embed.FS

var pageTmpl = template.Must(template.ParseFS(pageFS, "templates/curation.html"))

const pageSize = 50

// rowVM is one worklist row with the decision the next sweep would take —
// computed live so the page never shows stale verdicts, and so a row whose
// metadata was fixed five minutes ago immediately shows as "will fill".
type rowVM struct {
	ID        int64
	Title     string
	AnimeID   int
	AnimeName string
	Facts     string // compact facts summary for the operator's eye
	Rule      string
	Fills     string // "S2" / "S1 E5" when the sweep would write
}

// vm is a struct, not a map: a field the markup reads and the handler forgot
// is a render error instead of a silently empty cell.
type vm struct {
	Stats       Stats
	SeasonPct   int // % of anime releases with a season set
	Rows        []rowVM
	Total       int
	Page        int
	HasPrev     bool
	HasNext     bool
	PrevURL     string
	NextURL     string
	ShownFilter string
}

func (p *Plugin) render(c *gin.Context) (template.HTML, error) {
	ctx := c.Request.Context()

	stats, err := p.deps.Stats(ctx)
	if err != nil {
		return "", fmt.Errorf("stats: %w", err)
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	rows, total, err := p.deps.PageSeasonNull(ctx, pageSize, (page-1)*pageSize)
	if err != nil {
		return "", fmt.Errorf("worklist: %w", err)
	}

	// The page classifies each visible row exactly as the sweep would. One
	// facts read per distinct anime on the page, same as the sweep's cache.
	facts := make(map[int]*AnimeFacts)
	vms := make([]rowVM, 0, len(rows))
	for _, r := range rows {
		s, e := p.deps.ParseSeasonEpisode(r.Title)
		f, ok := facts[r.AnimeID]
		if !ok {
			f, _ = p.deps.AnimeFacts(ctx, r.AnimeID)
			facts[r.AnimeID] = f
		}
		d := Decide(s, e, f)
		vms = append(vms, rowVM{
			ID:        r.ID,
			Title:     r.Title,
			AnimeID:   r.AnimeID,
			AnimeName: factsName(f),
			Facts:     factsSummary(f),
			Rule:      d.Rule,
			Fills:     fillLabel(d),
		})
	}

	v := vm{
		Stats: stats,
		Rows:  vms,
		Total: total,
		Page:  page,
	}
	if stats.AnimeCompleted > 0 {
		v.SeasonPct = int(100 * (stats.AnimeCompleted - stats.SeasonNull) / stats.AnimeCompleted)
	}
	if page > 1 {
		v.HasPrev = true
		v.PrevURL = fmt.Sprintf("/admin/p/curation?page=%d", page-1)
	}
	if page*pageSize < total {
		v.HasNext = true
		v.NextURL = fmt.Sprintf("/admin/p/curation?page=%d", page+1)
	}

	var b strings.Builder
	if err := pageTmpl.ExecuteTemplate(&b, "curation.html", v); err != nil {
		return "", err
	}
	return template.HTML(b.String()), nil
}

func factsName(f *AnimeFacts) string {
	if f == nil {
		return ""
	}
	return f.Title
}

func factsSummary(f *AnimeFacts) string {
	if f == nil {
		return "no metadata row"
	}
	parts := make([]string, 0, 3)
	if f.Format != "" {
		parts = append(parts, f.Format)
	} else if f.Type != "" {
		parts = append(parts, f.Type)
	}
	if f.MappedSeason > 0 {
		parts = append(parts, fmt.Sprintf("mapped S%d", f.MappedSeason))
	}
	if f.TMDBSeasons > 0 {
		parts = append(parts, fmt.Sprintf("%d TMDB season(s)", f.TMDBSeasons))
	} else {
		parts = append(parts, "TMDB unknown")
	}
	return strings.Join(parts, ", ")
}

func fillLabel(d Decision) string {
	if d.Season == nil && d.Episode == nil {
		return ""
	}
	var b strings.Builder
	if d.Season != nil {
		fmt.Fprintf(&b, "S%d", *d.Season)
	}
	if d.Episode != nil {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "E%d", *d.Episode)
	}
	return b.String()
}
