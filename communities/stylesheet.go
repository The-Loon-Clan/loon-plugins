package communities

// communitiesCSS is what this plugin's fragments used to carry inline.
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
const communitiesCSS = `/* from communities_index.html */
        .c-page { padding: 1.4rem 1rem 3rem; }
        /* One card per row: the banner IS the card. The card is a thin frame
           of surface around the image; every word sits ON the banner over a
           left-weighted scrim, so any artwork stays legible without asking
           uploaders to leave dead space for text. */
        .c-list { display:flex; flex-direction:column; gap:1rem; max-width: 1100px; margin: 0 auto; }
        .c-card {
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 14px;
            padding: 0.6rem;
            display: block;
            text-decoration: none;
            color: inherit;
            transition: border-color 0.15s ease, transform 0.15s ease;
        }
        .c-card:hover { border-color: var(--blue); transform: translateY(-2px); }
        .c-banner {
            position: relative;
            border-radius: 10px;
            overflow: hidden;
            min-height: 190px;
            padding: 1.1rem 1.4rem;
            display: flex; flex-direction: column; justify-content: space-between; gap: 1.4rem;
            background: linear-gradient(135deg, rgba(91,138,245,0.35), rgba(168,85,247,0.25));
            background-size: cover; background-position: center;
        }
        /* The scrim: strongest where the text lives, gone by mid-image. A
           separate layer rather than a text-shadow alone, because a shadow
           cannot rescue white text over a white sky. */
        .c-banner::before {
            content: ""; position: absolute; inset: 0;
            background: linear-gradient(90deg, rgba(8,10,16,0.72), rgba(8,10,16,0.35) 45%, rgba(8,10,16,0.05) 75%);
        }
        .c-banner > * { position: relative; }
        .c-name {
            margin: 0; font-size: 1.55rem; font-weight: 800; line-height: 1.15;
            color: #fff; text-shadow: 0 2px 8px rgba(0,0,0,0.75);
            max-width: 32ch;
        }
        .c-info { color: #fff; text-shadow: 0 1px 5px rgba(0,0,0,0.8); max-width: 60ch; }
        .c-meta { font-size: 0.78rem; opacity: 0.92; }
        .c-meta strong { font-variant-numeric: tabular-nums; }
        .c-desc { font-size: 0.86rem; line-height: 1.45; margin-top: 0.3rem; opacity: 0.95; }
        .c-slug { font-size: 0.72rem; opacity: 0.75; }
        .c-empty { text-align:center; padding: 3rem 1rem; color: var(--text-muted); font-size: 0.9rem; }
        @media (max-width: 640px) {
            .c-banner { min-height: 150px; padding: 0.9rem 1rem; }
            .c-name { font-size: 1.2rem; }
        }

/* from community_join_requests.html */
        .rq-card { background: var(--bg-surface); border: 1px solid var(--border); border-radius: 12px; padding: 1rem 1.15rem; margin-bottom: 0.7rem; }
        .rq-head { display: flex; align-items: center; gap: 0.6rem; margin-bottom: 0.5rem; }
        .rq-avatar { width: 32px; height: 32px; border-radius: 50%; background: var(--bg-elevated); overflow: hidden; display:inline-flex; align-items:center; justify-content:center; font-weight:700; color:var(--blue); }
        .rq-avatar img { width:100%; height:100%; object-fit:cover; }
        .rq-stats { display:flex; gap:0.7rem; font-size:0.74rem; color: var(--text-muted); flex-wrap:wrap; }
        .rq-msg { font-size: 0.86rem; color: var(--text-primary); background: var(--bg-elevated); border-radius: 8px; padding: 0.5rem 0.7rem; margin: 0.5rem 0; }
        .rq-actions { display:flex; gap:0.5rem; align-items:center; flex-wrap:wrap; }
        .rq-actions input[type=text] { width: 220px; font-size: 0.78rem; }
        .inv-row { display:flex; align-items:center; gap:0.6rem; font-size:0.8rem; padding:0.4rem 0; border-bottom:1px solid var(--border); }
        .inv-row:last-child { border-bottom:none; }
        .inv-code { font-family:monospace; color:var(--blue); }

/* from community_thread_c.html */
        .ct-page { padding: 1.4rem 1rem 3rem; }
        .ct-shell { display: grid; grid-template-columns: minmax(0, 1fr) 320px; gap: 1.1rem; align-items: start; }
        @media (max-width: 1000px) { .ct-shell { grid-template-columns: 1fr; } }

        .ct-header { background: var(--bg-surface); border: 1px solid var(--border); border-radius: 14px; padding: 1.1rem 1.25rem; margin-bottom: 1rem; }
        .ct-header h1 { font-size: 1.45rem; font-weight: 800; margin: 0 0 0.4rem; color: var(--text-primary); }
        .ct-meta { font-size: 0.78rem; color: var(--text-muted); display: flex; gap: 0.7rem; flex-wrap: wrap; }
        .ct-meta a { color: var(--blue); text-decoration: none; }
        .ct-body { font-size: 0.95rem; color: var(--text-primary); margin-top: 0.85rem; line-height: 1.55; }
        .ct-body p { margin: 0 0 0.7rem; }

        .ct-mod-bar { display: flex; gap: 0.4rem; margin-top: 0.6rem; flex-wrap: wrap; }
        .ct-mod-bar form { display: inline-block; }

        .ct-post { background: var(--bg-surface); border: 1px solid var(--border); border-radius: 12px; padding: 0.9rem 1.05rem; margin-bottom: 0.6rem; }
        .ct-post-head { display: flex; align-items: center; gap: 0.55rem; font-size: 0.8rem; color: var(--text-muted); margin-bottom: 0.45rem; }
        .ct-avatar { width: 26px; height: 26px; border-radius: 50%; background: var(--bg-elevated); overflow: hidden; flex-shrink: 0; display:inline-flex; align-items:center; justify-content:center; font-weight:700; color:var(--blue); font-size: 0.78rem; }
        .ct-avatar img { width:100%; height:100%; object-fit:cover; }
        .ct-post-user { color: var(--text-primary); font-weight: 600; text-decoration: none; }
        .ct-post-body { font-size: 0.92rem; color: var(--text-primary); line-height: 1.5; }
        .ct-post-body p { margin: 0 0 0.55rem; }

        .ct-reply-form { background: var(--bg-surface); border: 1px solid var(--border); border-radius: 12px; padding: 0.9rem 1rem; margin-top: 1rem; }
        .ct-reply-form textarea { width: 100%; background: var(--bg-elevated); border: 1px solid var(--border); border-radius: 8px; color: var(--text-primary); padding: 0.6rem 0.75rem; font-size: 0.9rem; min-height: 96px; resize: vertical; }

        /* Sidebar reused from community_view */
        .c-side { display: flex; flex-direction: column; gap: 0.9rem; }
        .c-side-card { background: var(--bg-surface); border: 1px solid var(--border); border-radius: 12px; padding: 0.95rem 1rem; }
        .c-side-card h2 { font-size: 0.78rem; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 700; color: var(--text-muted); margin: 0 0 0.6rem; }
        .c-rule { border-bottom: 1px solid var(--border); font-size: 0.85rem; }
        .c-rule:last-child { border-bottom: none; }
        .c-rule > summary { display: flex; align-items: flex-start; gap: 0.4rem; padding: 0.5rem 0; cursor: pointer; list-style: none; }
        .c-rule > summary::-webkit-details-marker { display: none; }
        .c-rule > summary::marker { content: ""; }
        .c-rule-n { color: var(--text-muted); font-weight: 700; flex-shrink: 0; min-width: 1.1em; }
        .c-rule-title { font-weight: 600; color: var(--text-primary); flex: 1; min-width: 0; }
        .c-rule-chev { color: var(--text-muted); flex-shrink: 0; transition: transform 0.15s ease; }
        .c-rule[open] > summary .c-rule-chev { transform: rotate(180deg); }
        .c-rule-static { display: flex; align-items: flex-start; gap: 0.4rem; padding: 0.5rem 0; }
        .c-rule-body { font-size: 0.78rem; color: var(--text-muted); padding: 0 0 0.55rem 1.5rem; white-space: pre-wrap; }
        .c-mod-list { display: flex; flex-direction: column; gap: 0.4rem; }
        .c-mod { display: flex; align-items: center; gap: 0.55rem; text-decoration: none; color: var(--text-primary); font-size: 0.82rem; }
        .c-mod .m-avatar { width: 22px; height: 22px; border-radius: 50%; background: var(--bg-elevated); overflow: hidden; flex-shrink: 0; }
        .c-mod .m-avatar img { width:100%; height:100%; object-fit:cover; }
        .c-mod .m-role { font-size: 0.66rem; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.04em; }

/* from community_view.html */
        /* A modifier, not a redefinition of .c-page. Both this page and the
           index define .c-page, with different top padding, and they only
           got away with it while each shipped its CSS inside its own
           fragment. In one stylesheet the later rule would win on both
           pages and the index would lose its top spacing. This page zeroes
           it because it opens with a banner hero; that is what the name
           now says. */
        .c-page--hero { padding-top: 0; }
        /* Header banner — same recipe as the wiki hero. Falls back
           to a gradient panel when banner_url is empty. */
        .c-header {
            background: linear-gradient(135deg, rgba(91,138,245,0.20), rgba(168,85,247,0.15));
            background-size: cover; background-position: center;
            height: 140px;
            border-radius: 0 0 14px 14px;
            margin: 0 -1rem 0;
        }
        .c-id-row {
            display: flex; align-items: center; gap: 0.9rem;
            margin: -42px 0 1rem 0.4rem;
            position: relative; z-index: 1;
        }
        .c-icon-lg {
            width: 84px; height: 84px; border-radius: 14px;
            border: 4px solid var(--bg-page, #0a0a0e);
            background: var(--bg-elevated);
            color: var(--blue);
            display: inline-flex; align-items: center; justify-content: center;
            font-weight: 800; font-size: 1.9rem;
            overflow: hidden;
            flex-shrink: 0;
        }
        .c-icon-lg img { width:100%; height:100%; object-fit:cover; }
        .c-title-block { padding-top: 36px; }
        .c-title-block h1 { font-size: 1.6rem; font-weight: 800; margin: 0; color: var(--text-primary); }
        .c-title-block .slug { font-size: 0.78rem; color: var(--text-muted); }

        /* 2-col layout for thread list + sidebar. Collapses on narrow. */
        .c-shell { display: grid; grid-template-columns: minmax(0, 1fr) 320px; gap: 1.1rem; align-items: start; }
        @media (max-width: 1000px) { .c-shell { grid-template-columns: 1fr; } }

        /* Header action buttons — sit over the banner, so give them a
           solid translucent backing + bright text instead of the
           low-contrast outline-on-image they had before. */
        /* wrap, because these buttons are nowrap by design and there are four
           of them for a member who can moderate: Joined, New thread, Queue,
           Settings. On a 390px screen the row pushed the whole PAGE sideways
           by 189px -- the only member-facing page that did, and it went
           unseen because a community is not in the sitemap and the mobile
           check read its page list from there. */
        .c-actions { display:flex; flex-wrap:wrap; gap:0.45rem; align-items:center; }

        /* Below the phone breakpoint the identity row stacks: an avatar, a
           title and a four-button toolbar do not share 390px, and ms-auto
           pushing the toolbar right is what makes it overflow rather than
           sit under the title. */
        @media (max-width: 560px) {
            .c-id-row { flex-wrap: wrap; }
            .c-actions { margin-left: 0; padding-top: 0.5rem; width: 100%; }
        }
        .c-actions .c-btn {
            font-size: 0.8rem; font-weight: 600;
            padding: 0.35rem 0.85rem; border-radius: 8px;
            text-decoration: none; white-space: nowrap;
            background: rgba(20, 22, 34, 0.7);
            border: 1px solid rgba(255,255,255,0.18);
            color: #fff;
            backdrop-filter: blur(2px);
            transition: background 0.12s ease, border-color 0.12s ease;
        }
        .c-actions .c-btn:hover { background: rgba(20,22,34,0.92); border-color: rgba(255,255,255,0.4); }
        .c-actions .c-btn.primary { background: var(--blue); border-color: var(--blue); }
        .c-actions .c-btn.primary:hover { filter: brightness(1.1); }
        .c-actions .c-btn .qbadge { background: #facc15; color:#1c1f27; font-size:0.66rem; font-weight:800; border-radius:999px; padding:0 6px; margin-left:4px; }

        /* Reddit-style post rows — avatar + author/time top line, bold
           title, body excerpt, and a comments pill in the meta row. */
        .c-thread-list { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 0.55rem; }
        /* The row is a DIV (not an <a>) — nesting the username <a>
           inside an <a> is invalid HTML and the browser splits the
           DOM, which is what fragmented the row into stray boxes.
           The title is the link, stretched over the whole card via
           Bootstrap's .stretched-link; the username link sits above
           it (z-index) so it stays independently clickable. */
        .c-thread {
            position: relative;
            display: flex; align-items: flex-start; gap: 0.75rem;
            background: var(--bg-surface);
            border: 1px solid var(--border);
            border-radius: 12px;
            padding: 0.8rem 1rem;
            color: inherit;
            transition: border-color 0.12s ease, background 0.12s ease;
        }
        .c-thread:hover { border-color: var(--blue); background: var(--bg-elevated); }
        .c-thread .t-avatar { width: 32px; height: 32px; border-radius: 50%; background: var(--bg-elevated); overflow: hidden; flex-shrink: 0; display:inline-flex; align-items:center; justify-content:center; font-weight:700; color:var(--blue); margin-top:2px; }
        .c-thread .t-avatar img { width:100%; height:100%; object-fit:cover; }
        .c-thread .t-body { flex: 1; min-width: 0; }
        .c-thread .t-byline { font-size: 0.72rem; color: var(--text-muted); margin-bottom: 0.15rem; }
        .c-thread .t-byline a { position: relative; z-index: 2; color: var(--text-secondary, #9aa4b2); text-decoration: none; font-weight: 600; }
        .c-thread .t-byline a:hover { color: var(--blue); }
        .c-thread a.t-title { display:block; font-size: 1.02rem; font-weight: 700; color: var(--text-primary); margin: 0 0 0.25rem; line-height: 1.3; text-decoration: none; }
        .c-thread:hover a.t-title { color: var(--blue); }
        .c-thread .t-excerpt { font-size: 0.84rem; color: var(--text-muted); line-height: 1.4; margin: 0 0 0.45rem;
            display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
        .c-thread .t-meta { font-size: 0.74rem; color: var(--text-muted); display: flex; gap: 0.9rem; flex-wrap: wrap; align-items:center; }
        .c-thread .t-meta .pill { display:inline-flex; align-items:center; gap:0.3rem; }
        .badge-pinned { font-size:0.6rem;font-weight:700;text-transform:uppercase;padding:1px 6px;border-radius:4px;background:rgba(34,197,94,0.18);color:#22c55e;border:1px solid rgba(34,197,94,0.4);vertical-align:2px;margin-right:0.35rem; }
        .badge-locked { font-size:0.6rem;font-weight:700;text-transform:uppercase;padding:1px 6px;border-radius:4px;background:rgba(248,113,113,0.18);color:#f87171;border:1px solid rgba(248,113,113,0.4);vertical-align:2px;margin-right:0.35rem; }

        /* Sidebar */
        .c-side { display: flex; flex-direction: column; gap: 0.9rem; }
        .c-side-card { background: var(--bg-surface); border: 1px solid var(--border); border-radius: 12px; padding: 0.95rem 1rem; }
        .c-side-card h2 { font-size: 0.78rem; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 700; color: var(--text-muted); margin: 0 0 0.6rem; }
        .c-side-md { font-size: 0.86rem; color: var(--text-primary); line-height: 1.5; }
        .c-side-md p { margin: 0 0 0.5rem; }
        .c-side-stats { display: flex; gap: 0.95rem; font-size: 0.82rem; }
        .c-side-stats strong { color: var(--text-primary); font-variant-numeric: tabular-nums; font-size: 0.95rem; }
        .c-side-stats span { color: var(--text-muted); }

        /* Rules: collapsible via <details>/<summary> (Reddit-style —
           click the row/chevron to reveal the rule body). No JS. */
        .c-rule { border-bottom: 1px solid var(--border); font-size: 0.85rem; }
        .c-rule:last-child { border-bottom: none; }
        .c-rule > summary { display: flex; align-items: flex-start; gap: 0.4rem; padding: 0.5rem 0; cursor: pointer; list-style: none; }
        .c-rule > summary::-webkit-details-marker { display: none; } /* hide default disclosure triangle */
        .c-rule > summary::marker { content: ""; }
        .c-rule-n { color: var(--text-muted); font-weight: 700; flex-shrink: 0; min-width: 1.1em; }
        .c-rule-title { font-weight: 600; color: var(--text-primary); flex: 1; min-width: 0; }
        .c-rule-chev { color: var(--text-muted); flex-shrink: 0; transition: transform 0.15s ease; }
        .c-rule[open] > summary .c-rule-chev { transform: rotate(180deg); }
        .c-rule-static { display: flex; align-items: flex-start; gap: 0.4rem; padding: 0.5rem 0; }
        .c-rule-body { font-size: 0.78rem; color: var(--text-muted); padding: 0 0 0.55rem 1.5rem; white-space: pre-wrap; }

        .c-mod-list { display: flex; flex-direction: column; gap: 0.4rem; }
        .c-mod { display: flex; align-items: center; gap: 0.55rem; text-decoration: none; color: var(--text-primary); font-size: 0.82rem; }
        .c-mod .m-avatar { width: 22px; height: 22px; border-radius: 50%; background: var(--bg-elevated); overflow: hidden; flex-shrink: 0; }
        .c-mod .m-avatar img { width:100%; height:100%; object-fit:cover; }
        .c-mod .m-role { font-size: 0.66rem; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.04em; }

`
