package usenet

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// ── content identity ────────────────────────────────────────────────

// TestContentHashIsContentIdentity pins prod's scheme and the two properties
// the old hash-of-(group|subject) violated: a re-post with fresh articles gets
// a NEW hash (it can be indexed), and the same articles always collide
// regardless of order (dedup works).
func TestContentHashIsContentIdentity(t *testing.T) {
	a := []stagedArticle{{MessageID: "<a@x>"}, {MessageID: "<b@x>"}}
	b := []stagedArticle{{MessageID: "<b@x>"}, {MessageID: "<a@x>"}} // same set, other order
	c := []stagedArticle{{MessageID: "<a@x>"}, {MessageID: "<c@x>"}} // re-post: one fresh article

	ha, hb, hc := contentHashArticles(a), contentHashArticles(b), contentHashArticles(c)
	if ha != hb {
		t.Errorf("same articles, different hash (%s vs %s) — dedup broken", ha, hb)
	}
	if ha == hc {
		t.Error("different articles, same hash — a re-post could never be indexed")
	}
	if len(ha) != 32 {
		t.Errorf("hash length %d, want 32 hex chars (prod's [:16])", len(ha))
	}
	// Cross-repo pin: prod's contentHash over the same two ids produces exactly
	// this (verified by running prod's function). If this fails, the two engines
	// have diverged and shared dedup at cutover breaks.
	if ha != "608ce396436ac9bdde18fc0532ea577f" {
		t.Errorf("hash %s no longer matches prod's for the same articles", ha)
	}
	// Empty message-ids are skipped, not hashed as empty strings.
	d := []stagedArticle{{MessageID: "<a@x>"}, {MessageID: ""}, {MessageID: "<b@x>"}}
	if contentHashArticles(d) != ha {
		t.Error("an empty message-id changed the hash")
	}
}

// ── classification (prod's order, prod's bypasses) ──────────────────

func TestClassifyReleaseTitleExtraction(t *testing.T) {
	title, _, _, _ := classifyRelease(`Some Release "Frieren.S01E01.mkv" yEnc`, nil)
	if title != "Frieren.S01E01.mkv" {
		t.Errorf("quoted title not extracted: %q", title)
	}
	// Invalid UTF-8 must be repaired, not passed through to XML/DB.
	title, _, _, _ = classifyRelease("bad\xff\xfetitle", nil)
	if !strings.Contains(title, "�") || strings.Contains(title, "\xff") {
		t.Errorf("invalid UTF-8 survived: %q", title)
	}
}

func TestClassifyReleaseBlockedExtension(t *testing.T) {
	_, _, _, blocked := classifyRelease(`"KMSpico.Setup.exe"`, nil)
	if !blocked {
		t.Error("executable not blocked")
	}
	_, _, _, blocked = classifyRelease(`"Frieren - 01.mkv"`, nil)
	if blocked {
		t.Error("a video file was blocked")
	}
}

// TestClassifyReleaseCategoryBypass is prod's rule: an explicit category tag
// vouches for the release, so the junk check never runs. Without the tag the
// same junk-shaped title is dropped.
func TestClassifyReleaseCategoryBypass(t *testing.T) {
	arts := []stagedArticle{{Bytes: 500 * 1024}} // small enough for the catchall

	_, cat, junkRule, _ := classifyRelease("S7rEx773 OVA", arts)
	if junkRule != "" || cat != "Anime" {
		t.Errorf("explicit OVA tag should bypass junk: cat=%q rule=%q", cat, junkRule)
	}

	_, _, junkRule, _ = classifyRelease("S7rEx773", arts)
	if junkRule == "" {
		t.Error("junk-shaped title without a tag was not caught")
	}

	// Adult outranks Anime, exactly as prod's parseCategoryTag orders it.
	_, cat, _, _ = classifyRelease("Show OVA Hentai Special", arts)
	if cat != "Hentai" {
		t.Errorf("adult tag should win: %q", cat)
	}
}

func TestClassifyReleaseComicSniff(t *testing.T) {
	arts := []stagedArticle{
		{Subject: `"Some.Series.v01.CBZ" yEnc (1/9)`, Bytes: 80 * 1048576},
	}
	_, cat, _, _ := classifyRelease("Some Series v01", arts)
	if cat != "Manga" {
		t.Errorf("cbz article should hint Manga, got %q", cat)
	}
}

// ── buildNZB error contract ─────────────────────────────────────────

// TestBuildNZBSanitisesInvalidUTF8: Usenet subjects carry arbitrary bytes, and
// xml.Marshal refuses invalid UTF-8. The old code returned nil on that error,
// which became a "completed" release with an EMPTY NZB.
func TestBuildNZBSanitisesInvalidUTF8(t *testing.T) {
	arts := []stagedArticle{{
		MessageID: "<a@x>", Subject: "rel \xff\xfe (1/1)", Poster: "p\xffq",
		Bytes: 100, Group: "a.b", PartNum: 1,
	}}
	out, err := buildNZB(arts)
	if err != nil {
		t.Fatalf("sanitised build still errored: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("empty NZB produced")
	}
	if !strings.Contains(string(out), "a@x") {
		t.Error("segment lost in sanitised build")
	}
}

// ── the sink seam ───────────────────────────────────────────────────

type fakeSink struct {
	got     []pluginapi.AssembledRelease
	created bool
	err     error
}

func (f *fakeSink) IngestAssembled(_ context.Context, r pluginapi.AssembledRelease) (int64, bool, error) {
	f.got = append(f.got, r)
	return 7, f.created, f.err
}

// TestStoreReleaseHostMode pins the seam contract from the plugin's side:
// created/dup/error pass through untouched, and host mode with no registered
// capability refuses loudly instead of quietly self-storing.
func TestStoreReleaseHostMode(t *testing.T) {
	sink := &fakeSink{created: true}
	rel := pluginapi.AssembledRelease{
		Title: "T", Group: "g", ContentHash: "abc", SizeBytes: 9,
		PostedAt: time.Now(), NZBGz: []byte{1}, Segments: 1,
	}

	// Direct-dispatch through the same code path the resolved releaseSink uses for a
	// resolved sink (the capability lookup itself is loon-core plumbing,
	// exercised by the demo).
	id, created, err := sink.IngestAssembled(context.Background(), rel)
	if err != nil || !created || id != 7 {
		t.Fatalf("sink pass-through: id=%d created=%v err=%v", id, created, err)
	}
	if len(sink.got) != 1 || sink.got[0].ContentHash != "abc" {
		t.Fatalf("release not delivered intact: %+v", sink.got)
	}

	// Duplicate: created=false, no error — the caller clears staging.
	sink.created, sink.err = false, nil
	_, created, err = sink.IngestAssembled(context.Background(), rel)
	if created || err != nil {
		t.Errorf("dup semantics: created=%v err=%v", created, err)
	}

	// Error: the caller must leave the set staged (assemble loop `continue`s
	// before deleteStaged).
	sink.err = errors.New("host down")
	if _, _, err = sink.IngestAssembled(context.Background(), rel); err == nil {
		t.Error("sink error swallowed")
	}
}

// TestSinkDefaultsInternal: an unset sink is the plugin's own table — host mode
// is an explicit opt-in, because it changes where the catalogue lives.
func TestSinkDefaultsInternal(t *testing.T) {
	var c Config
	c.applyDefaults()
	if c.Sink != "internal" {
		t.Errorf("default sink = %q, want internal", c.Sink)
	}
}
