package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"sync"

	lpapi "github.com/the-loon-clan/loon-plugins/pluginapi"
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
	ID    string `json:"id"`
	Class string `json:"class"`
	// Bytes is the WIRE length — exactly what the pack streams, ZIP structure
	// included. It is not the sum of the member file sizes, which is what this
	// field used to carry and is always short by 30+name per local header,
	// 46+name per central entry and 22 for the end-of-central-directory record.
	// A client using that figure as Content-Length, or as the mark for "the
	// transfer is complete", stops before the EOCD on every single pack and
	// stores an archive no reader will open.
	Bytes int64 `json:"bytes"`
	// Content is the sum of the member file sizes: what a restore writes out,
	// as opposed to what the transfer costs.
	Content int64 `json:"content_bytes"`
	Members int   `json:"members"`

	// Raw marks a pack served as the member's own bytes, with no ZIP container
	// around it. Classic ZIP header fields are 32-bit, so a member over 4 GiB
	// cannot be represented — and `pg_dump -Fd` writes one file per table, so
	// the database class produces exactly that (nzbs alone is a 20 GB member).
	// zip64 would lift the limit at the cost of the break-glass compatibility
	// this format exists for; a bare file is MORE recoverable by hand than a
	// zip, so the degenerate case drops the container instead.
	//
	// Only ever set on a single-member pack: planPacks never groups a file
	// larger than the fill target with another, so an oversized member is
	// always alone.
	Raw bool `json:"raw,omitempty"`
	// Path and SHA256 are the sole member's, and only set for a raw pack.
	// A ZIP carries its members' names and CRCs in its own directory; a raw
	// pack has nowhere to put them, so the manifest has to — otherwise a
	// restore holds bytes it cannot name and cannot check.
	Path   string `json:"path,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
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

// genPlans is one generation's planned packs.
type genPlans struct {
	plans map[string]packPlan
	man   Manifest
}

// manifestCache holds plans per generation.
//
// Planning reads every file row for the generation, so recomputing it per pack
// request would turn a resumed transfer into a full scan per chunk. Sealed
// generations never change, so one build each is always correct.
//
// It keeps more than one because a pull is not instantaneous. A transfer of
// this corpus runs for hours; the index seals a new generation daily. If only
// the newest were resident, every pack ID the puller was working through would
// start returning "no such pack" the moment that happened — mid-transfer,
// through no fault of the client, and with the IDs it holds still perfectly
// valid for the generation it pinned.
type manifestCache struct {
	mu    sync.Mutex
	byGen map[int64]*genPlans
	order []int64 // oldest first, for eviction
}

// keptGenerations is how many sealed generations stay planned. Two covers the
// case that matters — a pull that started before the nightly index and is still
// running after it — without holding the whole history in memory.
const keptGenerations = 2

var packCache = manifestCache{byGen: map[int64]*genPlans{}}

// BuildManifest plans the newest sealed generation into packs.
func (p *Plugin) BuildManifest(ctx context.Context) (Manifest, error) {
	gen, err := p.st.lastSealedGeneration(ctx)
	if err != nil {
		return Manifest{}, err
	}
	if gen == 0 {
		return Manifest{}, fmt.Errorf("backup: no sealed generation yet — run the Backup Index job first")
	}
	g, err := p.plansFor(ctx, gen)
	if err != nil {
		return Manifest{}, err
	}
	return g.man, nil
}

// sortPacksByPriority puts the manifest in the order a puller should fetch it:
// the host's class order first, then name and id so the manifest stays
// byte-stable for a given generation.
//
// The rank is the entire point. This sort used to key on the class NAME, which
// silently threw away the priority the plan loop had just established — "site"
// sorts after "screenshots", so 14 files of user-uploaded artwork that exists
// nowhere else were served LAST, behind 1,901 packs and 126 GB of regenerable
// frames. A first pull cut short at 80% would have held every screenshot and
// none of the irreplaceable art: precisely backwards, and the opposite of what
// this file, the puller and the design document all claimed. Found on the first
// real pull, 2026-07-30, because the art was missing from the array.
func sortPacksByPriority(packs []PackInfo, classRank map[string]int) {
	// An undeclared class (a stale row, a renamed slug) must sort LAST, not
	// first. A bare map lookup returns 0 for a miss, which is the rank of the
	// MOST irreplaceable class — so an unknown class would quietly jump ahead
	// of the artwork this ordering exists to protect.
	rank := func(class string) int {
		if r, ok := classRank[class]; ok {
			return r
		}
		return len(classRank)
	}
	sort.Slice(packs, func(i, j int) bool {
		ri, rj := rank(packs[i].Class), rank(packs[j].Class)
		if ri != rj {
			return ri < rj
		}
		if packs[i].Class != packs[j].Class {
			return packs[i].Class < packs[j].Class
		}
		return packs[i].ID < packs[j].ID
	})
}

// plansFor returns a generation's packs, building them once.
func (p *Plugin) plansFor(ctx context.Context, gen int64) (*genPlans, error) {
	packCache.mu.Lock()
	defer packCache.mu.Unlock()
	if g, ok := packCache.byGen[gen]; ok {
		return g, nil
	}

	// Detach from the caller's cancellation. A puller that times out or
	// disconnects part-way through the scan would otherwise abort the build,
	// and every subsequent request would start it again from nothing — the
	// expensive work repeated indefinitely because nobody waited for it once.
	ctx = context.WithoutCancel(ctx)

	meta, err := p.st.generationMeta(ctx, gen)
	if err != nil {
		return nil, err
	}
	man := Manifest{Generation: gen, SealedAt: meta.SealedAt, Files: meta.Files, Bytes: meta.Bytes}
	plans := map[string]packPlan{}

	// Class order matters for a transfer that gets interrupted, exactly as it
	// does for the index: the cheap irreplaceable classes go first, so a puller
	// cut off part-way has the artwork that exists nowhere else rather than a
	// prefix of the screenshots.
	classRank := map[string]int{}
	for _, c := range orderedClasses(deps.Classes) {
		classRank[c.Slug] = len(classRank)
		rows, err := p.st.filesForGen(ctx, gen, c.Slug)
		if err != nil {
			return nil, fmt.Errorf("files for %s: %w", c.Slug, err)
		}
		for _, plan := range planPacks(c.Slug, rows, packTargetBytes, packMaxMembers) {
			id := packID(plan)
			plans[id] = plan
			info := PackInfo{
				ID: id, Class: plan.Class,
				Bytes: packWireSize(plan), Content: plan.Bytes, Members: len(plan.Members),
			}
			if packIsRaw(plan) {
				info.Raw = true
				info.Path = plan.Members[0].Path
				info.SHA256 = plan.Members[0].SHA256
			}
			man.Packs = append(man.Packs, info)
		}
	}
	sortPacksByPriority(man.Packs, classRank)

	g := &genPlans{plans: plans, man: man}
	packCache.byGen[gen] = g
	packCache.order = append(packCache.order, gen)
	for len(packCache.order) > keptGenerations {
		delete(packCache.byGen, packCache.order[0])
		packCache.order = packCache.order[1:]
	}
	return g, nil
}

// StreamPack writes one pack to w, reading its members as it goes.
//
// The whole point: nothing is buffered to disk and no temporary file is
// created, so serving a 64 MiB pack out of a 129 GB corpus costs one open file
// handle. skip discards that many leading bytes, which is how a resumed
// transfer continues from where it stopped — the pack is byte-identical every
// time it is built, so an offset means the same thing on the second attempt as
// on the first.
//
// gen pins which generation the id belongs to. Passing 0 means "the newest
// sealed one", which is only safe for a single short request — a transfer that
// outlives an index run must pin the generation it planned against, or its
// still-valid pack IDs start vanishing underneath it.
func (p *Plugin) StreamPack(ctx context.Context, w io.Writer, gen int64, id string, skip int64) error {
	if gen == 0 {
		latest, err := p.st.lastSealedGeneration(ctx)
		if err != nil {
			return err
		}
		gen = latest
	}
	g, err := p.plansFor(ctx, gen)
	if err != nil {
		return err
	}
	plan, ok := g.plans[id]
	if !ok {
		return fmt.Errorf("backup: no pack %q in generation %d", id, gen)
	}
	if skip < 0 {
		return fmt.Errorf("backup: negative resume offset %d", skip)
	}
	// Refuse an offset past the end rather than reading the whole pack off disk
	// to emit nothing. Resume regenerates and discards the prefix, so an
	// unbounded skip is an invitation to make the server read 64 MiB per
	// request for zero bytes of response.
	if size := packWireSize(plan); skip >= size {
		return fmt.Errorf("backup: resume offset %d is at or past the end of pack %s (%d bytes)", skip, id, size)
	}
	dst := w
	if skip > 0 {
		dst = &skipWriter{w: w, remaining: skip}
	}
	_, _, werr := writePack(dst, deps.Root, plan)
	return werr
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

// packServer adapts the plugin to pluginapi.BackupPacks.
//
// A separate type rather than methods on Plugin, so the capability's shape is
// visible in one place and the plugin's own Manifest/PackInfo stay private —
// the host talks to the neutral contract and never imports this package.
type packServer struct{ p *Plugin }

var _ lpapi.BackupPacks = packServer{}

func (s packServer) Manifest(ctx context.Context) (lpapi.BackupManifest, error) {
	m, err := s.p.BuildManifest(ctx)
	if err != nil {
		return lpapi.BackupManifest{}, err
	}
	out := lpapi.BackupManifest{
		Generation: m.Generation, SealedAt: m.SealedAt,
		Files: m.Files, Bytes: m.Bytes,
		Packs: make([]lpapi.BackupPack, 0, len(m.Packs)),
	}
	for _, p := range m.Packs {
		// Every field a client needs must be copied ACROSS, not just added to
		// the internal struct: this conversion is the whole contract, and a
		// field missing here is invisible until a puller behaves as though the
		// flag were false. Raw shipped that way once — the size came through
		// (it rides Bytes) while the flag did not, so the manifest described a
		// zip the server was streaming raw.
		out.Packs = append(out.Packs, lpapi.BackupPack{
			ID: p.ID, Class: p.Class, Bytes: p.Bytes, Content: p.Content, Members: p.Members,
			Raw: p.Raw, Path: p.Path, SHA256: p.SHA256,
		})
	}
	return out, nil
}

func (s packServer) WritePack(ctx context.Context, w io.Writer, gen int64, id string, skip int64) error {
	return s.p.StreamPack(ctx, w, gen, id, skip)
}

func (s packServer) Ack(ctx context.Context, a lpapi.BackupAck) error {
	return s.p.st.recordAck(ctx, a.Generation, a.Source, a.Packs, a.Bytes)
}
