package requests

// requestsCSS is what this plugin's fragments used to carry inline.
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
const requestsCSS = `
/* The pre-paint view switch. An inline script on the requests board adds one
   of these to <html> before the body paints, from the mode saved in
   localStorage; these rules are what it used to write as a <style> element.
   Keeping them here is what lets the host forbid inline style elements. */
.rq-view-grid #view-table { display: none; }
.rq-view-list #view-grid { display: none; }

/* from community_request_detail.html */
        .datagrid { display:grid; grid-template-columns:repeat(auto-fill,minmax(160px,1fr)); gap:0.75rem 1.25rem; }
        .datagrid-title { font-size:0.72rem; color:var(--text-muted); text-transform:uppercase; letter-spacing:0.04em; margin-bottom:3px; font-weight:500; }
        .datagrid-content { font-size:0.875rem; color:var(--text-primary); }
        /* A modifier, not a second .scrape-file. The list page defines the
           same class with tighter padding and no font-size, and once both
           sheets are merged the later rule would win on both pages. Only
           this page wants the roomier line. */
        .scrape-file--detail { padding:3px 0; font-size:0.82rem; }
        .scrape-file:last-child { border-bottom:none; }

/* from community_requests.html */
        .vote-btn { cursor:pointer; border:none; background:none; padding:2px 6px; font-size:0.82rem; }
        .vote-btn:hover { opacity:0.8; }
        .scrape-result { font-size:0.78rem; color:var(--text-muted); margin-top:0.5rem; }
        .scrape-file { padding:2px 0; border-bottom:1px solid var(--border); }
        .scrape-file:last-child { border-bottom:none; }
        .anime-preview { font-size:0.78rem; color:var(--text-muted); margin-top:0.5rem; padding:0.5rem; background:var(--bg-elevated); border-radius:4px; }
        /* "Already on the site?" rows — same muted compact lines as the
           release page's similar-releases list. */
        .existing-release-row { font-size:0.78rem; padding:3px 0; border-bottom:1px solid var(--border); }
        .existing-release-row:last-child { border-bottom:none; }
        .existing-release-row .badge { font-size:0.65rem; font-weight:500; }
        .existing-release-meta { color:var(--text-muted); }

        /* Cover-card overrides for the request/feed grids:
           - drop-shadow on every card so blank-cover ones still read
             as cards (the default nzb-card relies on the artwork
             itself to pop; without art the card disappeared into the
             page bg).
           - placeholder gets a subtle stripe pattern so it looks
             intentional rather than broken. */
        .cover-card {
            box-shadow: 0 2px 6px rgba(0,0,0,0.45);
        }
        .cover-card:hover {
            box-shadow: 0 4px 10px rgba(0,0,0,0.55);
        }
        .cover-card .cover-img-placeholder {
            background:
                repeating-linear-gradient(
                    45deg,
                    var(--bg-elevated) 0 12px,
                    rgba(255,255,255,0.025) 12px 14px
                );
            border-bottom:1px solid var(--border);
        }
        /* Footer-button uniformity: bootstrap's per-color-variant
           rendering (and its line-height handling around 'Q'-tail
           glyphs) was making NZB / Queued / Request / Parked render at
           subtly different heights despite identical classes. Lock the
           geometry explicitly so visual consistency doesn't depend on
           glyph metrics. */
        .cover-card .card-footer .btn {
            min-height: 30px;
            padding: 4px 10px;
            line-height: 1.2;
            font-size: 0.82rem;
            display: flex;
            align-items: center;
            justify-content: center;
        }

`
