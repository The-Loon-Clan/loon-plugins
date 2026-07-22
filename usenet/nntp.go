package usenet

import (
	"fmt"
	"strings"

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
	for _, l := range lines {
		if f := strings.Fields(l); len(f) > 0 {
			names = append(names, f[0])
		}
	}
	return names, nil
}
