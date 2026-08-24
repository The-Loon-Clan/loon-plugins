#!/usr/bin/env python3
"""Regenerate trackerdir/directory.json from a clone of Prowlarr/Indexers.

    python scripts/gen_trackerdir.py /path/to/prowlarr-indexers

WHAT THIS TAKES, AND WHAT IT DELIBERATELY DOES NOT
--------------------------------------------------
The upstream repo is ~545 Cardigann YAML definitions, each of which is two
things at once: a set of FACTS about a tracker (name, domains, public or
private, what content it carries, what its search endpoint can be asked) and an
IMPLEMENTATION (request templates, HTML selectors, login flows) for scraping
it. This script extracts only the facts, reshaped into our own schema.

The implementation half is left behind on purpose, twice over:

  - The upstream repo carries no license file. Facts are not copyrightable;
    their expression is. A domain list and a category table are facts. A
    hand-written CSS selector chain for parsing a search page is expression,
    and we do not take it.

  - Our search client will not scrape HTML anyway. The plan is to speak to the
    subset of trackers with clean interfaces (Torznab-capable, JSON APIs), for
    which the facts here -- who exists, what they carry, how precisely they can
    be asked, how politely they must be treated -- are the entire requirement.

WHY A GENERATOR AND A CHECKED-IN JSON rather than fetching at runtime: the
list changes on the timescale of weeks (a tracker dies, a domain moves), and a
site should not fail to boot because GitHub is down. Refreshing is a re-run of
this script against a fresh clone, and the diff IS the review.

Determinism: output is sorted by slug, keys are sorted, and the only
provenance stamp is the source commit -- no timestamps, so an unchanged
upstream produces a byte-identical file and an empty diff.
"""

import json
import subprocess
import sys
from pathlib import Path

import yaml

# The standard Newznab category tree, name -> id. These ids are the same
# vocabulary as catalog/categories.go; anything our own tree does not carry is
# coarsened by the Go side, not here -- the JSON keeps full precision so a
# future finer-grained consumer does not need a regeneration.
NEWZNAB = {
    "Console": 1000, "Console/NDS": 1010, "Console/PSP": 1020,
    "Console/Wii": 1030, "Console/XBox": 1040, "Console/XBox 360": 1050,
    "Console/Wiiware": 1060, "Console/PS3": 1080, "Console/Other": 1090,
    "Console/3DS": 1110, "Console/PS Vita": 1120, "Console/WiiU": 1130,
    "Console/XBox One": 1140, "Console/PS4": 1180,
    "Movies": 2000, "Movies/Foreign": 2010, "Movies/Other": 2020,
    "Movies/SD": 2030, "Movies/HD": 2040, "Movies/UHD": 2045,
    "Movies/BluRay": 2050, "Movies/3D": 2060, "Movies/DVD": 2070,
    "Movies/WEB-DL": 2080,
    "Audio": 3000, "Audio/MP3": 3010, "Audio/Video": 3020,
    "Audio/Audiobook": 3030, "Audio/Lossless": 3040, "Audio/Other": 3050,
    "Audio/Foreign": 3060,
    "PC": 4000, "PC/0day": 4010, "PC/ISO": 4020, "PC/Mac": 4030,
    "PC/Mobile-Other": 4040, "PC/Games": 4050, "PC/Mobile-iOS": 4060,
    "PC/Mobile-Android": 4070,
    "TV": 5000, "TV/WEB-DL": 5010, "TV/Foreign": 5020, "TV/SD": 5030,
    "TV/HD": 5040, "TV/UHD": 5045, "TV/Other": 5050, "TV/Sport": 5060,
    "TV/Anime": 5070, "TV/Documentary": 5080,
    "XXX": 6000, "XXX/DVD": 6010, "XXX/WMV": 6020, "XXX/XviD": 6030,
    "XXX/x264": 6040, "XXX/UHD": 6045, "XXX/Pack": 6050,
    "XXX/ImageSet": 6060, "XXX/Other": 6070, "XXX/SD": 6080,
    "XXX/WEB-DL": 6090,
    "Books": 7000, "Books/Mags": 7010, "Books/EBook": 7020,
    "Books/Comics": 7030, "Books/Technical": 7040, "Books/Other": 7050,
    "Books/Foreign": 7060,
    "Other": 8000, "Other/Misc": 8010, "Other/Hashed": 8020,
}

