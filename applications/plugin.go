// Package applications is a front door for a closed site: somebody who cannot
// get an invite from a member asks the site directly, staff read the answer,
// and an approval issues a real invite.
//
// It exists because "invite only" and "closed" are both wrong for a site that
// wants to grow deliberately. Invite-only delegates the decision to whoever
// already has an account; closed means nobody gets in at all. An application
// queue is the third thing — the site decides, one person at a time — and it
// is the mode most private trackers actually run.
//
// WHAT THIS PLUGIN DOES NOT DO is the important half. It does not create
// accounts, hash passwords, issue sessions, or mint its own codes. An approval
// calls pluginapi.InviteIssuer and the HOST opens the door: same invite, same
// expiry window the operator configured, same email, same entry in the chain
// that records who vouched for whom. The plugin decides who; the host decides
// how. A queue that admitted people by its own route would be a second door
// into the site with none of that behind it.
//
// It registers a REGISTRATION MODE rather than fighting the host's one. With
// "apply-first" selected, the host's register page stops offering a form and
// this plugin's call to action appears in its place — both because the mode
// says AllowsSignup false and RequiresInvite true, which together mean "you
// join through an invite, and applications are how you get one".
package applications

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed templates/*.html
var tmplFS embed.FS

func init() {
	core.RegisterPlugin("applications", func() core.Plugin { return &Plugin{} })
}

// ModeKey is the registration mode this plugin adds. The host stores it in the
// access setting, so it must never change once a site has selected it.
const ModeKey = "apply"

type Plugin struct {
	core *core.Core
	st   Store
	tmpl *template.Template

	// issuer opens the door on an approval. REQUIRED for accepting: without
	// it, staff could mark somebody accepted and nothing would let them in,
	// which is the worst of both — a queue that says yes and means no.
	issuer pluginapi.InviteIssuer
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "applications",
		Version:     "0.1.0",
		Description: "Apply to join: a public application form, a staff queue, and an approval that issues a real invite.",
		Migrations:  migrations,
		Processes:   []string{"web"},
		Flavours:    []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	// The queue page's CSS. See stylesheet.go.
	pluginapi.RegisterStylesheet(c, "applications", applicationsCSS)
	p.core = c
	db := c.Storage.SchemaDB(p.Metadata().Name)
	if db == nil {
		return fmt.Errorf("applications: Core.Storage.SchemaDB is nil")
	}
	p.st = NewPGStore(db)

	if err := declareEvents(c); err != nil {
		return fmt.Errorf("applications: declaring events: %w", err)
	}

	tmpl, err := template.New("applications").ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("applications: parsing templates: %w", err)
	}
	p.tmpl = tmpl

	// The mode this plugin adds. Registered in Provision because the host reads
	// the registry when it renders the access page and validates a saved
	// setting, and both can happen before any Start.
	if err := c.Register(pluginapi.RegistrationModePrefix+ModeKey,
		pluginapi.RegistrationMode(modeDescriptor{})); err != nil {
		return fmt.Errorf("applications: register mode: %w", err)
	}

	return p.registerViews(c)
}

// modeDescriptor is the policy this plugin offers the host.
//
// AllowsSignup false and RequiresInvite true together are the whole design:
// you cannot sign up directly, you join with an invite, and applications are
// how an invite is obtained. The host enforces both — this plugin never sees a
// registration.
type modeDescriptor struct{}

func (modeDescriptor) RegistrationMode() pluginapi.RegistrationModeInfo {
	return pluginapi.RegistrationModeInfo{
		Key:   ModeKey,
		Label: "Apply to join",
		Description: "No open sign-up. Visitors apply, staff decide, and an approval " +
			"sends them a real invite — so an accepted applicant joins the same way " +
			"an invited member does, and the invite chain records who approved them.",
		ActionHref:     "/p/apply",
		ActionLabel:    "Apply to join",
		AllowsSignup:   false,
		RequiresInvite: true,
	}
}

// Start resolves the issuer.
//
// In Start because every Provision runs first and the host registers its own
// capabilities during that window. Absence is LOUD: the queue still collects
// applications, and staff would otherwise discover at the moment of approving
// somebody that nothing happens.
func (p *Plugin) Start(ctx context.Context) error {
	if p.core == nil {
		return nil
	}
	if v, ok := p.core.Lookup(pluginapi.InviteIssuerName); ok {
		p.issuer, _ = v.(pluginapi.InviteIssuer)
	}
	if p.issuer == nil {
		log.Printf("applications: no %s capability — applications can be collected and "+
			"rejected, but ACCEPTING one cannot issue an invite and will refuse. "+
			"The host must register an invite issuer.", pluginapi.InviteIssuerName)
	}
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }
