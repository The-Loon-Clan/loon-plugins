package offers

// offersCSS is what this plugin's fragments used to carry inline.
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
/* Four accent literals lifted on 23 Aug 2026. Each was under 4.5:1 on the
   lightest raised ground the site has (nord --surface-3, #3d4658), and each
   became VISIBLE that day: the host aliased --bg-surface, which these cards
   had been asking for and not getting, so text that used to inherit an
   unknown ground suddenly had a defined one. The colours were always this
   dark; nothing had ever measured them.
     #5b8af5 -> #85b4ff   #ec4899 -> #ff8bdc
     #a78bfa -> #c0a4ff   #f87171 -> #ff9595
   Found by loon-demo-site scripts/audit_paint.py. */
const offersCSS = `/* from admin_offers.html */
        .ao-grid {
            display: grid;
            grid-template-columns: 2fr 1fr;
            gap: 1.25rem;
        }
        @media (max-width: 1000px) { .ao-grid { grid-template-columns: 1fr; } }
        .ao-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 12px;
            padding: 1rem 1.2rem;
            margin-bottom: 1.1rem;
        }
        .ao-card h2 {
            font-size: 0.92rem;
            font-weight: 700;
            margin: 0 0 0.65rem 0;
            color: var(--text-primary);
            text-transform: uppercase;
            letter-spacing: 0.05em;
        }
        .ao-status-pill {
            display: inline-block;
            font-size: 0.7rem;
            padding: 2px 9px;
            border-radius: 999px;
            margin-right: 0.35rem;
            font-weight: 600;
            text-transform: uppercase;
            letter-spacing: 0.04em;
        }
        .ao-open       { background: rgba(96,165,250,0.18); color:#60a5fa; }
        .ao-claimed    { background: rgba(234,179,8,0.18);  color:#facc15; }
        .ao-delivered  { background: rgba(74,222,128,0.18); color:#4ade80; }
        .ao-failed     { background: rgba(244,114,182,0.18); color:#f472b6; }
        .ao-cancelled  { background: rgba(148,163,184,0.18); color:#cbd5e1; }
        .ao-stuck      { color:#ff9595; font-weight:600; }
        table.ao-table { width: 100%; font-size: 0.82rem; }
        table.ao-table th { font-size: 0.7rem; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.04em; padding-bottom: 0.4rem; }
        table.ao-table td { padding: 0.45rem 0.45rem 0.45rem 0; border-top: 1px solid var(--border); vertical-align: top; }
        .of-pill {
            display: inline-block;
            font-size: 0.66rem;
            padding: 2px 7px;
            border-radius: 999px;
            margin-right: 0.25rem;
        }
        .of-pill-priv { background: rgba(96,165,250,0.15); color:#60a5fa; }
        .of-pill-pub  { background: rgba(74,222,128,0.15); color:#4ade80; }
        .of-pill-per  { background: rgba(168,85,247,0.15); color:#c084fc; }

/* from admin_trackers.html */
        .vis-pill { font-size:0.65rem; padding: 2px 8px; border-radius: 999px; }
        .vis-private  { background: rgba(96,165,250,0.15); color:#60a5fa; }
        .vis-public   { background: rgba(74,222,128,0.15); color:#4ade80; }
        .vis-personal { background: rgba(168,85,247,0.15); color:#c084fc; }
        .st-unvetted { background: rgba(234,179,8,0.18);  color:#facc15; }
        .st-active   { background: rgba(74,222,128,0.18); color:#4ade80; }
        .st-banned   { background: rgba(244,114,182,0.18); color:#f472b6; }

/* from offer_detail.html */
        .od-page   { max-width: 1100px; margin: 0 auto; padding: 1.5rem 1rem 4rem; }
        .od-head   { display: flex; gap: 1.25rem; align-items: flex-start; margin-bottom: 1.5rem; }
        .od-cover  { flex-shrink: 0; width: 140px; border-radius: 8px; overflow: hidden;
                     background: var(--bg-elevated); }
        .od-cover img { width: 100%; display: block; }
        .od-title  { font-size: 1.4rem; font-weight: 700; color: var(--text-primary); margin: 0 0 .35rem; }
        .od-ident  { color: var(--text-muted); font-size: 0.8rem; margin-bottom: .6rem; }
        .od-pills  { display: flex; flex-wrap: wrap; gap: .4rem; margin-bottom: .8rem; }
        .od-pill   { display: inline-block; padding: .12rem .5rem; border-radius: 999px;
                     font-size: .7rem; background: var(--bg-elevated); color: var(--text-muted);
                     border: 1px solid var(--border); }
        .od-card   { background: var(--bg-surface); border: 1px solid var(--border);
                     border-radius: 12px; padding: 1rem 1.15rem; margin-bottom: 1rem; }
        .od-card h2 { font-size: .95rem; margin: 0 0 .75rem; color: var(--text-primary); }
        .od-file   { border-top: 1px solid var(--border); padding: .7rem 0; }
        .od-file:first-of-type { border-top: 0; }
        .od-fname  { color: var(--text-primary); font-size: .85rem; word-break: break-all; }
        .od-meta   { color: var(--text-muted); font-size: .76rem; margin-top: .2rem; }
        .od-grid   { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
                     gap: .5rem .9rem; margin-top: .5rem; font-size: .78rem; }
        .od-k      { color: var(--text-muted); }
        .od-v      { color: var(--text-primary); }
        .od-track  { font-size: .76rem; color: var(--text-muted); }
        .od-note   { color: var(--text-muted); font-size: .78rem; }

/* from offers.html */
        .of-page { margin: 0 auto; padding: 1.5rem 1rem 4rem; }
        .of-hint  { color: var(--text-muted); font-size: 0.72rem; margin-top: 0.1rem; }
        .of-pill {
            display: inline-block;
            font-size: 0.66rem;
            padding: 2px 7px;
            border-radius: 999px;
            margin-right: 0.25rem;
        }
        .of-pill-priv { background: rgba(96,165,250,0.15); color:#60a5fa; }
        .of-pill-pub  { background: rgba(74,222,128,0.15); color:#4ade80; }
        .of-pill-per  { background: rgba(168,85,247,0.15); color:#c084fc; }
        /* The disclosure chevron on collapsed show-cards. The native marker
           is hidden (list-style:none on the summary + the webkit rule here)
           so the header reads as a card, not a bullet list. */
        .of-group summary::-webkit-details-marker { display: none; }
        .of-chevron { color: var(--text-muted); font-size: 0.9rem; transition: transform 0.15s ease; }
        .of-group[open] .of-chevron { transform: rotate(90deg); }

`