# External-id search parameters worth recording: each one is a way to ask a
# tracker for exactly one show or film with no title matching at all, which is
# the failure mode the whole release-name pipeline struggles against.
ID_PARAMS = ("imdbid", "tvdbid", "tmdbid", "rid", "doubanid")


def classify_auth(settings, deftype):
    """What a member must hand this tracker before it will answer.

    Read from the settings NAMES rather than the login block, because most
    private definitions carry no login block at all -- the credential is a
    query parameter, and the setting is the only place it shows. Precedence
    puts apikey first: a tracker offering both an API key and a password wants
    the key for programmatic access, which is the only access we will make.
    """
    names = {s.get("name", "") for s in settings or []}
    if "apikey" in names:
        return "apikey"
    # user/pass as well as username/password: exactly one definition in the
    # corpus (Bittorrentfiles) names its credential settings the short way,
    # and the first full verification pass caught it classified "unknown".
    if ("username" in names and "password" in names) or (
        "user" in names and "pass" in names
    ):
        return "credentials"
    if "cookie" in names:
        return "cookie"
    if "passkey" in names:
        return "passkey"
    # A public tracker with none of the above genuinely needs nothing. A
    # private one with none of the above is authenticated by some mechanism
    # its settings do not name (redacted API endpoints, OAuth); "unknown" is
    # honest and keeps it out of any "ready to wire" list.
    return "none" if deftype == "public" else "unknown"


def tv_precision(modes):
    """none | title | episode: how precisely the tracker can be asked for TV."""
    params = modes.get("tv-search") or []
    if not params:
        return "none"
    if "season" in params and "ep" in params:
        return "episode"
    return "title"


def music_precision(modes):
    """none | title | artist | album: the music analogue of tv precision.

    A small population today (ten definitions take artist, four of those
    album), but the music phase of the pipeline will ask exactly this
    question, and collapsing it to a boolean now means regenerating the
    world later to answer it.
    """
    params = modes.get("music-search") or []
    if not params:
        return "none"
    if "album" in params:
        return "album"
    if "artist" in params:
        return "artist"
    return "title"


def id_params(params):
    return sorted(p for p in (params or []) if p in ID_PARAMS)


def extract(path):
    d = yaml.safe_load(path.read_text(encoding="utf-8"))
    caps = d.get("caps") or {}
    modes = caps.get("modes") or {}
    settings = d.get("settings") or []

    cats, unmapped = set(), []
    for m in caps.get("categorymappings") or []:
        name = m.get("cat")
        if not name:
            continue
        if name in NEWZNAB:
            cats.add(NEWZNAB[name])
        else:
            unmapped.append(str(name))
    # The OLDER form as well: caps.categories is a plain mapping of the
    # tracker's own category id to a Newznab name ({1: TV}, {"XXX": "XXX"}).
    # 29 definitions still use it, and the first full verification pass found
    # every one of them extracted with no categories at all -- among them
    # EZTV, a TV-only tracker a "carries tv" filter would silently drop.
    for name in (caps.get("categories") or {}).values():
        if name in NEWZNAB:
            cats.add(NEWZNAB[name])
        else:
            unmapped.append(str(name))

    content = sorted({top_name(c) for c in cats})
    # Anime is a facet of its own, not a subcategory detail: the pipeline's
    # stated filter set is tv / anime / movies, and prod is anime-heavy. A
    # tracker mapping TV/Anime (5070) carries anime; it keeps "tv" too,
    # because an S/E query for a live-action show may still land there.
    if 5070 in cats:
        content = sorted(set(content) | {"anime"})

    row = {
        "slug": d["id"],
        "name": d["name"],
        "description": (d.get("description") or "").strip(),
        "language": d.get("language", ""),
        "type": d["type"],
        "domains": [str(u) for u in d.get("links") or []],
        "legacy_domains": [str(u) for u in d.get("legacylinks") or []],
        "request_delay_seconds": float(d.get("requestDelay") or 0),
        "auth": classify_auth(settings, d["type"]),
        "needs_flaresolverr": any(
            s.get("type") == "info_flaresolverr" for s in settings
        ),
        # Both of these disqualify a tracker from unattended wiring even
        # when the credentials are on hand, which is why they are facts and
        # not implementation: 45 "credentials" trackers demand an image
        # captcha at login, 24 a TOTP code, and a directory that omits them
        # answers "can this be wired" wrongly for every one.
        "login_captcha": bool((d.get("login") or {}).get("captcha")),
        # Substring, not exact names: the corpus spells it 2facode, 2fa_code
        # AND alt2fatoken, and an exact list missed two of the 25. The info_
        # prefix is excluded because those entries are display text about the
        # requirement, not the requirement.
        "login_2fa": any(
            "2fa" in s.get("name", "").lower()
            and not s.get("name", "").startswith("info")
            for s in settings
        ),
        "content": content,
        "categories": sorted(cats),
        "search": {
            "free_text": bool(modes.get("search")),
            "tv": tv_precision(modes),
            "tv_ids": id_params(modes.get("tv-search")),
            "movie": bool(modes.get("movie-search")),
            "movie_ids": id_params(modes.get("movie-search")),
            "music": music_precision(modes),
            "book": bool(modes.get("book-search")),
        },
    }
    return row, unmapped


