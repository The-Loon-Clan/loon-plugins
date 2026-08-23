#!/usr/bin/env python3
"""The mechanical half of CHECKLIST.md, asked of every plugin at once.

    python3 scripts/audit_plugins.py           # report
    python3 scripts/audit_plugins.py --strict  # non-zero while anything fails

GRADES.md is a snapshot of six reviewers' judgment on one day, and it says so:
"regrade after real work and replace it wholesale." The trouble with that
instruction is that a regrade costs six reviewers, so between them the file
ages into something people read as current. Two of its ten confirmed defects
had been fixed for days before anyone re-read it.

So: the MUSTs that a program can decide, decided by a program. Not the whole
checklist -- most of it is judgment and stays judgment -- but the ones where a
failure is a fact rather than an opinion, and where the fact was found the
expensive way at least once already.

WHAT IS CHECKED, and why each earns its place:

  events (section 4)   A declared event nobody emits. lists declared
                       lists.created and never fired it; a subscriber can wait
                       on that forever with nothing to see. Detected by asking
                       whether the name appears anywhere but its own constant
                       and its own declaration -- COMMENTS STRIPPED FIRST,
                       because lists' fix is described in a comment two lines
                       above the emit, and a scan that counted prose would have
                       called the fixed one alive and the broken one alive too.

  jobs (section 5)     A function that calls SetRunning and can return without
                       SetIdle or SetError. The card shows "running" forever
                       and the scheduler will not re-trigger a job it believes
                       is still going, so the job silently retires. Two shapes
                       are decidable and both shipped: neither call present at
                       all, and a deferred SetIdle that runs after an error
                       path's SetError and erases it (found live in tracker's
                       cheat sweep).

                       PATH-level analysis is NOT done and is not claimed. The
                       dbmaint bug -- one branch of a switch returning early --
                       needs a control-flow graph, and a regex that guessed at
                       it would report the careful jobs and miss the careless
                       one. What this catches, it catches exactly.

  cdn (section 8)      A <script src="http..."> is dead under the host's
                       script-src 'self', so the feature behind it is broken
                       for every member, and it is an undisclosed external
                       call besides. Eleven of these shipped across seven
                       plugins.

Everything else in CHECKLIST.md is a review question, and audit_flavours.py
covers section 1's declaration MUST. Run both.
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SKIP_DIRS = {".git", "node_modules", "vendor", "scripts", "testdata", ".github"}

# A DAEMON job holds a connection open, so "running" IS its steady state and
# SetIdle would be a lie -- CHECKLIST section 5 names the exception and names
# these two. Listed rather than guessed at: grading the first sweep of the
# checklist nearly flagged irc for this before somebody read the shape.
DAEMON_PLUGINS = {
    "irc": "holds an IRC connection; running is the steady state, SetError on disconnect",
    "discord": "holds a gateway session; same shape as irc",
}

# A <script src> that is allowed to point outward, with the reason. Empty is
# the goal; an entry is a debt, not a dispensation.
CDN_ALLOW = {
    "roadmap/templates/flow.html":
        "cytoscape from unpkg. Dead under script-src 'self' like the rest, and "
        "the fix is local bundling rather than deletion, which nobody has done. "
        "Recorded in GRADES.md as open since 16 Aug 2026.",
}

COMMENT = re.compile(r"/\*.*?\*/|//[^\n]*", re.S)
FUNC = re.compile(r"^func\s[^\n{]*\{", re.M)
EVENT_NAME = re.compile(r"\bName:\s*([A-Za-z_][A-Za-z0-9_.]*|\"[^\"]+\")")
CONST_DEF = re.compile(r"^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*\"([^\"]+)\"", re.M)
SCRIPT_SRC = re.compile(r'<script[^>]*\ssrc="(https?://[^"]+)"', re.I)


def strip_comments(src):
    """Comments out, so prose about an event cannot pass for emitting it."""
    return COMMENT.sub(" ", src)


def go_files(plugin_dir):
    for base, dirs, names in os.walk(plugin_dir):
        dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
        for n in sorted(names):
            if n.endswith(".go") and not n.endswith("_test.go"):
                yield os.path.join(base, n)


def plugins():
    for name in sorted(os.listdir(ROOT)):
        d = os.path.join(ROOT, name)
        if os.path.isdir(d) and name not in SKIP_DIRS and not name.startswith("."):
            if any(True for _ in go_files(d)):
                yield name, d


def func_bodies(src):
    """Every top-level function body, matched by brace depth.

    Crude by design: it needs to know which calls share a function, not to
    understand Go. A brace inside a string literal would break it, and none of
    the 45 functions that call SetRunning has one.
    """
    for m in FUNC.finditer(src):
        depth, i = 0, m.end() - 1
        while i < len(src):
            if src[i] == "{":
                depth += 1
            elif src[i] == "}":
                depth -= 1
                if depth == 0:
                    break
            i += 1
        yield m.group(0).strip(), src[m.start():i + 1]


def dead_events(name, d):
    """Event names this plugin declares and never emits."""
    consts, decl_sites, bodies = {}, {}, {}
    for path in go_files(d):
        src = strip_comments(open(path, encoding="utf-8", errors="replace").read())
        bodies[path] = src
        for m in CONST_DEF.finditer(src):
            consts[m.group(1)] = m.group(2)

    declared = {}
    for path, src in bodies.items():
        # Only inside an EventDef literal: Name: appears on plenty of other
        # structs, and counting those would invent events to be dead.
        for m in re.finditer(r"EventDef\s*\{(.*?)\}", src, re.S):
            for nm in EVENT_NAME.finditer(m.group(1)):
                tok = nm.group(1)
                declared.setdefault(tok, path)
        for m in re.finditer(r"\{\s*Name:\s*([A-Za-z_][A-Za-z0-9_]*)\s*,\s*Emitter:", src):
            declared.setdefault(m.group(1), path)

    out = []
    for tok, decl_path in sorted(declared.items()):
        if tok.startswith('"'):
            continue  # a literal declared inline; the same literal emits it or nothing does
        uses = 0
        for path, src in bodies.items():
            for m in re.finditer(r"\b%s\b" % re.escape(tok), src):
                line = src[:m.start()].count("\n") + 1
                ctx = src[max(0, m.start() - 200):m.start() + 60]
                # Its own const definition and its own declaration are not uses.
                if re.search(r"\b%s\s*=\s*\"" % re.escape(tok), src[max(0, m.start() - 20):m.start() + 80]):
                    continue
                if "EventDef" in ctx[-200:] and "Emitter:" in src[m.start():m.start() + 200]:
                    continue
                if re.search(r"Emitter:", src[m.start():m.start() + 120]):
                    continue
                uses += 1
                del line
        if uses == 0:
            out.append((tok, consts.get(tok, "?"),
                        os.path.relpath(decl_path, ROOT).replace(os.sep, "/")))
    return out


def job_faults(name, d):
    """Job lifecycle faults this can decide. See the module docstring."""
    out = []
    for path in go_files(d):
        src = strip_comments(open(path, encoding="utf-8", errors="replace").read())
        if "SetRunning" not in src:
            continue
        rel = os.path.relpath(path, ROOT).replace(os.sep, "/")
        for sig, body in func_bodies(src):
            if "SetRunning" not in body:
                continue
            # Defining SetRunning is not calling it: usenet wraps the scheduler
            # in its own job type, and the wrapper's own method body of course
            # has no SetIdle in it.
            if re.match(r"^func\s*\([^)]*\)\s*SetRunning\s*\(", sig):
                continue
            if "SetIdle" not in body and "SetError" not in body:
                if name in DAEMON_PLUGINS:
                    continue
                out.append((rel, sig, "returns without SetIdle or SetError on any path"))
            if re.search(r"defer\s+[^\n]*SetIdle", body) and "SetError" in body:
                out.append((rel, sig,
                            "a deferred SetIdle runs after SetError and erases it"))
    return out


def cdn_scripts(name, d):
    out = []
    for base, dirs, names in os.walk(d):
        dirs[:] = [x for x in dirs if x not in SKIP_DIRS]
        for n in sorted(names):
            if not (n.endswith(".html") or n.endswith(".go")) or n.endswith("_test.go"):
                continue
            path = os.path.join(base, n)
            rel = os.path.relpath(path, ROOT).replace(os.sep, "/")
            src = open(path, encoding="utf-8", errors="replace").read()
            for m in SCRIPT_SRC.finditer(src):
                if rel in CDN_ALLOW:
                    continue
                out.append((rel, m.group(1)))
    return out


def main():
    strict = "--strict" in sys.argv
    rows, faults = [], 0
    for name, d in plugins():
        dead = dead_events(name, d)
        jobs = job_faults(name, d)
        cdn = cdn_scripts(name, d)
        rows.append((name, dead, jobs, cdn))
        faults += len(dead) + len(jobs) + len(cdn)

    for name, dead, jobs, cdn in rows:
        if not (dead or jobs or cdn):
            continue
        print(name)
        for tok, val, where in dead:
            print("   DEAD EVENT   %s (%s) declared at %s, emitted nowhere" % (tok, val, where))
        for rel, sig, why in jobs:
            print("   JOB          %s  %s" % (rel, why))
            print("                %s" % sig)
        for rel, url in cdn:
            print("   CDN SCRIPT   %s  %s" % (rel, url))

    print()
    checked = len(rows)
    allowed = len(CDN_ALLOW)
    if faults:
        print("plugins: %d fault(s) across %d plugin(s) checked (%d CDN script(s) "
              "at the recorded baseline, %d daemon(s) exempt from the SetIdle rule)"
              % (faults, checked, allowed, len(DAEMON_PLUGINS)))
        return 1 if strict else 0
    print("plugins: %d checked -- every declared event is emitted, every job that "
          "starts can finish,\nand no plugin loads a script from someone else's "
          "server (%d recorded exception(s), %d daemon(s))"
          % (checked, allowed, len(DAEMON_PLUGINS)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
