// Package backups is the site-backup plugin: on a weekly (and admin-triggered)
// job it dumps the database and then runs EVERY plugin's Backupable hook,
// writing each into one archive. Plugins opt in by implementing
// pluginapi.Backupable and calling pluginapi.RegisterBackup(c, self) in their
// Provision — the backups plugin discovers them off the core extension registry,
// so adding a backup-capable plugin needs no change here.
// NOT to be confused with package `backup` (singular) in this same repo, which
// is production's index/pack/pull pipeline -- 5,700 lines to this file's 115,
// solving a different problem: assets too large to re-transfer whole, kept on
// hardware separate from the box that made them. This one is the generic
// answer and the one the demo host wires.
package backups

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func init() {
	core.RegisterPlugin("backups", func() core.Plugin { return &Plugin{} })
}

type Plugin struct {
	core *core.Core
	ctx  context.Context
	job  core.Job
}

func (p *Plugin) Metadata() core.Metadata {
	return core.Metadata{
		Name:        "backups",
		Version:     "0.1.0",
		Description: "Weekly site backup: dumps the database and runs every plugin's Backupable hook into one archive.",
		Processes:   []string{"worker"},
	}
}

func (p *Plugin) Provision(c *core.Core) error {
	p.core = c
	if deps == nil || deps.OpenEntry == nil {
		return fmt.Errorf("backups: SetDeps not called with OpenEntry before core.Boot")
	}
	p.job = c.Scheduler.RegisterJob("Backup", "Dumps the DB and every plugin's backup hook into one archive").MarkOffPeak()
	p.job.SetTrigger(func() { go p.run(p.ctx) })
	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	p.ctx = ctx
	p.core.Scheduler.RunLoop(ctx, p.job, 10*time.Minute, 7*24*time.Hour, p.run)
	return nil
}

func (p *Plugin) Stop(ctx context.Context) error { return nil }

func (p *Plugin) run(ctx context.Context) {
	if ctx == nil {
		return
	}
	p.job.SetRunning()

	// 1. the database dump (host-provided pg_dump), if wired.
	if deps.DumpDB != nil {
		if err := p.writeEntry(ctx, "database.sql", deps.DumpDB); err != nil {
			p.core.Errors.Report(ctx, "backups/db", err)
		}
	}

	// 2. every plugin's Backupable hook, discovered off the extension registry.
	hooks := pluginapi.Backups(p.core)
	for _, b := range hooks {
		if ctx.Err() != nil {
			return
		}
		if err := p.writeEntry(ctx, b.BackupName()+".bak", b.Backup); err != nil {
			p.core.Errors.Report(ctx, "backups/plugin", fmt.Errorf("%s: %w", b.BackupName(), err))
		}
	}

	p.job.Log("backup complete: db + %d plugin hooks", len(hooks))
	p.job.SetIdle(time.Now().Add(7 * 24 * time.Hour))
}

func (p *Plugin) writeEntry(ctx context.Context, name string, write func(context.Context, io.Writer) error) error {
	wc, err := deps.OpenEntry(ctx, name)
	if err != nil {
		return err
	}
	defer wc.Close()
	return write(ctx, wc)
}
