#!/usr/bin/env python3
"""Regenerate trackerdir/directory.json from Prowlarr's two definition corpora.

    python scripts/gen_trackerdir.py /path/to/prowlarr-indexers /path/to/prowlarr-src

Two inputs because Prowlarr keeps its knowledge in two places:

  - github.com/Prowlarr/Indexers -- ~545 community Cardigann YAML definitions.
  - github.com/Prowlarr/Prowlarr -- the majors, implemented natively in C#
    (src/NzbDrone.Core/Indexers/Definitions). BTN, AnimeBytes, HDBits,
    IPTorrents, MyAnonamouse and their peers have no YAML at all, and a
    directory built from the YAML alone silently lacks exactly the trackers
    an anime-heavy pipeline wants most.

WHAT THIS TAKES, AND WHAT IT DELIBERATELY DOES NOT
--------------------------------------------------
Both corpora are two things at once: FACTS about a tracker (name, domains,
public or private, what content it carries, what its search endpoint can be
asked) and an IMPLEMENTATION for talking to it. This script extracts only the
facts, reshaped into our own schema.

The implementation half is left behind on purpose, twice over:

  - The YAML repo carries no license file, and the C# is GPL-3.0. Facts are
    not copyrightable; their expression is. A domain list and a category
    table are facts. A selector chain or a request builder is expression,
    and we do not take it.

  - Our search client will not scrape HTML anyway. The plan is to speak to
    the subset of trackers with clean interfaces, for which the facts here --
    who exists, what they carry, how precisely they can be asked, how
    politely they must be treated -- are the entire requirement.

The C# is read with REGEXES, not a parser, and that is a considered choice:
the native definitions are a house style, one class per tracker with the
facts in stereotyped override properties, and a regex that misses simply
yields a row the invariant tests refuse. When both corpora define the same
tracker (four do today) the community YAML row wins and the native is
skipped: those rows already shipped, and churning them for identical facts
would put noise in every refresh diff.

WHY A GENERATOR AND A CHECKED-IN JSON rather than fetching at runtime: the
list changes on the timescale of weeks, and a site should not fail to boot
because GitHub is down. Refreshing is a re-run against fresh clones, and the
diff IS the review.

Determinism: output is sorted by slug, keys are sorted, and the only
provenance stamps are the two source commits -- no timestamps, so unchanged
upstreams produce a byte-identical file and an empty diff.
"""

import json
import re
import subprocess
import sys
from pathlib import Path

import yaml

# External-id search parameters worth recording: each one is a way to ask a
# tracker for exactly one show or film with no title matching at all, which is
# the failure mode the whole release-name pipeline struggles against.
# tvmazeid earns its place for one tracker (Nebulance) and one very local
# reason: OUR schedule source is TVmaze, so an episode gap already carries
# the exact id that tracker can be asked by -- no cross-id mapping at all.
ID_PARAMS = ("imdbid", "tvdbid", "tmdbid", "rid", "doubanid", "tvmazeid")

# The C# spells the same parameters as enum members.
CS_ID_PARAMS = {
    "ImdbId": "imdbid", "TvdbId": "tvdbid", "TmdbId": "tmdbid",
    "RId": "rid", "DoubanId": "doubanid", "TvMazeId": "tvmazeid",
}


def newznab_tables(prowlarr_src):
    """Both category maps, from Prowlarr's own table rather than a hand copy.

    NewznabStandardCategory.cs declares every category once:

        public static readonly IndexerCategory TVAnime = new(5070, "TV/Anime");

    which yields the enum-name map the C# definitions use AND the path map
    the YAML definitions use. Reading it retires the hand-typed table the
    first version of this script carried -- a copy of upstream data is a
    drift bug waiting for upstream to add a category.

    Paths outside 1000-8999 (the table has a zero-bucket "Other" alias) are
    kept out of the path map: the real Other is 8000, and an ambiguous path
    must resolve to the id the rest of the dataset uses.
    """
    src = (prowlarr_src / "src/NzbDrone.Core/Indexers/NewznabStandardCategory.cs").read_text(
        encoding="utf-8", errors="replace")
    enum_to_id, path_to_id = {}, {}
    for m in re.finditer(
            r'public static readonly IndexerCategory (\w+) = new\((\d+), "([^"]+)"\)', src):
        name, cid, path = m.group(1), int(m.group(2)), m.group(3)
        enum_to_id[name] = cid
        if 1000 <= cid <= 8999 and path not in path_to_id:
            path_to_id[path] = cid
    if not enum_to_id:
        sys.exit("NewznabStandardCategory.cs did not parse; upstream moved the table")
    return enum_to_id, path_to_id


