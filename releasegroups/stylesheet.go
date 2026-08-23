package releasegroups

// releasegroupsCSS is what this plugin's fragments used to carry inline.
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
const releasegroupsCSS = `/* from release_group_archive.html */
        .arch-page  { margin: 0 auto; padding: 1.5rem 1rem 3rem; }
        .arch-table { font-size: 0.85rem; }
        .arch-table th { font-size: 0.72rem; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; }
        .arch-table td { vertical-align: middle; }
        .arch-title { font-weight: 500; }
        .arch-meta  { font-size: 0.72rem; color: var(--text-muted); }
        .arch-badge-avail  { background: rgba(74,222,128,0.15); color: #4ade80; }
        .arch-badge-miss   { background: rgba(96,165,250,0.15); color: #60a5fa; }
        .arch-meta-row     { display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; margin-bottom: 1rem; font-size: 0.85rem; }

/* from release_group_bio_edit.html */
        .bio-page { margin: 0 auto; padding: 1.5rem 1rem 3rem; }
        .bio-page textarea {
            font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
            font-size: 0.85rem;
            min-height: 320px;
            line-height: 1.5;
        }
        .bio-help { font-size: 0.78rem; color: var(--text-muted); }
        .bio-help code { background: var(--bg-elevated); padding: 1px 4px; border-radius: 3px; font-size: 0.78em; }

/* from release_group_detail.html */
        .rg-page { margin: 0 auto; padding: 2rem 1rem 4rem; }
        .rg-header-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 12px;
            padding: 1.6rem 1.8rem;
            margin-bottom: 1.75rem;
            display: flex;
            gap: 1.5rem;
            align-items: flex-start;
        }
        .rg-header-logo {
            width: 96px;
            height: 96px;
            border-radius: 12px;
            background: var(--bg-elevated);
            object-fit: cover;
            flex-shrink: 0;
        }
        .rg-header-logo.placeholder {
            display: flex;
            align-items: center;
            justify-content: center;
            font-size: 2.4rem;
            font-weight: 700;
            color: var(--text-muted);
            text-transform: uppercase;
        }
        .rg-header-info { min-width: 0; flex: 1; }
        .rg-header-info .eyebrow {
            font-size: 0.7rem;
            text-transform: uppercase;
            letter-spacing: 0.08em;
            color: var(--text-muted);
            font-weight: 600;
            margin-bottom: 0.35rem;
        }
        .rg-header-info h1 {
            font-size: 1.7rem;
            font-weight: 800;
            margin: 0 0 0.5rem 0;
            color: var(--text-primary);
            letter-spacing: -0.01em;
        }
        .rg-header-info .desc {
            font-size: 0.88rem;
            color: var(--text-muted);
            line-height: 1.55;
            margin: 0.5rem 0 0.75rem 0;
        }
        .rg-header-stats {
            display: flex;
            gap: 1.5rem;
            font-size: 0.78rem;
            color: var(--text-muted);
        }
        .rg-header-stats strong { color: var(--text-primary); font-size: 0.95rem; display: block; }
        .rg-header-actions { display: flex; gap: 0.5rem; flex-shrink: 0; }
        .badge-status {
            font-size: 0.65rem;
            text-transform: uppercase;
            letter-spacing: 0.06em;
            padding: 3px 9px;
            border-radius: 999px;
            font-weight: 700;
        }
        .badge-confirmed { background: rgba(74, 222, 128, 0.15); color: #4ade80; }
        .badge-unknown   { background: rgba(251, 191, 36, 0.15); color: #fbbf24; }

/* from release_group_suggest.html */
        .suggest-page { margin: 0 auto; padding: 2rem 1rem 4rem; }
        .suggest-page .eyebrow {
            font-size: 0.72rem;
            text-transform: uppercase;
            letter-spacing: 0.08em;
            color: var(--text-muted);
            font-weight: 600;
            margin-bottom: 0.35rem;
        }
        .suggest-page h1 {
            font-size: 1.65rem;
            font-weight: 800;
            margin: 0 0 1.5rem 0;
            color: var(--text-primary);
            letter-spacing: -0.01em;
        }
        .suggest-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 12px;
            padding: 1.6rem 1.8rem;
        }

/* from release_groups_list.html */
        .rg-page { margin: 0 auto; padding: 2rem 1rem 4rem; }
        .rg-page-header {
            display: flex;
            justify-content: space-between;
            align-items: flex-end;
            gap: 1rem;
            margin-bottom: 1.5rem;
            flex-wrap: wrap;
        }
        .rg-page-header .eyebrow {
            font-size: 0.72rem;
            text-transform: uppercase;
            letter-spacing: 0.08em;
            color: var(--text-muted);
            font-weight: 600;
            margin-bottom: 0.35rem;
        }
        .rg-page-header h1 {
            font-size: 1.65rem;
            font-weight: 800;
            margin: 0;
            color: var(--text-primary);
            letter-spacing: -0.01em;
        }

        .rg-search { margin-bottom: 1.5rem; }

        .rg-grid {
            display: grid;
            grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
            gap: 1rem;
        }
        .rg-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 12px;
            padding: 1.2rem 1.4rem;
            text-decoration: none;
            transition: transform 0.2s ease, border-color 0.2s ease, background-color 0.2s ease, box-shadow 0.2s ease;
            display: flex;
            gap: 1rem;
            align-items: center;
        }
        .rg-card:hover {
            border-color: var(--blue);
            background: var(--bg-elevated);
            transform: translateY(-2px);
            box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25);
        }
        .rg-card:hover .rg-name { color: var(--blue); }

        .rg-logo {
            width: 56px;
            height: 56px;
            border-radius: 8px;
            background: var(--bg-elevated);
            object-fit: cover;
            flex-shrink: 0;
        }
        .rg-logo.placeholder {
            display: flex;
            align-items: center;
            justify-content: center;
            font-size: 1.4rem;
            font-weight: 700;
            color: var(--text-muted);
            text-transform: uppercase;
        }
        .rg-info { min-width: 0; flex: 1; }
        .rg-name {
            font-size: 1rem;
            font-weight: 700;
            color: var(--text-primary);
            transition: color 0.2s ease;
            white-space: nowrap;
            overflow: hidden;
            text-overflow: ellipsis;
        }
        .rg-meta {
            font-size: 0.72rem;
            color: var(--text-muted);
            margin-top: 0.35rem;
            text-transform: uppercase;
            letter-spacing: 0.06em;
            font-weight: 600;
        }
        .rg-empty {
            text-align: center;
            padding: 3rem 1rem;
            color: var(--text-muted);
            font-size: 0.9rem;
        }

`
