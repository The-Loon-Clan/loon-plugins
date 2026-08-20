// Package economy is a worker-only plugin hosting the per-grab "Points Grab
// Bonus", extracted from pkg/services. No web surface — it registers and runs
// one scheduled loop, and the points-per-grab rate stays on the host
// SettingsService so the admin settings page keeps configuring it.
//
// The annual tenure bonus used to live here. It is a per_unit reward in the
// rewards plugin now, because paying on the exact anniversary DAY meant a
// missed run cost a member a year — see that plugin, and docs/REWARDS.md.
package economy

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon/core"
)

func init() {
	core.RegisterPlugin("economy", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	grab *grabBonus
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "economy",
		Version:     "1.0.0",
		Description: "Points-economy worker job: the per-grab uploader bonus.",
		Processes:   []string{"worker"},
		Flavours:    []string{core.FlavourAny},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	if !deps.ready() {
		return fmt.Errorf("economy: SetDeps not called, or missing a seam — wire it in main()'s worker block before core.Boot")
	}
	if c.Points == nil {
		// The job exists to move points. Without the ledger it would run,
		// log success and award nothing, which is worse than refusing.
		return fmt.Errorf("economy: Core.Points is nil — there is no ledger to award into")
	}
	p.grab = newGrabBonus(*deps, c.Points)
	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	// ctx is the host root context, cancelled on SIGTERM, so the loop exits
	// cleanly on docker stop rather than being killed mid-award.
	p.grab.start(ctx)
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }
