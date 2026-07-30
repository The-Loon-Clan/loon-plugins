package usenet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisStaging is the fast, best-effort backend — a faithful lift of prod's
// Redis assembly pipeline (indexer-site pkg/storage/postgres/article_staging.go
// + redis_articles.go), adapted to the plugin's stagedArticle type and go-redis
// v9. See README.md.
//
// Unlike pgStaging, completeness is detected inline at Stage time (a set that
// just completed is pushed to nzb:ready) and hopeless sets are evicted on the
// same insert pipeline; the builder's candidateGroups() just drains the ready
// list. Staged data lives only in Redis with a short TTL (staging_ttl_hours,
// default 2h), so it is best-effort:
// under memory pressure incomplete stale sets are shed, but complete sets have
// already been queued. Durable NZBs still write through PGStore.
//
// Key model (lifted verbatim):
//
//	art:{group}:{hash}     Hash  per-article JSON, field "fileNum:partNum"   TTL staging_ttl_hours
//	grp:{group}:{hash}     Hash  set metadata (base_subject, totals, created_at) TTL staging_ttl_hours
//	nzb:ready              Set   "{group}:{hash}" of complete sets
//	active_groups:{group}  Set   in-progress hashes
//
// hash = hex(sha256(group + ":" + base)[:8]).
type redisStaging struct {
	rdb redis.UniversalClient
	// ttlHours is read per stage call so the admin knob applies live (same
	// convention as pgStaging.limits). Values <= 0 fall back to 2h.
	ttlHours func(context.Context) int
	// onEvict reports hopeless-set evictions to the caller (telemetry) — the
	// eviction is silent by design, and "is it failing or filtering?" is a
	// question the dashboard must be able to answer.
	onEvict func(n int)

	// report surfaces operationally significant staging events to the host
	// error log + the crawlers ring (p.reportErr). Optional — nil in tests.
	report func(ctx context.Context, op string, err error)

	// convMu/convDone gate the nzb:ready LIST→SET migration (see
	// ensureReadySet). Done stays false on a transient failure so the next
	// caller retries instead of erroring WRONGTYPE forever. convScanned/convLive
	// accumulate across the calls a large backlog takes to drain, purely so the
	// completion note can report what the migration actually did; both reset
	// when a run finishes.
	convMu      sync.Mutex
	convDone    bool
	convScanned int
	convLive    int
}

func newRedisStaging(rdb redis.UniversalClient, ttlHours func(context.Context) int, onEvict func(int), report func(context.Context, string, error)) *redisStaging {
	return &redisStaging{rdb: rdb, ttlHours: ttlHours, onEvict: onEvict, report: report}
}

// note reports a staging event when a reporter is wired. Never a silent drop:
// the migration discarding entries is exactly the kind of bounded coverage an
// operator must be told about rather than infer.
func (r *redisStaging) note(ctx context.Context, op string, err error) {
	if r.report != nil {
		r.report(ctx, op, err)
	}
}

// Conversion sizing. A whole-list LRange + one giant SAdd is what broke in
// prod: the legacy key held 7.3M entries, past Redis' 1M-argument multibulk
// limit, so the SAdd could never succeed — every attempt failed identically and
// nzb:ready stayed a LIST while every SAdd against it errored WRONGTYPE. Small
// windows finish well inside go-redis' default 3s timeouts; the per-call window
// cap keeps a large backlog from eating a crawl pass, since progress is durable
// and resumes on the next call.
//
// The window stays at 1000 because readyConvScript unpacks it onto the Lua
// stack (limit ~8000).
const (
	readyConvWindow     = 1000
	readyConvMaxWindows = 50
)

// readyConvScript migrates one window of the legacy LIST into the staging SET,
// keeping only entries whose set metadata still exists.
//
// It is a script, not three Go calls, because nzb:ready is a single fleet-wide
// key with no lease and convMu only serializes ONE process: N crawler workers
// convert concurrently (crawl.go splits groups across workers). LTrim removes
// by POSITION, so a read-then-trim pair lets a second converter trim a window
// it never copied — measured at 12-15k entries destroyed per overlapped pass on
// a 40k list, each one a completed release that would never reach the builder.
// Redis runs a script atomically, so the window a converter trims is exactly
// the window it copied.
//
// Liveness is decided by EXISTS on the set's grp: metadata rather than by
// position in the list. A ready entry is "{group}:{hash}" and grpKey is
// "grp:"+group+":"+hash, so the metadata key is just "grp:"+entry. An entry
// whose grp: hash has TTL'd out is dead by the same definition candidateGroups
// already uses ("Meta gone ... this ready entry is dead"), so dropping it here
// is the established semantics, not a new policy — and unlike an
// age-by-position guess it is checked rather than argued. That is what keeps a
// 7.3M-entry fossil from being imported wholesale into a set the builder would
// then sample 500-at-a-time forever, indexing nothing.
//
// Assumes a single keyspace: the grp: reads aren't declared in KEYS, as
// elsewhere in this backend (SUnionStore, the art:/grp: pipelines). Redis
// Cluster would need key-slot work across the whole staging design, not just
// here.
var readyConvScript = redis.NewScript(`
local batch = redis.call('LRANGE', KEYS[1], 0, tonumber(ARGV[1]) - 1)
local n = #batch
if n == 0 then return {0, 0} end
local live = {}
for i = 1, n do
  if redis.call('EXISTS', 'grp:' .. batch[i]) == 1 then
    live[#live + 1] = batch[i]
  end
end
if #live > 0 then
  redis.call('SADD', KEYS[2], unpack(live))
end
redis.call('LTRIM', KEYS[1], n, -1)
return {n, #live}
`)

