package tracker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// PeerTTL is the window after which an unreported peer drops out of the
// swarm. BitTorrent clients announce at ~30 min intervals; 45 gives a cushion
// for network hiccups without keeping ghosts around too long.
const PeerTTL = 45 * time.Minute

// Peer is the state we persist per (info_hash, peer_id) in Redis. Uploaded
// and Downloaded are the *last-seen* counters reported by the client, not
// deltas — the announce handler diffs them against the previous snapshot to
// compute how much to add to the per-user bytes.
type Peer struct {
	UserID     int    `json:"u"`
	PeerID     string `json:"p"`
	IP         string `json:"i"`
	Port       int    `json:"o"`
	Uploaded   int64  `json:"up"`
	Downloaded int64  `json:"dn"`
	Left       int64  `json:"lf"`
	Completed  bool   `json:"cp"`
	LastSeen   int64  `json:"ts"`
}

// PeerStore is a thin wrapper around the shared Redis client used for all
// tracker peer bookkeeping.
type PeerStore struct {
	rdb redis.UniversalClient
}

// NewPeerStore binds the store to an existing Redis client.
// NewPeerStore takes core.Redis's UniversalClient rather than a *redis.Client.
//
// Two changes from the host original, both forced by the seam and neither
// behavioural: the import moves from go-redis v8 to v9 (the host carries BOTH,
// which is its own cleanup), and the type widens to UniversalClient so the
// plugin accepts whatever topology the host built -- single, sentinel or
// cluster -- instead of pinning one.
func NewPeerStore(rdb redis.UniversalClient) *PeerStore { return &PeerStore{rdb: rdb} }

// swarmKey is the Redis hash key for one torrent's peer set.
// Fields within the hash are peer_ids (raw 20-byte client IDs, stored as hex
// so the value is safe to use as a hash field).
func swarmKey(infoHash string) string { return "tracker:swarm:" + infoHash }

// Put upserts a peer and (re)sets the swarm's TTL so the hash self-expires
// once the last announce ages out.
func (p *PeerStore) Put(ctx context.Context, infoHash string, peer Peer) error {
	peer.LastSeen = time.Now().Unix()
	b, err := json.Marshal(peer)
	if err != nil {
		return err
	}
	pipe := p.rdb.Pipeline()
	pipe.HSet(ctx, swarmKey(infoHash), peer.PeerID, b)
	pipe.Expire(ctx, swarmKey(infoHash), PeerTTL)
	_, err = pipe.Exec(ctx)
	return err
}

// Remove drops a peer explicitly (called on event=stopped).
func (p *PeerStore) Remove(ctx context.Context, infoHash, peerID string) error {
	return p.rdb.HDel(ctx, swarmKey(infoHash), peerID).Err()
}

// Get fetches the previous snapshot for a peer so the caller can compute
// (uploaded, downloaded) deltas. Returns (nil, nil) when the peer is new.
func (p *PeerStore) Get(ctx context.Context, infoHash, peerID string) (*Peer, error) {
	b, err := p.rdb.HGet(ctx, swarmKey(infoHash), peerID).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var peer Peer
	if err := json.Unmarshal(b, &peer); err != nil {
		return nil, err
	}
	return &peer, nil
}

// List returns all currently active peers for a torrent, dropping expired
// entries lazily. Redis's hash-level TTL already handles bulk expiry, but
// within a live swarm entry we still need to gate against stale rows.
func (p *PeerStore) List(ctx context.Context, infoHash string) ([]Peer, error) {
	raw, err := p.rdb.HGetAll(ctx, swarmKey(infoHash)).Result()
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-PeerTTL).Unix()
	out := make([]Peer, 0, len(raw))
	var toDelete []string
	for pid, val := range raw {
		var peer Peer
		if err := json.Unmarshal([]byte(val), &peer); err != nil {
			toDelete = append(toDelete, pid)
			continue
		}
		if peer.LastSeen < cutoff {
			toDelete = append(toDelete, pid)
			continue
		}
		out = append(out, peer)
	}
	if len(toDelete) > 0 {
		// best-effort cleanup; ignore error
		p.rdb.HDel(ctx, swarmKey(infoHash), toDelete...)
	}
	return out, nil
}

