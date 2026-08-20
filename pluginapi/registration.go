// registration.go lets a plugin add a way to join the site.
//
// A host ships the obvious three — open, invite only, closed — and they are
// hardcoded because they are the ones every site has. But "how do you get in"
// is exactly the kind of policy a site wants to differ on: an application form
// with a staff queue, a waiting list, an interview, a paid signup window. Each
// of those is a plugin, and before this there was nowhere for one to say so.
//
// The result was that adding a fourth mode meant editing the host's enum, its
// validator, its admin template and its register template — four edits in a
// repository the plugin author does not own, to add a policy the host has no
// opinion about.
//
// So a mode is REGISTERED, and the host renders whatever it finds. What the
// host keeps is the switch itself: exactly one mode is active, the operator
// chooses it on the access page, and it persists across restarts like the
// built-in three.
package pluginapi

import (
	"github.com/the-loon-clan/loon/core"
)

// RegistrationModePrefix is scanned on the extension registry. A plugin
// registers "auth.regmode.<key>", and the key is what the setting stores.
//
// A prefix rather than one entry holding a list, for the same reason the store
// item types use one: two plugins can each add a mode without knowing about
// each other, and neither has to merge into a slice somebody else owns.
const RegistrationModePrefix = "auth.regmode."

// RegistrationModeInfo is what the host needs to offer a mode and enforce it.
type RegistrationModeInfo struct {
	// Key is the stored value, and must match the registry suffix. Lowercase,
	// no spaces — it goes in a settings row and a radio button's value.
	Key string
	// Label is the radio button's text; Description the sentence under it,
	// which is where a mode explains what it actually does to a visitor.
	Label       string
	Description string

	// AllowsSignup says whether the sign-up FORM is offered to a visitor who
	// arrives with nothing.
	//
	// It governs the PAGE, not the endpoint — a distinction that cost a
	// working feature the first time round. A mode like "apply first" sets it
	// false because a stranger should be sent to the application form rather
	// than shown a sign-up box; but the person who then gets approved arrives
	// holding an invite, and registration must still accept them. Reading this
	// flag as "nobody may register" refused exactly the people the mode exists
	// to admit.
	//
	// So: false hides the form. RequiresInvite below is what decides whether a
	// submission is accepted, and the two are independent.
	AllowsSignup bool

	// ActionHref and ActionLabel are where a visitor should go instead, for a
	// mode that does not allow signing up.
	//
	// Part of the MODE rather than left to a widget, because a widget has to
	// be placed by an operator before it appears — and a site whose sign-up
	// page says "you cannot register" and nothing else, because nobody had
	// opened the widget editor yet, is a site that turns people away by
	// accident. The widget region stays for richer content; this is the floor.
	ActionHref  string
	ActionLabel string

	// RequiresInvite says the form must carry a valid invite code, the same
	// rule the built-in invite mode enforces.
	//
	// True with AllowsSignup FALSE is the application shape: a stranger sees
	// no form, an approved applicant arrives from the invite email with a code
	// in the URL and completes the ordinary registration. The code is still
	// what the host checks, so the plugin never reimplements redemption, the
	// email lock or the race guard.
	RequiresInvite bool
}

// RegistrationMode is what a plugin registers.
//
// One method, and deliberately not "handle the registration": the host already
// owns creating accounts, hashing passwords, issuing sessions and consuming
// invites, and a mode that took that over would be a second implementation of
// the most security-sensitive path on the site. A mode DESCRIBES a policy; the
// host enforces it.
type RegistrationMode interface {
	RegistrationMode() RegistrationModeInfo
}

// RegistrationModes collects every mode registered on the registry, in
// registration order.
//
// Modes whose key does not match their registry suffix are DROPPED rather than
// corrected: the key is what a settings row stores and what a radio button
// submits, and a mismatch means one of those two would disagree with the other
// — which is a bug that surfaces as "the site quietly reverted my choice".
func RegistrationModes(c *core.Core) []RegistrationModeInfo {
	// ConsistentKeys is the check this function has always made and the reason
	// it is a shared helper now: the registered key is what a settings row
	// stores and what a form submits, and a mode whose own Key disagrees with
	// it leaves one half of the site believing a setting took while the other
	// does not.
	modes := ConsistentKeys(
		Contributions[RegistrationMode](c, RegistrationModePrefix),
		func(m RegistrationMode) string { return m.RegistrationMode().Key },
	)
	out := make([]RegistrationModeInfo, 0, len(modes))
	for _, m := range modes {
		out = append(out, m.Value.RegistrationMode())
	}
	return out
}

// RegistrationModeByKey finds one registered mode.
func RegistrationModeByKey(c *core.Core, key string) (RegistrationModeInfo, bool) {
	for _, m := range RegistrationModes(c) {
		if m.Key == key {
			return m, true
		}
	}
	return RegistrationModeInfo{}, false
}
