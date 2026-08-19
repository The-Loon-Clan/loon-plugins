#!/usr/bin/env python3
"""Report a finished download back to the indexer it came from.

Works as a post-processing script for BOTH SABnzbd and NZBGet — one file, so
there is one thing to download, one thing to configure and one thing to update.
The two clients hand a script completely different things, so the first job is
working out which one is calling.

INSTALL
  SABnzbd  Settings -> Folders -> Scripts Folder, drop this file in, then
           Settings -> Switches -> Post-processing script (or set it per
           category under Settings -> Categories). Make it executable on
           Linux/macOS:  chmod +x report.py
  NZBGet   Settings -> PATHS -> ScriptDir, drop this file in, then enable it
           under Settings -> EXTENSION SCRIPTS.

CONFIGURE
  Nothing, if you downloaded this from the site — your key and the site URL are
  already filled in below. Otherwise set SITE and API_KEY by hand, or export
  INDEXER_URL and INDEXER_APIKEY in the environment.

WHAT IT SENDS
  The job's outcome (worked / did not work), the job name, and the URL the NZB
  came from if the client kept it. Nothing about your machine, your paths or
  what else you have downloaded.

WHAT IT DOES NOT DO
  It never fails your download. Every error in here is caught and printed; the
  script exits successfully whatever happens, because a post-processing script
  that can mark a perfectly good job as failed is worse than no script.
"""

import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

# ── Configuration ──────────────────────────────────────────────────────────
# Filled in for you when downloaded from the site's setup page.
SITE = os.environ.get("INDEXER_URL", "__SITE_URL__").rstrip("/")
API_KEY = os.environ.get("INDEXER_APIKEY", "__API_KEY__")

TIMEOUT_SECONDS = 15

# NZBGet reads the exit code: 93 = success, 94 = failure, 95 = none. SABnzbd
# treats any non-zero as a failure of the script itself, so 0 there.
NZBGET_SUCCESS = 93


def from_sabnzbd():
    """Read the job from SABnzbd's argv and SAB_* environment.

    SABnzbd 3.x sets SAB_* for everything and ALSO passes the historical
    positional arguments. The environment is preferred where present because
    the positional list has grown over the years and its meaning depends on the
    version; argv is the fallback for older builds.

    Positional (SABnzbd's documented order):
      1 directory  2 original nzb name  3 clean job name  4 report number
      5 category   6 group              7 postproc status 8 failure url
    Status 0 means it completed; anything else is one of the ways it did not.
    """
    argv = sys.argv
    status_raw = os.environ.get("SAB_PP_STATUS") or (argv[7] if len(argv) > 7 else "")
    name = os.environ.get("SAB_FILENAME") or (argv[3] if len(argv) > 3 else "")
    filename = os.environ.get("SAB_ORIG_NZB_GZ") or (argv[2] if len(argv) > 2 else "")
    url = os.environ.get("SAB_URL", "")
    detail = os.environ.get("SAB_FAIL_MSG", "")
    if not detail and len(argv) > 8:
        detail = argv[8]

    ok = str(status_raw).strip() in ("0", "")
    return {
        "client": "sabnzbd",
        "status": "ok" if ok else "failed",
        "name": name,
        "filename": filename,
        "url": url,
        "detail": detail,
    }


def from_nzbget():
    """Read the job from NZBGet's NZBPP_* environment.

    NZBPP_TOTALSTATUS is the summary — SUCCESS / FAILURE / WARNING / DELETED —
    and NZBPP_STATUS carries the detail ("FAILURE/UNPACK"). WARNING counts as a
    success here: the job produced files, and warning about a repair that
    worked is not a report the index can act on.
    """
    total = os.environ.get("NZBPP_TOTALSTATUS", "")
    return {
        "client": "nzbget",
        "status": "ok" if total in ("SUCCESS", "WARNING") else "failed",
        "name": os.environ.get("NZBPP_NZBNAME", ""),
        "filename": os.environ.get("NZBPP_NZBFILENAME", ""),
        "url": os.environ.get("NZBPP_URL", ""),
        "detail": os.environ.get("NZBPP_STATUS", ""),
    }


def collect():
    """Whichever client is calling. NZBGet is checked first because it sets a
    marker variable of its own; SABnzbd is the fallback because it is also what
    a bare manual run looks like."""
    if os.environ.get("NZBOP_SCRIPTDIR") or os.environ.get("NZBPP_TOTALSTATUS"):
        return from_nzbget()
    return from_sabnzbd()


def send(job):
    """Post the report and print whatever the site says back.

    The site's message is the only feedback a member ever sees — it lands in
    the client's history log — so it is printed verbatim rather than summarised.
    """
    if not SITE or SITE.startswith("__") or not API_KEY or API_KEY.startswith("__"):
        print("[indexer] Not configured: set SITE and API_KEY in this script "
              "(or INDEXER_URL / INDEXER_APIKEY in the environment).")
        return

    job["apikey"] = API_KEY
    body = urllib.parse.urlencode({k: v for k, v in job.items() if v}).encode()
    req = urllib.request.Request(
        SITE + "/api/downloads/report",
        data=body,
        headers={"Content-Type": "application/x-www-form-urlencoded",
                 "User-Agent": "loon-downloads-report/1.0"},
    )
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_SECONDS) as resp:
            payload = json.loads(resp.read().decode("utf-8", "replace"))
        print("[indexer] " + payload.get("message", "Reported."))
    except urllib.error.HTTPError as e:
        # The site answered and said no. Its body explains why — an invalid
        # key, an unmatched job — and that explanation is the useful part.
        try:
            payload = json.loads(e.read().decode("utf-8", "replace"))
            print("[indexer] " + payload.get("message", "HTTP %d" % e.code))
        except Exception:
            print("[indexer] HTTP %d from the site." % e.code)
    except Exception as e:  # noqa: BLE001 — see the module docstring
        print("[indexer] Could not reach the site: %s" % e)


def main():
    try:
        send(collect())
    except Exception as e:  # noqa: BLE001
        # Belt and braces. Nothing this script can do is worth failing a
        # download over.
        print("[indexer] Report skipped: %s" % e)
    if os.environ.get("NZBOP_SCRIPTDIR"):
        sys.exit(NZBGET_SUCCESS)
    sys.exit(0)


if __name__ == "__main__":
    main()
