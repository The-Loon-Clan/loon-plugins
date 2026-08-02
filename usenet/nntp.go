package usenet

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/the-loon-clan/loon/nntp"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// dialServer opens (and, if credentials are set, authenticates) a single
// one-shot NNTP connection for the admin helpers below (connection test, group
// listing). Crawling and backfilling do NOT use this — they share the pooled
// connections from pool.go.
func dialServer(srv pluginapi.Server) (*nntp.Conn, error) {
	if srv.Host == "" {
		return nil, fmt.Errorf("usenet: no server host configured")
	}
	port := srv.Port
	if port == 0 {
		port = 119
	}
	addr := fmt.Sprintf("%s:%d", srv.Host, port)
	var conn *nntp.Conn
	var err error
	if srv.TLS {
		conn, err = nntp.DialTLS("tcp", addr, nil)
	} else {
		conn, err = nntp.Dial("tcp", addr)
	}
	if err != nil {
		return nil, err
	}
	if srv.Username != "" {
		if err := conn.Authenticate(srv.Username, srv.Password); err != nil {
			_ = conn.Quit()
			return nil, fmt.Errorf("authenticate: %w", err)
		}
	}
	return conn, nil
}

// testConnect verifies the server is reachable + credentials work.
func testConnect(srv pluginapi.Server) error {
	conn, err := dialServer(srv)
	if err != nil {
		return err
	}
	return conn.Quit()
}

// listGroups fetches every group name via NNTP LIST. Unlike prod's dead-code
// path (which stored the whole "name high low status" line as the name), this
// splits each line and keeps only the group name.
func listGroups(srv pluginapi.Server) ([]string, error) {
	conn, err := dialServer(srv)
	if err != nil {
		return nil, err
	}
	defer conn.Quit()
	lines, err := conn.List()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(lines))
	skipped := 0
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		if !usableGroupName(f[0]) {
			skipped++
			continue
		}
		names = append(names, f[0])
	}
	return names, skippedNames(skipped)
}

// usableGroupName rejects a name this indexer could never act on.
//
// A group name is an IDENTIFIER — it goes back to the server verbatim in
// `GROUP <name>` — so an unusable one is dropped rather than repaired. Coercing
// the bytes would store a name that matches no group on the server, which
// looks like a real newsgroup in the admin list and can never be crawled.
// That is the opposite of the choice made for SAMPLES in blacklist.go, where
// the text is descriptive and a replacement character is better than losing
// the count.
//
// The immediate cause was a name Postgres refuses outright:
//
//	pq: invalid byte sequence for encoding "UTF8": 0xe8 0x71 0x75
//
// and because every name went in as ONE batched insert, that single entry
// failed the whole fetch — an operator could not add any newsgroup at all
// while the server's list contained one bad line. Filtering here, at the point
// the wire data enters, keeps the failure proportional to its cause.
func usableGroupName(name string) bool {
	if name == "" || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		// Controls and spaces cannot appear in a newsgroup name (RFC 3977
		// §4.1) and would break the wire protocol if echoed back.
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// skippedNames reports dropped names as a non-fatal condition. The fetch
// succeeded; the caller should say what it could not take.
func skippedNames(n int) error {
	if n == 0 {
		return nil
	}
	return errSkippedGroups(n)
}

// errSkippedGroups is an int rather than a string so a caller can tell this
// apart from a real failure and still surface the count.
type errSkippedGroups int

func (e errSkippedGroups) Error() string {
	return fmt.Sprintf("%d group name(s) skipped: not valid UTF-8, or contained control characters", int(e))
}
