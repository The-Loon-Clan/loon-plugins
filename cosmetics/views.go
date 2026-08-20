package cosmetics

import (
	"bytes"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// effectVM is one entry in a slot's list.
type effectVM struct {
	Slug        string
	Label       string
	Description string
	Class       string
	Tinted      bool
	Animated    bool

	// Owned and Worn drive the controls. Separate because the page shows the
	// WHOLE catalogue for a slot, not just what you have: a page listing only
	// your purchases sells nothing, and nobody can choose between eight effects
	// from eight names.
	Owned   bool
	Worn    bool
	Expires *time.Time
}

// slotVM is one section of the page.
type slotVM struct {
	Slot    string
	Label   string
	Effects []effectVM
	// Wearing is whether anything is on, which is the only thing the
	// "wear nothing" control needs to know.
	Wearing bool
}

// titleVM is the member's own title, in whatever state it is in.
type titleVM struct {
	// Allowed is whether they have bought the right. Everything else on this
	// struct is only meaningful when it is true.
	Allowed  bool
	Text     string
	State    string
	Reason   string
	Reviewed *time.Time
	Max      int
}

func (p *Plugin) page(c *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	ctx := c.Request.Context()

	owned, err := p.st.OwnedBy(ctx, u.ID)
	if err != nil {
		return "", err
	}
	have := make(map[string]Owned, len(owned))
	for _, o := range owned {
		have[o.Slug] = o
	}

	slots := make([]slotVM, 0, len(pluginapi.Slots))
	for _, slot := range pluginapi.Slots {
		worn, err := p.st.EquippedBy(ctx, u.ID, slot)
		if err != nil {
			return "", err
		}
		vm := slotVM{Slot: slot, Label: pluginapi.SlotLabel(slot), Wearing: worn != ""}
		for _, e := range pluginapi.EffectsFor(slot) {
			o, mine := have[e.Slug]
			vm.Effects = append(vm.Effects, effectVM{
				Slug: e.Slug, Label: e.Label, Description: e.Description,
				Class: pluginapi.EffectClass(e.Slug),
				Tinted: e.Tinted, Animated: e.Animated,
				Owned: mine, Worn: e.Slug == worn, Expires: o.ExpiresAt,
			})
		}
		slots = append(slots, vm)
	}

	title := titleVM{Max: titleMax}
	title.Allowed, err = p.st.Owns(ctx, u.ID, titleUnlock)
	if err != nil {
		return "", err
	}
	if t, found, err := p.st.TitleOf(ctx, u.ID); err == nil && found {
		title.Text, title.State, title.Reason, title.Reviewed = t.Text, t.State, t.Reason, t.ReviewedAt
	}

	return p.render("cosmetics_page.html", map[string]any{
		"CSRF": pluginapi.CSRFToken(p.core, c),
		// The viewer's OWN name in every preview, because a swatch of the word
		// "Sparkle" tells you nothing about what your name will look like — and
		// the length of a name is most of how an effect reads.
		"Name":  u.Username,
		"Slots": slots,
		"Title": title,
		"Err":   c.Query("err"),
		"Sent":  c.Query("sent") == "1",
	})
}

// equip puts one on or takes it off.
func (p *Plugin) equip(c *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	fail := func(key string) (template.HTML, error) {
		c.Redirect(http.StatusSeeOther, pagePath+"?err="+key)
		return "", nil
	}
	slot := strings.TrimSpace(c.PostForm("slot"))
	slug := strings.TrimSpace(c.PostForm("slug"))
	if !knownSlot(slot) {
		return fail("unknown")
	}
	// Empty is "take it off" and is always allowed. Anything else is checked
	// against the catalogue AND against the slot here, and against OWNERSHIP in
	// the statement — so a forged post can neither store an effect that does
	// not exist nor put an avatar frame on somebody's username.
	if slug != "" {
		e, ok := pluginapi.EffectBySlug(slug)
		if !ok || !e.FitsSlot(slot) {
			return fail("unknown")
		}
	}
	done, err := p.st.Equip(c.Request.Context(), u.ID, slot, slug)
	switch {
	case err != nil:
		return fail("failed")
	case !done && slug != "":
		// The statement wrote nothing, which means they do not own it (or it
		// lapsed between the page rendering and the click). Said plainly: this
		// is the one refusal a member can act on.
		return fail("notowned")
	}
	c.Redirect(http.StatusSeeOther, pagePath)
	return "", nil
}

// knownSlot refuses anything the contract does not name.
func knownSlot(slot string) bool {
	for _, s := range pluginapi.Slots {
		if s == slot {
			return true
		}
	}
	return false
}

// submitTitle takes a member's words and puts them in the queue.
func (p *Plugin) submitTitle(c *gin.Context) (template.HTML, error) {
	u, ok := p.core.Auth.CurrentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusSeeOther, "/login")
		return "", nil
	}
	fail := func(key string) (template.HTML, error) {
		c.Redirect(http.StatusSeeOther, pagePath+"?err="+key)
		return "", nil
	}
	ctx := c.Request.Context()
	// Checked HERE and not only by hiding the form: the right can lapse while
	// somebody has the page open, and a form in a browser outlives the page it
	// came from.
	allowed, err := p.st.Owns(ctx, u.ID, titleUnlock)
	if err != nil {
		return fail("failed")
	}
	if !allowed {
		return fail("notitle")
	}
	text, ok := cleanTitle(c.PostForm("text"))
	if !ok {
		return fail("badtitle")
	}
	if err := p.st.SubmitTitle(ctx, u.ID, text); err != nil {
		return fail("failed")
	}
	c.Redirect(http.StatusSeeOther, pagePath+"?sent=1")
	return "", nil
}

func (p *Plugin) render(name string, data any) (template.HTML, error) {
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func tmplFuncs() template.FuncMap {
	return template.FuncMap{
		"date": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.Format("2 Jan 2006")
		},
		// The site's own relative-time helper is not reachable from a plugin's
		// template set, so this is the small version — enough for a queue where
		// the only question is "how long has this been sitting here".
		"ago": func(t time.Time) string {
			d := time.Since(t)
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				return strconv.Itoa(int(d.Minutes())) + "m ago"
			case d < 24*time.Hour:
				return strconv.Itoa(int(d.Hours())) + "h ago"
			case d < 7*24*time.Hour:
				return strconv.Itoa(int(d.Hours()/24)) + "d ago"
			default:
				return t.Format("2 Jan 2006")
			}
		},
		// The first letter of a name, for the avatar-frame previews. A plugin
		// template set has no access to the host's initials helper, and one
		// character is all a 64px circle holds.
		"firstRune": func(s string) string {
			for _, r := range s {
				return strings.ToUpper(string(r))
			}
			return "?"
		},
	}
}
