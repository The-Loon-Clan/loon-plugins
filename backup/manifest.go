package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"sync"
)

// Serving packs straight to a puller, without staging anything locally.
//
// The archive job's design needs free space equal to what it protects, because
// it writes a full second copy to the same volume before anything leaves the
// box. On this install that is 202 GB against 180 GB available, so it has never
// once run — a backup system that cannot run is not a backup system.
//
// Nothing here touches the disk. A pack is assembled in the response writer as
// its members are read, one file at a time, so the peak extra space used by a
// backup is one file plus a buffer. The pull side decides where the bytes land.
//
// The pack ID is the fingerprint of its MEMBER LIST — every member's path, hash
// and size, in order. That is what makes "only fetch what changed" fall out for
// free: a pack whose files are untouched keeps its ID between generations, so a
// puller holding that ID already has those bytes and skips them. A pack with
// one edited file gets a new ID and is re-fetched, and the blast radius of any
// change is one pack rather than the whole class.

// PackInfo is one transferable unit in a generation's manifest.
type PackInfo struct {
	ID      string `json:"id"`
	Class   string `json:"class"`
	Bytes   int64  `json:"bytes"`
	Members int    `json:"members"`
}

// Manifest is everything a puller needs to decide what to fetch.
type Manifest struct {
	Generation int64      `json:"generation"`
	SealedAt   string     `json:"sealed_at"`
	Files      int64      `json:"files"`
	Bytes      int64      `json:"bytes"`
	Packs      []PackInfo `json:"packs"`
}

// packID fingerprints a pack by its members, not by its position.
//
// Position would be worse than useless: inserting one file early in a class
// would shift every later pack's identity and force a re-fetch of data that
// did not change. Hashing the member tuples means an untouched pack keeps its
// name for as long as its contents are untouched.
func packID(p packPlan) string {
	h := sha256.New()
	fmt.Fprintf(h, "class=%s\n", p.Class)
	for _, m := range p.Members {
		fmt.Fprintf(h, "%s %s %d\n", m.Path, m.SHA256, m.Size)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// manifestCache holds the plans for a generation.
//
// Planning reads every file row for the generation — 418k of them here — so
// recomputing it per pack request would turn a resumed transfer into a
// full-table scan per chunk. Generations are immutable once sealed, so one
// build per generation is always correct.
type manifestCache struct {
	mu    sync.Mutex
	gen   int64
	plans map[string]packPlan
	man   Manifest
}

var packCache manifestCache

// BuildManifest plans the newest sealed generation into packs.
//
// Returns the manifest and keeps the plans for StreamPack. Safe to call
// repeatedly: the work happens once per generation.
func (p *Plugin) BuildManifest(ctx context.Context) (Manifest, error) {
	gen, err := p.st.lastSealedGeneration(ctx)
	if err != nil {
		return Manifest{}, err
	}
	if gen == 0 {
		return Manifest{}, fmt.Errorf("backup: no sealed generation yet — run the Backup Index job first")
	}

	packCache.mu.Lock()
	defer packCache.mu.Unlock()
	if packCache.gen == gen && packCache.plans != nil {
		return packCache.man, nil
	}

	meta, err := p.st.generationMeta(ctx, gen)
	if err != nil {
		return Manifest{}, err
	}
	man := Manifest{Generation: gen, SealedAt: meta.SealedAt, Files: meta.Files, Bytes: meta.Bytes}
	plans := map[string]packPlan{}

	// Class order matters for a transfer that gets interrupted, exactly as it
	// does for the index: the cheap irreplaceable classes go first, so a puller
	// cut off part-way has the artwork that exists nowhere else rather than a
	// prefix of the screenshots.
	for _, c := range orderedClasses(deps.Classes) {
		rows, err := p.st.filesForGen(ctx, gen, c.Slug)
		if err != nil {
			return Manifest{}, fmt.Errorf("files for %s: %w", c.Slug, err)
		}
		for _, plan := range planPacks(c.Slug, rows, packTargetBytes, packMaxMembers) {
			id := packID(plan)
			plans[id] = plan
			man.Packs = append(man.Packs, PackInfo{
				ID: id, Class: plan.Class, Bytes: plan.Bytes, Members: len(plan.Members),
			})
		}
	}
	sort.Slice(man.Packs, func(i, j int) bool {
		if man.Packs[i].Class != man.Packs[j].Class {
			return man.Packs[i].Class < man.Packs[j].Class
		}
		return man.Packs[i].ID < man.Packs[j].ID
	})

	packCache.gen, packCache.plans, packCache.man = gen, plans, man
	return man, nil
}

// StreamPack writes one pack to w, reading its members as it goes.
//
// The whole point: nothing is buffered to disk and no temporary file is
// created, so serving a 64 MiB pack out of a 129 GB corpus costs one open file
// handle. skip discards that many leading bytes, which is how a resumed
// transfer continues from where it stopped — the pack is byte-identical every
// time it is built, so an offset means the same thing on the second attempt as
// on the first.
func (p *Plugin) StreamPack(ctx context.Context, w io.Writer, id string, skip int64) error {
	if _, err := p.BuildManifest(ctx); err != nil {
		return err
	}
	packCache.mu.Lock()
	plan, ok := packCache.plans[id]
	packCache.mu.Unlock()
	if !ok {
		return fmt.Errorf("backup: no pack %q in the current generation", id)
	}
	dst := w
	if skip > 0 {
		dst = &skipWriter{w: w, remaining: skip}
	}
	_, _, err := writePack(dst, deps.Root, plan)
	return err
}

// skipWriter drops the first n bytes written through it.
//
// Range resume without seeking: the pack does not exist as a file, so there is
// nothing to seek in. Regenerating and discarding the prefix costs re-reading
// those members but no extra storage, which is the trade this whole design is
// making — and it is only paid on a resume, not on a first fetch.
type skipWriter struct {
	w         io.Writer
	remaining int64
}

func (s *skipWriter) Write(p []byte) (int, error) {
	if s.remaining <= 0 {
		return s.w.Write(p)
	}
	if int64(len(p)) <= s.remaining {
		s.remaining -= int64(len(p))
		return len(p), nil
	}
	drop := s.remaining
	s.remaining = 0
	n, err := s.w.Write(p[drop:])
	return n + int(drop), err
}
