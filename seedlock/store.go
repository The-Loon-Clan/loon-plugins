package seedlock

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// Where a claim lives.
//
// Redis, not Postgres, and the reason is correctness rather than speed. The
// announce endpoints are registered on BOTH the web and api processes, so an
// in-memory map would give each process its own idea of who holds a torrent —
// and a member wanting to seed from two hosts could simply point one at each.
// The tracker already keeps its peer state here for the same reason.
//
// The TTL IS the lock window. A claim that stops being refreshed expires on its
// own, so there is no sweep to write and no way for a crashed client to hold a
// torrent forever.

// Store is the claim persistence.
type Store interface {
	// Acquire atomically claims a torrent for host, or reports who holds it.
	//
	// Returns held="" when the caller now holds the claim.
	Acquire(ctx context.Context, userID int64, infoHash, host string, window time.Duration) (held Claim, err error)
	// Refresh extends the caller's own claim.
	Refresh(ctx context.Context, userID int64, infoHash string, window time.Duration) error
	// Release drops a claim — on "stopped", or when a member clears it.
	Release(ctx context.Context, userID int64, infoHash string) error
	// Held reports the current claim, if any.
	Held(ctx context.Context, userID int64, infoHash string) (Claim, error)
	// HeldBy lists a member's live claims, for their own page.
	HeldBy(ctx context.Context, userID int64) (map[string]Claim, error)
}

// RedisStore is the Redis implementation.
type RedisStore struct{ rdb redis.UniversalClient }

func NewRedisStore(rdb redis.UniversalClient) *RedisStore { return &RedisStore{rdb: rdb} }

var _ Store = (*RedisStore)(nil)

func claimKey(userID int64, infoHash string) string {
	return "seedlock:" + itoa(userID) + ":" + infoHash
}

// memberPattern matches every claim a member holds. SCANned rather than kept as
// a second index, because the page that uses it is opened by one person at a
// time while the announce path — which must stay fast — never needs it.
func memberPattern(userID int64) string {
	return "seedlock:" + itoa(userID) + ":*"
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// Acquire claims the torrent, or says who already has it.
//
// SET NX is what makes this safe: two announces arriving together from
// different hosts must not both be told they hold the claim, and a GET followed
// by a SET would let exactly that happen.
func (s *RedisStore) Acquire(ctx context.Context, userID int64, infoHash, host string, window time.Duration) (Claim, error) {
	key := claimKey(userID, infoHash)
	ok, err := s.rdb.SetNX(ctx, key, host, window).Result()
	if err != nil {
		return Claim{}, err
	}
	if ok {
		return Claim{}, nil // the caller now holds it
	}
	cur, err := s.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		// It expired between the SET and the GET. Rare, and the honest answer
		// is "nobody holds it" — the next announce will claim it cleanly.
		return Claim{}, nil
	}
	if err != nil {
		return Claim{}, err
	}
	// LastSeen is derived from the remaining TTL rather than stored: the value
	// is a host and nothing else, so there is no second field to keep in step.
	ttl, err := s.rdb.TTL(ctx, key).Result()
	if err != nil {
		return Claim{Host: cur}, nil
	}
	return Claim{Host: cur, LastSeen: time.Now().Add(ttl - window)}, nil
}

func (s *RedisStore) Refresh(ctx context.Context, userID int64, infoHash string, window time.Duration) error {
	return s.rdb.Expire(ctx, claimKey(userID, infoHash), window).Err()
}

func (s *RedisStore) Release(ctx context.Context, userID int64, infoHash string) error {
	return s.rdb.Del(ctx, claimKey(userID, infoHash)).Err()
}

func (s *RedisStore) Held(ctx context.Context, userID int64, infoHash string) (Claim, error) {
	key := claimKey(userID, infoHash)
	host, err := s.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return Claim{}, nil
	}
	if err != nil {
		return Claim{}, err
	}
	return Claim{Host: host, LastSeen: time.Now()}, nil
}

// HeldBy lists a member's live claims.
//
// SCAN rather than KEYS: this runs on a page a member opened, and KEYS blocks
// the whole server while it walks the keyspace — which on a busy tracker is
// every announce waiting behind one person's curiosity.
func (s *RedisStore) HeldBy(ctx context.Context, userID int64) (map[string]Claim, error) {
	out := map[string]Claim{}
	prefixLen := len(claimKey(userID, ""))
	var cursor uint64
	for {
		keys, next, err := s.rdb.Scan(ctx, cursor, memberPattern(userID), 100).Result()
		if err != nil {
			return out, err
		}
		for _, k := range keys {
			if len(k) <= prefixLen {
				continue
			}
			host, err := s.rdb.Get(ctx, k).Result()
			if err != nil {
				continue // expired mid-scan; not an error, just gone
			}
			out[k[prefixLen:]] = Claim{Host: host, LastSeen: time.Now()}
		}
		cursor = next
		if cursor == 0 {
			return out, nil
		}
	}
}
