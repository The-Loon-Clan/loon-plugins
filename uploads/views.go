package uploads

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed templates/uploads.html
var pageFS embed.FS

var pageTmpl = template.Must(template.New("uploads").Funcs(template.FuncMap{
	"bytes": humanBytes,
}).ParseFS(pageFS, "templates/uploads.html"))

const pageSize = 50

// The three tabs, as the surface this replaced had them. Kept as a closed set
// so an unknown ?tab= falls back to the first rather than rendering an empty
// page — a stale bookmark should show something.
const (
	tabPublic         = "public"
	tabPrivateNZB     = "private-nzb"
	tabPrivateTorrent = "private-torrent"
)

// vm is a struct rather than a map: a field the markup reads and the handler
// forgets is then a render error instead of a silently empty cell, and
// html/template streams — so a missing map key shows half a page with nothing
// logged. That exact bug cost a Report button on a lifted list page.
type vm struct {
	ActiveTab string

	Public              []Upload
	PublicTotal         int
	PublicPagination    template.HTML
	PrivateNZB          []Upload
	PrivateNZBTotal     int
	PrivateNZBPager     template.HTML
	PrivateTorrent      []Upload
	PrivateTorrentTotal int
	PrivateTorrentPager template.HTML

	CSRFToken string
	Flash     string
}

func (p *Plugin) index(c *gin.Context) {
	v := p.viewer(c)
	if v == nil {
		return
	}
	// Three page parameters, one per tab, under the names the previous surface
	// used — its own links passed npage/tpage, and they are in members' history
	// and in other pages' markup.
	pages := Pages{
		Public:         atoiMin1(c.DefaultQuery("page", "1")),
		PrivateNZB:     atoiMin1(c.DefaultQuery("npage", "1")),
		PrivateTorrent: atoiMin1(c.DefaultQuery("tpage", "1")),
	}

	lists, err := deps.ListUploads(c.Request.Context(), v.ID, pages, pageSize)
	if err != nil {
		p.core.Errors.Report(c.Request.Context(), "uploads/list", err)
		// This must not read as "you have never uploaded anything".
		deps.RenderPage(c, http.StatusInternalServerError, "My Uploads",
			template.HTML(`<p class="text-danger">Your uploads could not be loaded. This has been logged.</p>`))
		return
	}

	tab := c.DefaultQuery("tab", tabPublic)
	switch tab {
	case tabPublic, tabPrivateNZB, tabPrivateTorrent:
	default:
		tab = tabPublic
	}

	// Each tab's pager keeps the OTHER tabs' page numbers in the link, so
	// paging one does not silently reset the others.
	base := func(active string, keep ...string) string {
		return "/account-settings/uploads?tab=" + active + "&" + strings.Join(keep, "&") + "&"
	}
	data := vm{
		ActiveTab:           tab,
		Public:              lists.Public,
		PublicTotal:         lists.PublicTotal,
		PrivateNZB:          lists.PrivateNZB,
		PrivateNZBTotal:     lists.PrivateNZBTotal,
		PrivateTorrent:      lists.PrivateTorrent,
		PrivateTorrentTotal: lists.PrivateTorrentTotal,
		CSRFToken:           deps.CSRFToken(c),
		Flash:               c.Query("msg"),
	}
	data.PublicPagination = deps.RenderPagination(pages.Public, pageSize, lists.PublicTotal,
		base(tabPublic, "npage="+strconv.Itoa(pages.PrivateNZB), "tpage="+strconv.Itoa(pages.PrivateTorrent)))
	data.PrivateNZBPager = deps.RenderPagination(pages.PrivateNZB, pageSize, lists.PrivateNZBTotal,
		base(tabPrivateNZB, "page="+strconv.Itoa(pages.Public), "tpage="+strconv.Itoa(pages.PrivateTorrent)))
	data.PrivateTorrentPager = deps.RenderPagination(pages.PrivateTorrent, pageSize, lists.PrivateTorrentTotal,
		base(tabPrivateTorrent, "page="+strconv.Itoa(pages.Public), "npage="+strconv.Itoa(pages.PrivateNZB)))

	body, err := p.render(data)
	if err != nil {
		p.core.Errors.Report(c.Request.Context(), "uploads/render", err)
		deps.RenderPage(c, http.StatusInternalServerError, "My Uploads", "")
		return
	}
	deps.RenderPage(c, http.StatusOK, "My Uploads", body)
}

