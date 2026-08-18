-- A default EARNED ladder, so 'earned' is reachable out of the box.
--
-- 001 through 004 built the machinery and left the shelf empty: the kind is in
-- the CHECK, the criteria are columns, the sweep in promote.go evaluates them —
-- and a fresh install has no earned group at all, so the Rank Promotion job
-- boots, finds nothing automatic, logs "there is nothing to promote to", and
-- that is the entire lifetime of the feature unless an operator hand-builds a
-- ladder through a form that (see CreateGroup) cannot even set the thresholds.
-- This is the ladder.
--
-- WHY THE PLUGIN AND NOT THE HOST. A ladder is CONFIGURATION and configuration
-- has to arrive with the thing it configures. The host cannot ship it: host
-- migrations run BEFORE loon applies plugin migrations, so on a fresh database
-- these tables do not exist yet when the host's numbered series runs — the
-- indexer's own migration 280 hit exactly that and had to derive its data from
-- a host table for the same reason. Seeding after boot in Go instead would put
-- a write behind a code path rather than a recorded, once-only migration.
--
-- WHY THIS IS NOT 001'S MISTAKE. 001 removed a seed and said why: it copied one
-- private site's live catalog, reaching into that host's schema, and baked a
-- fact about a single database into a plugin published for everyone. Nothing
-- here reads another schema, names another site, or claims to be anybody's
-- data. These are DEFAULTS — generic, editable, and the kind of thing every
-- tracker ships — which is the category 001 was careful to distinguish from an
-- adoption import.
--
-- GATED ON AN EMPTY EARNED LADDER, not on an empty catalog. A site that already
-- built its own earned tiers has answered this question and must not be given
-- four more; the demo host, which seeds Newcomer/Regular/Contributor from its
-- own seeder, is exactly that case and is left untouched. A site with only
-- PAID tiers has not answered it, and gets the ladder.
--
-- THE CRITERIA ARE RELEASES AND AGE, TOGETHER. Releases alone would let a
-- day-old account that bulk-imported an archive land on the top rung; age alone
-- rewards waiting, which migration 004 already names as the failure mode of a
-- tracker-less host. min_uploaded and min_ratio stay 0 — not asked — because a
-- host with no tracker reports zero for both and a rung gated on them could
-- never be earned. Qualifies() is conjunctive, so a site that later adds a
-- tracker can raise those on these same rows without rebuilding the ladder.
INSERT INTO groups (slug, name, kind, visible, color, title_color, icon,
                    cost_points, duration_days, sort_order,
                    min_releases, min_age_days)
SELECT v.slug, v.name, 'earned', TRUE, v.color, '', '',
       0,
       -- duration_days is NOT NULL CHECK (>= 1) and has no way to say
       -- "permanent", so it takes the value creating these through the admin
       -- form would store. It is unused for an earned rank: the sweep grants
       -- with a zero duration, which AddMember writes as a NULL expiry.
       30,
       -- BELOW every group already in the catalog, and this is a correctness
       -- requirement rather than a presentation preference.
       --
       -- BadgesForBatch returns a member's badges most-prominent-first by
       -- sort_order, and consumers that show ONE badge take the head. The
       -- Discord bot is one of them: it maps that head slug to a guild role and
       -- then strips every OTHER configured rank role from the member. Seed an
       -- earned rung above the paid tiers and the next five-minute role sync
       -- reads the head as a rank with no discord_role_<slug> setting, resolves
       -- the empty string, and removes the paid role the member is paying for —
       -- silently, from exactly the members most likely to notice. On the site
       -- this was written against, all three paid-rank holders were in the
       -- first promotion batch.
       --
       -- Computed from the live catalog rather than hardcoded so it holds on a
       -- catalog this migration cannot see. An empty catalog yields 0, giving
       -- -40..-10.
       (SELECT COALESCE(MIN(sort_order), 0) FROM groups) + v.sort_offset,
       v.min_releases, v.min_age_days
  FROM (VALUES
          ('uploader',        'Uploader',        'secondary', -40,    5,  30),
          ('power-uploader',  'Power Uploader',  'info',      -30,   50,  60),
          ('elite-uploader',  'Elite Uploader',  'primary',   -20,  500, 120),
          ('master-uploader', 'Master Uploader', 'warning',   -10, 5000, 240)
       ) AS v(slug, name, color, sort_offset, min_releases, min_age_days)
 WHERE NOT EXISTS (SELECT 1 FROM groups WHERE kind = 'earned')
    ON CONFLICT (slug) DO NOTHING;

-- NO group_entitlements rows, deliberately, and it is the second half of the
-- "quiet" property this ladder needs.
--
-- These ranks are RECOGNITION: a badge and a place on the ladder, not a quota
-- change. Attaching download.daily or api.daily here would mean the first sweep
-- silently re-rates a batch of members' limits at once, and every later
-- promotion and demotion would move somebody's quota with no notice to them.
-- An operator who wants a rung to confer something can add it in the admin
-- catalog, deliberately, to one rung at a time.
--
-- color is a Bootstrap CLASS NAME, not a hex string — the badge template renders
-- class="badge bg-{{.Color}}", so a hex value produces no colour at all.
