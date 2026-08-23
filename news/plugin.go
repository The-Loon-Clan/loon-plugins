// Package news is the site-news system — the public /news feed +
// per-post pages and the admin editor. Fully self-contained: it owns
// its models + Store and publishes the home-page "Latest news" rows
// as the news.home extension for the host to consume.
package news

import (
	"context"
	"fmt"
	"html"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func init() {
	core.RegisterPlugin("news", func() core.Plugin { return &Plugin{} })
}

// Deps are the host seams the news plugin renders through. SetDeps
// must be called before core.Boot; Provision fails loud otherwise.
type Deps struct {
	// RenderPage wraps a finished fragment in the site chrome. The four pages
	// are this plugin's markup now, so what it needs from the host is chrome
	// rather than a data map.
	RenderPage func(c *gin.Context, title string, body template.HTML)
	// HeadingInFragment says this host's chrome draws NO page heading, so
	// the fragments must render their own <h1> from the title passed to
	// render(). Hosts disagree here and both are right: a chrome that draws
	// a panel heading from the title makes a fragment h1 a duplicate (the
	// a11y audit caught exactly that), while a chrome that deliberately
	// draws nothing left every news post rendering headline-less — a date
	// and a body with no title anywhere on the page. Default false keeps
	// the chrome-draws-it behaviour.
	HeadingInFragment bool
	// Sanitize cleans admin-authored news body HTML before it is
	// rendered unescaped (the host's news sanitization policy).
	Sanitize func(html string) string
	// CSRFToken supplies the double-submit token for the admin forms — the
	// host's session concern, which is why the plugin asks rather than mints.
	//
	// REQUIRED, and its absence was not theoretical: this plugin shipped
	// without the seam, every admin form posted tokenless, and the host's CSRF
	// middleware refused the lot with 403 — create and delete were both dead,
	// and the access audit could not see it because it probes WITH a valid
	// token by design (it tests the gate, not the form).
	CSRFToken func(c *gin.Context) string
}

var deps Deps

// SetDeps installs the host seams. Call from main() before core.Boot.
func SetDeps(d Deps) { deps = d }

// HomeFeedName is the extension key the plugin publishes its
// home-page card rows under. The host Looks it up after core.Boot and
// wires it into its home handler; a host that never looks it up
// simply has no news card.
const HomeFeedName = "news.home"

// HomeFeedFunc is the extension's type: up to 4 sanitized,
// template-ready published posts (a []safePost-shaped value).
type HomeFeedFunc func(ctx context.Context) any

// Handlers serves /news* + /admin/news*.
type Handlers struct {
	store Store
	errs  core.ErrorReporter
}

type Plugin struct {
	handlers *Handlers
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "news",
		Version:     "1.0.0",
		Description: "Site news — public feed, per-post pages, admin editor.",
		Flavours:    []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	if deps.RenderPage == nil || deps.Sanitize == nil || deps.CSRFToken == nil {
		return fmt.Errorf("news: SetDeps not called (RenderPage/Sanitize/CSRFToken required) — wire it in main() before core.Boot")
	}
	db := c.Storage.DB()
	if db == nil {
		return fmt.Errorf("news: Core.Storage.DB() is nil")
	}
	p.handlers = &Handlers{store: NewPGStore(db), errs: c.Errors}

	// The stylesheet these pages used to carry inline. No-ops on a host with
	// no sink, where the fragments draw unstyled — visible rather than
	// silent, which is the right failure for a missing seam.
	pluginapi.RegisterStylesheet(c, "news", newsCSS)

	// Home page "Latest news" card — sanitized, template-ready rows.
	// Published as an extension rather than set directly on a host
	// hook: the plugin has no host import, so the host pulls the feed
	// out of the registry post-Boot (wireNewsHome in cmd/main.go).
	type safePost struct {
		ID        int64
		Title     string
		Slug      string
		Body      template.HTML
		CreatedAt interface{}
	}
	c.Register(HomeFeedName, HomeFeedFunc(func(ctx context.Context) any {
		raw, _ := p.handlers.store.GetPublishedNewsPosts(ctx, 4)
		out := make([]safePost, 0, len(raw))
		for _, post := range raw {
			out = append(out, safePost{post.ID, post.Title, post.Slug,
				template.HTML(deps.Sanitize(post.Body)), post.CreatedAt})
		}
		return out
	}))

	engine := c.Router.Engine()
	if engine == nil {
		return fmt.Errorf("news: Core.Router.Engine() is nil")
	}
	g := engine.Group("/news")
	g.Use(c.Auth.Authenticate()...)
	g.GET("", p.handlers.NewsAll)
	g.GET("/:slug", p.handlers.NewsDetail)

	adm := engine.Group("/admin/news")
	adm.Use(c.Auth.RequireUser(core.RoleMod)...)
	adm.GET("", p.handlers.NewsList)
	adm.POST("", p.handlers.CreateNews)
	adm.GET("/:id/edit", p.handlers.EditNewsPage)
	adm.POST("/:id/update", p.handlers.UpdateNews)
	adm.POST("/:id/delete", p.handlers.DeleteNews)

	// The link, registered beside the routes it points at so the two stay in
	// step. These pages are a route GROUP rather than a SlotAdminPage view —
	// the slot mounts one GET and these have several — so without this they
	// are served and in no nav at all, findable only by knowing the URL.
	if err := pluginapi.RegisterAdminNav(c, "news", func() []pluginapi.AdminNavEntry {
		return []pluginapi.AdminNavEntry{{Href: "/admin/news", Label: "News", Group: "Content", Weight: 10}}
	}); err != nil {
		return fmt.Errorf("news: register admin nav: %w", err)
	}
	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop(ctx context.Context) error  { return nil }

func (h *Handlers) NewsAll(c *gin.Context) {
	news, _ := h.store.GetPublishedNewsPosts(c.Request.Context(), 50)
	// The index carries an EXCERPT, not the post.
	//
	// It used to render every body in full, which is a list of articles rather
	// than a feed: nothing is scannable, one long post buries the four under
	// it, and a reader looking for what is new has to scroll past everything
	// they have already read. Every news index worth copying — UNIT3D's
	// article-preview among them — shows a headline, a date and a few lines.
	//
	// Plain text rather than truncated HTML. Cutting markup at a character
	// count closes no tags: the excerpt ships an open <em> and the rest of the
	// page inherits it. excerpt() strips first and cuts after, so there is
	// nothing left to leave open — which is also why Body is gone from this
	// view model entirely, rather than left in for a template to trim.
	type safePost struct {
		ID        int64
		Title     string
		Slug      string
		Excerpt   string
		Age       string
		CreatedAt interface{}
	}
	now := time.Now()
	safe := make([]safePost, 0, len(news))
	for _, p := range news {
		// Sanitize first, then strip. The detail page renders this same body
		// as HTML, so an admin's markup has to pass the host's policy either
		// way — and stripping tags off UNSANITISED input would quietly make
		// this the one path that never called it.
		safe = append(safe, safePost{p.ID, p.Title, p.Slug,
			excerpt(deps.Sanitize(p.Body), 280), humanAge(p.CreatedAt, now), p.CreatedAt})
	}
	render(c, "News", "news.html", gin.H{"News": safe})
}

// excerpt renders sanitised HTML down to a plain-text summary of at most max
// runes, cut at a word boundary, with an ellipsis when anything was dropped.
//
// max is in RUNES, not bytes: cutting a UTF-8 string by byte index splits a
// multi-byte character and the page gets a replacement glyph — the same class
// of bug as the mojibake in the release titles, arrived at from the other end.
func excerpt(htmlBody string, max int) string {
	text := stripTags(htmlBody)
	// Entities last: &amp;lt; in the source must not become < here, so unescape
	// once, after the tags are gone rather than before.
	text = html.UnescapeString(text)
	text = strings.Join(strings.Fields(text), " ")

	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	cut := string(runes[:max])
	// Back up to the last space so the excerpt does not end mid-word. If there
	// is no space at all — one very long token — the hard cut stands.
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

// humanAge renders how long ago t was, in the one unit that answers the
// question a reader is actually asking on a news index: is this new?
//
// "4 days ago" beats "Aug 11, 2026" here because it needs no arithmetic — the
// exact date matters once you have decided to read the post, and it is on the
// <time> element's title for that. This is what the reference tracker does, and
// what every news index does.
//
// now is a parameter rather than a call to time.Now inside, so the whole thing
// is testable without freezing a clock.
func humanAge(t, now time.Time) string {
	d := now.Sub(t)
	// A future timestamp is a clock skew or an admin post-dating a post, not an
	// error worth showing. It reads as brand new, which is what post-dating
	// means anyway.
	if d < time.Minute {
		return "just now"
	}
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit + " ago"
		}
		return strconv.Itoa(n) + " " + unit + "s ago"
	}
	switch {
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour")
	case d < 30*24*time.Hour:
		return plural(int(d.Hours()/24), "day")
	case d < 365*24*time.Hour:
		// Calendar months vary; this is 30-day months, deliberately. A reader
		// reading "3 months ago" is placing the post on a mental timeline, not
		// counting, and the alternative is date arithmetic that has to decide
		// what "a month before the 31st" means.
		return plural(int(d.Hours()/24/30), "month")
	default:
		return plural(int(d.Hours()/24/365), "year")
	}
}

// blockTags are the elements whose boundary is a word boundary. A <p> ending
// and another starting is a gap between two words; a </b> is not.
//
// This distinction is the whole reason the set exists. Emitting a space for
// EVERY tag reads "Real <b>body</b>. bad" as "Real body . bad", and emitting
// one for none reads "<p>one</p><p>two</p>" as "onetwo". Both were shipped
// before this — the first onto the feed, the second caught by a test.
//
// A short list is safe here because the input is already sanitised: what
// reaches this function is the host policy's subset, not arbitrary HTML. An
// element missing from the list is treated as inline, which is the failure
// worth having — a missing space between two blocks, rather than a space
// inserted into the middle of a sentence.
var blockTags = map[string]bool{
	"address": true, "article": true, "blockquote": true, "br": true,
	"dd": true, "div": true, "dl": true, "dt": true, "figcaption": true,
	"figure": true, "footer": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "header": true, "hr": true,
	"li": true, "ol": true, "p": true, "pre": true, "section": true,
	"table": true, "tbody": true, "td": true, "th": true, "thead": true,
	"tr": true, "ul": true,
}

// stripTags removes tags without a parser: the input is already sanitised, so
// what is left is a known-safe subset and the job is presentation, not defence.
func stripTags(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '<' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			// An unterminated '<' is text, not a tag. Sanitised input should
			// not contain one, and dropping the rest of the post because it
			// does is a worse answer than showing it.
			b.WriteString(s[i:])
			break
		}
		if blockTags[tagName(s[i+1:i+end])] {
			b.WriteByte(' ')
		}
		i += end + 1
	}
	return b.String()
}

