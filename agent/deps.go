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
}

var deps *Deps

// SetDeps hands the plugin its host adapters. Called once from the composition
// root before core.Boot.
func SetDeps(d Deps) { deps = &d }
