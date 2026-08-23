package donations

// donationsCSS is what this plugin's fragments used to carry inline.
//
// A fragment cannot reach the document head, so its CSS travelled inside the
// markup -- re-sent in every view that used it, and inline, which is what stops
// the host dropping style-src 'unsafe-inline'. Registered once at Provision
// now; the host gives it a URL, a content hash and a year of caching.
// See docs/BACKLOG.md #13 in loon-demo-site.
//
// Rules are unchanged and in template order. RegisterStylesheet no-ops on a
// host with no sink, where these pages draw unstyled -- visible rather than
// silent, which is the right failure for a missing seam.
const donationsCSS = `/* from admin_donate.html */
        .preview-table { width:100%; font-size:0.84rem; }
        .preview-table th, .preview-table td { padding:3px 10px; text-align:right; font-variant-numeric:tabular-nums; }
        .preview-table th { color:var(--text-muted); border-bottom:1px solid var(--border); }
        .preview-table td.dollar { text-align:left; color:var(--text-muted); }
        .donate-section { scroll-margin-top: 70px; }
        .section-anchor { padding-top:1rem; margin-top:1.5rem; border-top:2px solid var(--border); }
        .section-anchor:first-of-type { border-top:none; padding-top:0; }

/* from help_donate.html */
        /* ─────────── Hero ─────────── */
        .donate-hero {
            position: relative;
            background:
                radial-gradient(ellipse at 80% 50%, rgba(91,138,245,0.25) 0%, transparent 60%),
                linear-gradient(135deg, #0d1b3d 0%, #1a2a5e 40%, #2d1b4e 100%);
            border: 1px solid rgba(91,138,245,0.3);
            border-radius: 12px;
            padding: 2rem 2.5rem;
            overflow: hidden;
            min-height: 240px;
        }
        .donate-hero .hero-eyebrow {
            font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.08em;
            color: #93b4f5; font-weight: 600; margin-bottom: 0.5rem;
        }
        .donate-hero h1 { font-size: 1.85rem; font-weight: 700; color: #fff; margin-bottom: 0.75rem; }
        .donate-hero p { color: rgba(255,255,255,0.78); font-size: 0.92rem; line-height: 1.5; max-width: 460px; margin-bottom: 1.25rem; }
        .donate-hero-actions { display: flex; gap: 0.5rem; flex-wrap: wrap; }
        .donate-hero-actions .btn { font-size: 0.85rem; padding: 0.45rem 1rem; }
        /* The host's token, not a hardcoded blue. #5b8af5 was 3.28:1 under
           white — below AA for the label on it — and being a literal it did
           not follow the theme either, so the one button on this page was
           the same colour in all three. */
        .donate-hero .btn-primary { background: var(--primary-strong, #1877cc); border: 0; }
        .donate-hero .btn-primary:hover { background: #6f9bff; }
        .donate-hero .btn-outline { background: transparent; border: 1px solid rgba(255,255,255,0.25); color: #fff; }
        .donate-hero .btn-outline:hover { background: rgba(255,255,255,0.1); }
        .donate-hero-art {
            position: absolute; right: 0; top: 0; bottom: 0; width: 50%;
            background-image: url("/static/img/donate/hero-rain.png");
            background-size: cover; background-position: center right;
            opacity: 0.95;
        }
        @media (max-width: 768px) {
            .donate-hero-art { display: none; }
            .donate-hero p { max-width: none; }
        }

        /* ─────────── Goal cards (Monthly / Yearly) ─────────── */
        .goal-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 10px;
            padding: 1.25rem;
        }
        /* Annual-total headline card — sits under the Monthly/Yearly
           pair and gives a single bottom-line "what it costs to run
           ameNZB per year" number. Subtle gradient so it reads as a
           summary, not a third goal. */
        .annual-total-card {
            background: linear-gradient(90deg, rgba(91,138,245,0.10) 0%, rgba(167,139,250,0.10) 100%);
            border: 1px solid rgba(91,138,245,0.30);
            border-radius: 10px;
            padding: 0.8rem 1.1rem;
        }
        .annual-total-row { display: flex; justify-content: space-between; align-items: baseline; gap: 1rem; flex-wrap: wrap; }
        .annual-total-label { font-size: 0.78rem; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; font-weight: 600; }
        .annual-total-amount { font-size: 1.3rem; font-weight: 700; color: #b3c9ff; font-variant-numeric: tabular-nums; }
        .annual-total-amount .annual-total-per { font-size: 0.78rem; color: var(--text-muted); font-weight: 500; margin-left: 0.25rem; }
        .annual-total-breakdown { font-size: 0.72rem; color: var(--text-muted); margin-top: 0.15rem; font-variant-numeric: tabular-nums; }
        .goal-card .goal-head { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 0.5rem; }
        .goal-card .goal-title { font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--text-muted); font-weight: 600; }
        .goal-card .goal-title i { color: #5b8af5; margin-right: 4px; }
        .goal-card .goal-pct { font-size: 0.82rem; color: var(--text-muted); font-weight: 600; }
        .goal-card .goal-amounts { font-size: 1.55rem; font-weight: 700; color: #fff; font-variant-numeric: tabular-nums; }
        .goal-card .goal-amounts .goal-of { color: var(--text-muted); font-weight: 500; font-size: 1rem; }
        .goal-card .therm { height: 8px; border-radius: 4px; background: rgba(255,255,255,0.05); overflow: hidden; margin: 0.75rem 0 1rem; }
        .goal-card .therm-bar {
            height: 100%; border-radius: 4px;
            background: linear-gradient(90deg, #5b8af5, #b18cf2);
            transition: width 0.4s ease;
        }
        .goal-card .therm-bar.yearly { background: linear-gradient(90deg, #b18cf2, #f59ec0); }
        .goal-card .therm-bar.full { background: #22c55e; }
        .goal-card .goal-foot { display: flex; justify-content: space-between; align-items: center; font-size: 0.75rem; color: var(--text-muted); }
        .goal-card .goal-tags { display: flex; gap: 0.6rem; flex-wrap: wrap; }
        .goal-card .goal-tags .dot { width: 4px; height: 4px; border-radius: 50%; background: var(--text-muted); display: inline-block; margin-right: 6px; vertical-align: middle; }

        /* ─────────── Donation packages (limited stock) ─────────── */
        .pkg-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 10px;
            padding: 1rem 1.1rem;
            display: flex;
            flex-direction: column;
            height: 100%;
            transition: transform 0.15s ease, border-color 0.15s ease;
        }
        .pkg-card:hover { transform: translateY(-2px); border-color: rgba(91,138,245,0.5); }
        .pkg-head { display: flex; justify-content: space-between; align-items: baseline; gap: 0.6rem; margin-bottom: 0.4rem; }
        .pkg-label { font-size: 0.95rem; font-weight: 600; color: var(--text-primary); }
        .pkg-amount { font-size: 1.2rem; font-weight: 700; color: #5b8af5; font-variant-numeric: tabular-nums; flex-shrink: 0; }
        .pkg-desc { font-size: 0.78rem; color: var(--text-muted); margin-bottom: 0.5rem; line-height: 1.4; }
        .pkg-reward { font-size: 0.78rem; color: #b3c9ff; margin-bottom: 0.7rem; }
        .pkg-reward .reward-emoji { margin-right: 0.25rem; }
        .pkg-stock { margin-top: auto; margin-bottom: 0.7rem; }
        .pkg-stock-row { display: flex; justify-content: space-between; font-size: 0.74rem; color: var(--text-muted); margin-bottom: 0.3rem; font-variant-numeric: tabular-nums; }
        .pkg-stock-row strong { color: var(--text-primary); }
        .pkg-pct { color: #5b8af5; font-weight: 600; }
        .pkg-bar { width: 100%; height: 6px; background: rgba(255,255,255,0.06); border-radius: 4px; overflow: hidden; }
        .pkg-bar-fill { height: 100%; background: linear-gradient(90deg, #5b8af5, #a78bfa); transition: width 0.4s ease; }
        .pkg-form { margin: 0; }
        .pkg-claim { width: 100%; }
        .pkg-card-funded {
            background: linear-gradient(90deg, rgba(34,197,94,0.10) 0%, var(--bg-surface) 60%);
            border-color: rgba(34,197,94,0.40);
            opacity: 0.85;
        }
        .pkg-card-funded .pkg-amount { color: #4ade80; }
        .pkg-funded-badge { font-size: 0.78rem; color: #4ade80; font-weight: 600; margin-top: 0.3rem; }
        .pkg-funded-head { font-size: 0.78rem; color: #4ade80; text-transform: uppercase; letter-spacing: 0.05em; font-weight: 600; margin: 1.2rem 0 0.6rem; }

        /* ─────────── Cost cards (Where Your Donations Go) ─────────── */
        .section-head { display: flex; justify-content: space-between; align-items: baseline; margin: 2rem 0 0.25rem; }
        .section-head h2 { font-size: 1.15rem; font-weight: 600; margin: 0; }
        .section-head .section-meta { font-size: 0.72rem; color: var(--text-muted); }
        .section-sub { color: var(--text-muted); font-size: 0.85rem; margin: 0 0 1rem; }

        .cost-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 10px;
            padding: 1rem 1.1rem;
            height: 100%;
            /* Column flex so .cost-amount can use margin-top:auto to
               pin itself at the bottom of every card regardless of how
               many lines the description above runs. Cards in the same
               row therefore line their dollar amounts up cleanly even
               when one card's description is 1 line and another's is 3. */
            display: flex;
            flex-direction: column;
            transition: transform 0.15s ease, border-color 0.15s ease;
        }
        .cost-card:hover { transform: translateY(-2px); border-color: rgba(91,138,245,0.5); }
        .cost-card .cost-icon {
            width: 36px; height: 36px; border-radius: 8px;
            display: inline-flex; align-items: center; justify-content: center;
            background: rgba(91,138,245,0.15); color: #5b8af5;
            font-size: 1.1rem; margin-bottom: 0.6rem;
        }
        .cost-card .cost-title { font-size: 0.92rem; font-weight: 600; color: var(--text-primary); }
        .cost-card .cost-desc { font-size: 0.74rem; color: var(--text-muted); margin: 0.2rem 0 0.6rem; line-height: 1.35; }
        .cost-card .cost-amount {
            font-size: 1.05rem; font-weight: 700; color: #5b8af5;
            font-variant-numeric: tabular-nums;
            /* auto-margin-top is what does the bottom-pinning. Pair
               with the column flex on .cost-card above. */
            margin-top: auto;
            padding-top: 0.6rem;
        }
        .cost-card .cost-amount .cost-period { font-size: 0.72rem; color: var(--text-muted); font-weight: 500; }

        /* ─────────── Perks grid ─────────── */
        .perk-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 8px;
            padding: 0.85rem 1rem;
            height: 100%;
            display: flex; gap: 0.65rem; align-items: flex-start;
        }
        .perk-card .perk-icon {
            width: 30px; height: 30px; border-radius: 6px; flex-shrink: 0;
            display: inline-flex; align-items: center; justify-content: center;
            font-size: 1rem;
        }
        .perk-card .perk-body { min-width: 0; }
        .perk-card .perk-title { font-size: 0.82rem; font-weight: 600; color: var(--text-primary); }
        .perk-card .perk-desc { font-size: 0.7rem; color: var(--text-muted); line-height: 1.4; margin-top: 2px; }
        .perk-immunity   .perk-icon { background: rgba(91,138,245,0.15); color: #5b8af5; }
        .perk-avatar     .perk-icon { background: rgba(167,139,250,0.18); color: #a78bfa; }
        .perk-border     .perk-icon { background: rgba(245,158,11,0.15); color: #f59e0b; }
        .perk-effects    .perk-icon { background: rgba(236,72,153,0.15); color: #ec4899; }
        .perk-badge      .perk-icon { background: rgba(251,191,36,0.18); color: #fbbf24; }
        .perk-perm       .perk-icon { background: rgba(167,139,250,0.18); color: #a78bfa; }
        .perk-points     .perk-icon { background: rgba(251,191,36,0.18); color: #fbbf24; }
        .perk-extra      .perk-icon { background: rgba(167,139,250,0.18); color: #a78bfa; }

        .perks-note {
            background: rgba(91,138,245,0.10); border: 1px solid rgba(91,138,245,0.25);
            border-radius: 8px; padding: 0.75rem 1rem;
            color: #93b4f5; font-size: 0.78rem; display: flex; align-items: center; gap: 0.6rem;
            margin-top: 1rem;
        }

        /* ─────────── Tier cards ─────────── */
        .tier-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 10px;
            padding: 0.9rem 1.1rem;
            margin-bottom: 0.6rem;
            display: flex; align-items: center; gap: 0.8rem;
            position: relative; overflow: hidden;
        }
        .tier-card .tier-icon { width: 32px; flex-shrink: 0; text-align: center; font-size: 1.4rem; }
        .tier-card .tier-body { flex: 1; min-width: 0; }
        .tier-card .tier-name { font-size: 0.95rem; font-weight: 600; color: var(--text-primary); }
        .tier-card .tier-perks { font-size: 0.7rem; color: var(--text-muted); margin-top: 1px; }
        .tier-card .tier-price { font-size: 0.85rem; font-weight: 600; color: var(--text-primary); font-variant-numeric: tabular-nums; flex-shrink: 0; }
        .tier-card .tier-price .tier-per { color: var(--text-muted); font-weight: 500; font-size: 0.75rem; }
        /* One rule, not five. The ladder is configurable now, so there is
           no fixed set of tier names to key gradients off — and five
           gradients named for five specific tiers was the same hardcoding
           in another form. */

        /* ─────────── Community Goals (stretch) ─────────── */
        .stretch-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 10px;
            padding: 1.1rem;
            display: flex; gap: 1rem; align-items: flex-start;
        }
        .stretch-ring {
            position: relative; width: 70px; height: 70px; flex-shrink: 0;
        }
        .stretch-ring svg { transform: rotate(-90deg); width: 70px; height: 70px; }
        .stretch-ring .ring-track { stroke: rgba(255,255,255,0.08); }
        .stretch-ring .ring-fill { stroke: url(#stretchGrad); transition: stroke-dashoffset 0.4s ease; }
        .stretch-ring .ring-label {
            position: absolute; inset: 0; display: flex; align-items: center; justify-content: center;
            font-size: 0.82rem; font-weight: 700; color: var(--text-primary);
        }
        .stretch-body { flex: 1; min-width: 0; }
        .stretch-eyebrow { font-size: 0.7rem; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; }
        .stretch-title { font-size: 0.95rem; font-weight: 600; color: var(--text-primary); margin-top: 2px; }
        .stretch-desc { font-size: 0.74rem; color: var(--text-muted); line-height: 1.4; margin-top: 4px; }
        .stretch-raised { font-size: 0.82rem; color: var(--text-muted); margin-top: 0.8rem; font-variant-numeric: tabular-nums; }
        .stretch-raised strong { color: var(--text-primary); font-weight: 600; }

        .stretch-cta {
            background: rgba(91,138,245,0.10); border: 1px solid rgba(91,138,245,0.25);
            border-radius: 8px; padding: 0.7rem 1rem; color: #93b4f5;
            font-size: 0.82rem; text-align: center; margin-top: 0.6rem;
        }

        /* ─────────── Top supporters / referral / recent donors ─────────── */
        .panel {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 10px;
            padding: 1.1rem;
            /* Fill the parent column so panels in the same Bootstrap row
               end at the same Y coordinate. Bootstrap rows already make
               column heights equal — without h-100 the panel only
               extends as far as its content needs and the column shows
               empty space below it. */
            height: 100%;
            display: flex;
            flex-direction: column;
        }
        .panel h3 { font-size: 0.95rem; font-weight: 600; margin: 0 0 0.8rem; display: flex; justify-content: space-between; align-items: baseline; }
        .panel h3 .view-all { font-size: 0.78rem; font-weight: 500; color: #5b8af5; }
        .panel h3 .view-all:hover { text-decoration: underline; }

        .top-row {
            display: flex; align-items: center; gap: 0.7rem;
            padding: 0.45rem 0; border-bottom: 1px solid rgba(255,255,255,0.04);
            font-size: 0.85rem;
        }
        .top-row:last-child { border-bottom: 0; }
        .top-rank {
            width: 22px; height: 22px; border-radius: 5px; flex-shrink: 0;
            display: inline-flex; align-items: center; justify-content: center;
            background: rgba(255,255,255,0.06); color: var(--text-muted);
            font-size: 0.72rem; font-weight: 700;
        }
        .top-row.rank-1 .top-rank { background: linear-gradient(135deg, #f59e0b, #fbbf24); color: #1f1500; }
        .top-row.rank-2 .top-rank { background: linear-gradient(135deg, #94a3b8, #cbd5e1); color: #1a1f2b; }
        /* Bronze took dark ink and a lighter ramp on 22 Aug 2026. It was the
           only medal of the three wearing WHITE, and white on #d97706 is
           3.19:1 -- gold and silver above both use dark ink and clear 8.39 and
           6.42. The dark stop moved #b45309 -> #c6651b so the dark ink clears
           there too (4.53); it is still browner than the gold above it. */
        .top-row.rank-3 .top-rank { background: linear-gradient(135deg, #c6651b, #d97706); color: #1f1500; }
        .top-avatar {
            width: 26px; height: 26px; border-radius: 50%; flex-shrink: 0;
            background: var(--bg-elevated); object-fit: cover;
        }
        .top-name { flex: 1; color: var(--text-primary); font-weight: 500; min-width: 0;
                    overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
        .top-amount { color: var(--text-muted); font-variant-numeric: tabular-nums; flex-shrink: 0; }

        .referral-code-card {
            background: linear-gradient(135deg, rgba(91,138,245,0.15) 0%, rgba(167,139,250,0.12) 100%);
            border: 1px solid rgba(91,138,245,0.3);
            border-radius: 8px;
            padding: 0.8rem 1rem;
            margin-bottom: 0.9rem;
        }
        .referral-code-card .ref-label { font-size: 0.7rem; color: var(--text-muted); }
        .referral-code-card .ref-code {
            font-family: ui-monospace, "SF Mono", monospace;
            font-size: 1.4rem; font-weight: 700; color: #93b4f5;
            letter-spacing: 0.05em; margin: 2px 0;
            display: flex; align-items: center; gap: 0.4rem;
        }
        .referral-code-card .ref-code button {
            background: rgba(255,255,255,0.08); border: 0; border-radius: 4px;
            color: var(--text-muted); padding: 0 6px; font-size: 0.8rem; cursor: pointer;
        }
        .referral-code-card .ref-code button:hover { color: var(--text-primary); }

        .referral-stats { display: flex; gap: 0.8rem; margin: 0.6rem 0; padding-top: 0.6rem;
                          border-top: 1px solid rgba(255,255,255,0.04); }
        .referral-stats .stat { flex: 1; text-align: center; }
        .referral-stats .stat-label { font-size: 0.7rem; color: var(--text-muted); }
        .referral-stats .stat-value { font-size: 1.1rem; font-weight: 700; color: var(--text-primary); margin-top: 2px; }

        .referral-rewards { font-size: 0.78rem; }
        .referral-rewards .reward-row {
            display: flex; justify-content: space-between; padding: 4px 0;
            border-bottom: 1px solid rgba(255,255,255,0.04);
        }
        .referral-rewards .reward-row:last-child { border-bottom: 0; }
        .referral-rewards .reward-amount { color: #fbbf24; font-weight: 600; }
        .referral-cta {
            display: block; text-align: center; padding: 0.55rem 0; margin-top: 0.9rem;
            background: rgba(91,138,245,0.12); border: 1px solid rgba(91,138,245,0.25);
            border-radius: 6px; color: #93b4f5; font-size: 0.82rem; font-weight: 600;
            text-decoration: none;
        }
        .referral-cta:hover { background: rgba(91,138,245,0.18); color: #b3c9ff; }
        .referral-partners { margin-top: 0.9rem; padding-top: 0.7rem; border-top: 1px solid var(--border); }
        .ref-partners-label { font-size: 0.7rem; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.04em; margin-bottom: 0.4rem; }
        .partner-row {
            display: flex; align-items: center; gap: 0.5rem;
            padding: 5px 6px; border-radius: 6px; text-decoration: none;
            color: var(--text-primary); font-size: 0.78rem;
            transition: background 0.15s ease;
        }
        .partner-row:hover { background: rgba(91,138,245,0.12); color: #b3c9ff; }
        .partner-row .partner-icon { width: 18px; text-align: center; flex-shrink: 0; }
        .partner-row .partner-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
        .partner-row .partner-arrow { color: var(--text-muted); flex-shrink: 0; }

        /* ─────────── Recent donors carousel ─────────── */
        .recent-donor {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 8px;
            padding: 0.75rem 0.8rem;
            text-align: center;
            min-width: 100px;
            flex: 1 1 110px;
        }
        .recent-donor .rd-avatar {
            width: 38px; height: 38px; border-radius: 50%;
            background: var(--bg-elevated); object-fit: cover; margin: 0 auto 0.4rem;
            display: block;
            border: 2px solid rgba(91,138,245,0.3);
        }
        .recent-donor .rd-name { font-size: 0.78rem; font-weight: 600; color: var(--text-primary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
        .recent-donor .rd-amt { font-size: 0.72rem; color: var(--text-muted); font-variant-numeric: tabular-nums; margin-top: 1px; }
        .recent-donor .rd-when { font-size: 0.65rem; color: var(--text-muted); opacity: 0.7; margin-top: 1px; }

        /* ─────────── Outro banner ─────────── */
        .outro-banner {
            background: linear-gradient(90deg, rgba(91,138,245,0.18) 0%, rgba(167,139,250,0.14) 100%);
            border: 1px solid rgba(91,138,245,0.3);
            border-radius: 10px;
            padding: 1.1rem 1.5rem;
            display: flex; gap: 1rem; align-items: center;
            margin-top: 1.5rem;
        }
        .outro-banner .outro-icon {
            width: 40px; height: 40px; border-radius: 50%;
            background: rgba(91,138,245,0.2); color: #93b4f5;
            display: inline-flex; align-items: center; justify-content: center;
            font-size: 1.2rem; flex-shrink: 0;
        }
        .outro-banner .outro-title { font-size: 0.92rem; font-weight: 600; color: var(--text-primary); }
        .outro-banner .outro-text { font-size: 0.82rem; color: var(--text-muted); margin-top: 2px; }
        .outro-banner .outro-art {
            width: 90px; height: 90px; flex-shrink: 0;
            background-image: url("/static/img/donate/mascot-thumb.png");
            background-size: contain; background-repeat: no-repeat; background-position: center right;
            margin-left: auto;
        }
        @media (max-width: 600px) { .outro-banner .outro-art { display: none; } }

`