// tagName pulls the lower-cased element name out of a tag's innards —
// "/p", "a href=…" and "br /" all answer with the element.
func tagName(inner string) string {
	inner = strings.TrimPrefix(strings.TrimSpace(inner), "/")
	for i := 0; i < len(inner); i++ {
		if c := inner[i]; !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9') {
			return strings.ToLower(inner[:i])
		}
	}
	return strings.ToLower(inner)
}

func (h *Handlers) NewsDetail(c *gin.Context) {
	slug := c.Param("slug")
	post, err := h.store.GetNewsPostBySlug(c.Request.Context(), slug)
	if err != nil {
		c.Redirect(http.StatusFound, "/news")
		return
	}
	type safePost struct {
		ID        int64
		Title     string
		Slug      string
		Body      template.HTML
		CreatedAt interface{}
	}
	safe := safePost{post.ID, post.Title, post.Slug, template.HTML(deps.Sanitize(post.Body)), post.CreatedAt}
	// The POST's title, not "News". It is the page's heading, the last crumb in
	// the host's breadcrumb and the browser tab's text, and all three were
	// reading "News" — on a page whose whole subject is one article. The feed
	// is still called News because that is what the feed is.
	render(c, post.Title, "news_detail.html", gin.H{"Post": safe})
}

func (h *Handlers) NewsList(c *gin.Context) {
	posts, err := h.store.GetAllNewsPosts(c.Request.Context())
	if err != nil {
		h.fail(c, "loadfailed")
		return
	}
	render(c, "News — admin", "admin_news.html", gin.H{"Posts": posts, "CSRFToken": deps.CSRFToken(c)})
}

