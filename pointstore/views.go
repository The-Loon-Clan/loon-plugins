package pointstore

import (
	"bytes"
	"context"
	"fmt"
	"html/template"

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

// EquipFlair implements pluginapi.FlairGranter: the points store's way of
// selling what this plugin owns. GRANT-ONLY — the store has already debited
// and will refund if this errors — and equipping REPLACES the worn flair,
// which has been this plugin's rule since it had its own shop: one flair per
// member, a new one takes the slot.
func (p *Plugin) EquipFlair(ctx context.Context, userID int64, flairID string) (string, error) {
	f, ok := flairByID(flairID)
	if !ok {
		// Never a guess: an admin typo in an item's reward_ref must fail the
		// purchase (which refunds), not equip something the member did not buy.
		return "", fmt.Errorf("pointstore: unknown flair %q", flairID)
	}
	if err := p.st.SetFlair(ctx, userID, f.ID); err != nil {
		return "", err
	}
	return f.Name, nil
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
