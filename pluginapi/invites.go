// invites.go is how a plugin gets somebody in.
//
// A plugin that decides WHO may join — an application queue, an interview, a
// waiting list — still should not decide HOW they join. Invite codes are the
// host's: it mints them, expires them, locks them to an address, emails them,
// counts them against a member's balance and consumes them at registration.
// A plugin that issued its own would be a second door into the site with none
// of that behind it.
//
// So the plugin makes the decision and asks the host to open the door.
package pluginapi

import "context"

// InviteIssuerName is the Core extension-registry key. Absent means the host
// has no invite system, and a plugin that needed one must say so rather than
// approving people it cannot let in.
const InviteIssuerName = "auth.invite.issue"

// InviteRequest is an invitation the host should create and send.
type InviteRequest struct {
	// Email is who it is for. The host locks the invite to it and sends there,
	// under whatever rules the site has configured.
	Email string
	// Note is an optional line to the recipient, carried into the email — for
	// an application queue, this is where "your application was accepted"
	// belongs.
	Note string
	// IssuedBy is the staff member who approved it, recorded as the issuer so
	// the invite chain answers "who vouched for them" with a real person
	// rather than with the system.
	//
	// Zero means nobody in particular — a host may then record it against no
	// member, which is honest for an automated approval and is why this is not
	// required.
	IssuedBy int64
	// ChargeBalance says whether this should cost IssuedBy one of their
	// invites.
	//
	// False for a staff approval, which is the ordinary case: approving an
	// application is the site's decision, not a gift out of a moderator's
	// personal allowance, and charging it would quietly ration how many people
	// staff may admit.
	ChargeBalance bool
}

// IssuedInvite is what came back.
type IssuedInvite struct {
	Code string
	// Sent reports whether the email actually went. False is not a failure —
	// the invite exists and the code is in hand — but it is the difference
	// between "they have been told" and "somebody needs to tell them", and a
	// queue that approved somebody silently should be able to see which.
	Sent bool
}

// InviteIssuer mints an invite on a plugin's behalf.
type InviteIssuer interface {
	// IssueInvite creates the invite, sends it if the site sends invites, and
	// returns the code so the caller can record or display it.
	//
	// An error means no invite exists — the caller must not report success. A
	// host that cannot send mail still returns the code with Sent false, since
	// the invitation is real either way.
	IssueInvite(ctx context.Context, req InviteRequest) (IssuedInvite, error)
}
