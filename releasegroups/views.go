package releasegroups

import (
	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"embed"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// The five pages, embedded so a missing template is a build error here
// rather than a runtime 500 in a host.
//
//go:embed templates/*.html
var pageFS embed.FS

// pageTmpl is parsed in Provision, not init, because the FuncMap binds
// deps.Markdown and deps.RelativeTime — host seams.
var pageTmpl *template.Template

func parseTemplates() error {
	t, err := template.New("releasegroups").Funcs(template.FuncMap{
		// The site's username chip, drawn by the HOST: role colour, any
		// equipped name effect, the profile link. Asking rather than drawing
		// our own anchor is what keeps a member's cosmetics from stopping at
		// this plugin's pages.
		"userTag": func(name string) template.HTML {
			return pluginapi.RenderUserTag(fxCore, name)
		},
		// Host seams: the sanitising renderer and the site's time wording.
		"markdown":     func(s string) template.HTML { return deps.Markdown(s) },
		"relativeTime": func(v any) string { return deps.RelativeTime(v) },
		// Exact copies of the host FuncMap entries the pages rendered with —
		// parity is the lift's contract (the requests lift's review counted
		// the pixels on "improved" copies; not repeating that).
		"initial":     initialRune,
		"urlPlatform": urlPlatform,
		"formatBytes": formatBytes,
		"derefStr": func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		},
		"deref64": func(p *int64) int64 {
			if p == nil {
				return 0
			}
			return *p
		},
		"int64": func(v uint64) int64 { return int64(v) },
		"inc":   func(n int) int { return n + 1 },
		"sub":   func(a, b int64) int64 { return a - b },
	}).ParseFS(pageFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("release_groups: parse templates: %w", err)
	}
	pageTmpl = t
	return nil
}

// render draws one page: fragment from the plugin's own set, chrome from the
// host. The viewer keys the fragments read (CSRFToken, User, IsAdmin,
// BaseURL, ActiveNav) are injected here, once.
// fail renders a refusal in the site's chrome instead of a bare string.
//
// Eighteen handlers used to answer c.String(404, "release group not found")
// and the like — fourteen of them that same sentence — as plain text on a
// blank page, with the words living in Go where the translation seam could
// never reach them. The reason travels as a code and group_error.html holds
// them.
func (h *Handlers) fail(c *gin.Context, status int, reason string) {
	h.render(c, status, "Release groups", "group_error.html", gin.H{"Reason": reason})
}

func (h *Handlers) render(c *gin.Context, status int, title, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	data["CSRFToken"] = h.deps.CSRFToken(c)
	data["BaseURL"] = h.deps.BaseURL
	data["SiteName"] = h.deps.siteName()
	data["ActiveNav"] = "groups"
	data["NzbCardCSS"] = h.deps.NzbCardCSS()
	if v := h.deps.Viewer(c); v != nil {
		data["User"] = v
		data["IsAdmin"] = v.Mod
	} else {
		data["User"] = (*Viewer)(nil)
		data["IsAdmin"] = false
	}

	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, data); err != nil {
		// Never stream a partial page.
		c.String(500, "this page failed to render")
		return
	}
	h.deps.RenderPage(c, status, title, template.HTML(sb.String()))
}

// pageOffset converts a 1-based page to a query offset, clamping hostile
// input.
func pageOffset(page, pageSize int) int {
	if page < 1 {
		page = 1
	}
	return (page - 1) * pageSize
}

// jsonOK — the site-wide JSON envelope, copied on purpose (six lines; the
// same JavaScript reads it byte for byte).
func jsonOK(c *gin.Context, extras gin.H) {
	out := gin.H{"ok": true}
	for k, v := range extras {
		out[k] = v
	}
	c.JSON(200, out)
}

// initialRune is the no-avatar circle's letter — the first RUNE, never
// `slice .Name 0 1`, which counts bytes and puts half a rune into the page
// (or aborts the streamed render on an empty name).
func initialRune(name string) string {
	if name == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(name)
	if r == utf8.RuneError && size <= 1 {
		return ""
	}
	return name[:size]
}

// urlPlatform maps a social/site URL to its display name — exact copy of
// the host FuncMap entry.
func urlPlatform(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host := strings.ToLower(strings.TrimPrefix(u.Host, "www."))
	switch host {
	case "nekobt.to":
		return "nekoBT"
	case "nyaa.si":
		return "Nyaa"
	case "anidb.net":
		return "AniDB"
	case "x.com", "twitter.com":
		return "Twitter"
	case "bsky.app":
		return "Bluesky"
	case "anilist.co":
		return "AniList"
	case "myanimelist.net":
		return "MyAnimeList"
	case "discord.com", "discord.gg":
		return "Discord"
	case "t.me", "telegram.me":
		return "Telegram"
	case "github.com":
		return "GitHub"
	case "youtube.com", "youtu.be":
		return "YouTube"
	case "reddit.com", "old.reddit.com":
		return "Reddit"
	}
	return host
}

// formatBytes — exact copy of the host FuncMap entry.
func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(b)/float64(1<<10))
	case b > 0:
		return fmt.Sprintf("%d B", b)
	default:
		return "—"
	}
}

var _ embed.FS // keep the import when templates are stripped in tooling