func (h *Handlers) CreateNews(c *gin.Context) {
	title := c.PostForm("title")
	slug := c.PostForm("slug")
	body := c.PostForm("body")
	published := c.PostForm("published") == "1"
	if slug == "" {
		// auto-slug from title
		slug = slugify(title)
	}
	_, err := h.store.CreateNewsPost(c.Request.Context(), title, slug, body, published)
	if err != nil {
		log.Printf("news/create-post: %v", err)
		h.fail(c, "createfailed")
		return
	}
	c.Redirect(http.StatusFound, "/admin/news")
}

func (h *Handlers) EditNewsPage(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	post, err := h.store.GetNewsPostByID(c.Request.Context(), id)
	if err != nil {
		c.Redirect(http.StatusFound, "/admin/news")
		return
	}
	render(c, "News post", "admin_news_form.html", gin.H{"Post": post, "CSRFToken": deps.CSRFToken(c)})
}

func (h *Handlers) UpdateNews(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	title := c.PostForm("title")
	slug := c.PostForm("slug")
	body := c.PostForm("body")
	published := c.PostForm("published") == "1"
	if slug == "" {
		slug = slugify(title)
	}
	if err := h.store.UpdateNewsPost(c.Request.Context(), id, title, slug, body, published); err != nil {
		h.fail(c, "updatefailed")
		return
	}
	c.Redirect(http.StatusFound, "/admin/news")
}

func (h *Handlers) DeleteNews(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	_ = h.store.DeleteNewsPost(c.Request.Context(), id)
	c.Redirect(http.StatusFound, "/admin/news")
}

// slugify mirrors the host admin_handler helper byte-for-byte so
// new posts keep the same slug shape: every non-alphanumeric run
// becomes a single dash.
var reNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = reNonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}