def top_name(cat_id):
    return {
        1000: "console", 2000: "movies", 3000: "audio", 4000: "pc",
        5000: "tv", 6000: "xxx", 7000: "books", 8000: "other",
    }[cat_id // 1000 * 1000]


def content_of(cats):
    content = sorted({top_name(c) for c in cats})
    # Anime is a facet of its own, not a subcategory detail: the pipeline's
    # stated filter set is tv / anime / movies, and prod is anime-heavy. A
    # tracker mapping TV/Anime (5070) carries anime; it keeps "tv" too,
    # because an S/E query for a live-action show may still land there.
    if 5070 in cats:
        content = sorted(set(content) | {"anime"})
    return content


def slug_of(name):
    return "".join(c for c in name.lower() if c.isalnum())


# ---------------------------------------------------------------------------
# The Cardigann YAML corpus.
# ---------------------------------------------------------------------------

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
    # its settings do not name; "unknown" is honest and keeps it out of any
    # "ready to wire" list.
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

    A small population today, but the music phase of the pipeline will ask
    exactly this question, and collapsing it to a boolean now means
    regenerating the world later to answer it.
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


def extract_yaml(path, path_to_id):
    d = yaml.safe_load(path.read_text(encoding="utf-8"))
    caps = d.get("caps") or {}
    modes = caps.get("modes") or {}
    settings = d.get("settings") or []

    cats, unmapped = set(), []
    for m in caps.get("categorymappings") or []:
        name = m.get("cat")
        if not name:
            continue
        if name in path_to_id:
            cats.add(path_to_id[name])
        else:
            unmapped.append(str(name))
    # The OLDER form as well: caps.categories is a plain mapping of the
    # tracker's own category id to a Newznab name ({1: TV}, {"XXX": "XXX"}).
    # 29 definitions still use it, and the first full verification pass found
    # every one of them extracted with no categories at all -- among them
    # EZTV, a TV-only tracker a "carries tv" filter would silently drop.
    for name in (caps.get("categories") or {}).values():
        if name in path_to_id:
            cats.add(path_to_id[name])
        else:
            unmapped.append(str(name))

    row = {
        "slug": d["id"],
        "name": d["name"],
        "description": (d.get("description") or "").strip(),
        "language": d.get("language", ""),
        "type": d["type"],
        "origin": "cardigann",
        "domains": [str(u) for u in d.get("links") or []],
        "legacy_domains": [str(u) for u in d.get("legacylinks") or []],
        "request_delay_seconds": float(d.get("requestDelay") or 0),
        "auth": classify_auth(settings, d["type"]),
        "needs_flaresolverr": any(
            s.get("type") == "info_flaresolverr" for s in settings
        ),
        # Both of these disqualify a tracker from unattended wiring even
        # when the credentials are on hand: 45 "credentials" trackers demand
        # an image captcha at login, 25 a TOTP code, and a directory that
        # omits them answers "can this be wired" wrongly for every one.
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
        "content": content_of(cats),
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


# ---------------------------------------------------------------------------
# The native C# corpus.
# ---------------------------------------------------------------------------

def cs_settings_auth(settings_type, cs_files):
    """Auth for a native indexer: what a MEMBER TYPES IN, nothing else.

    Only properties carrying a [FieldDefinition] attribute -- and not a
    Hidden one -- count. The distinction is the whole answer: GazelleSettings
    has a PassKey property that is filled from the login RESPONSE, and
    SpeedAppSettings has an ApiKey the indexer fetches itself after logging
    in with the member's email and password. Classifying on bare property
    names called both "what you need to supply", and the verification pass
    caught five trackers mislabelled that way. Entered fields are collected
    across the whole inheritance chain (Username/Password live on the base),
    then ranked: an API key is the programmatic front door when offered;
    credentials over cookie because a cookie beside a username/password pair
    is an alternative, not the requirement.
    """
    entered = set()
    seen = set()
    current = settings_type
    while current and current not in seen:
        seen.add(current)
        body, base = cs_class(current, cs_files)
        if body is None:
            break
        for m in re.finditer(
                r"\[FieldDefinition\(([^\]]*)\)\]\s*public string (\w+)", body):
            if "HiddenType.Hidden" not in m.group(1):
                entered.add(m.group(2).lower())
        current = base

    if "apikey" in entered or "apiuser" in entered:
        return "apikey"
    if ({"username", "password"} <= entered or {"user", "pass"} <= entered
            or {"email", "password"} <= entered):
        return "credentials"
    if "cookie" in entered or "mamid" in entered:
        return "cookie"
    if "passkey" in entered:
        return "passkey"
    return "unknown"


def cs_class(name, cs_files):
    """The body and base of one class, found by scanning every source file."""
    pat = re.compile(r"class " + re.escape(name) + r"\s*:\s*(\w+)")
    for src in cs_files.values():
        m = pat.search(src)
        if not m:
            continue
        start = src.index("{", m.end())
        depth, i = 0, start
        while i < len(src):
            if src[i] == "{":
                depth += 1
            elif src[i] == "}":
                depth -= 1
                if depth == 0:
                    break
            i += 1
        return src[start:i], m.group(1)
    return None, None


def cs_search_params(body, kind):
    """The parameter list of one search mode, or None when the mode is absent."""
    m = re.search(kind + r"SearchParams = new List<\w+>\s*\{([^}]*)\}", body)
    if not m:
        return None
    return re.findall(kind + r"SearchParam\.(\w+)", m.group(1))


def extract_native(body, enum_to_id):
    """One tracker's facts out of one C# indexer class body."""
    def prop(name):
        m = re.search(r'public override string ' + name + r' => "([^"]*)"', body)
        return m.group(1) if m else ""

    def urls(name):
        # Both spellings occur: new[] { ... } and new string[] { ... }.
        m = re.search(r"public override string\[\] " + name + r" => new(?: string)?\[\]\s*\{([^}]*)\}", body)
        if not m:
            return []
        return re.findall(r'"([^"]+)"', m.group(1))

    name = prop("Name")
    if not name:
        return None, []

    privacy = "private"
    m = re.search(r"IndexerPrivacy\.(\w+)", body)
    if m:
        privacy = {"Public": "public", "Private": "private",
                   "SemiPrivate": "semi-private"}.get(m.group(1), "private")

    delay = 0.0
    m = re.search(r"RateLimit => TimeSpan\.FromSeconds\(([\d.]+)\)", body)
    if m:
        delay = float(m.group(1))

    cats, unmapped = set(), []
    for m in re.finditer(r"NewznabStandardCategory\.(\w+)", body):
        if m.group(1) in enum_to_id:
            cid = enum_to_id[m.group(1)]
            if 1000 <= cid <= 8999:
                cats.add(cid)
        else:
            unmapped.append(m.group(1))

    tv = cs_search_params(body, "Tv")
    movie = cs_search_params(body, "Movie")
    music = cs_search_params(body, "Music")
    book = cs_search_params(body, "Book")

    def ids_of(params):
        return sorted({CS_ID_PARAMS[p] for p in (params or []) if p in CS_ID_PARAMS})

    row = {
        "slug": slug_of(name),
        "name": name,
        "description": prop("Description"),
        "language": prop("Language") or "en-US",
        "type": privacy,
        "origin": "native",
        "domains": urls("IndexerUrls"),
        "legacy_domains": urls("LegacyUrls"),
        "request_delay_seconds": delay,
        "auth": "unknown",  # filled by the caller, which knows the settings type
        # The native implementations authenticate through APIs their C# owns;
        # none scrapes through an anti-bot wall or relays a captcha, which is
        # much of why these trackers went native in the first place.
        "needs_flaresolverr": False,
        "login_captcha": False,
        "login_2fa": False,
        "content": content_of(cats),
        "categories": sorted(cats),
        "search": {
            # ALWAYS true for natives: IndexerCapabilities' constructor
            # defaults SearchParams to {Q}, and no concrete definition
            # disables it. The first extraction looked for an explicit
            # declaration and got a perfect wrong split -- every native row
            # false, every YAML row true -- which the verification pass
            # caught precisely because it was too clean to be a fact.
            "free_text": True,
            "tv": ("episode" if tv and "Season" in tv and "Ep" in tv
                   else "title" if tv else "none"),
            "tv_ids": ids_of(tv),
            "movie": bool(movie),
            "movie_ids": ids_of(movie),
            "music": ("album" if music and "Album" in music
                      else "artist" if music and "Artist" in music
                      else "title" if music else "none"),
            "book": bool(book),
        },
    }
    return row, unmapped


def native_rows(prowlarr_src, enum_to_id):
    root = prowlarr_src / "src/NzbDrone.Core/Indexers"
    cs_files = {}
    for p in list((root / "Definitions").rglob("*.cs")) + list((root / "Settings").glob("*.cs")):
        cs_files[str(p)] = p.read_text(encoding="utf-8", errors="replace")

    # The protocol shells extend the same base as real trackers but are how
    # Prowlarr TALKS, not places to talk to: Cardigann executes YAML
    # definitions, Torznab/TorrentPotato/TorrentRss speak to arbitrary
    # endpoints the operator supplies. No fixed URL, no identity, no row.
    shells = {"Cardigann", "Torznab", "TorrentPotato", "TorrentRssIndexer"}

    # TRANSITIVELY, not just direct children: fourteen trackers -- the whole
    # AvistaZ network and the Gazelle family among them -- extend an
    # intermediate base (AvistazBase, GazelleBase, SpeedAppBase) that itself
    # extends TorrentIndexerBase, and the first native pass missed every one.
    # The children still declare their own facts; only the plumbing is
    # inherited, so extraction stays per-class.
    decls = {}  # class -> (base, settings generic, file, abstract)
    obsolete = {}  # class -> upstream's reason
    for path, src in cs_files.items():
        for m in re.finditer(
                r"public (abstract )?class (\w+)\s*(?:<\w+>)?\s*:\s*(\w+)(?:<(\w+)>)?",
                src):
            decls[m.group(2)] = (m.group(3), m.group(4), path, bool(m.group(1)))
            # Upstream marks retirements with an attribute and a reason:
            # [Obsolete("Rarbg has shutdown 2023-05-31")], [Obsolete("Moved
            # to YML for Cardigann")]. A shut-down tracker is not a place to
            # search -- found because the FIRST thing the naive extraction
            # ranked most-usable was rarbg, dead since 2023 -- and a moved
            # one is already covered by its community row.
            ob = re.search(
                r'\[Obsolete\("([^"]*)"\)\]\s*(?:\r?\n\s*)?public (?:abstract )?class '
                + re.escape(m.group(2)) + r"\b", src)
            if ob:
                obsolete[m.group(2)] = ob.group(1)

    def is_torrent_indexer(name, depth=0):
        if name == "TorrentIndexerBase":
            return True
        if depth > 6 or name not in decls:
            return False
        return is_torrent_indexer(decls[name][0], depth + 1)

    def settings_type_of(name, depth=0):
        # A child without its own generic (AvistaZ : AvistazBase) uses the
        # settings its base was declared with.
        if depth > 6 or name not in decls:
            return None
        base, generic, _, _ = decls[name]
        if base == "TorrentIndexerBase":
            return generic
        return settings_type_of(base, depth + 1) or generic

    rows, problems = [], []
    retired = []
    for cls in sorted(decls):
        base, generic, path, is_abstract = decls[cls]
        if is_abstract or cls in shells or not is_torrent_indexer(base):
            continue
        if cls in obsolete:
            retired.append(f"{cls} ({obsolete[cls]})")
            continue
        body, _ = cs_class(cls, {path: cs_files[path]})
        row, unmapped = extract_native(body or "", enum_to_id)
        if row is None:
            continue
        if not row["domains"]:
            problems.append(f"{cls}: native class with no IndexerUrls")
            continue
        # RateLimit is the one fact the intermediate bases actually hold:
        # AvistazBase declares six seconds and its five children none, so a
        # child-only read gave the whole family 0. Everything else -- urls,
        # caps, categories -- is declared per child; verified, not assumed.
        if row["request_delay_seconds"] == 0:
            anc, hops = base, 0
            while anc and anc != "TorrentIndexerBase" and hops < 6:
                abody, anext = cs_class(anc, cs_files)
                if abody is None:
                    break
                m2 = re.search(r"RateLimit => TimeSpan\.FromSeconds\(([\d.]+)\)", abody)
                if m2:
                    row["request_delay_seconds"] = float(m2.group(1))
                    break
                anc, hops = anext, hops + 1
        row["auth"] = cs_settings_auth(generic or settings_type_of(cls) or "", cs_files)
        # Same rule as the YAML side: a public tracker whose settings name no
        # credential genuinely needs nothing, and "unknown" would keep five
        # public search engines out of the wireable list for no reason.
        if row["auth"] == "unknown" and row["type"] == "public":
            row["auth"] = "none"
        rows.append(row)
        for u in unmapped:
            problems.append(f"{cls}: unmapped category enum {u!r}")
    if retired:
        print(f"native classes upstream marks [Obsolete], not extracted: {retired}")
    return rows, problems


# ---------------------------------------------------------------------------


def head_commit(clone):
    return subprocess.run(
        ["git", "-C", str(clone), "rev-parse", "HEAD"],
        capture_output=True, text=True, check=True,
    ).stdout.strip()


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    indexers, prowlarr = Path(sys.argv[1]), Path(sys.argv[2])
    defs = sorted((indexers / "definitions" / "v11").glob("*.yml"))
    if not defs:
        sys.exit(f"no definitions under {indexers}/definitions/v11 -- wrong path?")

    enum_to_id, path_to_id = newznab_tables(prowlarr)

    rows, problems = [], []
    for p in defs:
        try:
            row, unmapped = extract_yaml(p, path_to_id)
        except Exception as e:  # a malformed upstream file is their bug, shown not hidden
            problems.append(f"{p.name}: {e}")
            continue
        rows.append(row)
        for u in unmapped:
            problems.append(f"{p.name}: unmapped category name {u!r}")

    taken = {r["slug"] for r in rows}
    natives, nproblems = native_rows(prowlarr, enum_to_id)
    problems += nproblems
    skipped = []
    for row in natives:
        # The community YAML wins a collision: those rows already shipped,
        # and the facts agree -- churning them for an identical tracker
        # would put noise in every refresh diff.
        if row["slug"] in taken:
            skipped.append(row["slug"])
            continue
        rows.append(row)
        taken.add(row["slug"])

    slugs = [r["slug"] for r in rows]
    dupes = {s for s in slugs if slugs.count(s) > 1}
    if dupes:
        sys.exit(f"duplicate slugs, the file would be ambiguous: {sorted(dupes)}")

    rows.sort(key=lambda r: r["slug"])
    out = {
        "source": {
            "repo": "https://github.com/Prowlarr/Indexers",
            "commit": head_commit(indexers),
            "native_repo": "https://github.com/Prowlarr/Prowlarr",
            "native_commit": head_commit(prowlarr),
            "schema": "v11",
            "note": (
                "Facts extracted from the community Cardigann definitions "
                "plus Prowlarr's native C# indexer definitions: identity, "
                "domains, categories, search capability, politeness. The "
                "scraping and API implementations are deliberately not "
                "taken. Where both corpora define a tracker, the community "
                "row wins."
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

    by = {}
    for r in rows:
        key = (r["origin"], r["type"])
        by[key] = by.get(key, 0) + 1
    n_native = sum(1 for r in rows if r["origin"] == "native")
    print(f"wrote {dest} -- {len(rows)} trackers ({n_native} native), {dict(sorted(by.items()))}")
    ep = sum(1 for r in rows if r["search"]["tv"] == "episode")
    ids = sum(1 for r in rows if r["search"]["tv_ids"])
    print(f"tv: {ep} episode-precise, {ids} with external-id search")
    if skipped:
        print(f"native duplicates skipped in favour of the community rows: {sorted(skipped)}")
    if problems:
        print(f"{len(problems)} problem(s):")
        for q in problems[:20]:
            print("  " + q)
        sys.exit(1)


if __name__ == "__main__":
    main()
