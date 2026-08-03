// Package economy is a worker-only plugin hosting the points-economy cron jobs
// extracted from pkg/services: the annual "Points Tenure Bonus" and the
// per-grab "Points Grab Bonus". No web surface — it only registers + runs the
// two scheduled loops. The points-per-grab / points-per-year settings stay on
// the host SettingsService (injected via JobDeps) so the admin settings page
// keeps configuring them.
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
