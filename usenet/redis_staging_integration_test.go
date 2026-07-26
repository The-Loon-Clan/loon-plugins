//go:build integration

package usenet

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

// testRedis dials the throwaway Redis named by USENET_TEST_REDIS and flushes
// it. Mirrors the pg integration convention (USENET_TEST_DSN): a real server,
// because the whole point of these cases is Redis' own type semantics —
// WRONGTYPE, LTrim's delete-when-empty, SUnionStore — which a fake would only
// restate as the assumptions being tested.
func testRedis(t *testing.T) redis.UniversalClient {
	t.Helper()
	addr := os.Getenv("USENET_TEST_REDIS")
	if addr == "" {
		t.Skip("USENET_TEST_REDIS not set; skipping. Set it to host:port of a throwaway Redis.")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("ping %s: %v", addr, err)
	}
	if err := rdb.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("flushdb: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func newTestStaging(rdb redis.UniversalClient) *redisStaging {
	return newRedisStaging(rdb, func(context.Context) int { return 2 }, nil)
}

func TestEnsureReadySet_ConvertsLegacyList(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	r := newTestStaging(rdb)

	want := map[string]bool{}
	for i := 0; i < 250; i++ {
		m := fmt.Sprintf("alt.binaries.test:%04x", i)
		if err := rdb.RPush(ctx, readyKey, m).Err(); err != nil {
			t.Fatalf("seed: %v", err)
		}
		want[m] = true
	}

	r.ensureReadySet(ctx)

	if typ, _ := rdb.Type(ctx, readyKey).Result(); typ != "set" {
		t.Fatalf("type after conversion = %q, want set", typ)
	}
	got, err := rdb.SMembers(ctx, readyKey).Result()
	if err != nil {
		t.Fatalf("SMembers: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("converted %d members, want %d", len(got), len(want))
	}
	for _, m := range got {
		if !want[m] {
			t.Fatalf("unexpected member %q", m)
		}
	}
	if n, _ := rdb.Exists(ctx, readyConvKey).Result(); n != 0 {
		t.Error("conversion staging key left behind")
	}
}

func TestEnsureReadySet_LargeListConvertsAcrossCalls(t *testing.T) {
	// The prod stall: one whole-list LRange + one giant SAdd timed out
	// identically on every attempt, so the key stayed a LIST forever. A backlog
	// past the per-call window cap must still converge, one call at a time.
	ctx := context.Background()
	rdb := testRedis(t)
	r := newTestStaging(rdb)

	total := readyConvWindow*readyConvMaxWindows + 2500
	members := make([]interface{}, 0, 5000)
	for i := 0; i < total; i++ {
		members = append(members, fmt.Sprintf("g:%06x", i))
		if len(members) == 5000 {
			if err := rdb.RPush(ctx, readyKey, members...).Err(); err != nil {
				t.Fatalf("seed: %v", err)
			}
			members = members[:0]
		}
	}
	if len(members) > 0 {
		if err := rdb.RPush(ctx, readyKey, members...).Err(); err != nil {
			t.Fatalf("seed tail: %v", err)
		}
	}

	// First pass is capped and must leave the rest queued, not drop it.
	r.ensureReadySet(ctx)
	if typ, _ := rdb.Type(ctx, readyKey).Result(); typ != "list" {
		t.Fatalf("type after capped pass = %q, want list (conversion should not have finished)", typ)
	}
	if n, _ := rdb.LLen(ctx, readyKey).Result(); n != int64(total-readyConvWindow*readyConvMaxWindows) {
		t.Fatalf("remaining list len = %d, want %d", n, total-readyConvWindow*readyConvMaxWindows)
	}

	for i := 0; i < 5; i++ {
		r.ensureReadySet(ctx)
		if typ, _ := rdb.Type(ctx, readyKey).Result(); typ == "set" {
			break
		}
	}
	if typ, _ := rdb.Type(ctx, readyKey).Result(); typ != "set" {
		t.Fatalf("type after resumed passes = %q, want set", typ)
	}
	if n, _ := rdb.SCard(ctx, readyKey).Result(); n != int64(total) {
		t.Fatalf("converted %d members, want %d — a window was dropped", n, total)
	}
}

func TestEnsureReadySet_PreservesConcurrentlyQueuedEntries(t *testing.T) {
	// A partial conversion plus live entries in the real set: folding the
	// staged set in must UNION, not RENAME over the top.
	ctx := context.Background()
	rdb := testRedis(t)
	r := newTestStaging(rdb)

	if err := rdb.SAdd(ctx, readyConvKey, "legacy:aaa", "legacy:bbb").Err(); err != nil {
		t.Fatalf("seed staged: %v", err)
	}
	if err := rdb.SAdd(ctx, readyKey, "live:ccc").Err(); err != nil {
		t.Fatalf("seed live: %v", err)
	}

	r.ensureReadySet(ctx)

	got, err := rdb.SMembers(ctx, readyKey).Result()
	if err != nil {
		t.Fatalf("SMembers: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("members = %v, want the 2 migrated + 1 live entry", got)
	}
}

func TestStageArticles_SelfHealsWhenReadyBecomesList(t *testing.T) {
	// The 23h prod failure mode: convDone latches true on a healthy key, the
	// key later turns into a LIST, and every SAdd then errors WRONGTYPE for the
	// life of the container. Staging must recover on its own.
	ctx := context.Background()
	rdb := testRedis(t)
	r := newTestStaging(rdb)

	r.ensureReadySet(ctx) // key absent -> latches convDone
	r.convMu.Lock()
	latched := r.convDone
	r.convMu.Unlock()
	if !latched {
		t.Fatal("convDone should latch when the key is absent")
	}

	if err := rdb.RPush(ctx, readyKey, "stale:legacy").Err(); err != nil {
		t.Fatalf("seed list: %v", err)
	}

	err := r.readyRetry(ctx, func() error {
		return rdb.SAdd(ctx, readyKey, "fresh:entry").Err()
	})
	if err != nil {
		t.Fatalf("readyRetry did not recover from WRONGTYPE: %v", err)
	}
	if typ, _ := rdb.Type(ctx, readyKey).Result(); typ != "set" {
		t.Fatalf("type after self-heal = %q, want set", typ)
	}
	members, _ := rdb.SMembers(ctx, readyKey).Result()
	if len(members) != 2 {
		t.Fatalf("members = %v, want both the migrated legacy entry and the fresh one", members)
	}
}

func TestReadyRetry_PassesThroughNonWrongTypeErrors(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	r := newTestStaging(rdb)

	sentinel := fmt.Errorf("some other failure")
	calls := 0
	err := r.readyRetry(ctx, func() error {
		calls++
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("err = %v, want the original error", err)
	}
	if calls != 1 {
		t.Fatalf("op ran %d times, want 1 — only WRONGTYPE should retry", calls)
	}
}
