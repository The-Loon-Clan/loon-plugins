package pluginapi

import "context"

// UserPrefs is the per-user agent IP-pin surface. The pin state lives on
// the host's user_preferences table (not agent_tokens), and the IP hashing
// is a host concern — plaintext client IPs must never reach the plugin — so
// the whole check is expressed as one host call that returns a decision.
//
// The agent plugin holds this via its Deps and keeps its own HTTP
// response logic; the host owns the storage + hashing.
type UserPrefs interface {
	// AgentIPCheck evaluates the pin for one authenticated agent request.
	// allow=true when the client IP is acceptable: the pin is disabled,
	// this is the first connection (auto-captured), or the IP matches the
	// pinned one. allow=false when it's a new IP — in which case the new
	// IP is recorded as "pending" for the user to approve in account
	// settings. The host hashes clientIP internally.
	AgentIPCheck(ctx context.Context, userID int, clientIP string) (allow bool, err error)

	// AgentIPState reports the pin state for the account-settings UI:
	// whether the pin is enforced and whether an active / pending IP is
	// currently on file (the hashes themselves stay host-side).
	AgentIPState(ctx context.Context, userID int) (pinEnabled, hasActive, hasPending bool, err error)

	// ── Settings-page mutations ────────────────────────────────────────

	// ApproveAgentIP promotes the pending IP into the active slot.
	ApproveAgentIP(ctx context.Context, userID int) error
	// ClearAgentIP wipes both active and pending IPs (re-pair from a new
	// network).
	ClearAgentIP(ctx context.Context, userID int) error
	// SetAgentIPPin toggles whether the pin is enforced; disabling also
	// wipes stored pin state so re-enabling starts from a clean capture.
	SetAgentIPPin(ctx context.Context, userID int, enabled bool) error
}
