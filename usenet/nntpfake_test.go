package usenet

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/the-loon-clan/loon/nntp"
)

// fakeNNTP is the smallest server that lets a real *nntp.Pool open: greeting,
// QUIT, and a GROUP that always answers 411 (so fetch paths exercise their
// error accounting without overview plumbing). Mirrors loon's pool_test
// fakeServer, which is unexported in another module.
type fakeNNTP struct {
	ln     net.Listener
	silent bool // accept and say nothing: the black-holed-host shape
	// stallStat greets and answers everything EXCEPT STAT, which it swallows.
	// That is the shape a provider leaves behind when it has quietly stopped
	// serving: the socket is open, the session looks alive, and the one
	// command you care about never comes back.
	stallStat bool

	mu    sync.Mutex
	conns []net.Conn
}

func newFakeNNTP(t *testing.T, silent bool) *fakeNNTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeNNTP{ln: ln, silent: silent}
	go s.acceptLoop()
	t.Cleanup(s.shutdown)
	return s
}

func (s *fakeNNTP) acceptLoop() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, c)
		s.mu.Unlock()
		go s.serve(c)
	}
}

func (s *fakeNNTP) serve(c net.Conn) {
	if s.silent {
		return // hold the connection open, never greet
	}
	fmt.Fprint(c, "200 Welcome\r\n")
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		switch strings.ToUpper(strings.Fields(strings.TrimRight(line, "\r\n") + " x")[0]) {
		case "GROUP":
			fmt.Fprint(c, "411 No such group\r\n")
		case "STAT":
			if s.stallStat {
				continue // swallow it; the caller's deadline is the only way out
			}
			fmt.Fprint(c, "223 0 <x> article exists\r\n")
		case "QUIT":
			fmt.Fprint(c, "205 Bye\r\n")
			_ = c.Close()
			return
		default:
			fmt.Fprint(c, "500 Unknown command\r\n")
		}
	}
}

// shutdown closes the listener AND every accepted connection — the mid-life
// death shape: a provider that was fine when its pool opened and is gone now.
func (s *fakeNNTP) shutdown() {
	_ = s.ln.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.conns = nil
}

// newStallingNNTP greets and serves normally but never answers a STAT.
func newStallingNNTP(t *testing.T) *fakeNNTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeNNTP{ln: ln, stallStat: true}
	go s.acceptLoop()
	t.Cleanup(s.shutdown)
	return s
}

// asProvider describes the fake as a provider row.
func (s *fakeNNTP) asProvider(id int, role string) provider {
	host, portStr, _ := net.SplitHostPort(s.ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return provider{ID: id, Name: fmt.Sprintf("fake-%d", id), Host: host, Port: port,
		Enabled: true, Role: role, Priority: id, Connections: 1}
}

// livePool opens a real single-connection pool against a fresh fake server.
// The tracker tests use it where they once used a zero-connection pool —
// assignPools now deals workers by LIVE connections, so a dead pool gets no
// workers at all (which is the fix) and can no longer drive fetchBatch.
func livePool(t *testing.T, size int) *nntp.Pool {
	t.Helper()
	s := newFakeNNTP(t, false)
	pool := nntp.NewPool(nntp.PoolConfig{
		Addr: s.ln.Addr().String(), Size: size,
		DialTimeout: 2 * time.Second, OpTimeout: 2 * time.Second,
	})
	if err := pool.Open(context.Background()); err != nil {
		t.Fatalf("open fake pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}