// readyFoldScript folds the staging set into the live one. Atomic for the same
// reason as above: between a Go-side SUnionStore and Del, a sibling converter's
// SAdd into the staging key would be deleted unread. Returns -1 if the live key
// is somehow still a list (caller leaves it for the next pass), 0 if there was
// nothing staged, 1 on a fold.
var readyFoldScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[2]) == 0 then return 0 end
if redis.call('TYPE', KEYS[1])['ok'] == 'list' then return -1 end
redis.call('SUNIONSTORE', KEYS[1], KEYS[1], KEYS[2])
redis.call('DEL', KEYS[2])
return 1
`)

// readyConvKey is the staging set the LIST is copied into before it is folded
// into nzb:ready.
const readyConvKey = readyKey + ":setconv"

// ensureReadySet migrates nzb:ready from the pre-2026-07 LIST encoding to a
// SET. LRem on a deep list made every deleteStaged O(len) — a set makes
// queue/remove O(1) and deduplicates re-queued sets for free. Random drain
// order replaces FIFO, which the builder never relied on.
//
// Each window is copied and trimmed by one atomic script (see
// readyConvScript), so an interrupted conversion resumes from durable progress
// and a concurrent converter can never trim a window it did not copy.
//
// convDone latches only once the key is genuinely a set with nothing left to
// migrate. It is NOT a promise for the life of the process: if nzb:ready turns
// back into a list, callers reset the latch via readyRetry rather than erroring
// WRONGTYPE until the container restarts.
func (r *redisStaging) ensureReadySet(ctx context.Context) {
	r.convMu.Lock()
	defer r.convMu.Unlock()
	if r.convDone {
		return
	}
	t, err := r.rdb.Type(ctx, readyKey).Result()
	if err != nil {
		return
	}
	switch t {
	case "list":
		for i := 0; i < readyConvMaxWindows; i++ {
			res, err := readyConvScript.Run(ctx, r.rdb, []string{readyKey, readyConvKey}, readyConvWindow).Slice()
			if err != nil || len(res) != 2 {
				return
			}
			scanned, live := luaInt(res[0]), luaInt(res[1])
			if scanned == 0 {
				break
			}
			r.convScanned += scanned
			r.convLive += live
		}
		// LTrim drops the key with its last element, so "gone" means drained.
		if n, err := r.rdb.Exists(ctx, readyKey).Result(); err != nil || n > 0 {
			return // more to migrate — resume on a later call
		}
	case "none", "set":
		// Fall through: a previous call may have left a partial conversion set
		// behind, and folding it in here is what finishes that run.
	default:
		return // hash/zset/string: not ours to convert; let the caller's error surface
	}

	folded, err := readyFoldScript.Run(ctx, r.rdb, []string{readyKey, readyConvKey}).Int()
	if err != nil || folded < 0 {
		return
	}
	if dropped := r.convScanned - r.convLive; dropped > 0 {
		// Never silent: say what was migrated and what was discarded, and be
		// precise that these are NOT re-crawled — their batches staged fine, so
		// the crawl watermark is already past them.
		r.note(ctx, "usenet/staging-legacy-drop", fmt.Errorf(
			"nzb:ready LIST→SET migration: kept %d live entries, discarded %d whose grp: metadata had already expired (their staged articles are gone; the crawl watermark is past them, so they are not re-crawled)",
			r.convLive, dropped))
	}
	r.convDone = true
	r.convScanned, r.convLive = 0, 0
}

// luaInt reads one integer out of a script's multi-value reply.
func luaInt(v interface{}) int {
	n, _ := v.(int64)
	return int(n)
}

// readyRetry runs a nzb:ready operation and, if it failed only because the key
// is the wrong type, re-runs the migration and tries once more.
//
// Without this, convDone is a one-way latch per process: a key that becomes a
// LIST after the first check poisons every subsequent call for the life of the
// container. That is exactly how a stale pre-migration list stalled prod's
// crawl->stage->build pipeline for 23 hours — the watermark never advances on a
// staging error, so the same articles re-crawl and re-fail forever.
func (r *redisStaging) readyRetry(ctx context.Context, op func() error) error {
	err := op()
	if err == nil || !isWrongType(err) {
		return err
	}
	r.convMu.Lock()
	r.convDone = false
	r.convMu.Unlock()
	r.ensureReadySet(ctx)
	return op()
}

// isWrongType reports a Redis WRONGTYPE reply (operation against a key holding
// the wrong kind of value).
func isWrongType(err error) bool {
	return err != nil && strings.Contains(err.Error(), "WRONGTYPE")
}

// stagingTTL resolves the live TTL knob with the historical 2h floor-default.
func (r *redisStaging) stagingTTL(ctx context.Context) time.Duration {
	if r.ttlHours != nil {
		if h := r.ttlHours(ctx); h > 0 {
			return time.Duration(h) * time.Hour
		}
	}
	return 2 * time.Hour
}

var _ stagingStore = (*redisStaging)(nil)

const readyKey = "nzb:ready"

func artKey(group, hash string) string { return "art:" + group + ":" + hash }
func grpKey(group, hash string) string { return "grp:" + group + ":" + hash }
func activeKey(group string) string    { return "active_groups:" + group }

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// groupHashKey is the 16-hex key suffix for a (group, base_subject) pair. It is
// distinct from assemble.go's contentHashArticles (the NZB content_hash) — this
// one keys the transient Redis set, that one keys the durable nzbs row.
func groupHashKey(group, base string) string {
	d := sha256.New()
	d.Write([]byte(group))
	d.Write([]byte{':'})
	d.Write([]byte(base))
	var sum [sha256.Size]byte
	d.Sum(sum[:0])
	// 128 bits, not 64: posters control base subjects, and a 64-bit key is
	// within reach of an offline birthday attack — two subjects colliding
	// here would interleave their articles into one staged set. Sets under
	// the old width simply age out (staging is transient).
	return hex.EncodeToString(sum[:16])
}

func formatFieldKey(fileNum, partNum int) string {
	return strconv.Itoa(fileNum) + ":" + strconv.Itoa(partNum)
}

// compactArticle is the minimal per-article JSON stored in the art: hash. The
// short json tags describe the wire format; it is WRITTEN by marshalCompact (not
// encoding/json — see there) and read back with json.Unmarshal, exactly as prod
// does.
type compactArticle struct {
	MessageID  string `json:"m"`
	Subject    string `json:"s"`
	From       string `json:"f"`
	Bytes      int64  `json:"b"`
	Date       int64  `json:"d"`
	PartNum    int    `json:"p"`
	TotalParts int    `json:"tp"`
	SegTotal   int    `json:"st,omitempty"`
	FileNum    int    `json:"fn,omitempty"`
	TotalFiles int    `json:"tf,omitempty"`
	FileParts  bool   `json:"fp,omitempty"`
}

// bufPool reuses byte buffers for marshalCompact to reduce GC pressure on the
// hot ingest path.
var bufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 0, 512)
		return &b
	},
}

// marshalCompact encodes a compactArticle without reflection — and, critically,
// WITHOUT encoding/json's HTML escaping. Message-ids are always wrapped in
// <addr@host>, which json.Marshal expands to <…>: that inflates every
// staged value (the one thing redis mode is trying to conserve) and stops the
// bytes being identical to what prod writes into the same key space. Lifted from
// prod's MarshalCompact; decode stays json.Unmarshal, as prod does.
func marshalCompact(ca *compactArticle) []byte {
	bp := bufPool.Get().(*[]byte)
	b := (*bp)[:0]

	b = append(b, `{"m":"`...)
	b = appendEscaped(b, ca.MessageID)
	b = append(b, `","s":"`...)
	b = appendEscaped(b, ca.Subject)
	b = append(b, `","f":"`...)
	b = appendEscaped(b, ca.From)
	b = append(b, `","b":`...)
	b = strconv.AppendInt(b, ca.Bytes, 10)
	b = append(b, `,"d":`...)
	b = strconv.AppendInt(b, ca.Date, 10)
	b = append(b, `,"p":`...)
	b = strconv.AppendInt(b, int64(ca.PartNum), 10)
	b = append(b, `,"tp":`...)
	b = strconv.AppendInt(b, int64(ca.TotalParts), 10)
	if ca.SegTotal != 0 {
		b = append(b, `,"st":`...)
		b = strconv.AppendInt(b, int64(ca.SegTotal), 10)
	}
	if ca.FileNum != 0 {
		b = append(b, `,"fn":`...)
		b = strconv.AppendInt(b, int64(ca.FileNum), 10)
	}
	if ca.TotalFiles != 0 {
		b = append(b, `,"tf":`...)
		b = strconv.AppendInt(b, int64(ca.TotalFiles), 10)
	}
	if ca.FileParts {
		b = append(b, `,"fp":true`...)
	}
	b = append(b, '}')

	out := make([]byte, len(b))
	copy(out, b)
	*bp = b
	bufPool.Put(bp)
	return out
}

