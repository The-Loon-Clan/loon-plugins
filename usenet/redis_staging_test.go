package usenet

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// metaAndFields derives the Redis-side representation (grp metadata + art field
// keys) from a set of staged articles exactly as stageArticles does, so the two
// completeness implementations can be compared on identical input.
func metaAndFields(arts []stagedArticle) (map[string]string, []string) {
	maxTP, maxTF, maxST := 0, 0, 0
	for _, a := range arts {
		if a.TotalParts > maxTP {
			maxTP = a.TotalParts
		}
		if a.TotalFiles > maxTF {
			maxTF = a.TotalFiles
		}
		if a.SegTotal > maxST {
			maxST = a.SegTotal
		}
	}
	meta := map[string]string{
		"file_parts":  boolStr(len(arts) > 0 && arts[0].FileParts),
		"total_parts": strconv.Itoa(maxTP),
		"total_files": strconv.Itoa(maxTF),
		"seg_total":   strconv.Itoa(maxST),
	}
	fields := make([]string, 0, len(arts))
	for _, a := range arts {
		fields = append(fields, formatFieldKey(a.FileNum, a.PartNum))
	}
	return meta, fields
}

func single(parts, total int) []stagedArticle {
	out := make([]stagedArticle, 0, parts)
	for i := 1; i <= parts; i++ {
		out = append(out, stagedArticle{PartNum: i, TotalParts: total})
	}
	return out
}

// multi builds a multi-file set: segsPerFile[i] segments present for file i+1,
// each article carrying segTotals[i] as its SegTotal.
func multi(totalFiles int, segsPerFile, segTotals []int) []stagedArticle {
	var out []stagedArticle
	for f := 0; f < len(segsPerFile); f++ {
		for p := 1; p <= segsPerFile[f]; p++ {
			out = append(out, stagedArticle{
				FileParts: true, TotalFiles: totalFiles,
				FileNum: f + 1, PartNum: p, SegTotal: segTotals[f],
			})
		}
	}
	return out
}

// TestCompletenessParity pins the two completeness implementations against each
// other: assemble.go's isComplete (used by the builder, on []stagedArticle) and
// redis_staging.go's isGroupComplete (lifted from prod, on Redis meta + field
// keys). They must agree, because in redis mode isGroupComplete decides what gets
// queued and isComplete re-verifies it before assembly.
func TestCompletenessParity(t *testing.T) {
	cases := []struct {
		name string
		arts []stagedArticle
		want bool
	}{
		{"single-file complete", single(3, 3), true},
		{"single-file over-complete (dupes)", single(4, 3), true},
		{"single-file incomplete", single(2, 3), false},
		{"single-file no total", single(2, 0), false},
		{"multi-file complete uniform", multi(2, []int{2, 2}, []int{2, 2}), true},
		{"multi-file incomplete", multi(2, []int{2, 1}, []int{2, 2}), false},
		{"multi-file missing a file", multi(2, []int{2}, []int{2}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isComplete(tc.arts)
			meta, fields := metaAndFields(tc.arts)
			gotRedis := isGroupComplete(meta, fields)
			if got != tc.want {
				t.Errorf("isComplete = %v, want %v", got, tc.want)
			}
			if gotRedis != tc.want {
				t.Errorf("isGroupComplete = %v, want %v", gotRedis, tc.want)
			}
		})
	}
}

// TestCompletenessDivergence_HeterogeneousSegTotals documents a REAL, deliberate
// difference carried over from prod: for multi-file releases whose files have
// DIFFERENT segment counts, prod's Redis check compares every file against one
// global max seg_total, while the builder's isComplete uses each file's own
// segTotal. So Redis is STRICTER.
//
// This is safe in that direction — a set Redis queues always passes the builder's
// re-check, so nothing is assembled prematurely. The cost is that such a set is
// never queued in redis mode and sits until its 2h TTL. Preserved deliberately
// (prod behaves this way today); if we ever fix it, fix prod first, then re-lift.
func TestCompletenessDivergence_HeterogeneousSegTotals(t *testing.T) {
	// file 1: 2 of 2 segments; file 2: 3 of 3 segments. Both files are genuinely
	// complete, but the global max seg_total is 3.
	arts := multi(2, []int{2, 3}, []int{2, 3})

	if !isComplete(arts) {
		t.Fatal("isComplete: expected the set to be complete (per-file seg totals)")
	}
	meta, fields := metaAndFields(arts)
	if isGroupComplete(meta, fields) {
		t.Fatal("isGroupComplete: expected the known stricter behavior (global seg_total) " +
			"to report incomplete — if this now passes, prod's rule changed and the " +
			"lift needs re-checking")
	}
}

