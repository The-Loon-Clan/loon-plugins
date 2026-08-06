package logs

import (
	"strconv"
	"strings"
	"time"
)

// ParseQuery turns a Kibana/Lucene-lite query string into a Search. It never
// errors — an unparseable fragment degrades to a plain message term so the box
// always does *something* sensible. `now` is injected (not time.Now()) so
// tests are deterministic and relative windows (since:24h) are reproducible.
//
// Grammar (whitespace-separated tokens; double-quotes group spaces):
//
//	foo bar            message contains "foo" AND "bar"
//	"exact phrase"     message contains the whole phrase
//	-noise  -"go away" message does NOT contain these
//	op:usenet/assemble op is exactly this
//	op:usenet/*        op has this prefix
//	severity:error     severity is error (repeatable); sev: alias; fatal/warning too
//	path:/admin/users  request_path contains this substring
//	user:42            rows for user_id 42
//	count:>=5          count >= 5 (also count:5 → >=5; find flappers)
//	since:24h since:7d relative last_at lower bound (m/h/d/w); since:2026-07-01 absolute
//	until:1h           last_at upper bound (same forms)
//	is:archived        include dismissed rows
//	sort:count         sort by count (also recent | first)
//
// Unknown key:value tokens fall through to a message term, so a
// literal like "id:1234" in an error message is still findable.
func ParseQuery(raw string, now time.Time) Search {
	var q Search
	for _, tok := range tokenize(raw) {
		neg := false
		if strings.HasPrefix(tok, "-") && len(tok) > 1 {
			neg = true
			tok = tok[1:]
		}
		key, val, isPair := splitField(tok)
		if !isPair {
			if neg {
				q.NotTerms = append(q.NotTerms, tok)
			} else {
				q.Terms = append(q.Terms, tok)
			}
			continue
		}

		// Negatable set/substring fields (op, severity, path) route to
		// the exclusion filters; the scalar fields (user/count/since/
		// until/is/sort) can't be sensibly negated, so a leading '-' on
		// them falls through to a message NotTerm rather than silently
		// applying the POSITIVE filter (which would be the opposite of
		// what the operator typed).
		switch key {
		case "op":
			if neg {
				q.NotOps = append(q.NotOps, val)
			} else {
				q.Ops = append(q.Ops, val)
			}
		case "severity", "sev":
			if s := normalizeSeverity(val); s != "" {
				if neg {
					q.NotSeverities = append(q.NotSeverities, s)
				} else {
					q.Severities = append(q.Severities, s)
				}
			}
		case "path", "url":
			if neg {
				q.NotPaths = append(q.NotPaths, val)
			} else {
				q.Paths = append(q.Paths, val)
			}
		case "user", "uid":
			if neg {
				q.NotTerms = append(q.NotTerms, tok)
			} else if id, err := strconv.Atoi(val); err == nil {
				q.UserID = &id
			}
		case "count":
			if neg {
				q.NotTerms = append(q.NotTerms, tok)
			} else {
				q.MinCount = parseMinCount(val)
			}
		case "since", "after":
			if neg {
				q.NotTerms = append(q.NotTerms, tok)
			} else if t, ok := parseWhen(val, now); ok {
				q.From = &t
			}
		case "until", "before":
			if neg {
				q.NotTerms = append(q.NotTerms, tok)
			} else if t, ok := parseWhen(val, now); ok {
				q.To = &t
			}
		case "is":
			if neg {
				q.NotTerms = append(q.NotTerms, tok)
			} else if val == "archived" {
				q.IncludeArchived = true
			}
		case "sort":
			if neg {
				q.NotTerms = append(q.NotTerms, tok)
			} else {
				q.Sort = normalizeSort(val)
			}
		default:
			// Not a known field — keep the whole token as a term so
			// nothing silently vanishes.
			if neg {
				q.NotTerms = append(q.NotTerms, tok)
			} else {
				q.Terms = append(q.Terms, tok)
			}
		}
	}
	return q
}

// tokenize splits on whitespace but keeps double-quoted runs intact,
// including the quotes after a field colon (op:"a b"). Quotes are
// stripped from the emitted token.
func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t' || r == '\n') && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// splitField reports whether tok is a key:value pair and returns its
// parts. Only an alphabetic key before the FIRST colon counts, so an
// op value like "usenet/x" or a bare "http://..." term isn't
// mis-split (the key must be all letters).
func splitField(tok string) (key, val string, ok bool) {
	i := strings.IndexByte(tok, ':')
	if i <= 0 || i == len(tok)-1 {
		return "", "", false
	}
	key = tok[:i]
	for _, r := range key {
		if r < 'a' || r > 'z' {
			if r < 'A' || r > 'Z' {
				return "", "", false
			}
		}
	}
	return strings.ToLower(key), tok[i+1:], true
}

func normalizeSeverity(v string) string {
	switch strings.ToLower(v) {
	case "warn", "warning", "w":
		return "warning"
	case "err", "error", "e":
		return "error"
	case "fatal", "crit", "critical", "f":
		return "fatal"
	}
	return ""
}

func normalizeSort(v string) string {
	switch strings.ToLower(v) {
	case "count", "freq", "frequency":
		return "count"
	case "first", "oldest":
		return "first"
	default:
		return "recent"
	}
}

// parseMinCount reads count:N / count:>=N / count:>N. All collapse to
// a >= floor (there's no useful upper bound for "noisy rows").
func parseMinCount(v string) int {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, ">=")
	if strings.HasPrefix(v, ">") {
		if n, err := strconv.Atoi(strings.TrimPrefix(v, ">")); err == nil {
			return n + 1
		}
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return 0
}

// parseWhen accepts a relative window (30m, 24h, 7d, 2w) or an
// absolute date (2006-01-02) / datetime (2006-01-02T15:04). Relative
// forms are subtracted from now.
func parseWhen(v string, now time.Time) (time.Time, bool) {
	if d, ok := parseDurationExt(v); ok {
		return now.Add(-d), true
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// parseDurationExt extends time.ParseDuration with d (days) and w
// (weeks), which Go's stdlib doesn't support.
func parseDurationExt(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	last := v[len(v)-1]
	if last == 'd' || last == 'w' {
		n, err := strconv.ParseFloat(v[:len(v)-1], 64)
		if err != nil {
			return 0, false
		}
		unit := 24 * time.Hour
		if last == 'w' {
			unit = 7 * 24 * time.Hour
		}
		return time.Duration(n * float64(unit)), true
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d, true
	}
	return 0, false
}
