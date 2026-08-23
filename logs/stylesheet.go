package logs

// logsCSS is what this plugin's fragments used to carry inline.
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
const logsCSS = `/* from logs.html */
        .log-layout { display:grid; grid-template-columns:210px 1fr; gap:1.25rem; }
        @media (max-width: 900px){ .log-layout{ grid-template-columns:1fr; } }
        .facet-rail h6 { font-size:0.7rem; text-transform:uppercase; letter-spacing:0.04em;
                         color:var(--text-muted); margin:0.9rem 0 0.4rem; }
        .facet { display:flex; justify-content:space-between; align-items:center; gap:0.5rem;
                 padding:0.2rem 0.45rem; border-radius:5px; cursor:pointer; font-size:0.82rem; }
        .facet:hover { background:rgba(255,255,255,0.06); }
        .facet .k { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-family:monospace; }
        .facet .n { font-variant-numeric:tabular-nums; color:var(--text-muted); flex-shrink:0; }
        .histo { display:flex; align-items:flex-end; gap:2px; height:64px; margin:0.25rem 0 1rem;
                 padding:0.4rem 0.2rem; background:rgba(255,255,255,0.02); border-radius:6px; overflow-x:auto; }
        .histo .bar { flex:1 0 6px; min-width:6px; background:var(--accent,#5b8af5); border-radius:2px 2px 0 0;
                      opacity:0.75; }
        .histo .bar:hover { opacity:1; }
        .log-row.archiving { opacity:0.35; transition:opacity 0.3s; }
        .log-msg { font-family:monospace; font-size:0.78rem; max-width:640px;
                   overflow:hidden; text-overflow:ellipsis; white-space:nowrap; cursor:pointer; }
        .log-msg.expanded { white-space:normal; word-break:break-word; max-width:none; }
        .log-op { font-family:monospace; font-size:0.78rem; cursor:pointer; color:var(--accent,#5b8af5); }
        .log-count { font-variant-numeric:tabular-nums; }
        .dsl-help { font-size:0.78rem; background:rgba(255,255,255,0.03); border:1px solid var(--border,#2a2d37);
                    border-radius:6px; padding:0.75rem 1rem; margin-bottom:1rem; display:none; }
        .dsl-help.show { display:block; }
        .dsl-help code { color:var(--accent,#5b8af5); }
        #tailDot { display:inline-block; width:8px; height:8px; border-radius:50%; background:#3ddc84; margin-right:4px;
                   animation:pulse 1.2s infinite; }
        @keyframes pulse { 0%,100%{opacity:1;} 50%{opacity:0.25;} }

`
