package usenet

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Host adoption: carry a legacy host crawler's state into the plugin, so
// flipping a production site to sink=host resumes EXACTLY where the old
// crawler stopped instead of starting over.
//
// What carries, and why it matters:
//
//   - newsgroups (name + active flag): the operator's curation survives.
//   - watermarks + backfill_done, per group: without these the first pass
//     would treat every group as brand new — re-fetch the recent window,
//     and worse, re-run a YEARS-long backfill the old crawler already
//     finished. backfill_done is the single most expensive bit to lose.
//   - the operator blacklist: editorial policy, silently dropping releases;
//     losing it would let previously-excluded content flood in unnoticed.
//
// Junk rules self-seed from the TSV, staging starts empty on purpose (the
// resumed watermarks mean nothing is skipped), and dedup needs no carry at
// all: content_hash uses prod's exact scheme, so a release the old crawler
// stored dedups cleanly if the plugin ever re-assembles it.
//
// This is a Go bootstrap rather than a SQL migration because the state must
// be keyed by the BACKBONE — a runtime identity derived from the configured
// primary server, which does not exist when migrations run. It follows the
// same pattern as seedServer/seedJunkRules: called from Start, idempotent,
// and marked done in the settings table so it runs exactly once.
//
// The legacy tables are referenced as public.* with a to_regclass guard: on
// a standalone install (the demo) they simply do not exist and adoption
// marks itself done without touching anything.

const adoptedSettingKey = "host_adopted"

// adoptHostState runs the one-time carry. Only meaningful in host-sink mode —
// internal mode has nothing to adopt from — and safe to call every boot.
func (p *Plugin) adoptHostState(ctx context.Context) {
	if p.cfg.Sink != SinkHost {
		return
	}
	settings, err := p.st.getSettings(ctx)
	if err != nil {
		p.reportErr(ctx, "usenet/adopt-settings", err)
		return
	}
	if settings[adoptedSettingKey] == "1" {
		return
	}

	// The backbone key everything is filed under: the primary (first enabled)
	// server's. Prod's legacy watermarks ARE that provider's article numbers,
	// so this is not a guess — it is the only correct owner. No server yet
	// means we cannot know the key; retry next boot rather than mis-file
	// state under a placeholder.
	servers, err := p.st.listServers(ctx)
	if err != nil {
		p.reportErr(ctx, "usenet/adopt-servers", err)
		return
	}
	var backbone string
	for _, s := range servers {
		if s.Enabled {
			backbone = s.backboneKey()
			break
		}
	}
	if backbone == "" {
		p.core.Logger.Info("usenet: host adoption waiting — no enabled server yet (add one in the wizard)")
		return
	}

	groups, state, blacklist, covRanges, hostFound, err := p.st.adoptFromHost(ctx, backbone)
	if err != nil {
		p.reportErr(ctx, "usenet/adopt", err)
		return // retry next boot; nothing is marked done
	}
	if err := p.st.setSetting(ctx, adoptedSettingKey, "1"); err != nil {
		p.reportErr(ctx, "usenet/adopt-mark", err)
		return
	}
	if !hostFound {
		p.core.Logger.Info("usenet: no legacy host tables — fresh install, nothing to adopt")
		return
	}
	p.core.Logger.Info(fmt.Sprintf(
		"usenet: adopted host crawler state under backbone %q: %d group(s), %d watermark row(s), %d blacklist rule(s), %d coverage range(s)",
		backbone, groups, state, blacklist, covRanges))
}

