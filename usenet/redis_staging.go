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
// list. Staged data lives only in Redis with a 2h TTL, so it is best-effort:
// under memory pressure incomplete stale sets are shed, but complete sets have
// already been queued. Durable NZBs still write through PGStore.
//
// Key model (lifted verbatim):
//
//	art:{group}:{hash}     Hash  per-article JSON, field "fileNum:partNum"   TTL 2h
//	grp:{group}:{hash}     Hash  set metadata (base_subject, totals, created_at) TTL 2h
//	nzb:ready              List  "{group}:{hash}" of complete sets
//	active_groups:{group}  Set   in-progress hashes
//
// hash = hex(sha256(group + ":" + base)[:8]).
type redisStaging struct {
	rdb redis.UniversalClient
	// onEvict reports hopeless-set evictions to the caller (telemetry) — the
	// eviction is silent by design, and "is it failing or filtering?" is a
	// question the dashboard must be able to answer.
	onEvict func(n int)
}

func newRedisStaging(rdb redis.UniversalClient, onEvict func(int)) *redisStaging {
	return &redisStaging{rdb: rdb, onEvict: onEvict}
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
	return hex.EncodeToString(sum[:8])
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
	type groupUpdate struct {
		hash, groupName, baseSub string
		articles                 []stagedArticle
	}
	groups := make(map[string]*groupUpdate)
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
		pipe.HSetNX(ctx, gk, "created_at", now)
		pipe.SAdd(ctx, activeKey(gu.groupName), gu.hash)
		pipe.Expire(ctx, ak, 2*time.Hour)
		pipe.Expire(ctx, gk, 2*time.Hour)
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
			if err == nil && isGroupComplete(meta, fields) {
				readyGroups = append(readyGroups, ci.gu.groupName+":"+ci.gu.hash)
				continue
			}
		}
		// Hopeless-eviction: stale (>5min) and <30% of expected parts present.
		createdAt, _ := strconv.ParseInt(meta["created_at"], 10, 64)
		if createdAt > 0 && now-createdAt > 300 {
			if needed > 0 && length > 0 && float64(length)/float64(needed) < 0.30 {
				evictKeys = append(evictKeys,
					artKey(ci.gu.groupName, ci.gu.hash), grpKey(ci.gu.groupName, ci.gu.hash))
				evictMembers = append(evictMembers, evictEntry{ci.gu.groupName, ci.gu.hash})
			}
		}
	}

	if len(readyGroups) > 0 {
		// Checked: a dropped LPush here loses a COMPLETED release — it never
		// reaches the builder and completeness won't re-fire (the set has all
		// its articles now). Returning the error leaves the batch's watermark
		// un-advanced (crawl.go treats a staging error as ok=false), so the
		// same articles re-crawl and re-queue.
		if err := r.rdb.LPush(ctx, readyKey, readyGroups...).Err(); err != nil {
			return len(arts), fmt.Errorf("queue ready sets: %w", err)
		}
	}
	if len(evictKeys) > 0 {
		evPipe := r.rdb.Pipeline()
		evPipe.Del(ctx, evictKeys...)
		for _, em := range evictMembers {
			evPipe.SRem(ctx, activeKey(em.group), em.hash)
		}
		_, _ = evPipe.Exec(ctx)
		if r.onEvict != nil {
			r.onEvict(len(evictMembers))
		}
	}

	return len(arts), nil
}

// groupNeededParts returns how many article keys a set needs to be complete.
func groupNeededParts(meta map[string]string) int {
	fileParts := meta["file_parts"] == "true" || meta["file_parts"] == "1"
	totalParts, _ := strconv.Atoi(meta["total_parts"])
	totalFiles, _ := strconv.Atoi(meta["total_files"])
	segTotal, _ := strconv.Atoi(meta["seg_total"])
	if fileParts && totalFiles > 0 && segTotal > 0 {
		return totalFiles * segTotal
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
		completeFiles := 0
		for _, count := range fileSegs {
			if count >= segTotal {
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
func (r *redisStaging) candidateGroups(ctx context.Context, limit int) ([]groupKey, error) {
	if limit <= 0 {
		limit = 500
	}
	// PEEK, don't pop. The build loop is written for the pg contract "leave
	// staged, retry next pass" — it deleteStaged's a set only on success or a
	// permanent drop (blocked ext / blacklist / junk), and leaves it on a
	// transient sink error or on shutdown. Popping here would break that: every
	// entry returned but not built would be gone, losing a COMPLETED release on
	// the exact transient failure assemble.go promises to survive. Only one
	// builder runs at a time (the "Usenet Builder" job lease), so a peek cannot
	// double-dispatch. Entries leave nzb:ready only via deleteStaged.
	entries, err := r.rdb.LRange(ctx, readyKey, 0, int64(limit-1)).Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
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
		return nil, err
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
	if len(stale) > 0 {
		rem := r.rdb.Pipeline()
		for _, e := range stale {
			rem.LRem(ctx, readyKey, 0, e)
		}
		_, _ = rem.Exec(ctx) // best-effort: a leftover dead entry is retried harmlessly
	}
	return out, nil
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
	// Remove it from the work queue too — candidateGroups now PEEKS (LRange)
	// instead of popping, so this is the one place an entry leaves nzb:ready.
	pipe.LRem(ctx, readyKey, 0, group+":"+hash)
	_, err := pipe.Exec(ctx)
	return err
}

// deleteJunkStaged is a no-op in redis mode: junk base_subjects are dropped at
// ingest (parseOverviews) and again at build (isJunkTitle in runBuild), and
// there is no cheap way to scan every staged set — so nothing to sweep here.
func (r *redisStaging) deleteJunkStaged(ctx context.Context) (int64, error) { return 0, nil }

// prune is a no-op in redis mode: the 2h key TTL + the inline hopeless-eviction
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
	if n, err := r.rdb.LLen(ctx, readyKey).Result(); err == nil {
		out.ReadyGroups = n
	}
	info, err := r.rdb.Info(ctx, "memory").Result()
	if err != nil {
		return out, err
	}
	out.MemUsedBytes = parseInfoInt(info, "used_memory:")
	out.MemMaxBytes = parseInfoInt(info, "maxmemory:")
	return out, nil
}

// incompleteSets walks every active set and returns the largest incomplete
// ones. SCAN over the per-group active sets + one pipelined HGetAll/HLen pair
// per staged set — bounded by what is actually in flight (sets expire after
// 2h), fine once per build pass, NOT for the render path.
func (r *redisStaging) incompleteSets(ctx context.Context, limit int) ([]pendingSet, error) {
	if limit <= 0 || limit > 100 {
		limit = 15
	}
	// Collect (group, hash) pairs from the active_groups:* sets.
	type ref struct{ group, hash string }
	var refs []ref
	iter := r.rdb.Scan(ctx, 0, activeKey("*"), 200).Iterator()
	for iter.Next(ctx) {
		group := strings.TrimPrefix(iter.Val(), "active_groups:")
		hashes, err := r.rdb.SMembers(ctx, iter.Val()).Result()
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
	for i, rf := range refs {
		meta, err := metas[i].Result()
		if err != nil || len(meta) == 0 {
			continue // set expired between SCAN and read
		}
		have := int(lens[i].Val())
		need := atoiField(meta["seg_total"])
		if need <= 0 {
			need = atoiField(meta["total_parts"])
		}
		if need <= 0 || have >= need {
			continue // complete (or unknowable) — the builder will take it
		}
		out = append(out, pendingSet{
			Base: meta["base_subject"], Group: rf.group,
			Have: have, Need: need, Segments: have,
			Multi: meta["file_parts"] == "true",
		})
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
