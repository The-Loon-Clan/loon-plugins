package agent

import (
	"context"
	"errors"
	"time"

	"github.com/gin-gonic/gin"
)

// ErrNameTaken is the sentinel a host's CreateAgentFor returns (wrapped or
// bare) when the member already has an agent by that name. It is the one
// refusal the member can fix themselves, so the page names it instead of
// showing the generic something-went-wrong banner. Any other error stays
// generic on purpose — a create failure's details are the host's logs'
// business, not the form's.
var ErrNameTaken = errors.New("agent name taken")

// ErrNotFound is the sentinel a host returns when the agent id does not belong
// to that member -- or no longer exists at all. Deliberately ONE sentinel for
// both: a host scopes its update by owner in SQL, so "not yours" and "gone"
// arrive as the same zero-rows answer, and distinguishing them in the message
// would confirm that somebody else's agent id is real.
var ErrNotFound = errors.New("agent not found")

// Agent is one row of the fleet card. It is deliberately NOT the host's agent
// token record: the card renders three fields, and the token carries the
// secret hash, the owner id, and the revocation state alongside them. A plugin
// type that can only hold what is displayed cannot leak the rest into a
// template, and it keeps the host free to reshape its own record.
type Agent struct {
	ID       int
	Name     string
	LastSeen *time.Time
}

// Task is what an agent is working on right now, or nil for idle. Two fields,
// for the same reason as above — the host's lock row also carries fail
// reasons, warnings and the requesting user's name, none of which belong on
// someone's public-ish profile widget.
type Task struct {
	RequestID int64
	Progress  string
}

// AgentDetail is one agent on the OWNER's /p/agents page: the roster row
// plus its latest live report. Status is nil when the agent has never
// reported or its report has aged out host-side.
type AgentDetail struct {
	Agent
	Status *AgentStatus
}

// AgentStatus is the live report, narrowed to what the member page draws.
// The host's own status record carries more (per-file lock warnings, seed
// buckets, reservation accounting); the same cannot-leak-what-it-cannot-hold
// reasoning as Agent applies, and PublicIP is here because it is the OWNER's
// page — the public profile variant never renders any of this.
type AgentStatus struct {
	Phase         string
	VPNStatus     string
	PublicIP      string
	DownloadSpeed string
	UploadSpeed   string
	DiskFreeGB    float64
	TaskTitle     string
	RequestID     int64
	Files         []FileDetail
}

// FileDetail is one file's progress within the live report.
type FileDetail struct {
	Name    string
	Percent float64
	Phase   string
	Speed   string
}

// AdminAgent is one row of the admin roster: the whole fleet, with its owner.
//
// Narrowed for the same reason as Agent above, and here the reason has teeth.
// A host's own token record carries the bearer secret (or its hash) beside
// these fields, and this page renders in an admin template where a stray
// {{.Token}} would be both invisible in review and catastrophic. A type that
// cannot hold the secret cannot print it.
//
// Status is the host's own word ("active" / "revoked") rather than a bool,
// because a fleet grows states — expired, suspended — and a bool would force
// the next one to be a lie or a second field.
type AdminAgent struct {
	ID        int
	Name      string
	Owner     string
	Status    string
	LastSeen  *time.Time
	CreatedAt time.Time
}

// Online reports whether this agent polled recently enough to show a green
// dot. Display only, and the same window the profile card uses so an operator
// and a member never disagree about who is up.
func (a AdminAgent) Online() bool {
	return a.LastSeen != nil && time.Since(*a.LastSeen) < onlineWindow()
}

// AdminLink is one entry on the dispatch panel's jump list: a host's own
// agent-related admin page, named by the host that mounts it.
type AdminLink struct {
	Label string
	Href  string
}

// AgentGroup is one posting profile pushed to the fleet: which newsgroups a
// kind of upload goes to, and the per-group overrides agents apply while
// building it.
//
// The four override fields are POINTERS and every one of them has to stay
// that way. Blank means "the agent uses its own default for this group",
// which is a different instruction from zero — 0 screenshots means take
// none, and 0% PAR2 means ship without recovery. A value type would collapse
// those two answers into one and silently turn every unset field into an
// aggressive setting.
//
// Version is the host's, read-only here: agents poll for it to notice a
// profile changed, so the plugin must never invent one.
type AgentGroup struct {
	ID               int
	Name             string
	Type             string
	Newsgroups       []string
	BannedExtensions []string
	Screenshots      *int
	SampleSeconds    *int
	Par2Redundancy   *int
	Obfuscate        *bool
	WatermarkText    string
	Version          int
}

