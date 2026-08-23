package wiki

// wikiCSS is what this plugin's fragments used to carry inline.
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
const wikiCSS = `/* from admin_wiki_post_form.html */
        .editor-wrap { display: flex; gap: 0; border: 1px solid var(--border); border-radius: 6px; overflow: hidden; min-height: 520px; }
        .editor-wrap textarea {
            flex: 1; border: none; border-right: 1px solid var(--border); border-radius: 0;
            resize: none; font-family: monospace; font-size: 0.82rem;
            background: var(--bg-elevated); color: var(--text-primary); padding: 0.75rem;
            outline: none; line-height: 1.6;
        }
        .editor-wrap textarea:focus { box-shadow: none; }
        .preview-pane {
            flex: 1; overflow-y: auto; padding: 1rem;
            background: var(--bg-surface); font-size: 0.875rem;
            color: var(--text-primary); line-height: 1.7;
        }
        .toolbar { display: flex; gap: 4px; flex-wrap: wrap; margin-bottom: 6px; }
        .toolbar button, .toolbar label {
            font-size: 0.78rem; padding: 2px 9px; line-height: 1.6;
            background: var(--bg-elevated); border: 1px solid var(--border);
            color: var(--text-primary); border-radius: 4px; cursor: pointer;
        }
        .toolbar button:hover, .toolbar label:hover { background: var(--border); }
        /* Preview styles */
        .preview-pane h1,.preview-pane h2,.preview-pane h3,
        .preview-pane h4,.preview-pane h5,.preview-pane h6 { margin-top:1.25rem;margin-bottom:0.4rem;color:var(--text-primary);font-weight:600; }
        .preview-pane p { margin-bottom:0.75rem; }
        .preview-pane code { background:var(--bg-elevated);padding:0.15em 0.4em;border-radius:3px;font-size:0.875em;color:var(--blue); }
        .preview-pane pre { background:var(--bg-elevated);border:1px solid var(--border);border-radius:6px;padding:0.75rem;overflow-x:auto;margin-bottom:0.75rem; }
        .preview-pane pre code { background:none;padding:0;color:var(--text-primary); }
        .preview-pane blockquote { border-left:3px solid var(--border);padding-left:1rem;margin-left:0;color:var(--text-muted);margin-bottom:0.75rem; }
        .preview-pane img { max-width:100%;border-radius:4px; }
        .preview-pane table { width:100%;border-collapse:collapse;margin-bottom:0.75rem; }
        .preview-pane th,.preview-pane td { border:1px solid var(--border);padding:0.4rem 0.6rem; }
        .preview-pane th { background:var(--bg-elevated); }
        .preview-pane a { color:var(--blue); }
        .preview-pane ul,.preview-pane ol { padding-left:1.5rem;margin-bottom:0.75rem; }
        .preview-pane hr { border-color:var(--border);margin:1rem 0; }

/* from wiki.html */
        /* ── Shell ─────────────────────────────────────────────────
           One column. The old three-column shell (explorer rail,
           main, four-card right rail) plus a 260px banner hero was
           retired 2026-08-17 as clutter: the explorer duplicated the
           category grid, and the rail cards buried the two lists
           members actually come for. What survives is the content —
           category grid, recent updates, popular articles — under a
           one-line header. */
        .wiki-page { margin: 0 auto; padding: 1.4rem 1rem 3rem; }

        /* ── Compact header ───────────────────────────────────────── */
        .wiki-head {
            display: flex;
            align-items: flex-end;
            justify-content: space-between;
            gap: 1rem;
            flex-wrap: wrap;
            margin-bottom: 1.4rem;
        }
        .wiki-head h2 {
            font-size: 1.45rem;
            font-weight: 800;
            color: var(--text-primary);
            letter-spacing: -0.01em;
            margin: 0 0 0.15rem;
            line-height: 1.1;
        }
        .wiki-head .wiki-tagline {
            font-size: 0.85rem;
            color: var(--text-muted);
            margin: 0;
        }
        .wiki-head-side {
            display: flex;
            align-items: center;
            gap: 0.9rem;
            flex-wrap: wrap;
        }
        .wiki-head-links {
            font-size: 0.8rem;
            white-space: nowrap;
        }
        .wiki-head-links a { color: var(--blue); text-decoration: none; }
        .wiki-head-links a:hover { text-decoration: underline; }
        .wiki-head-links .sep { color: var(--text-muted); margin: 0 0.35rem; }

        .wiki-search { position: relative; min-width: 240px; }
        .wiki-search input {
            width: 100%;
            background: var(--bg-elevated);
            border: 1px solid var(--border);
            border-radius: 10px;
            color: var(--text-primary);
            font-size: 0.84rem;
            padding: 0.45rem 2.6rem 0.45rem 2rem;
            outline: none;
            transition: border-color 0.12s ease;
        }
        .wiki-search input:focus { border-color: var(--blue); }
        .wiki-search input::placeholder { color: var(--text-muted); }
        .wiki-search .ws-icon {
            position: absolute;
            left: 0.6rem;
            top: 50%;
            transform: translateY(-50%);
            color: var(--text-muted);
            font-size: 0.88rem;
        }
        .wiki-search .ws-kbd {
            position: absolute;
            right: 0.45rem;
            top: 50%;
            transform: translateY(-50%);
            font-size: 0.66rem;
            color: var(--text-muted);
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 5px;
            padding: 1px 5px;
            font-family: inherit;
        }

        /* ── Section headings ─────────────────────────────────────── */
        .wiki-section-h {
            font-size: 1.05rem;
            font-weight: 700;
            color: var(--text-primary);
            margin: 0 0 0.85rem;
        }
        .wiki-section-head {
            display: flex;
            align-items: center;
            justify-content: space-between;
            gap: 1rem;
        }
        .wiki-section-head .wiki-section-h { margin-bottom: 0.85rem; }
        .wiki-section-link {
            color: var(--blue);
            font-size: 0.8rem;
            text-decoration: none;
            white-space: nowrap;
        }
        .wiki-section-link:hover { text-decoration: underline; }

        /* ── Browse Categories grid ───────────────────────────────── */
        .wiki-cat-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
            gap: 0.7rem;
        }
        @media (max-width: 480px) {
            .wiki-cat-grid { grid-template-columns: minmax(0, 1fr); }
            .wiki-cat-card { min-height: 0; }
        }
        .wiki-cat-card {
            position: relative;
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 12px;
            padding: 1rem 0.95rem 0.85rem;
            text-decoration: none;
            color: inherit;
            display: flex;
            flex-direction: column;
            transition: border-color 0.15s ease, transform 0.15s ease, background-color 0.15s ease;
            min-height: 158px;
        }
        .wiki-cat-card:hover {
            border-color: var(--blue);
            background: var(--bg-elevated);
            transform: translateY(-2px);
        }
        .wiki-cat-card .cc-ico {
            width: 38px; height: 38px;
            display: inline-flex; align-items: center; justify-content: center;
            border-radius: 10px;
            margin-bottom: 0.6rem;
            flex-shrink: 0;
        }
        .wiki-cat-card .cc-name {
            font-size: 0.98rem;
            font-weight: 700;
            color: var(--text-primary);
            margin-bottom: 0.2rem;
        }
        .wiki-cat-card .cc-desc {
            font-size: 0.75rem;
            color: var(--text-muted);
            line-height: 1.4;
            flex: 1;
            margin-bottom: 0.55rem;
        }
        .wiki-cat-card .cc-foot {
            display: flex;
            align-items: center;
            justify-content: space-between;
            font-size: 0.74rem;
            color: var(--text-muted);
        }
        .wiki-cat-card .cc-arrow {
            color: var(--blue);
            font-size: 0.85rem;
            font-weight: 600;
            transition: transform 0.15s ease;
        }
        .wiki-cat-card:hover .cc-arrow { transform: translateX(3px); }

        /* Per-slug accent tints for the icon tile. Falls back to a
           neutral blue when a slug isn't in the lookup. New topics
           default to blue until added here. */
        .cc-ico { background: var(--blue-tint, rgba(91,138,245,0.18)); color: var(--blue); }
        .wiki-cat-card[data-slug="tools"]   .cc-ico { background: rgba(91,138,245,0.18);  color: #5b8af5; }
        .wiki-cat-card[data-slug="usenet"]  .cc-ico { background: rgba(245,158,11,0.18);  color: #f59e0b; }
        .wiki-cat-card[data-slug="guides"]  .cc-ico { background: rgba(168,85,247,0.18);  color: #a855f7; }
        .wiki-cat-card[data-slug="api"]     .cc-ico { background: rgba(34,197,94,0.18);   color: #22c55e; }
        .wiki-cat-card[data-slug="policies"]   .cc-ico,
        .wiki-cat-card[data-slug="security"]   .cc-ico { background: rgba(248,113,113,0.18); color: #f87171; }
        .wiki-cat-card[data-slug="community"]  .cc-ico { background: rgba(91,138,245,0.18);  color: #5b8af5; }

        /* ── Recent Posts ─────────────────────────────────────────── */
        /* The BOX is the host panel's now (panelV2 + panel__body--flush), so
           this draws no border, no background and no radius of its own.
           Nesting a second card inside the first is what made the page read as
           a stack of identical grey rectangles with nothing telling them
           apart. What stays is the clipping, which the rounded panel needs to
           keep the first row's hover from spilling past the corner. */
        .wiki-recent-panel { overflow: hidden; }
        .wiki-recent-tabs {
            display: flex;
            gap: 0.35rem;
            padding: 0.45rem 0.85rem 0;
            border-bottom: 1px solid var(--border);
        }
        .wiki-recent-tab {
            color: var(--text-muted);
            font-size: 0.76rem;
            padding: 0.5rem 0.65rem 0.55rem;
            text-decoration: none;
            border-bottom: 2px solid transparent;
        }
        .wiki-recent-tab:hover { color: var(--text-primary); }
        .wiki-recent-tab.is-active {
            color: var(--text-primary);
            border-bottom-color: var(--purple);
        }
        .wiki-recent-list { display:flex; flex-direction:column; }
        .wiki-recent-item {
            display: flex;
            align-items: center;
            gap: 0.95rem;
            background: transparent;
            border-bottom: 1px solid var(--border);
            padding: 0.75rem 1rem;
            text-decoration: none;
            color: inherit;
            transition: background-color 0.12s ease;
        }
        .wiki-recent-item:last-child { border-bottom: 0; }
        .wiki-recent-item:hover { background: var(--bg-elevated); }
        .wiki-recent-item .ri-ico {
            width: 36px; height: 36px;
            border-radius: 9px;
            background: rgba(91,138,245,0.15);
            color: var(--blue);
            display: inline-flex; align-items: center; justify-content: center;
            flex-shrink: 0;
        }
        /* Per-slug tile colour on the recent-posts list, matching the
           Browse Categories accents. */
        .wiki-recent-item[data-slug="usenet"]   .ri-ico { background: rgba(245,158,11,0.18);  color: #f59e0b; }
        .wiki-recent-item[data-slug="guides"]   .ri-ico { background: rgba(168,85,247,0.18);  color: #a855f7; }
        .wiki-recent-item[data-slug="api"]      .ri-ico { background: rgba(34,197,94,0.18);   color: #22c55e; }
        .wiki-recent-item[data-slug="policies"] .ri-ico,
        .wiki-recent-item[data-slug="security"] .ri-ico { background: rgba(248,113,113,0.18); color: #f87171; }
        .wiki-recent-item[data-slug="rules"]    .ri-ico { background: rgba(248,113,113,0.18); color: #f87171; }
        .wiki-recent-item .ri-body { flex: 1; min-width: 0; }
        .wiki-recent-item .ri-title {
            font-size: 0.95rem;
            font-weight: 700;
            color: var(--text-primary);
            margin: 0 0 0.2rem;
            white-space: nowrap;
            overflow: hidden;
            text-overflow: ellipsis;
        }
        .wiki-recent-item .ri-meta {
            font-size: 0.74rem;
            color: var(--text-muted);
            display: flex;
            align-items: center;
            gap: 0.55rem;
            flex-wrap: wrap;
        }
        .ri-cat-badge {
            font-size: 0.66rem;
            font-weight: 700;
            text-transform: uppercase;
            letter-spacing: 0.04em;
            padding: 1px 7px;
            border-radius: 999px;
            background: rgba(91,138,245,0.12);
            color: var(--blue);
            border: 1px solid rgba(91,138,245,0.35);
        }
        .wiki-recent-item[data-slug="usenet"]   .ri-cat-badge { background: rgba(245,158,11,0.12);  color: #f59e0b; border-color: rgba(245,158,11,0.40); }
        .wiki-recent-item[data-slug="guides"]   .ri-cat-badge { background: rgba(168,85,247,0.12);  color: #a855f7; border-color: rgba(168,85,247,0.40); }
        .wiki-recent-item[data-slug="api"]      .ri-cat-badge { background: rgba(34,197,94,0.12);   color: #22c55e; border-color: rgba(34,197,94,0.40); }
        .wiki-recent-item[data-slug="policies"] .ri-cat-badge,
        .wiki-recent-item[data-slug="security"] .ri-cat-badge,
        .wiki-recent-item[data-slug="rules"]    .ri-cat-badge { background: rgba(248,113,113,0.12); color: #f87171; border-color: rgba(248,113,113,0.40); }
        .wiki-recent-item .ri-when {
            font-size: 0.74rem;
            color: var(--text-muted);
            flex-shrink: 0;
            white-space: nowrap;
        }
        .wiki-recent-foot {
            display: flex;
            justify-content: center;
            border-top: 1px solid var(--border);
            padding: 0.75rem;
        }
        .wiki-view-all {
            font-size: 0.82rem;
            color: var(--blue);
            padding: 0.2rem 0.6rem;
            text-decoration: none;
            font-weight: 600;
        }
        .wiki-view-all:hover { text-decoration: underline; }

        /* ── Popular Articles — slim single-column list ───────────── */
        /* As the recent panel above: the host panel draws the box. Padding
           stays, because panel__body--flush deliberately removes it and a
           list flush against the border reads as clipped. */
        .wiki-pop-panel { padding: 0.9rem 1rem; }
        .pop-list { list-style:none; padding:0; margin:0; display:flex; flex-direction:column; gap:0.25rem; }
        .pop-item {
            display: flex;
            align-items: center;
            gap: 0.65rem;
            text-decoration: none;
            color: inherit;
            padding: 0.45rem 0.45rem;
            border-radius: 8px;
            transition: background-color 0.12s ease;
        }
        .pop-item:hover { background: var(--bg-elevated); }
        .pop-item .pop-ico {
            width: 28px; height: 28px;
            background: var(--bg-elevated);
            border-radius: 7px;
            display: inline-flex; align-items: center; justify-content: center;
            color: var(--blue);
            flex-shrink: 0;
        }
        .pop-item .pop-title {
            flex: 1; min-width: 0;
            font-size: 0.84rem;
            font-weight: 600;
            color: var(--text-primary);
            white-space: nowrap;
            overflow: hidden;
            text-overflow: ellipsis;
        }
        .pop-item .pop-views {
            font-size: 0.7rem;
            color: var(--text-muted);
            white-space: nowrap;
        }

        .wiki-empty { text-align: center; padding: 3rem 1rem; color: var(--text-muted); font-size: 0.9rem; }

/* from wiki_post.html */
        /* One column, prose width. The explorer sidebar (every folder's
           full page tree, pre-rendered per view) was retired 2026-08-17
           with the landing page's rails — the breadcrumb already answers
           "where am I", and the topic page answers "what else is here".
           An article page is for reading the article. */
        .wiki-page { margin: 0 auto; padding: 1.4rem 1rem 3rem; }

        /* Article header strip — breadcrumb + title + actions. */
        .article-header {
            display: flex;
            justify-content: space-between;
            align-items: flex-start;
            gap: 1rem;
            margin-bottom: 1rem;
            flex-wrap: wrap;
        }
        .article-header .breadcrumb-line {
            font-size: 0.78rem;
            color: var(--text-muted);
            margin-bottom: 0.4rem;
        }
        .article-header .breadcrumb-line a {
            color: var(--blue);
            text-decoration: none;
        }
        .article-header .breadcrumb-line a:hover { text-decoration: underline; }
        .article-header h2 {
            font-size: 1.7rem;
            font-weight: 800;
            margin: 0;
            color: var(--text-primary);
            letter-spacing: -0.01em;
            line-height: 1.2;
        }
        .article-actions {
            display: flex;
            gap: 0.5rem;
            flex-shrink: 0;
            align-items: center;
        }

        /* Article body */
        .wiki-post-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 14px;
            padding: 1.6rem 1.8rem;
        }
        .wiki-post-meta {
            font-size: 0.68rem;
            color: var(--text-muted);
            text-transform: uppercase;
            letter-spacing: 0.06em;
            font-weight: 600;
            padding-top: 1.2rem;
            margin-top: 1.2rem;
            border-top: 1px solid var(--border);
        }

        .wiki-content { color: var(--text-primary); line-height: 1.75; font-size: 0.95rem; }
        .wiki-content h1, .wiki-content h2, .wiki-content h3,
        .wiki-content h4, .wiki-content h5, .wiki-content h6 {
            margin-top: 1.75rem;
            margin-bottom: 0.6rem;
            color: var(--text-primary);
            font-weight: 700;
            letter-spacing: -0.005em;
        }
        .wiki-content h1 { font-size: 1.5rem; }
        .wiki-content h2 { font-size: 1.25rem; }
        .wiki-content h3 { font-size: 1.1rem; }
        .wiki-content p { margin-bottom: 1rem; }
        .wiki-content code {
            background: var(--bg-elevated);
            padding: 0.15em 0.45em;
            border-radius: 4px;
            font-size: 0.875em;
            color: var(--blue);
        }
        .wiki-content pre {
            background: var(--bg-elevated);
            border: 1px solid var(--border);
            border-radius: 8px;
            padding: 1rem 1.2rem;
            overflow-x: auto;
            margin-bottom: 1.1rem;
        }
        .wiki-content pre code { background: none; padding: 0; color: var(--text-primary); }
        .wiki-content blockquote {
            border-left: 3px solid var(--blue);
            padding: 0.25rem 0 0.25rem 1rem;
            margin: 1rem 0;
            color: var(--text-muted);
        }
        .wiki-content img { max-width: 100%; border-radius: 8px; }
        .wiki-content table {
            width: 100%;
            border-collapse: collapse;
            margin-bottom: 1.1rem;
            border-radius: 8px;
            overflow: hidden;
        }
        .wiki-content th, .wiki-content td {
            border: 1px solid var(--border);
            padding: 0.55rem 0.85rem;
        }
        .wiki-content th {
            background: var(--bg-elevated);
            font-weight: 700;
            text-align: left;
        }
        .wiki-content a { color: var(--blue); }
        .wiki-content a:hover { text-decoration: underline; }
        .wiki-content ul, .wiki-content ol {
            padding-left: 1.6rem;
            margin-bottom: 1rem;
        }
        .wiki-content li { margin-bottom: 0.25rem; }
        .wiki-content hr {
            border: none;
            border-top: 1px solid var(--border);
            margin: 1.5rem 0;
        }

/* from wiki_topic.html */
        /* One column. The 260px explorer sidebar (every folder with its
           full page tree) duplicated navigation the landing page and the
           breadcrumb already provide, and was retired 2026-08-17 with the
           landing page's rails. What remains: a compact folder header and
           the article cards. */
        .wiki-page { margin: 0 auto; padding: 1.4rem 1rem 3rem; }

        .wiki-back-link {
            display: inline-flex;
            align-items: center;
            gap: 0.35rem;
            font-size: 0.8rem;
            color: var(--blue);
            text-decoration: none;
            margin-bottom: 0.7rem;
        }
        .wiki-back-link:hover { text-decoration: underline; }

        /* Compact folder header — icon + name + description on one
           card row, replacing the old hero + eyebrow block. */
        .wiki-overview {
            display: flex;
            align-items: center;
            gap: 0.85rem;
            flex-wrap: wrap;
            margin-bottom: 1.1rem;
        }
        .wiki-overview .overview-icon {
            color: var(--blue);
            font-size: 1.25rem;
            display: inline-flex;
            width: 40px; height: 40px;
            border-radius: 10px;
            overflow: hidden;
            background: var(--blue-tint, rgba(91,138,245,0.15));
            align-items: center; justify-content: center;
            flex-shrink: 0;
        }
        .wiki-overview .overview-icon img { width: 100%; height: 100%; object-fit: cover; }
        .wiki-overview h2 {
            font-size: 1.35rem;
            font-weight: 700;
            color: var(--text-primary);
            margin: 0;
            line-height: 1.15;
        }
        .wiki-overview .overview-body { min-width: 0; }
        .wiki-overview p {
            margin: 0.1rem 0 0;
            color: var(--text-muted);
            font-size: 0.85rem;
        }
        .wiki-overview .overview-count {
            margin-left: auto;
            font-size: 0.78rem;
            color: var(--text-muted);
            white-space: nowrap;
        }

        .wiki-article-grid {
            display: grid;
            grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
            gap: 1rem;
        }
        @media (max-width: 480px) {
            .wiki-article-grid { grid-template-columns: minmax(0, 1fr); }
        }
        /* Article card — paper icon left, title + excerpt + meta on
           the right, "Show →" affordance for the click target. */
        .wiki-article-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 12px;
            padding: 1.1rem 1.25rem 1rem;
            text-decoration: none;
            display: flex;
            gap: 0.85rem;
            align-items: flex-start;
            transition: transform 0.2s ease, border-color 0.2s ease, background-color 0.2s ease, box-shadow 0.2s ease;
        }
        .wiki-article-card:hover {
            border-color: var(--blue);
            background: var(--bg-elevated);
            transform: translateY(-2px);
            box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25);
        }
        .wiki-article-card:hover .wiki-article-title { color: var(--blue); }
        .wiki-article-icon {
            width: 36px; height: 36px;
            background: var(--blue-tint-soft);
            color: var(--blue);
            display: inline-flex; align-items: center; justify-content: center;
            border-radius: 9px;
            font-size: 1.1rem;
            flex-shrink: 0;
        }
        .wiki-article-body { min-width: 0; flex: 1; display: flex; flex-direction: column; gap: 0.35rem; }
        .wiki-article-title {
            font-size: 1.02rem;
            font-weight: 700;
            line-height: 1.35;
            color: var(--text-primary);
            transition: color 0.2s ease;
        }
        .wiki-article-excerpt {
            font-size: 0.84rem;
            color: var(--text-muted);
            line-height: 1.55;
            display: -webkit-box;
            -webkit-line-clamp: 3;
            -webkit-box-orient: vertical;
            overflow: hidden;
        }
        .wiki-article-meta {
            font-size: 0.74rem;
            color: var(--text-muted);
            display: flex;
            align-items: center;
            gap: 0.6rem;
            margin-top: 0.15rem;
        }
        .wiki-article-meta .show-link {
            color: var(--blue);
            font-weight: 600;
            margin-left: auto;
        }
        /* A modifier, not a second .wiki-empty. The index defines the same
           class without the box, and in one stylesheet the later rule would
           win on both pages — giving the index a dashed border it has never
           had. Only this page wants the box. */
        .wiki-empty--boxed {
            background: var(--bg-surface);
            border: 1px dashed var(--border);
            border-radius: 12px;
        }

`