// appendEscaped appends s to b with JSON string escaping for \ and " and control
// characters — deliberately not for < and > (see marshalCompact).
func appendEscaped(b []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b = append(b, '\\', c)
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0',
				"0123456789abcdef"[c>>4],
				"0123456789abcdef"[c&0xf])
		default:
			b = append(b, c)
		}
	}
	return b
}

// stageArticles inserts articles + metadata, detects newly-complete sets, and
// evicts hopeless ones — all on one pipeline round-trip. A set that completes is
// pushed to nzb:ready for the builder. Lifted from prod's RedisInsertArticles.
func (r *redisStaging) stageArticles(ctx context.Context, arts []stagedArticle) (int, error) {
	if len(arts) == 0 {
		return 0, nil
	}
	r.ensureReadySet(ctx)
	ttl := r.stagingTTL(ctx)
	type groupUpdate struct {
		hash, groupName, baseSub string
		articles                 []stagedArticle
	}

	groups := make(map[string]*groupUpdate)
	// Article-number bounds per touched set, folded once the pipeline lands.
	var spans []spanUpdate
	for _, a := range arts {
		if a.BaseSubject == "" || a.MessageID == "" {
			continue
		}
		hash := groupHashKey(a.Group, a.BaseSubject)
		key := a.Group + ":" + hash
		gu := groups[key]
		if gu == nil {
			gu = &groupUpdate{hash: hash, groupName: a.Group, baseSub: a.BaseSubject}
			groups[key] = gu
		}
		gu.articles = append(gu.articles, a)
	}

	pipe := r.rdb.Pipeline()
	now := time.Now().Unix()

	for _, gu := range groups {
		fields := make([]interface{}, 0, len(gu.articles)*2)
		for _, a := range gu.articles {
			ca := compactArticle{
				MessageID: a.MessageID, Subject: a.Subject, From: a.Poster,
				Bytes: a.Bytes, Date: a.Posted.Unix(), PartNum: a.PartNum,
				TotalParts: a.TotalParts, SegTotal: a.SegTotal, FileNum: a.FileNum,
				TotalFiles: a.TotalFiles, FileParts: a.FileParts,
			}
			fields = append(fields, formatFieldKey(a.FileNum, a.PartNum), marshalCompact(&ca))
		}
		pipe.HSet(ctx, artKey(gu.groupName, gu.hash), fields...)
	}

	for _, gu := range groups {
		gk := grpKey(gu.groupName, gu.hash)
		ak := artKey(gu.groupName, gu.hash)
		first := gu.articles[0]
		maxTP, maxTF, maxST := 0, 0, 0
		var newestDate int64
		for _, a := range gu.articles {
			if a.TotalParts > maxTP {
				maxTP = a.TotalParts
			}
			if a.TotalFiles > maxTF {
				maxTF = a.TotalFiles
			}
			if a.SegTotal > maxST {
				maxST = a.SegTotal
			}
			if a.Posted.Unix() > newestDate {
				newestDate = a.Posted.Unix()
			}
		}
		pipe.HSet(ctx, gk,
			"base_subject", gu.baseSub,
			"group_name", gu.groupName,
			"file_parts", boolStr(first.FileParts),
			"total_parts", maxTP,
			"total_files", maxTF,
			"seg_total", maxST,
			"newest_date", newestDate,
		)
		// Per-file segment totals, as segFieldKey(fileNum).
		//
		// seg_total above is the MAX across the whole set, and a release is one
		// large media file plus a handful of tiny par2s — so applying that max
		// to every file demands orders of magnitude more articles than exist.
		// Production showed a complete set reading have=156,882 need=2,016,196
		// and never building. assemble.go's isComplete has always tracked this
		// per file; the Redis port collapsed it to one global value, and only
		// the per-file numbers can restore the distinction.
		//
		// Written per touched file and only ever upward, so a later batch
		// carrying a larger count for the same file corrects an earlier one.
		perFile := map[int]int{}
		lo, hi := 0, 0
		for _, a := range gu.articles {
			if a.SegTotal > perFile[a.FileNum] {
				perFile[a.FileNum] = a.SegTotal
			}
			if a.ArticleNum > 0 {
				if lo == 0 || a.ArticleNum < lo {
					lo = a.ArticleNum
				}
				if a.ArticleNum > hi {
					hi = a.ArticleNum
				}
			}
		}
		if lo > 0 {
			spans = append(spans, spanUpdate{key: gk, lo: lo, hi: hi})
		}
		for fn, st := range perFile {
			if st > 0 {
				pipe.HSet(ctx, gk, segFieldKey(fn), st)
			}
		}
		pipe.HSetNX(ctx, gk, "created_at", now)
		// touched_at moves on every batch that adds to this set. The eviction
		// below needs "has it stopped growing", not "how long ago did it start" —
		// see there.
		pipe.HSet(ctx, gk, "touched_at", now)
		pipe.SAdd(ctx, activeKey(gu.groupName), gu.hash)
		pipe.Expire(ctx, ak, ttl)
		pipe.Expire(ctx, gk, ttl)
		// The active set MUST expire too (refreshed on every touch): art:/grp:
		// keys that TTL out incomplete otherwise leave their hash in
		// active_groups:{group} forever, and incompleteSets walks every
		// accumulated ref once per build pass — an unbounded leak. Double the
		// data TTL so the ref always outlives the data it points at.
		pipe.Expire(ctx, activeKey(gu.groupName), 2*ttl)
	}

	// Cheap HLen + meta per touched group; HKeys (which ships every field name)
	// only runs out-of-band below when HLen says a set could be complete — prod
	// learned the hard way that pipelining HKeys on every batch OOM-kills Redis.
	type checkItem struct {
		gu      *groupUpdate
		metaCmd *redis.MapStringStringCmd
		lenCmd  *redis.IntCmd
	}
	checks := make([]checkItem, 0, len(groups))
	for _, gu := range groups {
		checks = append(checks, checkItem{
			gu:      gu,
			metaCmd: pipe.HGetAll(ctx, grpKey(gu.groupName, gu.hash)),
			lenCmd:  pipe.HLen(ctx, artKey(gu.groupName, gu.hash)),
		})
	}

	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return 0, fmt.Errorf("redis insert pipeline: %w", err)
	}
	// After the metadata exists, so a fold can never create a half-formed set.
	r.foldSpans(ctx, spans)

	var readyGroups []interface{}
	var evictKeys []string
	type evictEntry struct{ group, hash string }
	var evictMembers []evictEntry

	for _, ci := range checks {
		meta := ci.metaCmd.Val()
		length := int(ci.lenCmd.Val())
		needed := groupNeededParts(meta)

		if needed > 0 && length >= needed {
			fields, err := r.rdb.HKeys(ctx, artKey(ci.gu.groupName, ci.gu.hash)).Result()
			if err != nil {
				// This is the ONE moment a set can be recognised as complete:
				// completeness is checked only on a batch that ADDS to the set,
				// so a set whose last batch hits this error is never re-checked
				// and sits staged until it expires. Swallowing it made that
				// loss indistinguishable from a set that was never finished.
				r.note(ctx, "usenet/staging-complete-check",
					fmt.Errorf("%s: %w", ci.gu.groupName, err))
			}
			if err == nil && isGroupComplete(meta, fields) {
				readyGroups = append(readyGroups, ci.gu.groupName+":"+ci.gu.hash)
				continue
			}
		}
		// Hopeless-eviction: a set that has STOPPED GROWING and is still far
		// short of what it needs.
		//
		// Measured from touched_at, not created_at. A large release is tens of
		// thousands of segments spread over dozens of batches, and a bulk
		// re-read fetches those batches out of order across many parallel
		// connections — so it cannot be 30% complete five minutes after its
		// first article lands. Judging from creation deleted exactly the big
		// multi-file releases while they were still actively filling: prod shed
		// ~128 sets a minute during a re-read and built nothing.
		//
		// created_at is the fallback for sets staged before touched_at existed;
		// they age out within the staging TTL.
		if age, ok := evictionStaleness(meta, now); ok && age > 300 {
			if needed > 0 && length > 0 && float64(length)/float64(needed) < 0.30 {
				evictKeys = append(evictKeys,
					artKey(ci.gu.groupName, ci.gu.hash), grpKey(ci.gu.groupName, ci.gu.hash))
				evictMembers = append(evictMembers, evictEntry{ci.gu.groupName, ci.gu.hash})
			}
		}
	}

	if len(readyGroups) > 0 {
		// Checked: a dropped SAdd here loses a COMPLETED release — it never
		// reaches the builder and completeness won't re-fire (the set has all
		// its articles now). Returning the error leaves the batch's watermark
		// un-advanced (crawl.go treats a staging error as ok=false), so the
		// same articles re-crawl and re-queue.
		err := r.readyRetry(ctx, func() error {
			return r.rdb.SAdd(ctx, readyKey, readyGroups...).Err()
		})
		if err != nil {
			return len(arts), fmt.Errorf("queue ready sets: %w", err)
		}
	}
	if len(evictKeys) > 0 {
		evPipe := r.rdb.Pipeline()
		evPipe.Del(ctx, evictKeys...)
		for _, em := range evictMembers {
			evPipe.SRem(ctx, activeKey(em.group), em.hash)
		}
		// Count evictions only when the pipeline actually ran: this counter
		// exists to prove eviction is WORKING, so over-reporting on the exact
		// failure it should reveal would defeat it.
		if _, err := evPipe.Exec(ctx); err == nil && r.onEvict != nil {
			r.onEvict(len(evictMembers))
		}
	}

	return len(arts), nil
}