// TestCompactArticleWireFormat pins the bytes we write into art: hashes. They
// must stay identical to prod's MarshalCompact, otherwise a plugin process and a
// prod process pointed at the same Redis write divergent (and larger) values.
//
// Regression guard: encoding/json escapes < and > to </>, and every
// message-id is <addr@host> — so json.Marshal is NOT a drop-in here. That is
// exactly why marshalCompact is lifted rather than reimplemented.
func TestCompactArticleWireFormat(t *testing.T) {
	minimal := compactArticle{
		MessageID: "<a@b>", Subject: "S", From: "P", Bytes: 100, Date: 1234,
		PartNum: 1, TotalParts: 2,
	}
	want := `{"m":"<a@b>","s":"S","f":"P","b":100,"d":1234,"p":1,"tp":2}`
	if got := string(marshalCompact(&minimal)); got != want {
		t.Errorf("minimal:\n got %s\nwant %s", got, want)
	}

	full := compactArticle{
		MessageID: "<a@b>", Subject: "S", From: "P", Bytes: 100, Date: 1234,
		PartNum: 1, TotalParts: 2, SegTotal: 3, FileNum: 4, TotalFiles: 5, FileParts: true,
	}
	want = `{"m":"<a@b>","s":"S","f":"P","b":100,"d":1234,"p":1,"tp":2,"st":3,"fn":4,"tf":5,"fp":true}`
	if got := string(marshalCompact(&full)); got != want {
		t.Errorf("full:\n got %s\nwant %s", got, want)
	}

	// Quotes and backslashes still escape; angle brackets deliberately do not.
	esc := compactArticle{MessageID: `<a"b\c>`, Subject: "S", From: "P"}
	want = `{"m":"<a\"b\\c>","s":"S","f":"P","b":0,"d":0,"p":0,"tp":0}`
	if got := string(marshalCompact(&esc)); got != want {
		t.Errorf("escaping:\n got %s\nwant %s", got, want)
	}
}

// TestMarshalCompactDecodes confirms the hand-rolled encoder's output is still
// valid JSON that json.Unmarshal (the read path, as in prod) reads back exactly.
func TestMarshalCompactDecodes(t *testing.T) {
	in := compactArticle{
		MessageID: `<x"y\z@host>`, Subject: "Some.Release \"quoted\"", From: "p\\oster",
		Bytes: 4242, Date: 1700000000, PartNum: 2, TotalParts: 9,
		SegTotal: 3, FileNum: 1, TotalFiles: 2, FileParts: true,
	}
	var out compactArticle
	if err := json.Unmarshal(marshalCompact(&in), &out); err != nil {
		t.Fatalf("marshalCompact produced invalid JSON: %v", err)
	}
	if out != in {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", out, in)
	}
}

// TestCompactArticleRoundTrip checks a staged article survives the Redis
// encode/decode cycle with the fields assembly depends on intact.
func TestCompactArticleRoundTrip(t *testing.T) {
	posted := time.Unix(1700000000, 0)
	in := compactArticle{
		MessageID: "<x@y>", Subject: "Some.Release.part1", From: "poster",
		Bytes: 4242, Date: posted.Unix(), PartNum: 2, TotalParts: 9,
		SegTotal: 3, FileNum: 1, TotalFiles: 2, FileParts: true,
	}
	blob, err := json.Marshal(&in)
	if err != nil {
		t.Fatal(err)
	}
	var out compactArticle
	if err := json.Unmarshal(blob, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", out, in)
	}
	if !time.Unix(out.Date, 0).Equal(posted) {
		t.Errorf("date round-trip: got %v want %v", time.Unix(out.Date, 0), posted)
	}
}

// TestGroupHashKey pins the key-suffix contract: 16 hex chars, deterministic, and
// distinct per (group, base) — including that the group:base separator can't be
// spoofed by a base that contains a colon.
func TestGroupHashKey(t *testing.T) {
	h := groupHashKey("alt.binaries.x", "Some.Release")
	if len(h) != 16 {
		t.Errorf("hash length = %d, want 16 (sha256[:8] hex)", len(h))
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("non-hex char %q in %s", c, h)
		}
	}
	if h != groupHashKey("alt.binaries.x", "Some.Release") {
		t.Error("hash is not deterministic")
	}
	if h == groupHashKey("alt.binaries.y", "Some.Release") {
		t.Error("hash collides across groups")
	}
	if h == groupHashKey("alt.binaries.x", "Other.Release") {
		t.Error("hash collides across base subjects")
	}
	// Known, accepted property of prod's algorithm (sha256(group + ":" + base)):
	// the separator is ambiguous, so ("a", "b:c") and ("a:b", "c") collide. Not a
	// practical risk — newsgroup names cannot contain ':' — and changing it would
	// break key compatibility with prod's live staging, so it is preserved.
	if groupHashKey("a", "b:c") != groupHashKey("a:b", "c") {
		t.Error("expected prod's known separator ambiguity to be preserved verbatim")
	}
}

func TestParseInfoInt(t *testing.T) {
	info := "# Memory\r\nused_memory:1048576\r\nused_memory_human:1.00M\r\nmaxmemory:2097152\r\nmaxmemory_policy:noeviction\r\n"
	if got := parseInfoInt(info, "used_memory:"); got != 1048576 {
		t.Errorf("used_memory = %d, want 1048576", got)
	}
	if got := parseInfoInt(info, "maxmemory:"); got != 2097152 {
		t.Errorf("maxmemory = %d, want 2097152", got)
	}
	if got := parseInfoInt(info, "not_present:"); got != 0 {
		t.Errorf("missing field = %d, want 0", got)
	}
}
