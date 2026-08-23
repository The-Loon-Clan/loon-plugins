package roadmap

// roadmapCSS is what this plugin's fragments used to carry inline.
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
const roadmapCSS = `/* from flow.html */
        /* The flow graph is a fullscreen pan/zoom canvas, and ON THAT PAGE
           locking body scroll is correct — the page's old inline <style>
           made that scoping automatic by only existing there. This sheet is
           plugin-GLOBAL, and every page links every registered sheet, so the
           bare rule froze scrolling on the entire site the moment a host
           served it (found on ameNZB within the hour: /release-groups and
           /wiki stopped scrolling). :has() re-scopes it to the one page that
           renders the shell; a browser without :has() simply keeps its
           scrollbar, which is the harmless direction to fail. */
        body:has(.flow-shell) { overflow: hidden; }
        .flow-shell { display: flex; flex-direction: column; height: calc(100vh - 56px); }
        .flow-toolbar {
            display: flex; align-items: center; gap: 0.5rem;
            padding: 0.5rem 0.75rem;
            background: var(--bg-elevated);
            border-bottom: 1px solid var(--border);
            font-size: 0.85rem;
        }
        .flow-toolbar .spacer { flex: 1; }
        .flow-toolbar .status {
            font-size: 0.78rem; color: var(--text-muted);
            font-family: monospace;
        }
        .flow-canvas { flex: 1; position: relative; background: var(--bg-base); }
        #cy { position: absolute; inset: 0; }

        .flow-inspector {
            position: absolute; top: 0; right: 0; bottom: 0; width: 320px;
            background: var(--bg-elevated);
            border-left: 1px solid var(--border);
            transform: translateX(100%);
            transition: transform 0.15s ease-out;
            display: flex; flex-direction: column;
            z-index: 10;
        }
        .flow-inspector.open { transform: translateX(0); }
        .flow-inspector-header {
            padding: 0.6rem 0.75rem;
            border-bottom: 1px solid var(--border);
            display: flex; align-items: center; justify-content: space-between;
            font-size: 0.85rem; font-weight: 600;
        }
        .flow-inspector-body { padding: 0.75rem; overflow-y: auto; flex: 1; font-size: 0.85rem; }
        .flow-inspector .form-label { font-size: 0.72rem; color: var(--text-muted); margin-bottom: 2px; text-transform: uppercase; letter-spacing: 0.04em; }
        .flow-inspector .form-control, .flow-inspector .form-select {
            background: var(--bg-base); border-color: var(--border); color: var(--text-primary);
            font-size: 0.85rem;
        }
        .flow-inspector textarea { min-height: 90px; }
        .flow-inspector-footer {
            padding: 0.6rem 0.75rem;
            border-top: 1px solid var(--border);
            display: flex; gap: 0.5rem;
        }
        .flow-locked-badge {
            font-size: 0.65rem; padding: 1px 6px; border-radius: 3px;
            background: rgba(var(--bs-warning-rgb), 0.2);
            color: var(--bs-warning);
        }
        .flow-meta { font-size: 0.72rem; color: var(--text-muted); margin-top: 0.25rem; }

        .flow-comments { margin-top: 0.75rem; border-top: 1px dashed var(--border); padding-top: 0.5rem; }
        .flow-comment { font-size: 0.78rem; margin-bottom: 0.35rem; padding-bottom: 0.35rem; border-bottom: 1px solid var(--border); }
        .flow-comment:last-child { border-bottom: none; }
        .flow-comment-meta { font-size: 0.7rem; color: var(--text-muted); }

        /* Per-mockup canvas overlays. Each mockup node gets a
           position:absolute wrapper that mirrors the cytoscape node's
           bounding box on every viewport tick, with a sandboxed iframe
           inside rendering the user's HTML. The iframe has sandbox=""
           (no allow-tokens) so script + form-submission + plugins are
           neutralised — even malicious HTML can't escape its frame.
           pointer-events:none keeps clicks falling through to the
           underlying cy node so drag / connect / inspector-open keep
           working; the node outline is what the user actually grabs. */
        .flow-mockup-overlay {
            position: absolute;
            pointer-events: none;
            border-radius: 6px;
            overflow: hidden;
            background: #fff;
            box-shadow: 0 1px 6px rgba(0,0,0,0.4);
            z-index: 4;
            transform-origin: top left;
        }
        .flow-mockup-overlay iframe {
            border: 0;
            width: 100%;
            height: 100%;
            pointer-events: none;
            background: #fff;
        }

        .flow-connect-banner {
            position: absolute; top: 0.75rem; left: 50%;
            transform: translateX(-50%);
            background: rgba(91,138,245,0.95);
            color: #fff;
            border-radius: 6px;
            padding: 0.45rem 0.85rem;
            font-size: 0.85rem;
            display: none;
            align-items: center;
            box-shadow: 0 2px 12px rgba(0,0,0,0.3);
            z-index: 8;
        }
        .flow-connect-banner.open { display: inline-flex; }

        /* Port-picker popover. Shown when the user is mid-connect and
           the relevant node has more than one named port. Anchored to
           top-center under the connect banner — small, modal-feeling
           but click-outside-to-cancel. */
        .flow-port-picker {
            position: absolute; top: 3.5rem; left: 50%;
            transform: translateX(-50%);
            background: var(--bg-elevated);
            border: 1px solid var(--border);
            border-radius: 6px;
            padding: 0.5rem;
            min-width: 220px;
            box-shadow: 0 4px 16px rgba(0,0,0,0.35);
            display: none;
            z-index: 9;
        }
        .flow-port-picker.open { display: block; }
        .flow-port-picker h6 {
            font-size: 0.7rem; color: var(--text-muted);
            text-transform: uppercase; letter-spacing: 0.04em;
            margin-bottom: 0.3rem;
        }
        .flow-port-picker .port-row {
            display: flex; align-items: center; gap: 0.4rem;
            padding: 0.3rem 0.45rem; border-radius: 4px;
            cursor: pointer; font-size: 0.82rem;
        }
        .flow-port-picker .port-row:hover {
            background: rgba(91,138,245,0.15);
        }
        .flow-port-picker .port-row .port-name {
            font-weight: 600; color: var(--text-primary);
        }
        .flow-port-picker .port-row .port-hint {
            font-size: 0.72rem; color: var(--text-muted);
            margin-left: auto; max-width: 60%;
            white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
        }

        .flow-help {
            position: absolute; bottom: 0.75rem; left: 0.75rem;
            max-width: 320px;
            background: var(--bg-elevated);
            border: 1px solid var(--border);
            border-radius: 6px;
            padding: 0.7rem 0.85rem;
            font-size: 0.78rem;
            color: var(--text-muted);
            line-height: 1.5;
            display: none;
            z-index: 5;
        }
        .flow-help.open { display: block; }
        .flow-help kbd {
            background: var(--bg-base); border: 1px solid var(--border);
            border-radius: 3px; padding: 0 4px; font-size: 0.72rem;
        }

    /* The two <dialog>s. These replace .modal/.modal-dialog/.modal-lg/.modal-xl,
       none of which any stylesheet this site serves defines -- so neither dialog
       has ever had a width, a backdrop or a border of its own. */
    dialog.dlg {
        padding: 0;
        border: 1px solid var(--border);
        border-radius: 6px;
        background: var(--bg-elevated);
        color: var(--text-primary);
        max-height: 85vh;
        overflow: hidden;
    }
    dialog.dlg::backdrop { background: rgba(0, 0, 0, 0.6); }
    dialog.dlg--lg { width: min(800px, 92vw); }
    dialog.dlg--xl { width: min(1140px, 92vw); }
    .dlg__header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 1rem;
        padding: 0.5rem 1rem;
        border-bottom: 1px solid var(--border);
    }
    .dlg__body { padding: 0.75rem 1rem; overflow-y: auto; max-height: 70vh; }

/* from flow_mockup_detail.html */
        /* Permalink page = single-mockup deep-dive: full-size preview
           up top, side-by-side parent diff (when this mockup proposes
           a change), notes panel, comments panel. All sections share
           a 1-column layout up to ~1100px so wide-screen reviewers
           can see content + comments side-by-side without scrolling. */
        .mockup-frame {
            width: 100%; min-height: 60vh;
            border: 1px solid var(--border);
            border-radius: 6px; background: #fff;
        }
        .mockup-side {
            display: grid;
            grid-template-columns: 1fr 1fr;
            gap: 0.75rem;
        }
        .mockup-side > div { min-width: 0; }
        .mockup-side .frame-label {
            font-size: 0.72rem; color: var(--text-muted);
            text-transform: uppercase; letter-spacing: 0.04em;
            margin-bottom: 0.25rem;
        }
        .mockup-side iframe {
            width: 100%; height: 320px;
            border: 1px solid var(--border); border-radius: 4px;
            background: #fff;
        }
        .notes-rendered {
            background: var(--bg-base);
            border: 1px solid var(--border);
            border-radius: 4px;
            padding: 0.6rem 0.75rem;
            font-size: 0.88rem;
        }
        .notes-rendered code {
            background: rgba(91,138,245,0.1);
            padding: 1px 4px; border-radius: 3px;
            font-size: 0.85em;
        }
        .mockup-comment {
            font-size: 0.85rem; padding: 0.5rem 0.75rem;
            border-bottom: 1px solid var(--border);
        }
        .mockup-comment:last-child { border-bottom: none; }
        .mockup-comment-meta {
            font-size: 0.7rem; color: var(--text-muted);
            margin-bottom: 2px;
        }

/* from flow_mockups.html */
        /* Mockup cards: thumbnail iframe + meta. Grid auto-flows so the
           page reflows on small screens without me hand-rolling
           breakpoints. Sandbox="" on the iframe is the security
           boundary; the rest is layout. */
        .mockup-grid {
            display: grid;
            grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
            gap: 1rem;
        }
        .mockup-card {
            background: var(--bg-elevated);
            border: 1px solid var(--border);
            border-radius: 6px;
            overflow: hidden;
            display: flex; flex-direction: column;
            transition: border-color 0.12s ease;
        }
        .mockup-card:hover { border-color: rgba(167,139,250,0.6); }
        .mockup-thumb {
            position: relative;
            background: #fff;
            height: 180px;
            overflow: hidden;
            border-bottom: 1px solid var(--border);
        }
        .mockup-thumb iframe {
            position: absolute; inset: 0;
            width: 100%; height: 100%;
            border: 0;
            pointer-events: none; /* card is the click target */
        }
        .mockup-thumb-link {
            position: absolute; inset: 0;
            display: block;
            z-index: 2;
        }
        .mockup-meta { padding: 0.6rem 0.75rem; font-size: 0.85rem; }
        .mockup-meta .label { font-weight: 600; color: var(--text-primary); }
        .mockup-meta .sub   { font-size: 0.72rem; color: var(--text-muted); }
        .mockup-meta a      { color: inherit; text-decoration: none; }
        .mockup-meta a:hover{ color: var(--blue); }
        .mockup-vote-pill {
            display: inline-flex; align-items: center; gap: 4px;
            font-size: 0.72rem; color: var(--text-muted);
            padding: 1px 6px; border-radius: 8px;
            border: 1px solid var(--border);
        }
        .mockup-edit-tag {
            display: inline-block; font-size: 0.65rem;
            padding: 1px 6px; border-radius: 3px;
            background: rgba(251,191,36,0.15); color: #fbbf24;
            text-transform: uppercase; letter-spacing: 0.04em;
        }

/* from flow_proposals.html */
        /* ── Page header ─────────────────────────────────────────── */
        .fr-hero {
            display: flex; align-items: flex-start; gap: 1rem;
            padding: 1.1rem 1.25rem; margin-bottom: 1rem;
            border: 1px solid var(--border); border-radius: 10px;
            background:
                radial-gradient(120% 140% at 100% 0%, rgba(91,138,245,0.16) 0%, rgba(91,138,245,0) 60%),
                var(--surface-2);
        }
        .fr-hero h2 { font-size: 1.45rem; font-weight: 650; margin: 0; letter-spacing: -0.01em; }
        .fr-hero p  { font-size: 0.82rem; color: var(--text-muted); margin: 0.35rem 0 0; max-width: 54ch; }

        /* ── Filter strip ────────────────────────────────────────── */
        .fr-filters { padding: 0.7rem 0.9rem; }
        .fr-filter-row { display: flex; flex-wrap: wrap; align-items: center; gap: 0.35rem; }
        .fr-filter-row + .fr-filter-row { margin-top: 0.4rem; }
        .fr-filter-label {
            font-size: 0.66rem; color: var(--text-muted); font-weight: 700;
            text-transform: uppercase; letter-spacing: 0.07em;
            width: 4.2rem; flex-shrink: 0;
        }
        .filter-pill {
            display: inline-flex; align-items: center; gap: 0.3rem;
            font-size: 0.74rem; padding: 3px 11px; border-radius: 999px;
            border: 1px solid var(--border); color: var(--text-muted);
            text-decoration: none; transition: all .12s; white-space: nowrap;
        }
        .filter-pill:hover  { border-color: var(--blue); color: var(--text-primary); }
        .filter-pill.active { border-color: var(--blue); color: var(--blue); background: rgba(91,138,245,0.10); }
        /* The count rides inside the pill so a member can see there are four
           bugs without clicking Bug. Muted, because it is the smaller half of
           the label. */
        .pill-n { font-size: 0.66rem; opacity: 0.65; font-variant-numeric: tabular-nums; }

        /* ── Two-column body ─────────────────────────────────────── */
        .fr-cols { display: grid; grid-template-columns: minmax(0,1fr) 320px; gap: 1rem; align-items: start; }
        @media (max-width: 991px) {
            /* The sidebar is guidance and context; on a narrow screen it goes
               below the form rather than squeezing it. */
            .fr-cols { grid-template-columns: minmax(0,1fr); }
        }

        /* ── The request form ────────────────────────────────────── */
        .fr-step { display: flex; gap: 0.8rem; padding: 0.95rem 0; }
        .fr-step + .fr-step { border-top: 1px solid var(--border); }
        .fr-step-n {
            width: 26px; height: 26px; border-radius: 50%; flex-shrink: 0;
            display: flex; align-items: center; justify-content: center;
            font-size: 0.76rem; font-weight: 700;
            background: rgba(91,138,245,0.14); color: var(--blue);
            border: 1px solid rgba(91,138,245,0.35);
        }
        .fr-step-body { flex: 1; min-width: 0; }
        .fr-step-title { font-size: 0.9rem; font-weight: 600; color: var(--text-primary); }
        .fr-step-hint  { font-size: 0.74rem; color: var(--text-muted); margin: 0.1rem 0 0.55rem; }

        /* Category cards — a bigger target than a dropdown, and each one can
           carry a line of its own explaining what belongs in it, which is what
           stops everything arriving tagged "other". */
        .fr-cats { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 0.5rem; }
        .fr-cat {
            text-align: left; padding: 0.6rem 0.65rem; border-radius: 8px;
            border: 1px solid var(--border); background: var(--bg-elevated);
            cursor: pointer; transition: all .12s; color: var(--text-primary);
        }
        .fr-cat:hover { border-color: rgba(91,138,245,0.55); }
        .fr-cat.selected { border-color: var(--blue); background: rgba(91,138,245,0.10); }
        .fr-cat-name { font-size: 0.8rem; font-weight: 600; display: block; }
        .fr-cat-desc { font-size: 0.68rem; color: var(--text-muted); display: block; margin-top: 0.15rem; line-height: 1.35; }

        .fr-counter { font-size: 0.68rem; color: var(--text-muted); text-align: right; margin-top: 0.2rem; font-variant-numeric: tabular-nums; }
        .fr-counter.over { color: var(--bs-danger); }

        /* Drop zone for screenshots and mockups. */
        .fr-drop {
            border: 1px dashed var(--border); border-radius: 8px;
            padding: 1.1rem; text-align: center; cursor: pointer;
            transition: all .12s; background: var(--bg-elevated);
        }
        .fr-drop:hover, .fr-drop.dragover { border-color: var(--blue); background: rgba(91,138,245,0.06); }
        .fr-drop-text { font-size: 0.78rem; color: var(--text-muted); }
        .fr-drop-text b { color: var(--blue); font-weight: 600; }
        .fr-shots { display: flex; flex-wrap: wrap; gap: 0.45rem; margin-top: 0.55rem; }
        .fr-shot { position: relative; }
        .fr-shot img { width: 72px; height: 72px; object-fit: cover; border-radius: 6px; border: 1px solid var(--border); }
        .fr-shot button {
            position: absolute; top: -6px; right: -6px; width: 18px; height: 18px;
            border-radius: 50%; border: 1px solid var(--border); background: var(--surface-2);
            color: var(--text-muted); font-size: 0.7rem; line-height: 1; cursor: pointer; padding: 0;
        }
        .fr-shot button:hover { color: var(--bs-danger); border-color: var(--bs-danger); }

        /* ── Sidebar ─────────────────────────────────────────────── */
        .fr-tip { display: flex; gap: 0.6rem; padding: 0.5rem 0; }
        .fr-tip + .fr-tip { border-top: 1px solid var(--border); }
        .fr-tip-icon {
            width: 28px; height: 28px; border-radius: 7px; flex-shrink: 0;
            display: flex; align-items: center; justify-content: center;
            background: rgba(91,138,245,0.12); color: var(--blue);
        }
        .fr-tip-name { font-size: 0.78rem; font-weight: 600; }
        .fr-tip-desc { font-size: 0.7rem; color: var(--text-muted); line-height: 1.4; }

        .fr-recent { display: block; padding: 0.5rem 0; text-decoration: none; }
        .fr-recent + .fr-recent { border-top: 1px solid var(--border); }
        .fr-recent:hover .fr-recent-title { color: var(--blue); }
        .fr-recent-title { font-size: 0.79rem; font-weight: 500; color: var(--text-primary); }
        .fr-recent-meta { font-size: 0.68rem; color: var(--text-muted); margin-top: 0.2rem; display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; }

        /* ── Chips: category + lifecycle ─────────────────────────── */
        .prop-tag {
            display: inline-block; font-size: 0.62rem; padding: 2px 8px;
            border-radius: 999px; font-weight: 600;
            text-transform: uppercase; letter-spacing: 0.05em;
        }
        .prop-tag-ui          { background: rgba(91,138,245,0.18);  color: #85b4ff; }
        .prop-tag-bug         { background: rgba(248,113,113,0.18); color: #ff9595; }
        .prop-tag-feature     { background: rgba(74,222,128,0.18);  color: #4ade80; }
        .prop-tag-performance { background: rgba(167,139,250,0.18); color: #c4b5fd; }
        .prop-tag-other       { background: rgba(148,163,184,0.18); color: #94a3b8; }
        .prop-status {
            display: inline-block; font-size: 0.62rem; padding: 2px 8px;
            border-radius: 4px; font-weight: 600;
            text-transform: uppercase; letter-spacing: 0.05em;
        }
        .prop-status-open        { background: rgba(148,163,184,0.18); color: #cbd5e1; }
        .prop-status-planned     { background: rgba(91,138,245,0.22);  color: #93c5fd; }
        .prop-status-in_progress { background: rgba(251,191,36,0.22);  color: #fbbf24; }
        .prop-status-done        { background: rgba(74,222,128,0.22);  color: #4ade80; }
        .prop-status-declined    { background: rgba(248,113,113,0.22); color: #ff9595; }

        .role-badge {
            font-size: 0.62rem; padding: 2px 8px; border-radius: 999px;
            font-weight: 600; text-transform: uppercase; letter-spacing: 0.06em;
        }
        .role-admin       { background: rgba(var(--bs-danger-rgb), 0.2); color: var(--bs-danger); }
        .role-contributor { background: rgba(91,138,245,0.2); color: var(--blue); }
        /* .role-admin and .role-contributor had rules and this did not, so a
           moderator's badge rendered as bare text beside an admin's red one. */
        .role-mod         { background: rgba(var(--bs-success-rgb), 0.2); color: var(--bs-success); }

        /* ── The full list ───────────────────────────────────────── */
        .prop-row {
            display: flex; align-items: center; gap: 0.75rem;
            padding: 0.6rem 0.85rem; border-bottom: 1px solid var(--border);
            cursor: pointer; transition: background .12s;
        }
        .prop-row:hover { background: rgba(255,255,255,0.02); }
        .prop-row.expanded { background: rgba(91,138,245,0.04); }
        .prop-row:last-child { border-bottom: none; }
        .prop-avatar { width: 36px; height: 36px; border-radius: 50%; object-fit: cover; border: 1px solid var(--border); flex-shrink: 0; }
        .prop-avatar-fallback {
            width: 36px; height: 36px; border-radius: 50%; background: #3a7bd5;
            color: #fff; display: flex; align-items: center; justify-content: center;
            font-size: 0.95rem; font-weight: 700; flex-shrink: 0;
        }
        .prop-title { font-size: 0.92rem; font-weight: 500; color: var(--text-primary); }
        .prop-meta  { font-size: 0.72rem; color: var(--text-muted); }
        .prop-stat  { font-size: 0.78rem; color: var(--text-muted); white-space: nowrap; }
        @media (max-width: 575px) {
            /* Avatar and the comment count are the first things worth losing;
               the title, the vote and the status are the row. */
            .prop-avatar, .prop-avatar-fallback, .prop-stat { display: none; }
            .prop-row { padding: 0.6rem 0.6rem; gap: 0.5rem; }
        }

        .vote-btn {
            display: inline-flex; align-items: center; gap: 4px;
            font-size: 0.78rem; padding: 2px 10px;
            border: 1px solid var(--border); border-radius: 4px;
            background: transparent; color: var(--text-primary);
            cursor: pointer; user-select: none;
        }
        .vote-btn:hover  { border-color: var(--bs-warning); color: var(--bs-warning); }
        .vote-btn.voted  { border-color: var(--bs-warning); color: var(--bs-warning); background: rgba(251,191,36,0.1); }
        .vote-btn.disabled { opacity: 0.45; cursor: not-allowed; color: var(--text-muted); border-color: var(--border); }
        .vote-btn.disabled:hover { color: var(--text-muted); border-color: var(--border); }

        .prop-detail {
            padding: 0.9rem 1.1rem; background: var(--surface-2);
            border-bottom: 1px solid var(--border); font-size: 0.88rem;
        }
        .prop-detail :where(h1,h2,h3) { font-size: 1rem; margin-top: 0.5rem; }
        .prop-detail p { margin-bottom: 0.5rem; }
        .prop-detail pre { font-size: 0.78rem; padding: 0.5rem; background: rgba(0,0,0,0.25); border-radius: 4px; }
        .prop-detail img { max-width: 100%; height: auto; border-radius: 6px; }
        .prop-comment {
            padding: 0.5rem 0.7rem; border-left: 2px solid var(--border);
            margin-top: 0.4rem; background: rgba(0,0,0,0.10); border-radius: 0 4px 4px 0;
        }
        .prop-comment-meta { font-size: 0.7rem; color: var(--text-muted); margin-bottom: 0.2rem; }

        .dup-hint {
            margin-top: 0.4rem; padding: 0.5rem 0.7rem;
            background: rgba(251,191,36,0.08);
            border: 1px solid rgba(251,191,36,0.3);
            border-radius: 4px; font-size: 0.78rem;
        }
        .dup-hint a { color: var(--bs-warning); text-decoration: none; }
        .dup-hint a:hover { text-decoration: underline; }

/* from help_roadmap.html */
        .rc-section {
            margin-bottom: 1.5rem;
        }
        .rc-section-head {
            font-size: 0.78rem;
            font-weight: 700;
            text-transform: uppercase;
            letter-spacing: 0.08em;
            color: var(--text-muted);
            margin-bottom: 0.6rem;
            padding-bottom: 0.3rem;
            border-bottom: 1px solid var(--border);
        }
        /* Roadmap rows */
        .roadmap-entry {
            display: grid;
            grid-template-columns: 24px 1fr auto;
            gap: 0.6rem;
            padding: 0.6rem 0;
            align-items: start;
            border-bottom: 1px dotted var(--border);
        }
        .roadmap-entry:last-child { border-bottom: none; }
        .roadmap-marker {
            font-size: 1rem;
            line-height: 1;
            margin-top: 0.2rem;
        }
        .roadmap-marker.in_flight { color: var(--warn); }
        .roadmap-marker.backlog   { color: var(--text-muted); }
        .roadmap-title {
            font-size: 0.92rem;
            font-weight: 600;
            color: var(--text-primary);
        }
        .roadmap-desc, .changelog-desc {
            font-size: 0.82rem;
            color: var(--text-muted);
            margin-top: 0.15rem;
        }
        /* Changelog rows */
        .changelog-bucket-head {
            font-size: 0.78rem;
            font-weight: 700;
            text-transform: uppercase;
            letter-spacing: 0.08em;
            color: var(--text-muted);
            margin: 1.4rem 0 0.5rem;
            padding-bottom: 0.3rem;
            border-bottom: 1px solid var(--border);
        }
        .changelog-bucket-head:first-child { margin-top: 0.2rem; }
        .changelog-entry {
            display: grid;
            grid-template-columns: 70px 1fr;
            gap: 0.7rem;
            padding: 0.6rem 0;
            align-items: start;
            border-bottom: 1px dotted var(--border);
        }
        .changelog-entry:last-child { border-bottom: none; }
        .changelog-title { font-size: 0.92rem; font-weight: 600; color: var(--text-primary); }
        .cat-badge {
            display: inline-block;
            font-size: 0.62rem;
            font-weight: 700;
            text-transform: uppercase;
            letter-spacing: 0.05em;
            padding: 2px 8px;
            border-radius: 999px;
            text-align: center;
            line-height: 1.35;
        }
        /* Category palette pulls from tokens.css. Adding a new
           category? Define it there once; both this badge style and
           the graph node colour below pick it up. */
        .cat-feature  { background: var(--blue-tint);   color: var(--blue);   }
        .cat-fix      { background: var(--green-tint);  color: var(--green);  }
        .cat-perf     { background: var(--purple-tint); color: var(--purple); }
        .cat-security { background: var(--pink-tint);   color: var(--pink);   }
        .cat-infra    { background: var(--slate-tint);  color: var(--slate);  }
        .cat-docs     { background: var(--warn-tint);   color: var(--warn);   }
        .cat-agent    { background: var(--orange-tint); color: var(--orange); }

`