// segFieldKey names the per-file segment-total field in a set's meta hash.
// Prefixed so it cannot collide with the fixed keys beside it.
func segFieldKey(fileNum int) string { return "st:" + strconv.Itoa(fileNum) }

// perFileSegTotals reads the segFieldKey entries back out of a meta hash.
func perFileSegTotals(meta map[string]string) map[int]int {
	out := map[int]int{}
	for k, v := range meta {
		if !strings.HasPrefix(k, "st:") {
			continue
		}
		fn, err := strconv.Atoi(k[3:])
		if err != nil {
			continue
		}
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			out[fn] = n
		}
	}
	return out
}

// evictionStaleness reports how long a set has gone WITHOUT a new article, and
// whether that is knowable at all.
//
// Measured from touched_at rather than created_at, and that distinction is the
// whole point. A large release is tens of thousands of segments across dozens
// of batches, and a bulk re-read fetches those out of order over many parallel
// connections — so it cannot be far along five minutes after its first article
// lands. Counting from creation deleted exactly those releases while they were
// still filling; production shed ~128 sets a minute during a re-read and built
// nothing. A set still receiving articles is not hopeless, however incomplete.
//
// created_at is the fallback for sets staged before touched_at existed.
func evictionStaleness(meta map[string]string, now int64) (age int64, ok bool) {
	from, _ := strconv.ParseInt(meta["touched_at"], 10, 64)
	if from == 0 {
		from, _ = strconv.ParseInt(meta["created_at"], 10, 64)
	}
	if from <= 0 {
		return 0, false
	}
	return now - from, true
}

