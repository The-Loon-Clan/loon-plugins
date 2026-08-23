package chat

// chatCSS is what this plugin's fragments used to carry inline.
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
const chatCSS = `/* from chat.html */
        /* ── Discord-style 3-column layout ── */
        .chat-layout {
            display: flex;
            height: calc(100vh - 56px); /* below navbar */
            overflow: hidden;
        }

        /* Left: channels */
        .chat-sidebar {
            width: 200px;
            flex-shrink: 0;
            background: var(--bg-elevated, #1a1d24);
            border-right: 1px solid var(--border);
            padding: 1rem 0;
            overflow-y: auto;
        }
        .chat-sidebar h6 {
            font-size: 0.68rem;
            text-transform: uppercase;
            letter-spacing: 0.06em;
            color: var(--text-muted);
            padding: 0 0.75rem;
            margin-bottom: 0.4rem;
        }
        .chat-channel {
            display: flex;
            align-items: center;
            gap: 0.4rem;
            padding: 0.35rem 0.75rem;
            font-size: 0.88rem;
            color: var(--text-muted);
            text-decoration: none;
            border-radius: 4px;
            margin: 0 0.4rem;
        }
        .chat-channel:hover { background: rgba(255,255,255,0.05); color: var(--text); }
        .chat-channel.active { background: rgba(91,138,245,0.15); color: var(--text); }
        .chat-channel .hash { color: var(--text-muted); font-weight: 700; }

        /* Center: messages */
        .chat-center {
            flex: 1;
            display: flex;
            flex-direction: column;
            min-width: 0;
        }
        .chat-header {
            padding: 0.6rem 1rem;
            border-bottom: 1px solid var(--border);
            font-size: 0.88rem;
            font-weight: 600;
            display: flex;
            align-items: center;
            gap: 0.5rem;
        }
        .chat-header .hash { color: var(--text-muted); font-size: 1.1rem; }
        .chat-header .desc { font-weight: 400; color: var(--text-muted); font-size: 0.78rem; margin-left: 0.5rem; }
        .chat-stream {
            flex: 1;
            overflow-y: auto;
            padding: 0.5rem 1rem;
        }
        .chat-msg {
            display: flex;
            gap: 0.6rem;
            padding: 0.3rem 0;
        }
        .chat-msg:hover { background: rgba(255,255,255,0.02); }
        .chat-avatar {
            width: 32px;
            height: 32px;
            border-radius: 50%;
            object-fit: cover;
            flex-shrink: 0;
            margin-top: 2px;
        }
        .chat-avatar-fallback {
            width: 32px;
            height: 32px;
            border-radius: 50%;
            background: #3a7bd5;
            display: flex;
            align-items: center;
            justify-content: center;
            color: #fff;
            font-weight: 700;
            font-size: 0.85rem;
            flex-shrink: 0;
            margin-top: 2px;
        }
        .chat-meta { font-size: 0.78rem; color: var(--text-muted); margin-bottom: 0.1rem; }
        .chat-author { font-weight: 600; color: var(--text); margin-right: 0.4rem; }
        .chat-author.role-admin { color: #f87171; }
        .chat-author.role-mod { color: #fbbf24; }
        .chat-author.role-contributor { color: #38bdf8; }
        .chat-body { font-size: 0.9rem; line-height: 1.35; word-wrap: break-word; white-space: pre-wrap; }
        .chat-source-badge {
            display: inline-block; font-size: 0.6rem; padding: 0 0.25rem;
            border-radius: 3px; background: rgba(91,138,245,0.15); color: #5b8af5;
            text-transform: uppercase; letter-spacing: 0.04em; vertical-align: middle;
        }
        .chat-status {
            font-size: 0.75rem; color: var(--text-muted);
            padding: 0.4rem 1rem; border-top: 1px solid var(--border);
            display: flex; justify-content: space-between; align-items: center;
        }
        .chat-status .dot {
            display: inline-block; width: 8px; height: 8px;
            border-radius: 50%; background: #6b7485; margin-right: 0.3rem; vertical-align: middle;
        }
        .chat-status .dot.live { background: #4ade80; }
        .chat-status .dot.error { background: #f87171; }

        /* Right: online users */
        .chat-users {
            width: 200px;
            flex-shrink: 0;
            background: var(--bg-elevated, #1a1d24);
            border-left: 1px solid var(--border);
            padding: 1rem 0;
            overflow-y: auto;
        }
        .chat-users h6 {
            font-size: 0.68rem;
            text-transform: uppercase;
            letter-spacing: 0.06em;
            color: var(--text-muted);
            padding: 0 0.75rem;
            margin-bottom: 0.5rem;
        }
        .chat-user {
            display: flex;
            align-items: center;
            gap: 0.5rem;
            padding: 0.25rem 0.75rem;
            font-size: 0.82rem;
            color: var(--text-muted);
        }
        .chat-user-dot {
            width: 8px; height: 8px; border-radius: 50%;
            background: #4ade80; flex-shrink: 0;
        }
        .chat-user-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

        /* ── Mobile: hide sidebars ── */
        @media (max-width: 900px) {
            .chat-sidebar { display: none; }
            .chat-users { display: none; }
        }
        @media (max-width: 600px) {
            .chat-layout { height: calc(100vh - 56px); }
        }

`
