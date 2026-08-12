package usenet

import "strings"

// xrefGroups extracts the newsgroup names from an Xref header value.
//
// The wire form is the server's own name followed by group:articlenumber pairs:
//
//	news.example.com alt.binaries.teevee:12345 alt.binaries.mom:98765
//
// The leading token is the SERVER, not a group, and the numbers are that
// server's per-group article numbers — meaningless anywhere else (RFC 3977 s6),
// so only the names are kept.
//
// This is the protocol's own crosspost signal. RFC 5536 s3.2.14 says user
// agents use Xref "to avoid multiple processing of crossposted articles", which
// is precisely the problem: a crosspost is ONE article filed under several
// groups, and crawling those groups separately yields it once per group with no
// way to tell that they are the same posting. content_sketch recovers that
// AFTER the fact, having already paid to fetch, stage and assemble each copy;
// Xref says it up front.
//
// Filtering to binary groups follows NNTmux's rule, and the reason is not
// cosmetic: a binary crosspost routinely also lands in discussion groups we do
// not crawl and never will, and recording those as places this release lives
// would put groups in the NZB that our members' providers may not carry at all.
// A token that survives the filter is one we would plausibly crawl.
//
// Returns nil for an empty or unparseable header — the caller falls back to the
// group it was crawling, which is always correct if incomplete.
func xrefGroups(xref string) []string {
	if xref == "" {
		return nil
	}
	var out []string
	seen := make(map[string]struct{}, 4)
	for i, tok := range strings.Fields(xref) {
		if i == 0 && !strings.Contains(tok, ":") {
			continue // the server name, which carries no colon-number suffix
		}
		name := tok
		if idx := strings.LastIndex(name, ":"); idx > 0 {
			name = name[:idx]
		}
		if !isBinaryGroupName(name) {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// isBinaryGroupName matches the shape NNTmux accepts —
// `^[a-z]{2,3}\.bin(aries|arios|aer)\....` — without a regex, since this runs
// per article on the ingest path. The three spellings are the English, Spanish
// and Dutch hierarchies that actually carry binaries.
func isBinaryGroupName(s string) bool {
	if len(s) < 6 {
		return false
	}
	first := strings.IndexByte(s, '.')
	if first < 2 || first > 3 {
		return false
	}
	for i := 0; i < first; i++ {
		if c := s[i]; c < 'a' || c > 'z' {
			return false
		}
	}
	rest := s[first+1:]
	for _, kind := range []string{"binaries.", "binarios.", "binaer."} {
		if len(rest) > len(kind) && strings.HasPrefix(rest, kind) {
			return true
		}
	}
	return false
}

// validMessageID enforces RFC 5536 s3.1.3 on a wire-supplied message-id.
//
// The spec is narrower than RFC 5322's: at most 250 octets INCLUDING the angle
// brackets, printable ASCII only (0x21-0x7E after the brackets are accounted
// for), no whitespace, and no '>' inside. It is deliberately strict — the id is
// the only thing a downloader can fetch by, and an id we cannot serve is worse
// than an article we never indexed, because the release looks complete and
// fails at download time.
//
// Accepts both bracketed and bare forms: staged ids arrive either way depending
// on the wire path, and the NZB stores the trimmed form.
func validMessageID(id string) bool {
	if id == "" || len(id) > 250 {
		return false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(id, "<"), ">")
	if body == "" {
		return false
	}
	// An '@' is required by the grammar (id-left "@" id-right) and is the
	// cheapest way to reject a truncated or garbage field that happens to be
	// printable.
	if !strings.Contains(body, "@") {
		return false
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		// Printable ASCII excluding space; '<' and '>' cannot occur inside.
		if c <= 0x20 || c >= 0x7F || c == '<' || c == '>' {
			return false
		}
	}
	return true
}