// pendingNeed is what the "forming releases" card shows as a set's requirement.
// It is deliberately a thin alias for groupNeededParts rather than its own
// calculation: the card exists to explain why the builder has not taken a set,
// so any divergence between them makes it actively misleading.
func pendingNeed(meta map[string]string) int { return groupNeededParts(meta) }

// groupNeededParts is a cheap LOWER BOUND on the article count a set needs, used
// only to decide whether the exact check (isGroupComplete, which costs an HKEYS)
// is worth running.
//
// A lower bound is the correct shape here. Overestimating does not merely delay
// a set — it withholds it forever, because the exact check never gets to run.
// The previous totalFiles*seg_total was an over-estimate by roughly the ratio
// between a release's media file and its par2s, and it kept every multi-file
// release out of the builder permanently.
func groupNeededParts(meta map[string]string) int {
	fileParts := meta["file_parts"] == "true" || meta["file_parts"] == "1"
	totalParts, _ := strconv.Atoi(meta["total_parts"])
	totalFiles, _ := strconv.Atoi(meta["total_files"])
	segTotal, _ := strconv.Atoi(meta["seg_total"])

	if fileParts && totalFiles > 0 {
		// Sum of what each KNOWN file needs. Files not seen yet contribute
		// nothing, so the bound only rises as the set fills — never above the
		// truth, which is exactly the property required.
		if per := perFileSegTotals(meta); len(per) > 0 {
			sum := 0
			for _, n := range per {
				sum += n
			}
			// Every file still unseen owes at least one article.
			if missing := totalFiles - len(per); missing > 0 {
				sum += missing
			}
			if sum > 0 {
				return sum
			}
		}
		// Sets staged before per-file totals existed: the largest file plus one
		// article for each remaining file. Still a lower bound, still safe.
		if segTotal > 0 {
			return segTotal + totalFiles - 1
		}
		return totalFiles
	}
	if totalParts > 0 {
		return totalParts
	}
	return 0
}

// isGroupComplete mirrors assemble.go's isComplete but works on Redis meta +
// field keys (so it can run in the insert pipeline without loading article data).
func isGroupComplete(meta map[string]string, fields []string) bool {
	if len(meta) == 0 || len(fields) == 0 {
		return false
	}
	fileParts := meta["file_parts"] == "true" || meta["file_parts"] == "1"
	totalParts, _ := strconv.Atoi(meta["total_parts"])
	totalFiles, _ := strconv.Atoi(meta["total_files"])
	segTotal, _ := strconv.Atoi(meta["seg_total"])

	if fileParts && totalFiles > 0 && segTotal > 0 {
		fileSegs := make(map[int]int)
		for _, f := range fields {
			p := strings.SplitN(f, ":", 2)
			if len(p) != 2 {
				continue
			}
			fn, _ := strconv.Atoi(p[0])
			fileSegs[fn]++
		}
		// Each file is judged against ITS OWN segment total, mirroring
		// assemble.go's isComplete. Judging every file against the set-wide
		// maximum meant only the largest file could ever qualify, so a release
		// of one media file plus a dozen par2s counted 1 complete file out of
		// 13 and stayed pending until it expired.
		per := perFileSegTotals(meta)
		completeFiles := 0
		for fn, count := range fileSegs {
			need := per[fn]
			if need <= 0 {
				// Pre-upgrade set with no per-file total recorded. The set-wide
				// max is the old behaviour and is wrong for small files, but it
				// is the only figure available; these expire within the staging
				// TTL.
				need = segTotal
			}
			if count >= need {
				completeFiles++
			}
		}
		return completeFiles >= totalFiles
	}
	if !fileParts && totalParts > 0 {
		return len(fields) >= totalParts
	}
	return false
}

