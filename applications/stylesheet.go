package applications

// applicationsCSS styles the staff queue. Registered once at Provision; the
// host gives it a URL, a content hash and a year of caching.
// RegisterStylesheet no-ops on a host with no sink, where the page draws
// unstyled -- visible rather than silent, which is the right failure for a
// missing seam.
//
// The stat tiles themselves are the host's .stat-tile component; only the
// strip that rows them up is this plugin's.
const applicationsCSS = `/* from applications_queue.html */
        .stat-strip { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 1rem; }
        .stat-strip .stat-tile { flex: 1 1 120px; }

        .app-queue { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 0.8rem; }
        .app-queue__item {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 8px;
            padding: 0.9rem 1.1rem;
        }
        .app-queue__head { display: flex; align-items: baseline; gap: 0.6rem; flex-wrap: wrap; font-size: 0.85rem; color: var(--text-muted); }
        .app-queue__head strong { color: var(--text-primary); }
        /* Their own words with their own paragraph breaks -- the thing being
           judged, so the formatting they chose is kept. anywhere guards the
           card against an unbroken run pushing the page wide. */
        .app-queue__body { margin: 0.6rem 0 0.8rem; font-size: 0.88rem; line-height: 1.5; white-space: pre-wrap; overflow-wrap: anywhere; }
        .app-queue__decide { display: flex; flex-wrap: wrap; gap: 0.5rem; align-items: center; }
        .app-queue__decide .form__input { flex: 1 1 16rem; min-width: 0; }
`
