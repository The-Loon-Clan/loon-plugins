package applications

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The two surfaces: the form a stranger fills in, and the queue staff work
// through.
//
// The form is PUBLIC — it has to be, since the whole point is somebody who has
// no account and no invite — which makes it the one page here a stranger can
// POST to. Everything that follows from that is in submit() below.

const (
	// bodyMax bounds the answer. Generous: a site asking "why do you want to
	// join" is asking for a paragraph, and truncating somebody mid-sentence
	// loses the thing being judged.
	bodyMax = 4000
	// queueLimit is how many decided rows the staff page shows beside the
	// pending ones.
	queueLimit = 50
)

func (p *Plugin) registerViews(c *core.Core) error {
	if err := c.RegisterView(core.View{
		Slug:        "apply",
		Title:       "Apply to join",
		Slot:        core.SlotSitePage,
		Public:      true, // a stranger with no account is the whole audience
		Description: "Ask to join a closed site.",
		Nav:         core.NavHint{Menu: core.NavHidden},
		Render:      p.applyPage,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"submit": p.submit,
		},
	}); err != nil {
		return fmt.Errorf("applications: register apply view: %w", err)
	}
	if err := c.RegisterView(core.View{
		Slug:    "applications",
		Title:   "Applications",
		Slot:    core.SlotAdminPage,
		MinRole: core.RoleMod,
		Nav:     core.NavHint{Group: "Moderation"},
		Render:  p.queuePage,
		Actions: map[string]func(*gin.Context) (template.HTML, error){
			"decide": p.decide,
		},
	}); err != nil {
		return fmt.Errorf("applications: register queue view: %w", err)
	}
	// The call to action on the host's sign-up page. A WIDGET rather than a
	// host template edit: the host owns that page's layout, and this plugin
	// only has something to say when its mode is the active one.
	return c.RegisterWidget(core.Widget{
		Slug:        "apply-cta",
		Title:       "Apply to join",
		Public:      true,
		ConfigLabel: "",
		Render:      p.applyWidget,
	})
}

// applyPage renders the form.
func (p *Plugin) applyPage(c *gin.Context) (template.HTML, error) {
	return p.render("applications_apply.html", map[string]any{
		"CSRF": pluginapi.CSRFToken(p.core, c),
		"Sent": c.Query("sent") == "1",
		"Err":  c.Query("err"),
	})
}

// applyWidget is the link the host's register page shows in place of a form.
func (p *Plugin) applyWidget(c *gin.Context) (template.HTML, error) {
	return p.render("applications_cta.html", nil)
}

// submit takes one application from a stranger.
//
// PUBLIC POST, which is the whole risk surface of this plugin, so the refusals
// are all here and all before anything is written:
//
//   - an address that already has an application waiting (one queue entry per
//     person, not one per time they pressed the button)
//   - a shape check on the address, because an approval emails it and an
//     invite sent nowhere is a slot given to nobody
//   - a bounded body
//
// What it deliberately does NOT do is tell the applicant whether their address
// already has an account. That is the same enumeration oracle the invite form
// refuses to be, and here it would be worse: this endpoint takes no
// credentials at all.
func (p *Plugin) submit(c *gin.Context) (template.HTML, error) {
	email := strings.ToLower(strings.TrimSpace(c.PostForm("email")))
	username := strings.TrimSpace(c.PostForm("username"))
	body := strings.TrimSpace(c.PostForm("body"))

	fail := func(key string) (template.HTML, error) {
		c.Redirect(303, "/p/apply?err="+key)
		return "", nil
	}
	if !looksLikeEmail(email) {
		return fail("bad-email")
	}
	if body == "" {
		return fail("no-body")
	}
	if len(body) > bodyMax || len(username) > 64 || len(email) > 254 {
		return fail("too-long")
	}
	ctx := c.Request.Context()
	if pending, err := p.st.PendingFor(ctx, email); err != nil {
		return fail("failed")
	} else if pending {
		// Said plainly, and it is not an oracle: it only reveals that an
		// application exists, which the person asking already knows because
		// they made it.
		return fail("already")
	}

	// The IP, HASHED. The operator needs to see that six applications came
	// from one place; nobody needs the address itself sitting in a table a
	// moderator can read, and a hash answers the question that is actually
	// asked.
	if err := p.st.Create(ctx, Application{
		Email: email, Username: username, Body: body,
		IPHash: hashIP(c.ClientIP()),
	}); err != nil {
		return fail("failed")
	}
	// No email and no IP hash travel with it — see events.go.
	p.emit(ctx, EventSubmitted, Submitted{})
	c.Redirect(303, "/p/apply?sent=1")
	return "", nil
}

