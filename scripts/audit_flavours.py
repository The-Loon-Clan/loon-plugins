#!/usr/bin/env python3
"""Every plugin must say which half of a site it belongs to.

CHECKLIST.md section 1 requires `Flavours` in a plugin's Metadata:
`core.FlavourIndexer`, `core.FlavourTracker`, or `core.FlavourAny` for the
majority that do not care.

WHY A SCRIPT AND NOT A REVIEW NOTE. An empty Flavours behaves identically to
FlavourAny — core runs the plugin everywhere either way — so this is precisely
the kind of omission that never shows up as a bug and never gets noticed in a
diff. It is only visible by asking the whole tree at once, which is what this
does.

WHAT IT CANNOT SEE, said here rather than discovered later: it reads the source
for a `Flavours:` key inside the Metadata literal. A plugin building its
Metadata somewhere clever — a helper, a package-level var, a struct copy —
would be reported as undeclared even though it declares. There is one shape in
this tree and it is the literal, so the simple reader is right today; if that
changes, this needs to grow rather than be silenced.

    python scripts/audit_flavours.py            # report
    python scripts/audit_flavours.py --strict   # exit 1 if any are undeclared
"""

import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# The three answers, as they appear in source.
VALID = ("FlavourIndexer", "FlavourTracker", "FlavourAny")

# Directories that are not plugins.
SKIP = {"pluginapi", "scripts", "img", "docs"}


def metadata_block(src):
    """The Metadata literal from a Metadata() method, or "".

    Braces are counted rather than matched with a regex: the literal contains
    nested ones (Processes, Flavours) and a lazy match stops at the first.
    """
    i = src.find("core.Metadata{")
    if i < 0:
        return ""
    depth, start = 0, i + len("core.Metadata")
    for j in range(start, len(src)):
        if src[j] == "{":
            depth += 1
        elif src[j] == "}":
            depth -= 1
            if depth == 0:
                return src[start:j + 1]
    return ""


def declared_flavours(block):
    """The flavour constants named in a Metadata block."""
    m = re.search(r"Flavours:\s*\[\]string\{([^}]*)\}", block)
    if not m:
        return None
    return re.findall(r"Flavour[A-Za-z]+", m.group(1))


def main():
    strict = "--strict" in sys.argv
    missing, invalid, ok = [], [], {}

    for name in sorted(os.listdir(ROOT)):
        d = os.path.join(ROOT, name)
        if not os.path.isdir(d) or name.startswith(".") or name in SKIP:
            continue
        # EVERY .go file in the directory, not just plugin.go.
        #
        # It read plugin.go alone until 22 Aug 2026, and three plugins keep
        # their Metadata in a file named after themselves — games/games.go,
        # magic/magic.go, medals/medals.go. All three were skipped by a bare
        # `continue`: not reported as unreadable, not counted, simply absent.
        # All three also fail the MUST this script exists to enforce, so the
        # check was silent about the plugins it was most needed for.
        #
        # plugin.go first, because that is where 48 of the 51 put it and
        # reading it first keeps the common case cheap.
        src, block = None, None
        candidates = ["plugin.go", name + ".go"] + sorted(
            f for f in os.listdir(d)
            if f.endswith(".go") and not f.endswith("_test.go"))
        seen = set()
        for fn in candidates:
            if fn in seen:
                continue
            seen.add(fn)
            path = os.path.join(d, fn)
            if not os.path.exists(path):
                continue
            with open(path, encoding="utf-8") as f:
                text = f.read()
            if "core.RegisterPlugin" not in text and "core.Metadata{" not in text:
                continue
            src = text
            block = metadata_block(text)
            if block:
                break
        if src is None:
            continue
        if not block:
            # No Metadata literal here — plugin.go exists but the metadata is
            # built elsewhere. Reported rather than skipped: the reader above
            # says what it can and cannot see, and this is the case it cannot.
            invalid.append((name, "no core.Metadata{...} literal found"))
            continue
        got = declared_flavours(block)
        if got is None:
            missing.append(name)
        elif not got:
            invalid.append((name, "Flavours is empty; say FlavourAny"))
        else:
            bad = [g for g in got if g not in VALID]
            if bad:
                invalid.append((name, "unknown: " + ", ".join(bad)))
            else:
                ok[name] = got

    print("flavours: checking %d plugin(s)" % (len(ok) + len(missing) + len(invalid)))

    if ok:
        by_flavour = {}
        for name, got in ok.items():
            key = " + ".join(sorted(g.replace("Flavour", "").lower() for g in got))
            by_flavour.setdefault(key, []).append(name)
        for key in sorted(by_flavour):
            print("  %-18s %s" % (key, " ".join(sorted(by_flavour[key]))))

    if invalid:
        print("\n  MALFORMED (%d):" % len(invalid))
        for name, why in invalid:
            print("     %-16s %s" % (name, why))

    if missing:
        print("\n  UNDECLARED (%d) — CHECKLIST section 1:" % len(missing))
        for name in missing:
            print("     %s" % name)
        print("\n  Add Flavours to the Metadata literal. FlavourAny is the right")
        print("  answer for anything that does not care what the site indexes,")
        print("  and saying it is the point: an empty field and 'belongs to")
        print("  both' behave identically, so only a declaration tells them apart.")

    bad = len(missing) + len(invalid)
    print("\nflavours: %d declared, %d undeclared, %d malformed" %
          (len(ok), len(missing), len(invalid)))
    if strict and bad:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