// candidateGroups drains up to `limit` complete sets from nzb:ready. Each entry
// is "{group}:{hash}"; we resolve base_subject from grp meta so the builder
// (which re-verifies with isComplete and titles the NZB from base) gets a real
// groupKey. Popping matches prod's BLPOP semantics — a popped set that fails to
// build is left in art:/grp: to TTL out rather than re-queued.
func (r *redisStaging) candidateGroups(ctx context.Context, limit int) ([]groupKey, candidateStats, error) {
	if limit <= 0 {
		limit = 500
	}
	var stats candidateStats
	// Depth BEFORE the draw. The draw is a random sample, not a queue head, so
	// depth-vs-sampled is the only way to see the backlog it is sampling from.
	if n, err := r.rdb.SCard(ctx, readyKey).Result(); err == nil {
		stats.ReadyDepth = n
	}
	// PEEK, don't pop. The build loop is written for the pg contract "leave
	// staged, retry next pass" — it deleteStaged's a set only on success or a
	// permanent drop (blocked ext / blacklist / junk), and leaves it on a
	// transient sink error or on shutdown. Popping here would break that: every
	// entry returned but not built would be gone, losing a COMPLETED release on
	// the exact transient failure assemble.go promises to survive. Only one
	// builder runs at a time (the "Usenet Builder" job lease), so a peek cannot
	// double-dispatch. Entries leave nzb:ready only via deleteStaged.
	// SRandMemberN samples without removing; order across passes is random,
	// which is fine — every entry stays queued until deleteStaged takes it.
	r.ensureReadySet(ctx)
	var entries []string
	err := r.readyRetry(ctx, func() error {
		var e error
		entries, e = r.rdb.SRandMemberN(ctx, readyKey, int64(limit)).Result()
		if e == redis.Nil {
			return nil
		}
		return e
	})
	if err != nil {
		return nil, stats, err
	}
	stats.Sampled = len(entries)
	if len(entries) == 0 {
		return nil, stats, nil
	}
	pipe := r.rdb.Pipeline()
	cmds := make([]*redis.StringCmd, len(entries))
	gh := make([][2]string, len(entries)) // [group, hash]
	for i, e := range entries {
		g, h, ok := strings.Cut(e, ":")
		gh[i] = [2]string{g, h}
		if ok {
			cmds[i] = pipe.HGet(ctx, grpKey(g, h), "base_subject")
		}
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, stats, err
	}
	out := make([]groupKey, 0, len(entries))
	var stale []interface{}
	for i, c := range cmds {
		if c == nil {
			stale = append(stale, entries[i])
			continue
		}
		base, err := c.Result()
		if err != nil || base == "" {
			// Meta gone (evicted / TTL'd out): the articles are already gone, so
			// this ready entry is dead. LRange left it in place — drop it here,
			// or it would be re-read every pass forever.
			stale = append(stale, entries[i])
			continue
		}
		out = append(out, groupKey{Group: gh[i][0], Base: base})
	}
	stats.Live, stats.Fossil = len(out), len(stale)
	if len(stale) > 0 {
		_ = r.rdb.SRem(ctx, readyKey, stale...).Err() // best-effort: a leftover dead entry is retried harmlessly
	}
	return out, stats, nil
}

// groupArticles loads a set's articles by (group, base) — the hash is recomputed
// from the pair (it's deterministic), so the (group, base) interface fits Redis.
func (r *redisStaging) groupArticles(ctx context.Context, group, base string) ([]stagedArticle, error) {
	artMap, err := r.rdb.HGetAll(ctx, artKey(group, groupHashKey(group, base))).Result()
	if err != nil {
		return nil, err
	}
	out := make([]stagedArticle, 0, len(artMap))
	for _, data := range artMap {
		var ca compactArticle
		if err := json.Unmarshal([]byte(data), &ca); err != nil {
			continue
		}
		out = append(out, stagedArticle{
			MessageID: ca.MessageID, Subject: ca.Subject, BaseSubject: base,
			Poster: ca.From, Bytes: ca.Bytes, Posted: time.Unix(ca.Date, 0), Group: group,
			PartNum: ca.PartNum, TotalParts: ca.TotalParts, SegTotal: ca.SegTotal,
			FileNum: ca.FileNum, TotalFiles: ca.TotalFiles, FileParts: ca.FileParts,
		})
	}
	return out, nil
}

func (r *redisStaging) deleteStaged(ctx context.Context, group, base string) error {
	hash := groupHashKey(group, base)
	pipe := r.rdb.Pipeline()
	pipe.Del(ctx, artKey(group, hash))
	pipe.Del(ctx, grpKey(group, hash))
	pipe.SRem(ctx, activeKey(group), hash)
	// Remove it from the work queue too — candidateGroups PEEKS instead of
	// popping, so this is the one place an entry leaves nzb:ready.
	pipe.SRem(ctx, readyKey, group+":"+hash)
	_, err := pipe.Exec(ctx)
	return err
}

// deleteJunkStaged is a no-op in redis mode: junk base_subjects are dropped at
// ingest (parseOverviews) and again at build (classifyRelease), and
// there is no cheap way to scan every staged set — so nothing to sweep here.
// deleteStagedBatch removes every named set in as few round-trips as it can.
//
// deleteStaged issues four commands in one pipeline for ONE set, so a pass that
// discards 500 junk sets pays 500 round-trips. Batching folds those into a
// handful: the commands are identical, only the count changes. Chunked because a
// single pipeline holding four commands per set for an unbounded draw is a large
// buffer on both ends for no benefit.
func (r *redisStaging) deleteStagedBatch(ctx context.Context, keys []groupKey) (int, error) {
	const chunk = 500 // 4 commands each -> 2000 per round-trip
	done := 0
	for start := 0; start < len(keys); start += chunk {
		end := start + chunk
		if end > len(keys) {
			end = len(keys)
		}
		pipe := r.rdb.Pipeline()
		for _, k := range keys[start:end] {
			hash := groupHashKey(k.Group, k.Base)
			pipe.Del(ctx, artKey(k.Group, hash))
			pipe.Del(ctx, grpKey(k.Group, hash))
			pipe.SRem(ctx, activeKey(k.Group), hash)
			// candidateGroups PEEKS rather than pops, so this is the one place
			// an entry leaves nzb:ready.
			pipe.SRem(ctx, readyKey, k.Group+":"+hash)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			// Partial failure still removed everything before it; report what
			// landed so the caller's drain count stays honest.
			return done, err
		}
		done += end - start
	}
	return done, nil
}

func (r *redisStaging) deleteJunkStaged(ctx context.Context) (int64, error) { return 0, nil }

// prune is a no-op in redis mode: the key TTL + the inline hopeless-eviction
// in stageArticles are the drain (there is no added_at horizon to sweep).
func (r *redisStaging) prune(ctx context.Context) (int64, error) { return 0, nil }

// pressure reports used_memory / maxmemory (0.0-1.0). Returns 0 when maxmemory is
// unset (0 = unbounded) — no ceiling means no back-pressure signal.
func (r *redisStaging) pressure(ctx context.Context) (float64, error) {
	info, err := r.rdb.Info(ctx, "memory").Result()
	if err != nil {
		return 0, err
	}
	used := parseInfoInt(info, "used_memory:")
	max := parseInfoInt(info, "maxmemory:")
	if max <= 0 {
		return 0, nil
	}
	ratio := float64(used) / float64(max)
	if ratio > 1 {
		ratio = 1
	}
	return ratio, nil
}