// queuePage is the staff view.
func (p *Plugin) queuePage(c *gin.Context) (template.HTML, error) {
	ctx := c.Request.Context()
	pending, err := p.st.List(ctx, StatusPending, queueLimit)
	if err != nil {
		return "", err
	}
	accepted, err := p.st.List(ctx, StatusAccepted, queueLimit)
	if err != nil {
		return "", err
	}
	rejected, err := p.st.List(ctx, StatusRejected, queueLimit)
	if err != nil {
		return "", err
	}
	counts, err := p.st.Counts(ctx)
	if err != nil {
		return "", err
	}
	return p.render("applications_queue.html", map[string]any{
		"CSRF":     pluginapi.CSRFToken(p.core, c),
		"Pending":  pending,
		"Accepted": accepted,
		"Rejected": rejected,
		"Counts":   counts,
		// Whether accepting can actually do anything — see Start. Stated on the
		// page rather than discovered at the moment somebody clicks Accept.
		"CanIssue": p.issuer != nil,
	})
}

// decide records an outcome, and on an acceptance opens the door.
//
// ORDER MATTERS AND IS THE OPPOSITE OF THE OBVIOUS ONE. The invite is issued
// FIRST, then the decision is recorded with the code in it. Recording first
// would leave an application marked accepted whose invite failed to mint — an
// applicant the queue believes was let in and who never received anything, and
// no way to tell that apart from one who ignored their email.
//
// The cost of this order is the other failure: an invite minted for an
// application that then loses the race to another moderator. That is one
// unused invite in the table, which expires on its own, and it is the smaller
// harm by a wide margin.
func (p *Plugin) decide(c *gin.Context) (template.HTML, error) {
	id, err := strconv.ParseInt(c.PostForm("id"), 10, 64)
	if err != nil || id <= 0 {
		c.Redirect(303, "/admin/p/applications")
		return "", nil
	}
	ctx := c.Request.Context()
	app, ok, err := p.st.Get(ctx, id)
	if err != nil || !ok || !app.Pending() {
		c.Redirect(303, "/admin/p/applications")
		return "", nil
	}

	var actor int64
	if u, okU := p.core.Auth.CurrentUser(c); okU && u != nil {
		actor = u.ID
	}
	note := strings.TrimSpace(c.PostForm("note"))
	if len(note) > 1000 {
		note = note[:1000]
	}

	if c.PostForm("decision") != "accept" {
		if _, err := p.st.Decide(ctx, id, StatusRejected, actor, note, ""); err != nil {
			return "", err
		}
		p.emit(ctx, EventDecided, Decided{
			ApplicationID: id, Status: StatusRejected, DecidedBy: actor,
		})
		c.Redirect(303, "/admin/p/applications")
		return "", nil
	}

	if p.issuer == nil {
		// Refused rather than recorded. An acceptance that cannot issue an
		// invite is a queue saying yes and meaning no.
		c.Redirect(303, "/admin/p/applications?err=no-issuer")
		return "", nil
	}
	issued, err := p.issuer.IssueInvite(ctx, pluginapi.InviteRequest{
		Email:    app.Email,
		Note:     note,
		IssuedBy: actor,
		// Not charged. Admitting somebody is the site's decision, not a gift
		// out of the approving moderator's personal allowance — see
		// pluginapi.InviteRequest.
		ChargeBalance: false,
	})
	if err != nil {
		c.Redirect(303, "/admin/p/applications?err=issue-failed")
		return "", nil
	}
	if _, err := p.st.Decide(ctx, id, StatusAccepted, actor, note, issued.Code); err != nil {
		return "", err
	}
	// The invite CODE is not announced. It is a credential, and an event is
	// the one place on this site a value reaches several plugins at once.
	p.emit(ctx, EventDecided, Decided{
		ApplicationID: id, Status: StatusAccepted, DecidedBy: actor,
	})
	c.Redirect(303, "/admin/p/applications")
	return "", nil
}

func (p *Plugin) render(name string, data any) (template.HTML, error) {
	var buf bytes.Buffer
	if err := p.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// hashIP turns a client address into something that answers "same source?"
// without being an address.
//
// Unsalted on purpose is NOT the choice here — the site's own salt is not
// reachable from a plugin, so this uses the address alone and is therefore
// reversible by anybody who guesses an address and compares. That is
// acceptable for what it is used for (spotting a run from one source in a
// staff queue) and is why the column is never displayed, only compared.
func hashIP(ip string) string {
	if ip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("applications:" + ip))
	return hex.EncodeToString(sum[:8])
}

// looksLikeEmail is a shape check, not validation — the only authority on
// whether an address exists is the mail server, and every stricter regex
// rejects somebody's real address.
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 || strings.Count(s, "@") != 1 {
		return false
	}
	domain := s[at+1:]
	dot := strings.IndexByte(domain, '.')
	return dot > 0 && dot < len(domain)-1 && !strings.ContainsAny(s, " \t")
}
