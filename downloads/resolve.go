package downloads

import (
	"context"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"
)

// Working out WHICH release a download client is talking about.
//
// This is the hard part of the whole feature, and it is worth saying why: a
// download client reports on a JOB. A job has a name, a category and a
// directory; it does not have a release id, because the id is ours and the
// client never saw it. Three routes back, in falling order of trust:
//
//  1. The script sent an id. Only happens when the client kept our URL, but
//     when it happens it is certain.
//  2. The script sent the URL it fetched from, and our download links carry
//     ?id=. Also certain — we are reading our own link back.
//  3. Nothing but a name. Matched against WHAT THIS MEMBER RECENTLY GRABBED,
//     never against the index at large. That scoping is what makes matching by
//     name defensible: the candidate set is a handful of rows they chose
//     themselves, so a loose match lands on something they actually downloaded
//     rather than on whichever of 160,000 releases had the closest title.
//
// Route 3 can still miss — a client that renames a job past recognition, a
// member who grabbed it a month ago — and a miss is answered honestly rather
// than guessed at. A wrong match would attach one member's failure to somebody
// else's release, which is worse than no report at all.

// grabWindow is how many of the member's recent grabs route 3 considers.
//
// Generous rather than tight: post-processing can run days after the grab (a
// queued job, a paused client, a retry), and the cost of a wider window is one
// more row to compare a string against.
const grabWindow = 200

// resolveRelease returns the release this report is about, and a short note
// explaining how it was matched — appended to the message the member sees in
// their download log, so a fuzzy match is visible rather than silent.
func (p *Plugin) resolveRelease(ctx context.Context, userID int64, req reportRequest) (int64, string) {
	if req.ID > 0 {
		return req.ID, ""
	}
	if id := releaseIDFromURL(req.URL); id > 0 {
		return id, ""
	}
	if p.grabs == nil {
		return 0, ""
	}
	// Both names are worth trying and they are often different: SABnzbd's job
	// name is cleaned up for display, while the filename is closer to what we
	// served.
	names := []string{req.Name, req.Filename}
	grabs, err := p.grabs.RecentGrabs(ctx, userID, grabWindow)
	if err != nil || len(grabs) == 0 {
		return 0, ""
	}
	for _, n := range names {
		key := foldTitle(stripNZBExt(n))
		if key == "" {
			continue
		}
		for _, g := range grabs {
			if foldTitle(g.Title) == key {
				return g.ID, " (matched by name)"
			}
		}
	}
	return 0, ""
}

// releaseIDFromURL reads the id out of a link we served.
//
// Handles both shapes this site publishes — the Newznab download
// (/api?t=get&id=N) and the plain one (/nzb/N) — because a member may have
// grabbed either, and a link that has been through a client is often stripped
// of everything but the path.
func releaseIDFromURL(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	if v := u.Query().Get("id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	// /nzb/12345 or /nzb/12345.nzb — the last path segment, extension trimmed.
	seg := path.Base(u.Path)
	seg = strings.TrimSuffix(seg, ".nzb")
	if n, err := strconv.ParseInt(seg, 10, 64); err == nil && n > 0 {
		return n
	}
	return 0
}

// stripNZBExt drops the .nzb a client may or may not have kept.
func stripNZBExt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 4 && strings.EqualFold(s[len(s)-4:], ".nzb") {
		return s[:len(s)-4]
	}
	return s
}

// foldTitle reduces a title to what "the same release" means across a client's
// renaming: lowercase letters and digits only.
//
// The same fold the series key uses, and for the same reason — a download
// client turns "Some.Show.S01E02.1080p-GRP" into "Some Show S01E02 1080p-GRP"
// or "some_show_s01e02_1080p_grp" depending on its settings, and every one of
// those is the release the member grabbed. Dropping punctuation makes them one
// string; keeping the letters and digits is what stops the fold from making
// genuinely different releases equal.
func foldTitle(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
