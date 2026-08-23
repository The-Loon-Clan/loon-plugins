package messages

// messagesCSS is what this plugin's fragments used to carry inline.
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
const messagesCSS = `/* from inbox.html */
        /* Two-pane: left list always visible, right pane swaps
           between empty / conversation / compose. Single shell
           so the grid stays put when the right pane changes. */
        .inbox-layout {
            display: grid;
            grid-template-columns: 340px minmax(0, 1fr);
            gap: 1rem;
            /* Stretch (the grid default), not start: the list and the pane
               are one surface split in two, and the short-card-beside-tall-
               pane look was half of what made the page read as scattered. */
            min-height: 560px;
        }
        @media (max-width: 820px) {
            .inbox-layout { grid-template-columns: 1fr; }
            /* A grid item's min-width is auto, so it cannot shrink below its
               widest child — and the list pane is full of white-space:nowrap
               (.ib-name, .ib-preview, .ib-meta). One long correspondent name
               therefore set the CARD's width and pushed it past the screen: on
               a 390px phone the pane measured 403 and the page scrolled
               sideways.
               Safe to let it shrink because each of those children already has
               overflow:hidden and text-overflow:ellipsis — they were written to
               truncate, and this is what finally lets them. */
            .inbox-layout > * { min-width: 0; }
        }

        /* ── Left pane ───────────────────────────────────────── */
        .ib-list-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 14px;
            padding: 0.85rem;
            /* A column, so the row list is the part that grows and scrolls
               while search and the summary stay put — the same shape the
               right pane already has. max-height matches .ib-pane because
               the two are halves of one surface. */
            display: flex;
            flex-direction: column;
            max-height: 80vh;
        }
        #ib-row-list {
            flex: 1;
            overflow-y: auto;
            min-height: 0;
            display: flex;
            flex-direction: column;
            gap: 0.5rem;
        }
        .ib-list-search {
            position: relative;
            margin-bottom: 0.7rem;
        }
        .ib-list-search input {
            width: 100%;
            background: var(--bg-base);
            border: 1px solid var(--border);
            border-radius: 8px;
            padding: 0.55rem 0.6rem 0.55rem 2.1rem;
            color: var(--text-primary);
            font-size: 0.85rem;
        }
        .ib-list-search input:focus { outline: none; border-color: var(--primary, var(--blue)); }
        .ib-list-search::before {
            content: "🔍";
            position: absolute;
            left: 0.65rem; top: 50%;
            transform: translateY(-50%);
            font-size: 0.85rem;
            opacity: 0.6;
            pointer-events: none;
        }
        .ib-list-summary {
            font-size: 0.78rem;
            color: var(--text-muted);
            padding: 0 0.2rem 0.5rem;
            display: flex;
            justify-content: space-between;
            align-items: center;
            gap: 0.5rem;
        }
        /* Each conversation is its own CARD — the same lift-off-the-panel
           treatment the news feed uses, and for the same NN/g reason: things
           inside one boundary read as one item, and a run of borderless rows
           read as one undifferentiated list. Colour comes from the host's
           SEMANTIC tokens (--primary and friends), not the literal --blue
           set, so the accent follows whichever theme the member picked. */
        .ib-row {
            display: flex;
            align-items: center;
            gap: 0.6rem;
            padding: 0.6rem 0.65rem;
            border-radius: 10px;
            background: var(--surface-3, var(--bg-elevated, transparent));
            border: 1px solid var(--border);
            text-decoration: none;
            color: var(--text-primary);
            transition: border-color 0.12s, background-color 0.12s;
        }
        .ib-row:hover { border-color: var(--primary, var(--blue)); }
        .ib-row.is-active {
            border-color: var(--primary, var(--blue));
            background: var(--primary-soft, var(--blue-tint-soft));
        }
        .ib-row.is-active .ib-name { color: var(--primary-tint, var(--blue)); }
        .ib-avatar {
            width: 36px; height: 36px;
            border-radius: 50%;
            object-fit: cover;
            border: 1px solid var(--border);
            flex-shrink: 0;
        }
        .ib-avatar-fallback {
            width: 36px; height: 36px;
            border-radius: 50%;
            background: var(--primary-soft, var(--blue-dim));
            color: var(--primary-tint, var(--blue));
            display: inline-flex;
            align-items: center; justify-content: center;
            font-weight: 700;
            font-size: 0.95rem;
            flex-shrink: 0;
        }
        .ib-avatar-system {
            background: var(--primary, var(--blue));
            color: #fff;
        }
        .ib-body { flex: 1; min-width: 0; }
        .ib-name {
            font-size: 0.88rem;
            font-weight: 600;
            color: var(--text-strong, var(--text-primary));
            white-space: nowrap;
            overflow: hidden;
            text-overflow: ellipsis;
        }
        .ib-preview {
            font-size: 0.76rem;
            color: var(--text-muted);
            white-space: nowrap;
            overflow: hidden;
            text-overflow: ellipsis;
        }
        .ib-meta {
            font-size: 0.7rem;
            color: var(--muted-2, var(--text-muted));
            white-space: nowrap;
            display: flex;
            flex-direction: column;
            align-items: flex-end;
            gap: 0.2rem;
        }
        .ib-unread {
            display: inline-flex;
            align-items: center; justify-content: center;
            min-width: 18px; height: 18px;
            padding: 0 5px;
            border-radius: 9px;
            background: var(--primary, var(--blue));
            color: #fff;
            font-size: 0.65rem;
            font-weight: 700;
        }

        /* ── Right pane (shared shell, three modes) ─────────── */
        .ib-pane {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 14px;
            display: flex;
            flex-direction: column;
            min-height: 560px;
            max-height: 80vh;
        }
        .ib-pane-head {
            display: flex;
            justify-content: space-between;
            align-items: center;
            padding: 0.85rem 1rem;
            border-bottom: 1px solid var(--border);
            font-size: 0.9rem;
        }
        .ib-pane-head strong { color: var(--text-strong, var(--text-primary)); }
        .ib-pane-head .ib-pane-sub {
            color: var(--text-muted);
            font-size: 0.78rem;
            margin-top: 2px;
        }
        .ib-pane-body {
            flex: 1;
            overflow-y: auto;
            padding: 1rem;
        }
        .ib-pane-foot {
            padding: 0.75rem 0.9rem;
            border-top: 1px solid var(--border);
            display: flex;
            justify-content: space-between;
            align-items: center;
        }

        /* DM stream messages */
        .ib-stream { display: flex; flex-direction: column; gap: 0.6rem; }
        .ib-msg {
            max-width: 70%;
            padding: 0.55rem 0.85rem;
            border-radius: 12px;
            font-size: 0.88rem;
            line-height: 1.4;
            word-break: break-word;
        }
        .ib-msg p:last-child { margin-bottom: 0; }
        .ib-msg-meta {
            font-size: 0.7rem;
            color: var(--text-muted);
            margin: 0 0.25rem 0.15rem;
        }
        .ib-msg-from-them {
            align-self: flex-start;
            background: var(--bg-elevated);
            border: 1px solid var(--border);
        }
        .ib-msg-from-me {
            align-self: flex-end;
            background: var(--primary, var(--blue));
            color: #fff;
        }
        .ib-msg-row { display: flex; flex-direction: column; }
        .ib-msg-row.from-them { align-items: flex-start; }
        .ib-msg-row.from-me   { align-items: flex-end; }

        /* Empty state */
        .ib-empty {
            display: flex;
            flex-direction: column;
            align-items: center; justify-content: center;
            text-align: center;
            padding: 3rem 1.5rem;
            color: var(--text-muted);
            flex: 1;
        }
        .ib-empty .ib-empty-icon {
            font-size: 2.5rem;
            width: 2.5rem;
            height: 2.5rem;
            opacity: 0.6;
            margin-bottom: 0.5rem;
        }
        .ib-empty h2 {
            font-size: 1.15rem;
            color: var(--text-primary);
            margin: 0.5rem 0;
        }
        .ib-empty p {
            font-size: 0.88rem;
            margin: 0 0 1rem;
        }

        .ib-no-permission {
            background: var(--bg-elevated);
            border: 1px solid var(--border);
            border-radius: 10px;
            padding: 0.75rem 0.9rem;
            font-size: 0.82rem;
            color: var(--text-muted);
            margin-bottom: 0.75rem;
        }
        .ib-no-permission strong { color: var(--text-primary); }

        /* Compose form */
        .ib-compose label {
            display: block;
            font-size: 0.72rem;
            color: var(--text-muted);
            text-transform: uppercase;
            letter-spacing: 0.04em;
            font-weight: 500;
            margin: 0 0 4px;
        }
        .ib-compose input[type="text"] {
            width: 100%;
            background: var(--bg-base);
            border: 1px solid var(--border);
            border-radius: 6px;
            padding: 0.55rem 0.7rem;
            color: var(--text-primary);
            font-size: 0.88rem;
            font-family: inherit;
            margin-bottom: 0.9rem;
        }
        .ib-compose input[type="text"]:focus { outline: none; border-color: var(--primary, var(--blue)); }

`