def top_name(cat_id):
    return {
        1000: "console", 2000: "movies", 3000: "audio", 4000: "pc",
        5000: "tv", 6000: "xxx", 7000: "books", 8000: "other",
    }[cat_id // 1000 * 1000]


def main():
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    src = Path(sys.argv[1])
    defs = sorted((src / "definitions" / "v11").glob("*.yml"))
    if not defs:
        sys.exit(f"no definitions under {src}/definitions/v11 -- wrong path?")

    commit = subprocess.run(
        ["git", "-C", str(src), "rev-parse", "HEAD"],
        capture_output=True, text=True, check=True,
    ).stdout.strip()

    rows, problems = [], []
    for p in defs:
        try:
            row, unmapped = extract(p)
        except Exception as e:  # a malformed upstream file is their bug, shown not hidden
            problems.append(f"{p.name}: {e}")
            continue
        rows.append(row)
        for u in unmapped:
            problems.append(f"{p.name}: unmapped category name {u!r}")

    slugs = [r["slug"] for r in rows]
    dupes = {s for s in slugs if slugs.count(s) > 1}
    if dupes:
        sys.exit(f"duplicate slugs, the file would be ambiguous: {sorted(dupes)}")

    rows.sort(key=lambda r: r["slug"])
    out = {
        "source": {
            "repo": "https://github.com/Prowlarr/Indexers",
            "commit": commit,
            "schema": "v11",
            "note": (
                "Facts extracted from the community definitions: identity, "
                "domains, categories, search capability, politeness. The "
                "scraping implementations are deliberately not taken. "
                "KNOWN OMISSION: Prowlarr implements its major trackers as "
                "native C# indexers with no YAML definition, so BTN, "
                "AnimeBytes, HDBits, IPTorrents, MyAnonamouse and their "
                "peers are absent here; they need hand-curated entries if "
                "the pipeline is to use them."
            ),
        },
        "trackers": rows,
    }

    dest = Path(__file__).resolve().parent.parent / "trackerdir" / "directory.json"
    dest.parent.mkdir(exist_ok=True)
    # open() rather than write_text: this Python is 3.9 and write_text only
    # grew a newline argument in 3.10, and the file must be LF regardless of
    # the platform regenerating it or every refresh diffs every line.
    with open(dest, "w", encoding="utf-8", newline="\n") as f:
        f.write(json.dumps(out, indent=1, sort_keys=True, ensure_ascii=False) + "\n")

    by_type = {}
    for r in rows:
        by_type[r["type"]] = by_type.get(r["type"], 0) + 1
    print(f"wrote {dest} -- {len(rows)} trackers {by_type} from {commit[:12]}")
    ep = sum(1 for r in rows if r["search"]["tv"] == "episode")
    ids = sum(1 for r in rows if r["search"]["tv_ids"])
    print(f"tv: {ep} episode-precise, {ids} with external-id search")
    if problems:
        print(f"{len(problems)} problem(s):")
        for q in problems[:20]:
            print("  " + q)
        sys.exit(1)


if __name__ == "__main__":
    main()
