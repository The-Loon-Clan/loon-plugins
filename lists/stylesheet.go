package lists

// listsCSS is what this plugin's fragments used to carry inline.
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
const listsCSS = `/* from community_watchlists.html */
        /* Watchlist-specific overrides on top of nzb-card-css. The
           card body shows creator + title + item count; no resolution
           overlay since lists aren't single-NZB rows. */
        .wl-card { box-shadow: 0 2px 6px rgba(0,0,0,0.45); }
        .wl-card:hover { box-shadow: 0 4px 12px rgba(0,0,0,0.55); }
        .wl-creator {
            display: flex; align-items: center; gap: 4px;
            font-size: 0.78rem; font-weight: 600;
            color: var(--bs-info);
            text-decoration: none;
            margin-bottom: 4px;
        }
        .wl-creator:hover { color: var(--bs-primary); }
        .wl-title {
            font-size: 0.92rem; font-weight: 600; color: var(--text-primary);
            line-height: 1.25; overflow: hidden;
            display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical;
            margin-bottom: 8px;
        }
        .wl-itemcount {
            text-align: center; padding: 6px 0;
            font-size: 0.82rem; color: var(--text-muted);
            border-top: 1px solid var(--border);
        }
        .wl-toolbar {
            display: flex; gap: 0.6rem; align-items: center; flex-wrap: wrap;
            margin-bottom: 1rem;
        }
        .wl-toolbar input, .wl-toolbar select {
            min-width: 0;
        }
        .wl-no-cover {
            background:
                repeating-linear-gradient(
                    45deg,
                    var(--bg-elevated) 0 12px,
                    rgba(255,255,255,0.025) 12px 14px
                );
        }

/* from list_detail.html */
        /* Banner — uses the list's primary cover blown out as a
           background, with a dark gradient overlay so the title +
           description stay legible. Falls back to a subtle stripe
           pattern when no cover is available. */
        .list-banner {
            position: relative;
            min-height: 280px;
            border-radius: 8px;
            overflow: hidden;
            background:
                linear-gradient(180deg, rgba(0,0,0,0.05) 0%, rgba(0,0,0,0.85) 100%),
                repeating-linear-gradient(
                    45deg,
                    var(--bg-elevated) 0 16px,
                    rgba(255,255,255,0.03) 16px 18px
                );
            background-size: cover;
            background-position: center;
            margin-bottom: 1.25rem;
        }
        .list-banner.has-cover {
            background:
                linear-gradient(180deg, rgba(0,0,0,0.45) 0%, rgba(0,0,0,0.85) 100%),
                var(--banner-url) center / cover no-repeat;
        }
        .list-banner-inner {
            position: relative; z-index: 1;
            padding: 1.25rem 1.5rem;
            display: flex; gap: 1.25rem;
            min-height: 280px;
            align-items: center;
        }
        .list-banner-cover {
            width: 140px; height: 200px;
            object-fit: cover; border-radius: 6px;
            box-shadow: 0 6px 18px rgba(0,0,0,0.55);
            flex-shrink: 0;
        }
        .list-banner-body { min-width: 0; flex: 1; }
        .list-banner-title {
            font-size: 1.6rem; font-weight: 700;
            color: #fff;
            text-shadow: 0 2px 8px rgba(0,0,0,0.7);
            margin-bottom: 0.4rem;
        }
        .list-banner-creator {
            display: inline-flex; align-items: center; gap: 5px;
            color: var(--bs-info); font-weight: 600; font-size: 0.92rem;
            text-decoration: none;
        }
        .list-banner-creator:hover { color: var(--bs-primary); }
        .list-banner-desc {
            color: rgba(255,255,255,0.88);
            font-size: 0.92rem; line-height: 1.5;
            margin-top: 0.6rem;
            white-space: pre-wrap;
        }
        .list-stats-row {
            display: flex; gap: 0.6rem; flex-wrap: wrap;
            margin-top: 0.8rem;
        }
        .list-stat {
            background: rgba(0,0,0,0.45);
            color: rgba(255,255,255,0.92);
            font-size: 0.78rem;
            padding: 3px 10px;
            border-radius: 12px;
            border: 1px solid rgba(255,255,255,0.1);
        }
        .sidebar-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 6px;
            padding: 0.85rem 1rem;
            margin-bottom: 1rem;
        }
        .sidebar-card h6 {
            font-size: 0.72rem; font-weight: 700; letter-spacing: 0.06em;
            text-transform: uppercase; color: var(--text-muted);
            margin: 0 0 0.6rem 0;
        }
        .sidebar-card .row-line {
            display: flex; justify-content: space-between;
            font-size: 0.82rem; color: var(--text-muted);
            padding: 4px 0;
        }
        .sidebar-card .row-line span:last-child { color: var(--text-primary); }
        .empty-state {
            text-align: center; padding: 3rem;
            color: var(--text-muted);
            background: var(--bg-surface);
            border: 1px dashed var(--border);
            border-radius: 8px;
        }
        @media (max-width: 768px) {
            .list-banner-inner { flex-direction: column; text-align: center; }
        }

/* from user_lists.html */
        .form-control { background: var(--bg-surface); color: var(--text-primary); border-color: var(--border); }
        .form-control:focus { background: var(--bg-surface); color: var(--text-primary); border-color: var(--blue); box-shadow: none; }

`
