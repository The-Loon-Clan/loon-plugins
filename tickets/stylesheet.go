package tickets

// ticketsCSS is what this plugin's fragments used to carry inline.
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
const ticketsCSS = `/* from support_ticket.html */
        .msg { max-width: 75%; padding: 0.65rem 0.9rem; border-radius: 12px; font-size: 0.88rem; line-height: 1.55; }
        .msg-user { background: rgba(var(--bs-primary-rgb), 0.15); margin-left: auto; border-bottom-right-radius: 4px; }
        .msg-admin { background: var(--bg-elevated); border-bottom-left-radius: 4px; }
        .msg-meta { font-size: 0.72rem; color: var(--text-muted); margin-top: 2px; }
        .msg-body { word-break: break-word; }
        .msg-body p:last-child { margin-bottom: 0; }
        .msg-body code { background: rgba(0,0,0,0.25); padding: 0.1em 0.35em; border-radius: 3px; font-size: 0.85em; }
        .msg-body blockquote { border-left: 3px solid var(--blue); padding: 0.1rem 0 0.1rem 0.7rem; margin: 0.3rem 0; color: var(--text-muted); }

        /* Bounded conversation pane — fixed height + internal scroll
           so the reply form stays anchored at the bottom of the
           viewport on long threads. Auto-scrolls to the most recent
           message on load (JS at the bottom). */
        .ticket-thread {
            max-height: 60vh;
            min-height: 320px;
            overflow-y: auto;
            padding-right: 0.5rem;
        }

/* from admin_tickets.html */
        /* The rows navigate via onclick, which no UA renders as clickable.
           The class name is a Tailwind utility the template already uses;
           defined here because no utility framework is loaded. */
        .cursor-pointer { cursor: pointer; }

`
