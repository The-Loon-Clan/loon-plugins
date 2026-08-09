package perks

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The member's wallet page.
//
// The view model is flattened to plain fields in Go rather than computed in the
// template, for the reason this codebase keeps rediscovering: html/template
// streams, so a missing method truncates the page mid-row and still returns
// 200 — a failure that looks like a styling bug and gets found by a member.

// Handlers serves the wallet.
type Handlers struct {
	plugin *Plugin
	auth   core.AuthService
	tmpl   *template.Template
}

func NewHandlers(p *Plugin, auth core.AuthService) *Handlers {
	return &Handlers{plugin: p, auth: auth}
}

func (h *Handlers) SetTemplates(t *template.Template) { h.tmpl = t }

type walletVM struct {
	Held         []heldVM
	Targets      []targetVM
	DurationText string
	CSRF         string
	Message      string
	Error        string
}

type heldVM struct {
	Label string
	Count int
}

type targetVM struct {
	Name        string
	AppliedText string
	Offers      []offerVM
}

type offerVM struct {
	InfoHash string
	Kind     string
	Label    string
}

// label is the member-facing name of a perk. Kept here rather than on the Kind
// constants because it is presentation — the announce path never needs it.
func label(k Kind) string {
	switch k {
	case Freeleech:
		return "Freeleech"
	case UploadDouble:
		return "2x upload"
	}
	return string(k)
}

// WalletPage shows what a member holds and where they can spend it.
func (h *Handlers) WalletPage(c *gin.Context) {
	h.renderWallet(c, "", "")
}

func (h *Handlers) renderWallet(c *gin.Context, msg, errMsg string) {
	u, ok := h.auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	ctx := c.Request.Context()

	held, err := h.plugin.st.Unspent(ctx, u.ID)
	if err != nil {
		h.fail(c, err)
		return
	}
	targets, err := h.plugin.st.SpendTargets(ctx, u.ID)
	if err != nil {
		h.fail(c, err)
		return
	}

	vm := walletVM{
		DurationText: durationText(h.plugin.tokenDuration()),
		Message:      msg,
		Error:        errMsg,
	}
	if fn := deps().CSRFToken; fn != nil {
		vm.CSRF = fn(c)
	}
	// Only kinds the member actually holds, in a fixed order — a map would
	// reshuffle the chips on every request.
	for _, k := range []Kind{Freeleech, UploadDouble} {
		if n := held[k]; n > 0 {
			vm.Held = append(vm.Held, heldVM{Label: label(k), Count: n})
		}
	}
	for _, t := range targets {
		row := targetVM{Name: t.Name}
		applied := map[Kind]bool{}
		for _, k := range t.Applied {
			applied[k] = true
			if row.AppliedText != "" {
				row.AppliedText += ", "
			}
			row.AppliedText += label(k)
		}
		// Offer only what the member HOLDS and this torrent does not already
		// have. A button guaranteed to fail reads as the site being broken
		// rather than as the perk already being there.
		for _, k := range []Kind{Freeleech, UploadDouble} {
			if held[k] > 0 && !applied[k] {
				row.Offers = append(row.Offers, offerVM{
					InfoHash: t.InfoHash, Kind: string(k), Label: "Use " + label(k),
				})
			}
		}
		vm.Targets = append(vm.Targets, row)
	}

	var buf bytes.Buffer
	if err := h.tmpl.ExecuteTemplate(&buf, "wallet.html", vm); err != nil {
		h.fail(c, err)
		return
	}
	if fn := deps().RenderPage; fn != nil {
		fn(c, "Your perks", template.HTML(buf.String()))
		return
	}
	c.String(http.StatusInternalServerError, "perks: no page renderer wired")
}

// SpendAction applies a held token to a torrent.
func (h *Handlers) SpendAction(c *gin.Context) {
	u, ok := h.auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	kind := Kind(c.PostForm("kind"))
	hash := c.PostForm("info_hash")
	if hash == "" || !Known(kind) {
		h.renderWallet(c, "", "That is not a perk this site offers.")
		return
	}

	err := h.plugin.Spend(c.Request.Context(), u.ID, kind, hash)
	switch {
	case err == nil:
		h.renderWallet(c, fmt.Sprintf("%s applied — it takes effect on your next announce.", label(kind)), "")
	case errors.Is(err, ErrNoToken):
		// Not an error page. The member asked for something reasonable and the
		// answer is simply no; telling them why is the whole response.
		h.renderWallet(c, "", "You have no "+label(kind)+" tokens left.")
	case errors.Is(err, ErrAlreadyApplied):
		h.renderWallet(c, "", label(kind)+" is already on that torrent.")
	default:
		h.fail(c, err)
	}
}

func (h *Handlers) fail(c *gin.Context, err error) {
	_ = err
	c.String(http.StatusInternalServerError, "Could not load your perks. Try again shortly.")
}

func durationText(d time.Duration) string {
	switch {
	case d <= 0:
		return "indefinitely"
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 24*time.Hour:
		return "1 day"
	default:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
}
