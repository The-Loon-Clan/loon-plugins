package tracker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// AnnounceRequest is the parsed form of a BitTorrent tracker GET.
// Everything is already validated (info_hash/peer_id length, sane port).
type AnnounceRequest struct {
	InfoHashHex string
	PeerID      string // hex
	IP          string
	Port        int
	Uploaded    int64
	Downloaded  int64
	Left        int64
	Event       string // "", started, stopped, completed
	NumWant     int
	Compact     bool
	NoPeerID    bool
}

// ParseAnnounce extracts query params a BitTorrent client sends. Missing
// required fields return a descriptive error so the caller can bencode a
// failure reason that the client will surface to the user.
func ParseAnnounce(values map[string]string, remoteAddr string) (*AnnounceRequest, error) {
	ih := values["info_hash"]
	if len(ih) != 20 {
		return nil, errors.New("info_hash must be 20 bytes")
	}
	pid := values["peer_id"]
	if len(pid) != 20 {
		return nil, errors.New("peer_id must be 20 bytes")
	}
	portStr := values["port"]
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port %q", portStr)
	}
	up, _ := strconv.ParseInt(values["uploaded"], 10, 64)
	down, _ := strconv.ParseInt(values["downloaded"], 10, 64)
	left, _ := strconv.ParseInt(values["left"], 10, 64)
	if up < 0 {
		up = 0
	}
	if down < 0 {
		down = 0
	}
	if left < 0 {
		left = 0
	}
	numWant := 50
	if s := values["numwant"]; s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			if n > 200 {
				n = 200
			}
			numWant = n
		}
	}
	infoHex, err := DecodeInfoHashRaw(ih)
	if err != nil {
		return nil, err
	}
	peerHex, err := DecodePeerID(pid)
	if err != nil {
		return nil, err
	}
	return &AnnounceRequest{
		InfoHashHex: infoHex,
		PeerID:      peerHex,
		IP:          ClientIP(remoteAddr),
		Port:        port,
		Uploaded:    up,
		Downloaded:  down,
		Left:        left,
		Event:       values["event"],
		NumWant:     numWant,
		Compact:     values["compact"] != "0",
		NoPeerID:    values["no_peer_id"] == "1",
	}, nil
}

// AnnounceDelta is how much to add to the user's lifetime up/down counters
// for this announce. Clients sometimes restart or re-report lower totals;
// we clamp to zero in that case rather than credit back.
type AnnounceDelta struct {
	Up      int64
	Down    int64
	Left    int64
	Done    bool // true only on the left→0 transition
	Stopped bool
}

// ComputeDelta diffs the current announce against the prior snapshot (or
// nil if first-time) and decides whether this is a completion event.
func ComputeDelta(prev *Peer, cur *AnnounceRequest) AnnounceDelta {
	d := AnnounceDelta{Left: cur.Left}
	if cur.Event == "stopped" {
		d.Stopped = true
		return d
	}
	if prev == nil {
		// First-announce-ever for this (user, torrent): credit nothing;
		// the client's `uploaded`/`downloaded` snapshot is the baseline.
		if cur.Event == "completed" || cur.Left == 0 {
			d.Done = true
		}
		return d
	}
	if cur.Uploaded >= prev.Uploaded {
		d.Up = cur.Uploaded - prev.Uploaded
	}
	if cur.Downloaded >= prev.Downloaded {
		d.Down = cur.Downloaded - prev.Downloaded
	}
	// Completion is the left→0 transition, which is idempotent at the DB
	// layer (stats.completed is ORed) so repeated 'completed' events don't
	// double-count snatches as long as the caller gates Snatches++ on the
	// transition, not the event flag.
	if !prev.Completed && cur.Left == 0 {
		d.Done = true
	}
	return d
}

// EncodeAnnounceResponse produces the bencoded response body. On error, the
// BEP-3 failure format is used so the client surfaces the reason.
func EncodeAnnounceResponse(interval int, complete, incomplete int, peers4, peers6 []byte) []byte {
	e := benc{}
	e.out = append(e.out, 'd')

	// "complete" = seeders
	e.str("complete")
	e.intVal(int64(complete))
	// "incomplete" = leechers
	e.str("incomplete")
	e.intVal(int64(incomplete))
	// "interval" = next announce delay (seconds)
	e.str("interval")
	e.intVal(int64(interval))
	// "min interval" = hard lower bound
	e.str("min interval")
	e.intVal(int64(interval / 2))
	// "peers" = compact IPv4 list (BEP-23)
	e.str("peers")
	e.bytes(peers4)
	if len(peers6) > 0 {
		e.str("peers6")
		e.bytes(peers6)
	}
	e.out = append(e.out, 'e')
	return e.out
}

// EncodeAnnounceFailure is the standard "failure reason" bencode.
func EncodeAnnounceFailure(reason string) []byte {
	e := benc{}
	e.out = append(e.out, 'd')
	e.str("failure reason")
	e.str(reason)
	e.out = append(e.out, 'e')
	return e.out
}

// EncodeScrapeResponse produces the multi-torrent scrape body.
type ScrapeEntry struct {
	InfoHash   string // 20 raw bytes
	Complete   int
	Downloaded int
	Incomplete int
}

func EncodeScrapeResponse(entries []ScrapeEntry) []byte {
	e := benc{}
	e.out = append(e.out, 'd')
	e.str("files")
	e.out = append(e.out, 'd')
	for _, sc := range entries {
		e.bytes([]byte(sc.InfoHash))
		// per-torrent stats dict
		e.out = append(e.out, 'd')
		e.str("complete")
		e.intVal(int64(sc.Complete))
		e.str("downloaded")
		e.intVal(int64(sc.Downloaded))
		e.str("incomplete")
		e.intVal(int64(sc.Incomplete))
		e.out = append(e.out, 'e')
	}
	e.out = append(e.out, 'e')
	e.out = append(e.out, 'e')
	return e.out
}

// AnnounceInterval is what we tell clients to wait between announces. 30 min
// is the conventional default; peers flushed by PeerTTL won't leave the
// swarm prematurely as long as this stays well below PeerTTL.
const AnnounceInterval = 30 * 60

// noCtx is a hatch so helper-level tests can avoid threading a ctx; not used
// in production paths (handler always passes request ctx).
var noCtx = context.Background