// stagingInfo answers the dashboard's staging readout with the numbers redis
// can give in O(1): DBSIZE (≈2 keys per staged set), the ready-queue depth,
// and the memory ceiling driving back-pressure. A per-article count would mean
// summing HLENs across every set — a scan — so it stays zero in this mode.
func (r *redisStaging) stagingInfo(ctx context.Context) (stagingInfo, error) {
	out := stagingInfo{Mode: "redis"}
	if n, err := r.rdb.DBSize(ctx).Result(); err == nil {
		out.Keys = n
	}
	if n, err := r.rdb.SCard(ctx, readyKey).Result(); err == nil {
		out.ReadyGroups = n
	}
	// TWO calls, not one INFO with two sections. Multi-section INFO needs Redis
	// 7.0; against an older server it yields nothing usable and every field
	// below silently reads zero — which is indistinguishable from a healthy,
	// unbounded, never-evicting Redis. That misreport is worse than no reading
	// at all, and it happened: the census reported "unbounded, 0 evicted" while
	// the backfill's own single-section pressure() read 99% off the same server.
	// Two plain calls work on every version.
	info, err := r.rdb.Info(ctx, "memory").Result()
	if err != nil {
		return out, err
	}
	out.MemUsedBytes = parseInfoInt(info, "used_memory:")
	out.MemMaxBytes = parseInfoInt(info, "maxmemory:")
	out.MaxMemoryPolicy = parseInfoStr(info, "maxmemory_policy:")

	// Eviction counters are best-effort: losing them must not cost the memory
	// figures, which are the ones the back-pressure gate acts on.
	if stats, err := r.rdb.Info(ctx, "stats").Result(); err == nil {
		out.EvictedKeys = parseInfoInt(stats, "evicted_keys:")
		out.ExpiredKeys = parseInfoInt(stats, "expired_keys:")
	}
	return out, nil
}

// parseInfoStr is parseInfoInt for the fields that are not numbers —
// maxmemory_policy being the one that decides whether this Redis destroys
// staged releases or refuses the write.
func parseInfoStr(info, prefix string) string {
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// incompleteSets walks every active set and returns the largest incomplete
// ones. SCAN over the per-group active sets + one pipelined HGetAll/HLen pair
// per staged set — bounded by what is actually in flight (sets expire on the
// staging TTL), fine once per build pass, NOT for the render path.
// pendingSampleCap bounds how many staged sets the forming-releases readout
// inspects per call. The card shows at most a hundred rows; sampling twenty
// times that leaves plenty of headroom to find the largest ones while keeping
// the cost independent of how many sets are in flight.
const pendingSampleCap = 2000

func (r *redisStaging) incompleteSets(ctx context.Context, limit int) ([]pendingSet, error) {
	if limit <= 0 || limit > 100 {
		limit = 15
	}
	// SAMPLE the active sets; never enumerate them.
	//
	// This used SMEMBERS, which returns every hash in the group. That is fine
	// with a few thousand sets in flight and catastrophic at the scale this
	// backend actually reaches: production held ~3.5 MILLION staged sets, so
	// each call pulled millions of strings into the client and then issued a
	// pipeline of twice that many HGetAll/HLen commands — per build pass, on a
	// Redis already at its memory ceiling. Work that heavy, that often, starves
	// the server's own expiry cycle, which is the mechanism supposed to be
	// draining the backlog. The readout was helping cause the condition it was
	// installed to explain.
	//
	// SRandMemberN with a positive count returns DISTINCT members, so a bounded
	// sample costs one round trip per group regardless of how many sets exist.
	// The result is therefore "the largest incomplete sets IN A SAMPLE", not the
	// global top-N; at these cardinalities that distinction does not change what
	// an operator learns, and the alternative is a readout that cannot be
	// afforded at all.
	type ref struct{ group, hash string }
	refs := make([]ref, 0, pendingSampleCap)
	iter := r.rdb.Scan(ctx, 0, activeKey("*"), 200).Iterator()
	for iter.Next(ctx) {
		if len(refs) >= pendingSampleCap {
			break
		}
		group := strings.TrimPrefix(iter.Val(), "active_groups:")
		hashes, err := r.rdb.SRandMemberN(ctx, iter.Val(), int64(pendingSampleCap-len(refs))).Result()
		if err != nil {
			continue
		}
		for _, h := range hashes {
			refs = append(refs, ref{group: group, hash: h})
		}
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, nil
	}

	pipe := r.rdb.Pipeline()
	metas := make([]*redis.MapStringStringCmd, len(refs))
	lens := make([]*redis.IntCmd, len(refs))
	for i, rf := range refs {
		metas[i] = pipe.HGetAll(ctx, grpKey(rf.group, rf.hash))
		lens[i] = pipe.HLen(ctx, artKey(rf.group, rf.hash))
	}
	_, _ = pipe.Exec(ctx) // per-cmd errors surface below; expired keys just read empty

	var out []pendingSet
	dead := map[string][]interface{}{}
	for i, rf := range refs {
		meta, err := metas[i].Result()
		if err != nil {
			continue
		}
		if len(meta) == 0 {
			// Expired set: also drop its ref so the active set stops growing
			// (belt to the Expire-braces above for refs from before that fix).
			dead[rf.group] = append(dead[rf.group], rf.hash)
			continue
		}
		have := int(lens[i].Val())
		// The writer stores file_parts as "1"/"0" (boolStr) — matching every
		// other reader; == "true" was never true, which hid multi-file sets.
		multi := meta["file_parts"] == "1" || meta["file_parts"] == "true"
		// CALL the builder's function rather than restating it. This block used
		// to carry its own copy, with a comment claiming the two agreed; they
		// stopped agreeing the moment groupNeededParts was corrected to a
		// per-file lower bound, and the card went on reporting the old
		// impossible figures — 2,016,196 needed for a 157k-article set — while
		// the builder had already moved on. A dashboard that disagrees with the
		// code it describes is worse than no dashboard, because it is what an
		// operator reaches for when something is wrong.
		need := pendingNeed(meta)
		if need <= 0 || have >= need {
			continue // complete (or unknowable) — the builder will take it
		}
		per := perFileSegTotals(meta)
		files, _ := strconv.Atoi(meta["total_files"])
		lo, _ := strconv.Atoi(meta["art_lo"])
		hi, _ := strconv.Atoi(meta["art_hi"])
		out = append(out, pendingSet{
			Base: meta["base_subject"], Group: rf.group,
			Have: have, Need: need, Segments: have,
			Multi: multi,
			Files: files, Seen: len(per), PerFile: formatPerFileTotals(per),
			ArtLo: lo, ArtHi: hi,
		})
	}
	for group, hashes := range dead {
		_ = r.rdb.SRem(ctx, activeKey(group), hashes...).Err() // best-effort cleanup
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Need-out[i].Have > out[j].Need-out[j].Have })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func atoiField(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// parseInfoInt pulls an integer field out of a Redis INFO section (CRLF lines
// like "used_memory:12345").
func parseInfoInt(info, prefix string) int64 {
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, prefix) {
			v := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			n, _ := strconv.ParseInt(v, 10, 64)
			return n
		}
	}
	return 0
}

