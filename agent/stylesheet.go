package agent

// agentCSS is what the member page's fragments used to carry inline (20
// attributes on /p/agents at first ship — the demo's style ratchet caught
// them). A fragment cannot reach the document head, so inline was the only
// alternative to this: registered once at Provision, the host gives it a
// URL, a content hash and a year of caching. RegisterStylesheet no-ops on a
// host with no sink, where the page draws unstyled — visible rather than
// silent, which is the right failure for a missing seam.
const agentCSS = `/* /p/agents — the member page */
.ag-intro { color: var(--text-muted); max-width: 62ch; }
.ag-empty { color: var(--text-muted); }
.ag-dot--on  { color: var(--green); }
.ag-dot--off { color: var(--text-muted); }
.ag-seen { color: var(--text-muted); font-size: 0.78rem; margin-left: 0.4rem; }
.ag-status { font-size: 0.85rem; }
.ag-status__meta { display: flex; gap: 1.2rem; flex-wrap: wrap; color: var(--text-muted); margin-bottom: 0.4rem; }
.ag-status__meta strong { color: var(--text-primary); }
.ag-status__task { margin-bottom: 0.3rem; }
.ag-status__req { color: var(--text-muted); }
.ag-file { display: flex; gap: 0.6rem; align-items: baseline; font-size: 0.8rem; color: var(--text-muted); }
.ag-file__name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ag-file__pct { font-variant-numeric: tabular-nums; }
.ag-task { font-size: 0.85rem; color: var(--text-muted); }
.ag-name-input { max-width: 260px; }
.ag-save { margin-top: 0.5rem; }

/* the profile fleet card */
.ag-card__row { font-size: 0.88rem; min-width: 0; }
.ag-card__when { color: var(--text-muted); font-size: 0.75rem; white-space: nowrap; }
.ag-card__manage { font-size: 0.78rem; }

/* /admin/settings — the dispatch panel.
   Every value here was an inline style= until CHECKLIST §8 named the rule:
   a style attribute outranks any host stylesheet short of !important, so a
   host that wants its own spacing has to fight the plugin for it. */
.ag-disp__lead { margin-bottom: 0.75rem; font-size: 0.8rem; }
.ag-disp__card { padding: 0.7rem 0.9rem; }
.ag-disp__figure { font-size: 1.3rem; font-weight: 700; line-height: 1; }
.ag-disp__on { color: var(--green); }
.ag-disp__label {
    font-size: 0.72rem; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.04em; margin-top: 0.25rem;
}
.ag-disp__links { display: flex; gap: 0.5rem; flex-wrap: wrap; }
.ag-disp__foot {
    font-size: 0.72rem; color: var(--text-muted); margin: 0.6rem 0 0;
}

/* /admin/p/agents — the roster */
.ag-adm__summary {
    display: flex; gap: 1.4rem; flex-wrap: wrap;
    color: var(--text-muted); font-size: 0.85rem; margin-bottom: 0.9rem;
}
.ag-adm__summary strong { color: var(--text-primary); }
.ag-adm__table { font-size: 0.82rem; }
.ag-adm__badge { font-size: 0.65rem; }
/* Dates line up as a column, so they are read as one. */
.ag-adm__when { color: var(--text-muted); font-variant-numeric: tabular-nums; }

/* /admin/p/agent-groups — the posting profiles */
.ag-hint { color: var(--text-muted); font-size: 0.78rem; }
.ag-grp__heading { font-weight: 600; margin-bottom: 12px; }
.ag-grp__type { font-size: 0.7rem; }
.ag-grp__ver { color: var(--text-muted); font-size: 0.75rem; margin-left: 8px; }
.ag-grp__del { font-size: 0.75rem; }
.ag-grp__label { font-size: 0.82rem; }
.ag-grp__none { text-align: center; padding: 2rem; }
.ag-grp__foot { margin-top: 8px; }

/* the one-time token reveal */
.ag-token {
    display: block; padding: 0.6rem;
    background: var(--bg-elevated); border: 1px solid var(--border);
    border-radius: 6px; font-size: 0.85rem;
    word-break: break-all; user-select: all;
}`