func (p *Plugin) render(data vm) (template.HTML, error) {
	var b strings.Builder
	if err := pageTmpl.ExecuteTemplate(&b, "uploads.html", data); err != nil {
		return "", err
	}
	return template.HTML(b.String()), nil
}

func atoiMin1(s string) int {
	n, _ := strconv.Atoi(s)
	if n < 1 {
		return 1
	}
	return n
}

// bulkAction applies one action to one upload, or to everything the member
// owns. Both live on one route because the form is one form — the difference is
// whether an id came with it.
func (p *Plugin) bulkAction(c *gin.Context) {
	v := p.viewer(c)
	if v == nil {
		return
	}
	ctx := c.Request.Context()
	action := c.PostForm("action")
	idRaw := c.PostForm("id")

	var err error
	var msg string
	if idRaw == "" {
		// The sweeping forms. Each reports how many rows it touched, because
		// "done" over a no-op is indistinguishable from a silent failure.
		var n int
		switch action {
		case "delete":
			n, err = deps.Actions.SoftDeleteAll(ctx, v.ID)
		case "restore":
			n, err = deps.Actions.RestoreAll(ctx, v.ID)
		case "anonymous":
			n, err = deps.Actions.SetAllAnonymous(ctx, v.ID, true)
		case "deanonymous":
			n, err = deps.Actions.SetAllAnonymous(ctx, v.ID, false)
		case "true-anonymous":
			// Irreversible: it severs the owner link, so these rows leave this
			// page for good. Gated on an explicit confirm field rather than the
			// action alone, so a mis-click or a replayed form cannot do it.
			if c.PostForm("confirm") != "permanent" {
				p.redirect(c, "that action needs confirmation")
				return
			}
			n, err = deps.Actions.SetAllTrueAnonymous(ctx, v.ID)
		default:
			p.redirect(c, "unknown action")
			return
		}
		msg = fmt.Sprintf("%d upload(s) updated", n)
	} else {
		id, perr := strconv.ParseInt(idRaw, 10, 64)
		if perr != nil || id <= 0 {
			p.redirect(c, "bad id")
			return
		}
		switch action {
		case "delete":
			err = deps.Actions.SoftDelete(ctx, v.ID, id)
		case "restore":
			err = deps.Actions.Restore(ctx, v.ID, id)
		case "anonymous":
			err = deps.Actions.SetAnonymous(ctx, v.ID, id, true)
		case "deanonymous":
			err = deps.Actions.SetAnonymous(ctx, v.ID, id, false)
		case "true-anonymous":
			if c.PostForm("confirm") != "permanent" {
				p.redirect(c, "that action needs confirmation")
				return
			}
			err = deps.Actions.SetTrueAnonymous(ctx, v.ID, id)
		default:
			p.redirect(c, "unknown action")
			return
		}
		msg = "upload updated"
	}
	if err != nil {
		// Owner-scoped writes decide what a member can see and manage, so a
		// failure is never silenced — the page must not imply it worked.
		p.core.Errors.Report(ctx, "uploads/"+action, err)
		p.redirect(c, "that did not save. It has been logged.")
		return
	}
	p.redirect(c, msg)
}

func (p *Plugin) torrentVisibility(c *gin.Context) {
	v := p.viewer(c)
	if v == nil {
		return
	}
	id, err := strconv.ParseInt(c.PostForm("request_id"), 10, 64)
	if err != nil || id <= 0 {
		p.redirect(c, "bad id")
		return
	}
	keep := c.PostForm("keep_private") == "true" || c.PostForm("keep_private") == "1"
	if err := deps.Actions.KeepPrivate(c.Request.Context(), v.ID, id, keep); err != nil {
		p.core.Errors.Report(c.Request.Context(), "uploads/keep-private", err)
		p.redirect(c, "that did not save. It has been logged.")
		return
	}
	p.redirect(c, "visibility updated")
}

func (p *Plugin) redirect(c *gin.Context, msg string) {
	c.Redirect(http.StatusSeeOther, "/account-settings/uploads?msg="+urlQueryEscape(msg))
}

// urlQueryEscape is spelled out rather than imported so the message cannot
// carry markup into the flash on the next render.
func urlQueryEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ' ':
			b.WriteByte('+')
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, "%%%02X", r)
		}
	}
	return b.String()
}

// humanBytes renders a size the way the rest of the site does.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n)
	for _, u := range units {
		v /= unit
		if v < unit {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f PB", v/unit)
}