// Counts returns (seeders, leechers) for a torrent, using Left==0 as the
// seeder definition. Uses List so we honor the TTL cutoff even if Redis
// hasn't GC'd the fields yet.
func (p *PeerStore) Counts(ctx context.Context, infoHash string) (int, int, error) {
	peers, err := p.List(ctx, infoHash)
	if err != nil {
		return 0, 0, err
	}
	var seed, leech int
	for _, pp := range peers {
		if pp.Left == 0 {
			seed++
		} else {
			leech++
		}
	}
	return seed, leech, nil
}

// --- BitTorrent compact peer list encoding (BEP 23) -------------------------

// CompactPeers4 encodes IPv4 peers in the 6-byte-per-peer format. Peers with
// invalid IPs or non-IPv4 addresses are skipped. The BEP-23 format is what
// every mainline client understands by default.
func CompactPeers4(peers []Peer, want int, selfPeerID string) []byte {
	if want <= 0 || want > 200 {
		want = 50
	}
	out := make([]byte, 0, 6*want)
	n := 0
	for _, pp := range peers {
		if n >= want {
			break
		}
		if pp.PeerID == selfPeerID {
			continue
		}
		ip := net.ParseIP(pp.IP)
		if ip == nil {
			continue
		}
		v4 := ip.To4()
		if v4 == nil {
			continue
		}
		out = append(out, v4...)
		var port [2]byte
		binary.BigEndian.PutUint16(port[:], uint16(pp.Port))
		out = append(out, port[:]...)
		n++
	}
	return out
}

// CompactPeers6 encodes IPv6 peers in the 18-byte-per-peer format (BEP 7).
func CompactPeers6(peers []Peer, want int, selfPeerID string) []byte {
	if want <= 0 || want > 200 {
		want = 50
	}
	out := make([]byte, 0, 18*want)
	n := 0
	for _, pp := range peers {
		if n >= want {
			break
		}
		if pp.PeerID == selfPeerID {
			continue
		}
		ip := net.ParseIP(pp.IP)
		if ip == nil || ip.To4() != nil {
			continue
		}
		v6 := ip.To16()
		if v6 == nil {
			continue
		}
		out = append(out, v6...)
		var port [2]byte
		binary.BigEndian.PutUint16(port[:], uint16(pp.Port))
		out = append(out, port[:]...)
		n++
	}
	return out
}

// DecodePeerID normalizes a peer_id into hex for use as a Redis hash field.
// Clients send the 20 raw bytes URL-encoded; we don't care what the bytes
// actually are, only that they round-trip consistently.
func DecodePeerID(raw string) (string, error) {
	if len(raw) != 20 {
		return "", fmt.Errorf("peer_id: expected 20 bytes, got %d", len(raw))
	}
	return toHex([]byte(raw)), nil
}

// DecodeInfoHashRaw parses the 20 raw bytes a client sends in the announce
// query's info_hash and returns the 40-char hex form used in DB + Redis
// keys.
func DecodeInfoHashRaw(raw string) (string, error) {
	if len(raw) != 20 {
		return "", fmt.Errorf("info_hash: expected 20 bytes, got %d", len(raw))
	}
	return toHex([]byte(raw)), nil
}

func toHex(b []byte) string {
	const hexchars = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexchars[c>>4]
		out[i*2+1] = hexchars[c&0xf]
	}
	return string(out)
}

// ErrUnknownTorrent is returned by the announce flow when the client sends
// an info_hash we've never registered.
var ErrUnknownTorrent = errors.New("tracker: unknown torrent")

// normalizeIP collapses "127.0.0.1:1234" style to just the host part and
// handles bracketed IPv6. Announce handlers pass the connection remote
// address plus the optional ?ip= override.
func normalizeIP(s string) string {
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "[") {
		if idx := strings.Index(s, "]"); idx > 0 {
			return s[1:idx]
		}
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s[:i], ":") {
		return s[:i]
	}
	return s
}

// ClientIP chooses the effective announce IP: trust the connection remote
// for v1 and ignore the ?ip= override (which is only for NAT hairpinning by
// peer-agreed trackers and is trivially spoofable).
func ClientIP(remoteAddr string) string {
	return normalizeIP(remoteAddr)
}