// formatPerFileTotals renders the declared segment total for each seen file, in
// file order, for the pending-set readout: "1:1000 2:14 3:14".
//
// Bounded to a dozen entries because a set can legitimately hold hundreds of
// par2 volumes and this goes into telemetry JSON that the admin page renders on
// every poll. The count of remaining files is stated rather than dropped —
// silently truncating a diagnostic reads as "that is all there is".
func formatPerFileTotals(per map[int]int) string {
	if len(per) == 0 {
		return ""
	}
	nums := make([]int, 0, len(per))
	for fn := range per {
		nums = append(nums, fn)
	}
	sort.Ints(nums)

	const maxShown = 12
	var b strings.Builder
	for i, fn := range nums {
		if i >= maxShown {
			fmt.Fprintf(&b, " +%d more", len(nums)-maxShown)
			break
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%d:%d", fn, per[fn])
	}
	return b.String()
}

// reapReadyQueue removes dead entries from nzb:ready.
//
// The queue is drained by a random sample of build_drain_per_pass entries, and
// nothing ever removed the dead ones except that same sample — so a queue that
// grows faster than the draw accumulates fossils without bound and dilutes
// itself. Production reached 7,403,408 entries against a 500-entry draw, of
// which 407 per draw were already dead: a completed release had roughly a 1 in
// 15,000 chance of being picked in a given pass, and its articles expire after
// two hours. That is not a queue, it is a lottery nobody wins.
//
// A fossil is an entry whose set metadata is gone — the same definition
// candidateGroups already uses, so this changes no policy, only who applies it
// and how often. SSCAN with a cursor, bounded per call: a full sweep of
// millions of entries inside one build pass would stall the pass it is meant to
// help, and progress is durable because removal is idempotent.
//
// Returns entries scanned and entries removed.
func (r *redisStaging) reapReadyQueue(ctx context.Context, maxScan int) (scanned, removed int, err error) {
	if maxScan <= 0 {
		return 0, 0, nil
	}
	r.ensureReadySet(ctx)
	var cursor uint64
	for scanned < maxScan {
		var batch []string
		batch, cursor, err = r.rdb.SScan(ctx, readyKey, cursor, "", 512).Result()
		if err != nil {
			return scanned, removed, err
		}
		if len(batch) == 0 && cursor == 0 {
			break
		}
		scanned += len(batch)

		// One pipelined EXISTS per entry, then one SRem for the dead. A ready
		// entry is "{group}:{hash}" and grpKey is "grp:"+group+":"+hash, so the
		// metadata key is simply "grp:"+entry.
		pipe := r.rdb.Pipeline()
		cmds := make([]*redis.IntCmd, len(batch))
		for i, e := range batch {
			cmds[i] = pipe.Exists(ctx, "grp:"+e)
		}
		if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
			return scanned, removed, err
		}
		var dead []interface{}
		for i, c := range cmds {
			if n, err := c.Result(); err == nil && n == 0 {
				dead = append(dead, batch[i])
			}
		}
		if len(dead) > 0 {
			if e := r.rdb.SRem(ctx, readyKey, dead...).Err(); e != nil {
				return scanned, removed, e
			}
			removed += len(dead)
		}
		if cursor == 0 {
			break // full circuit complete
		}
	}
	return scanned, removed, nil
}

// spanFoldScript folds a batch's article-number bounds into each set's running
// span, keeping the minimum art_lo and the maximum art_hi.
//
// A script because Redis has no "set this hash field only if smaller": doing it
// in Go needs a read, a compare and a write, and between the read and the write
// a sibling crawler working the same group on another backbone would clobber
// the value. One script call folds a whole batch atomically.
//
// Single-keyspace assumption, as elsewhere in this backend: the grp: keys are
// passed as ARGV rather than KEYS. Redis Cluster would need key-slot work
// across the whole staging design, not just here.
var spanFoldScript = redis.NewScript(`
for i = 1, #ARGV, 3 do
  local k  = ARGV[i]
  local lo = tonumber(ARGV[i+1])
  local hi = tonumber(ARGV[i+2])
  local cur = redis.call('HGET', k, 'art_lo')
  if (not cur) or tonumber(cur) > lo then redis.call('HSET', k, 'art_lo', lo) end
  cur = redis.call('HGET', k, 'art_hi')
  if (not cur) or tonumber(cur) < hi then redis.call('HSET', k, 'art_hi', hi) end
end
return #ARGV / 3
`)

// spanFoldChunk bounds how many sets one fold call carries. Each set costs three
// ARGV entries and Lua's stack tops out around 8000, so this leaves ample room.
const spanFoldChunk = 500

// foldSpans records each set's article-number bounds. Best-effort: this is a
// diagnostic, and losing a span reading must never fail a staging write.
func (r *redisStaging) foldSpans(ctx context.Context, spans []spanUpdate) {
	for start := 0; start < len(spans); start += spanFoldChunk {
		end := start + spanFoldChunk
		if end > len(spans) {
			end = len(spans)
		}
		argv := make([]interface{}, 0, (end-start)*3)
		for _, sp := range spans[start:end] {
			argv = append(argv, sp.key, sp.lo, sp.hi)
		}
		if err := spanFoldScript.Run(ctx, r.rdb, nil, argv...).Err(); err != nil && err != redis.Nil {
			r.note(ctx, "usenet/staging-span", err)
			return // one failure is enough; do not spam the log per chunk
		}
	}
}

// spanUpdate is one set's article-number bounds within a single batch.
type spanUpdate struct {
	key    string
	lo, hi int
}
