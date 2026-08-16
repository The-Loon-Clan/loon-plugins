package pointstore

import (
	"bytes"
	"errors"
	"html/template"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// flair is a purchasable cosmetic. Static catalog — no admin UI in the demo.
type flair struct {
	ID    string
	Name  string
	Color string // bootstrap contextual colour (info/warning/danger/...)
	Cost  int
}

var flairs = []flair{
	{"supporter", "Supporter", "info", 10},
	{"vip", "VIP", "warning", 25},
	{"legend", "Legend", "danger", 50},
}

func flairByID(id string) (flair, bool) {
	for _, f := range flairs {
		if f.ID == id {
			return f, true
		}
	}
	return flair{}, false
}

func (p *Plugin) renderStore(c *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok {
		return "", nil // site gate prevents this
	}
	ctx := c.Request.Context()
	bal, _ := p.core.Points.Balance(ctx, u.ID)
	current, _ := p.st.Flair(ctx, u.ID)

	type itemVM struct {
		ID, Name, Color   string
		Cost              int
		Owned, Affordable bool
	}
	items := make([]itemVM, len(flairs))
	for i, f := range flairs {
		items[i] = itemVM{f.ID, f.Name, f.Color, f.Cost, f.ID == current, bal >= f.Cost}
	}

	data := map[string]any{
		"Balance": bal,
		"Items":   items,
		"Msg":     c.Query("msg"),
		"Err":     c.Query("err"),
	}
	if cf, ok := flairByID(current); ok {
		data["Current"], data["CurrentName"], data["CurrentColor"] = true, cf.Name, cf.Color
	}
	return p.exec("store.html", data)
}

func (p *Plugin) buy(c *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok {
		c.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	f, ok := flairByID(c.PostForm("flair"))
	if !ok {
		return storeRedirect(c, "err", "Unknown item.")
	}
	ctx := c.Request.Context()
	if cur, _ := p.st.Flair(ctx, u.ID); cur == f.ID {
		return storeRedirect(c, "err", "You already have that flair.")
	}
	if _, err := p.core.Points.Deduct(ctx, u.ID, f.Cost, "spend_flair", "Bought "+f.Name+" flair", 0); err != nil {
		if errors.Is(err, core.ErrInsufficientPoints) {
			return storeRedirect(c, "err", "Not enough points.")
		}
		p.core.LoggerFor("pointstore").Error("deduct", "err", err)
		return storeRedirect(c, "err", "Purchase failed.")
	}
	if err := p.st.SetFlair(ctx, u.ID, f.ID); err != nil {
		p.core.LoggerFor("pointstore").Error("set flair", "err", err)
		// REFUND before reporting failure. Deduct has already committed, so
		// stopping here would keep the member's points and give them nothing —
		// the debit and the grant are two writes in two stores and cannot share
		// a transaction, which makes the unwind the contract (store/handlers.go
		// runs the same shape: deduct, grant, refund on failure).
		//
		// A refund that itself fails is reported loudly and NOT retried: at
		// that point the ledger is the operator's to repair, and the member's
		// error message says so rather than claiming a clean failure.
		if _, rerr := p.core.Points.Award(ctx, u.ID, f.Cost, "refund_flair",
			"Refund: equipping "+f.Name+" failed", 0); rerr != nil {
			p.core.LoggerFor("pointstore").Error("refund after failed equip",
				"user", u.ID, "amount", f.Cost, "err", rerr)
			return storeRedirect(c, "err", "Purchase failed and the refund did too — contact staff.")
		}
		return storeRedirect(c, "err", "Purchase failed; your points were refunded.")
	}
	return storeRedirect(c, "msg", "Equipped "+f.Name+"!")
}

// renderProfileFlair fills the SlotUserWidget flair card for the profile SUBJECT
// (core.ViewSubject). Renders nothing if the subject has no flair, so the card
// is simply omitted.
func (p *Plugin) renderProfileFlair(c *gin.Context) (template.HTML, error) {
	id, ok := core.ViewSubject(c)
	if !ok {
		return "", nil
	}
	fid, err := p.st.Flair(c.Request.Context(), id)
	if err != nil || fid == "" {
		return "", nil
	}
	f, ok := flairByID(fid)
	if !ok {
		return "", nil
	}
	return p.exec("flair.html", map[string]any{"Name": f.Name, "Color": f.Color})
}

func (p *Plugin) exec(name string, data any) (template.HTML, error) {
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func storeRedirect(c *gin.Context, key, val string) (template.HTML, error) {
	c.Redirect(http.StatusSeeOther, "/p/store?"+key+"="+url.QueryEscape(val))
	return "", nil
}
