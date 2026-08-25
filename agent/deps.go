package agent

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

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
}

var deps *Deps

// SetDeps hands the plugin its host adapters. Called once from the composition
// root before core.Boot.
func SetDeps(d Deps) { deps = &d }