// adoptFromHost copies the legacy host tables (public.newsgroups,
// public.blacklist_regexes) into the plugin's schema. Returns rows copied and
// whether the host tables existed at all. Every statement is a no-op on
// conflict, so a partially-failed adoption is safe to re-run.
func (s *PGStore) adoptFromHost(ctx context.Context, backbone string) (groups, state, blacklist, covRanges int64, hostFound bool, err error) {
	err = s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var reg *string
		if err := tx.GetContext(ctx, &reg, `SELECT to_regclass('public.newsgroups')::text`); err != nil {
			return err
		}
		if reg == nil {
			return nil // standalone install: no host to adopt from
		}
		hostFound = true

		// Groups: name, the operator's active curation, AND the per-group
		// tuning they configured on the legacy crawler-settings page —
		// retention override, throttle, priority tier, manual order. DO
		// NOTHING on conflict so plugin-side edits made before adoption are
		// never clobbered.
		// retention_days translates rather than copies: the legacy schema
		// requires a depth on every group, the plugin treats NULL as "inherit
		// the global". Carrying the default-valued rows as overrides would pin
		// every group to today's depth forever (raising the global would
		// silently change nothing) — so the plugin-default depth becomes
		// inherit and only genuinely custom values survive as overrides.
		res, err := tx.ExecContext(ctx, `
			INSERT INTO newsgroups (name, active, retention_days, throttle_ms, tier, sort_order)
			SELECT h.name, h.active, NULLIF(h.retention_days, 6431),
			       COALESCE(h.throttle_ms, 0),
			       CASE WHEN COALESCE(h.low_priority, FALSE) THEN 'low' ELSE 'normal' END,
			       COALESCE(h.sort_order, 0)
			  FROM public.newsgroups h
			ON CONFLICT (name) DO NOTHING`)
		if err != nil {
			return fmt.Errorf("adopt groups: %w", err)
		}
		groups, _ = res.RowsAffected()

		// Watermarks, filed under the primary backbone. Only active groups:
		// an inactive group's stale numbers are not worth carrying, and the
		// group row above preserves the inactive flag if the operator ever
		// re-enables it (a fresh crawl then re-establishes bounds).
		//
		// backfill_done is DERIVED: the legacy schema has no such column —
		// its convention is "back watermark at or below server_low means the
		// walk reached the beginning". Getting this wrong in either
		// direction is expensive: false means re-running a finished
		// multi-year backfill against a paying provider, true means never
		// fetching history the old crawler still owed.
		res, err = tx.ExecContext(ctx, `
			INSERT INTO newsgroup_state
			      (backbone, group_name, high_watermark, high_watermark_date,
			       back_watermark, back_watermark_date, server_low, server_high,
			       backfill_done)
			SELECT $1, h.name, h.high_watermark, h.high_watermark_date,
			       h.back_watermark, h.back_watermark_date, h.server_low, h.server_high,
			       (h.back_watermark IS NOT NULL AND h.back_watermark <= h.server_low)
			  FROM public.newsgroups h
			 WHERE h.active = TRUE
			ON CONFLICT (backbone, group_name) DO NOTHING`, backbone)
		if err != nil {
			return fmt.Errorf("adopt state: %w", err)
		}
		state, _ = res.RowsAffected()

		// Seed the coverage map to match the watermarks just imported.
		//
		// newsgroup_ranges records what has been FETCHED. Left empty for an
		// adopted group, the coverage strip renders ~0% for a newsgroup whose
		// history the legacy crawler really did index — prod's
		// alt.binaries.multimedia.anime.highspeed showed 1.05% while holding
		// 218k releases back to 2016. The two bars on the admin page then
		// disagree, and the honest-LOOKING one is the one that is wrong.
		//
		// The span is what the imported watermarks already claim: the back
		// watermark (floored at server_low — nothing below it is fetchable)
		// up to the forward mark. That is inherited from the old crawler
		// exactly like backfill_done above, and wrong in the same direction
		// if the old crawler lied. Still better than asserting nothing was
		// ever fetched, which is wrong for every correctly-adopted group.
		res, err = tx.ExecContext(ctx, `
			INSERT INTO newsgroup_ranges (backbone, group_name, range_start, range_end)
			SELECT $1, s.group_name,
			       GREATEST(COALESCE(s.back_watermark, s.server_low), s.server_low),
			       s.high_watermark
			  FROM newsgroup_state s
			 WHERE s.backbone = $1
			   AND s.high_watermark > 0
			   AND s.high_watermark > GREATEST(COALESCE(s.back_watermark, s.server_low), s.server_low)
			   AND NOT EXISTS (
			       SELECT 1 FROM newsgroup_ranges r
			        WHERE r.backbone = s.backbone AND r.group_name = s.group_name)`, backbone)
		if err != nil {
			return fmt.Errorf("adopt coverage: %w", err)
		}
		covRanges, _ = res.RowsAffected()

		// The operator blacklist, deduped by (pattern, field) since the
		// plugin table has no natural key — adoption must not double rules
		// on a re-run after a partial failure.
		var blReg *string
		if err := tx.GetContext(ctx, &blReg, `SELECT to_regclass('public.blacklist_regexes')::text`); err != nil {
			return err
		}
		if blReg != nil {
			res, err = tx.ExecContext(ctx, `
				INSERT INTO blacklist_regexes (pattern, field, enabled)
				SELECT h.pattern, h.field, h.enabled FROM public.blacklist_regexes h
				 WHERE NOT EXISTS (
				       SELECT 1 FROM blacklist_regexes b
				        WHERE b.pattern = h.pattern AND b.field = h.field)`)
			if err != nil {
				return fmt.Errorf("adopt blacklist: %w", err)
			}
			blacklist, _ = res.RowsAffected()
		}
		return nil
	})
	return groups, state, blacklist, covRanges, hostFound, err
}