// Deps carries everything the plugin cannot know for itself. It is function-
// typed rather than an interface set because every one of these needs
// adapting at the boundary anyway (host records to the view types above), and
// a func makes the host's adapter the obvious place for that to happen.
//
// The agent TABLES stay with the host: /api/agent/* serves the fleet runtime
// and the dispatch machinery works the same rows. This plugin owns the two
// read-only SURFACES over that data, nothing else.
type Deps struct {
	// Viewer identifies the signed-in requester; ok is false when anonymous.
	// The fleet card needs this to answer "is this the profile's owner", which
	// loon's Public/MinRole visibility cannot express.
	Viewer func(c *gin.Context) (userID int, ok bool)

	// AgentsForUser lists one member's agents, for their own profile card.
	AgentsForUser func(ctx context.Context, userID int) ([]Agent, error)

	// ActiveTask reports what an agent is currently working on; nil for idle.
	ActiveTask func(ctx context.Context, agentID int) (*Task, error)

	// CountAgents powers the admin overview: how many agents reported in since
	// onlineSince, and how many exist at all.
	CountAgents func(ctx context.Context, onlineSince time.Time) (online, total int, err error)

	// MaxConcurrent is the host's per-agent dispatch cap, displayed read-only.
	// Editing it stays in the host's Agent Defaults form on the same page.
	MaxConcurrent func(ctx context.Context) int

	// ── The member page's OPTIONAL seams ─────────────────────────────
	//
	// Everything below may be nil, and each nil degrades one feature of
	// /p/agents rather than failing Provision: a host that has not built a
	// seam yet still boots, renders the card and the basic page, and grows
	// into the rest. Every verb is owner-scoped by SIGNATURE — the host
	// implementation filters on ownerID, so the plugin cannot ask for
	// another member's agents even by bug.

	// AgentsDetail lists one member's agents WITH their live status, for
	// the owner's /p/agents page. Nil falls back to AgentsForUser +
	// ActiveTask (roster rows, no live detail).
	AgentsDetail func(ctx context.Context, ownerID int) ([]AgentDetail, error)

	// CreateAgentFor registers a new agent for the member and returns its
	// bearer token — the ONE time it is ever shown; the host stores only
	// the hash. Nil hides self-service entirely (rotate and delete too):
	// creation is where the token ceremony lives, and offering rotate
	// without create is a half-open door.
	CreateAgentFor func(ctx context.Context, ownerID int, name string) (token string, err error)

	// RotateTokenFor replaces the agent's token and returns the new one,
	// shown once. The old token stops working immediately.
	RotateTokenFor func(ctx context.Context, ownerID, agentID int) (token string, err error)

	// DeleteAgentFor removes the member's own agent.
	DeleteAgentFor func(ctx context.Context, ownerID, agentID int) error

	// ShowOnProfile reads the member's public-visibility opt-in: whether
	// their fleet card renders on /u/<name> for OTHER viewers. Host-stored
	// because this plugin is surfaces-only by charter — it owns no tables.
	// Default is HIDDEN (the inverse of achievements' absence-means-shown):
	// an agent roster names machines, and nobody consented to that by
	// installing an agent. Nil means the opt-in does not exist and the
	// card stays owner-only everywhere.
	ShowOnProfile func(ctx context.Context, ownerID int) (bool, error)

	// SetShowOnProfile records the opt-in from the /p/agents page.
	SetShowOnProfile func(ctx context.Context, ownerID int, show bool) error

	// AllAgents lists every agent on the host, for the ADMIN roster at
	// /admin/p/agents. Nil means the roster page is not registered at all
	// rather than rendering empty — an operator who reaches a page that
	// exists and shows nothing cannot tell a missing seam from an empty
	// fleet, and only one of those is worth investigating.
	//
	// It is separate from AgentsForUser because it is a different question
	// with a different answer: that one is owner-scoped by signature so the
	// plugin cannot ask about anyone else, and widening it with an optional
	// "all" mode would give the member surfaces a parameter that turns the
	// scoping off. Two seams, each of which can only do its own job.
	AllAgents func(ctx context.Context) ([]AdminAgent, error)

	// OnlineWindow is how long an agent may be silent and still read as
	// online. Nil, or a non-positive value, falls back to 5 minutes.
	//
	// The host knows its own poll interval and this plugin does not, so a
	// constant here is a guess that CONTRADICTS the host in front of an
	// operator: with agents ~4 minutes quiet, a host page on a 3-minute
	// window read "0 of 5 online" beside this plugin's three green dots,
	// same fleet, same instant. CountAgents already took its cutoff from
	// the host; this makes the other two surfaces agree with it.
	OnlineWindow func() time.Duration

	// AdminLinks are the host's OWN agent-related admin pages, listed on
	// the dispatch panel beside the plugin's.
	//
	// It is a seam and not a hardcoded list because a plugin cannot know a
	// host's route names. The panel used to hardcode /admin/dispatch and
	// /admin/agents; the first is a 404 on a host that draws its dispatch
	// queue as a panel rather than a page, and a link-audit found it. A
	// plugin that invents a host's URLs is dead on arrival everywhere but
	// the host it was written against.
	//
	// Nil renders only the plugin's own pages, which always exist because
	// the plugin mounts them.
	AdminLinks func() []AdminLink

	// ── The agent-groups admin page's seams ─────────────────────────
	//
	// ListAgentGroups nil means no page, on the same reasoning as
	// AllAgents. The three mutators are separately optional and gate the
	// FORMS rather than the page: a host that can list but not write
	// gets a read-only view of its posting profiles, which is a useful
	// thing to have and not a broken page.

	// ListAgentGroups returns every posting profile, newest schema first
	// as the host orders them.
	ListAgentGroups func(ctx context.Context) ([]AgentGroup, error)

	// CreateAgentGroup adds a profile. The host assigns ID and Version.
	CreateAgentGroup func(ctx context.Context, g AgentGroup) error

	// UpdateAgentGroup replaces a profile by ID. The host bumps Version,
	// which is how agents notice the change on their next poll.
	UpdateAgentGroup func(ctx context.Context, g AgentGroup) error

	// DeleteAgentGroup removes a profile by ID.
	DeleteAgentGroup func(ctx context.Context, id int) error

	// CanUseAgents decides whether a member may use the fleet surfaces at
	// all: the profile card, the member page, and every action on it.
	//
	// It exists because "who may run an agent" is a real operator question --
	// keeping new members from being handed a capability they have not asked
	// for, or restricting the fleet to staff -- and the plugin has no basis
	// to answer it. The host does: see EntitlementKey.
	//
	// NIL MEANS ALLOWED, which is the opposite of the tracker plugin's gate
	// and deliberately so. tracker.access gates a capability that was gated
	// from birth, so a host that has not wired the decider has not decided
	// everyone may announce. Agents are the other case: hosts have been
	// running them for years, so failing closed on an absent seam would
	// switch off a working fleet on upgrade. A gate retrofitted onto a live
	// capability has to default to today's behaviour.
	CanUseAgents func(ctx context.Context, userID int) (bool, error)
}

// EntitlementKey is the entitlement a host grants to let a member use agents.
//
// Exported because the HOST grants it and needs the same string; a literal in
// two repositories is how somebody ends up entitled to something nothing
// checks. Spelled like tracker.access, and a host wires it through the same
// core.Entitlements service:
//
//	CanUseAgents: func(ctx context.Context, uid int) (bool, error) {
//	    return c.Entitlements.Has(ctx, int64(uid), agent.EntitlementKey), nil
//	}
const EntitlementKey = "agent.use"

// allowed reports whether this member may see the fleet surfaces. An error
// from the host is treated as a refusal: the seam exists to answer this
// question, and a seam that failed has not answered it.
func allowed(ctx context.Context, userID int) bool {
	if deps == nil || deps.CanUseAgents == nil {
		return true
	}
	ok, err := deps.CanUseAgents(ctx, userID)
	return err == nil && ok
}

var deps *Deps

// SetDeps hands the plugin its host adapters. Called once from the composition
// root before core.Boot.
func SetDeps(d Deps) { deps = &d }
