package news

// newsCSS is what the two fragments used to carry inline. Registered in
// Provision through pluginapi.RegisterStylesheet, which no-ops on a host
// that offers no sink -- there the pages simply draw unstyled, which is
// visible rather than silent.
const newsCSS = `/* news: the plugin's own stylesheet.

   Was two <style> blocks inside the fragments, which meant these bytes
   were re-sent in every document that used them -- 90 lines across four
   pages, unchanged between deploys. Registered once at Provision now;
   the host gives it a URL with a content hash and a year of caching.
   See docs/BACKLOG.md #13 in loon-demo-site. */

/* from news.html */
        /* One card per row. A grid across put two posts side by side, which
           reads as a catalogue of equal things — and news is not that: it is
           ordered, the top one matters most, and a reader goes down it. The
           reference index is a single column for the same reason.

           The card spans the full width; the TEXT inside it does not — see the
           measure on the excerpt below. */
        .news-feed {
            display: flex;
            flex-direction: column;
            gap: 0.75rem;
            padding: 0;
        }

        .news-card {
            /* The card has to LIFT off the panel it sits on, or it reads as a
               box drawn inside a box. --surface-3 is the palette's step above
               the panel in every theme; the fallbacks are for a host that has
               neither token, where a bordered transparent card is still a card. */
            position: relative;
            display: flex;
            flex-direction: column;
            background: var(--surface-3, var(--bg-elevated, transparent));
            border: 1px solid var(--border);
            border-radius: 10px;
            padding: 1.35rem 1.45rem 1.25rem;
            transition: border-color 0.15s ease, transform 0.15s ease, box-shadow 0.15s ease;
        }
        .news-card:hover {
            border-color: var(--blue);
            transform: translateY(-2px);
            box-shadow: 0 6px 18px rgba(0, 0, 0, 0.25);
        }
        /* The focus ring belongs to the CARD, not to the invisible overlay:
           tabbing to the headline must show the region that will open. */
        .news-card:focus-within {
            border-color: var(--blue);
            outline: 2px solid var(--blue);
            outline-offset: 2px;
        }

        .news-card__date {
            display: block;
            margin-bottom: 0.7rem;
            font-size: 0.72rem;
            font-variant-numeric: tabular-nums;
            color: var(--text-muted);
            text-transform: uppercase;
            letter-spacing: 0.05em;
        }

        /* The headline carries the card. At 1.08rem it was the same weight as
           the excerpt under it and the page had no hierarchy to scan; the
           reference sets its own near 1.5rem. */
        .news-card__title { margin: 0 0 0.35rem; font-size: 1.3rem; line-height: 1.3; font-weight: 700; }
        .news-card__title a { color: var(--text-primary); text-decoration: none; }
        .news-card:hover .news-card__title a { color: var(--blue); }
        /* The stretched link. inset:0 over a position:relative card, so the
           whole region is the target while the anchor's TEXT stays the
           headline. z-index keeps it above the card's own background without
           covering text selection elsewhere on the page. */
        .news-card__title a::after { content: ""; position: absolute; inset: 0; z-index: 1; }
        .news-card__title a:focus { outline: none; } /* the card carries it — see :focus-within */

        /* Full-strength text, not the site's muted grey: this is the post
           talking, and muted grey is the colour of captions and helper text.
           An excerpt set in it reads as a footnote to a headline. */
        .news-card__excerpt {
            margin: 0;
            font-size: 0.9rem;
            line-height: 1.6;
            color: var(--text-primary);
            /* The card is as wide as the panel; the line is not. Prose past
               about 75 characters costs the reader the return sweep — they lose
               which line they were on — and a full-width card at this width
               runs to 110. This is the one thing here the reference gets wrong,
               and it costs nothing to fix. */
            max-width: 74ch;
        }

        /* A span, not a second anchor. The card is already one link; a "Read
           more" that goes to the same place would be read out twice and tabbed
           to twice for no destination a reader does not already have. */
        .news-card__more { margin-top: 0.7rem; font-size: 0.82rem; font-weight: 600; color: var(--blue); }

/* from news_detail.html */
        /* Full-strength text, same reasoning as the feed's excerpt: this is
           the post talking, and muted grey is the colour of captions. */
        .news-body { font-size: 0.95rem; line-height: 1.7; color: var(--text-primary); }
        .news-body p { margin-bottom: 0.8rem; }
        .news-body h2, .news-body h3 { color: var(--text-primary); margin-top: 1.5rem; }
        .news-body a { color: var(--blue); }
        .news-body img { max-width: 100%; border-radius: 4px; margin: 0.5rem 0; }
        .news-body ul, .news-body ol { padding-left: 1.5rem; margin-bottom: 0.8rem; }
        /* The same card the FEED draws its previews in — a reader clicks a
           card there and lands on bare text floating on the page background
           here, which reads as leaving the news section rather than opening
           the post. Same tokens, same fallbacks. */
        .news-post-card {
            background: var(--surface-3, var(--bg-elevated, transparent));
            border: 1px solid var(--border);
            border-radius: 10px;
            padding: 1.6rem 1.75rem 1.5rem;
        }

`
