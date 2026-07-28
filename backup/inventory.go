package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The index pass: walk the asset classes, decide what changed, hash what did,
// and record the result as a generation.
//
// The expensive part is hashing, and almost nothing changes between runs — the
// corpus is ~417k immutable images. So the stat gate does the real work and the
// hash is the exception, which is what makes a daily index affordable against
// 131 GB.

// AssetClass is one directory to index, and how it should be treated. The host
// supplies these because only it knows which directories are persistent; see
// Deps.Classes.
type AssetClass struct {
	// Slug is the stable identifier recorded against every file.
	Slug string
	// Dir is the path to walk, relative to the process working directory.
	Dir string
	// Order decides which classes are indexed first. Lower runs earlier.
	//
	// This is not cosmetic on this install: screenshots are 117 GB of the
	// 131 GB total, so a pass that leads with them spends all its time there
	// and a run cut short by a deploy or a restart would have indexed nothing
	// else. Cheap and irreplaceable classes go first, so an interrupted pass
	// still protected the 30 MB that cannot be re-fetched.
	Order int
}

// fileRow is one indexed file.
type fileRow struct {
	Path    string
	Class   string
	SHA256  string
	Size    int64
	MtimeNS int64
	CtimeNS int64
	Inode   int64
}

// statKey is the cheap identity used to decide whether a file needs re-reading.
//
// Size and mtime alone are not enough. If the clock steps backward — an NTP
// correction, a VM migration — and a file is rewritten to the same length, the
// recorded mtime can repeat and the change becomes invisible. ctime cannot be
// moved backward from userspace, and the inode catches a replace-by-rename that
// happened to preserve both.
type statKey struct {
	Size    int64
	MtimeNS int64
	CtimeNS int64
	Inode   int64
}

func (r fileRow) statKey() statKey {
	return statKey{Size: r.Size, MtimeNS: r.MtimeNS, CtimeNS: r.CtimeNS, Inode: r.Inode}
}

// indexResult summarises one pass.
type indexResult struct {
	Files    int64
	Bytes    int64
	Hashed   int64
	Skipped  int64 // carried forward on an unchanged stat
	Suspect  int64
	PerClass map[string]classTotal
}

type classTotal struct {
	Files int64
	Bytes int64
}

// hashFile reads a file and returns its sha256, plus a structural sanity check.
//
// The check is not decoration. Five writers used to create asset files at their
// final path, so a file could be observed truncated — and once written its
// mtime never changed again, meaning a stat-based incremental would carry the
// torn version forward forever. Those writers are fixed, but files written
// before that fix are still on disk, and a backup that cannot recognise a
// truncated image will faithfully preserve it.
func hashFile(path string, size int64) (sum string, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	h := sha256.New()
	// Keep the last few bytes to check the format's end marker without a second
	// read: a truncated JPEG/PNG/WebP is exactly what a torn write leaves.
	tail := make([]byte, 0, 32)
	buf := make([]byte, 256<<10)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			chunk := buf[:n]
			if len(chunk) > 32 {
				chunk = chunk[len(chunk)-32:]
			}
			tail = append(tail, chunk...)
			if len(tail) > 32 {
				tail = tail[len(tail)-32:]
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", false, rerr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), !hasCompleteTail(path, tail, size), nil
}

// hasCompleteTail reports whether the file ends the way its format says it
// should. Unknown extensions and empty files pass — this must never be the
// reason a file is excluded from a backup, only a reason to flag it.
func hasCompleteTail(path string, tail []byte, size int64) bool {
	if size == 0 || len(tail) == 0 {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		// FFD9 end-of-image.
		return len(tail) >= 2 && tail[len(tail)-2] == 0xFF && tail[len(tail)-1] == 0xD9
	case ".png":
		// IEND chunk type, followed by its 4-byte CRC.
		return len(tail) >= 8 && string(tail[len(tail)-8:len(tail)-4]) == "IEND"
	case ".gif":
		return tail[len(tail)-1] == 0x3B
	default:
		// webp, svg, txt, anything else: no cheap end marker worth trusting.
		return true
	}
}

// walkClass lists one class's files with their stat identity. Symlinks are
// skipped: following them would let a link out of the tree pull arbitrary
// filesystem content into the backup, and a link within it would duplicate.
func walkClass(root string, c AssetClass) ([]fileRow, error) {
	base := filepath.Join(root, c.Dir)

	// The class root must be a directory if it exists at all. filepath.Walk on
	// a FILE happily walks it as a single entry and reports no error, so a
	// class whose directory has been replaced by a file would index that one
	// file and seal a generation claiming the class holds exactly it. Refusing
	// is the only honest answer: something is wrong with the deployment, and
	// the alternative is a backup that quietly redefines what the class means.
	if st, err := os.Stat(base); err == nil && !st.IsDir() {
		return nil, fmt.Errorf("class root %s is not a directory", c.Dir)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat class root %s: %w", c.Dir, err)
	}

	var out []fileRow
	err := filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			// A directory that does not exist yet is not an error — a class
			// with no uploads has no directory until the first one.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			// The temp files atomic writes leave mid-flight are deliberately
			// dot-prefixed; indexing one would record a path that is about to
			// vanish.
			if strings.HasPrefix(filepath.Base(p), ".") && p != base {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil // symlinks, sockets, devices
		}
		if strings.HasPrefix(filepath.Base(p), ".") {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		row := fileRow{
			Path:    filepath.ToSlash(rel),
			Class:   c.Slug,
			Size:    info.Size(),
			MtimeNS: info.ModTime().UnixNano(),
		}
		row.CtimeNS, row.Inode = statIdentity(info)
		out = append(out, row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// orderedClasses returns the classes in indexing order, cheap and irreplaceable
// first. See AssetClass.Order.
func orderedClasses(cs []AssetClass) []AssetClass {
	out := append([]AssetClass(nil), cs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

// rehashDue decides whether a file is up for a content re-read even though its
// stat is unchanged.
//
// Deterministic on the path, so coverage is uniform and every file is verified
// once per `denom` runs rather than depending on chance. This is the only thing
// that catches bit-rot and a torn write whose mtime never moved again.
func rehashDue(path string, gen int64, denom int) bool {
	if denom <= 1 {
		return true
	}
	var h uint32 = 2166136261
	for i := 0; i < len(path); i++ {
		h ^= uint32(path[i])
		h *= 16777619
	}
	return int64(h%uint32(denom)) == gen%int64(denom)
}

// indexPass walks every class and records a generation.
//
// The generation is sealed only if the whole walk succeeded. A partial walk is
// left unsealed on purpose: a class that failed to list looks identical to a
// class that legitimately emptied, and treating the second as authoritative is
// how a retention pass deletes the last copy of something.
func (p *Plugin) indexPass(ctx context.Context, root string, classes []AssetClass, rehashDenom int) (indexResult, error) {
	res := indexResult{PerClass: map[string]classTotal{}}
	if p.st == nil {
		return res, fmt.Errorf("backup: no inventory store")
	}

	gen, err := p.st.startGeneration(ctx)
	if err != nil {
		return res, fmt.Errorf("start generation: %w", err)
	}

	known, err := p.st.currentStats(ctx)
	if err != nil {
		return res, fmt.Errorf("load current inventory: %w", err)
	}

	for _, c := range orderedClasses(classes) {
		if ctx.Err() != nil {
			_ = p.st.failGeneration(ctx, gen, "cancelled")
			return res, ctx.Err()
		}
		rows, werr := walkClass(root, c)
		if werr != nil {
			// Record and stop. Sealing a generation whose walk failed would
			// publish a corpus that is short by an unknown amount.
			_ = p.st.failGeneration(ctx, gen, fmt.Sprintf("%s: %v", c.Slug, werr))
			return res, fmt.Errorf("walk %s: %w", c.Slug, werr)
		}

		var ct classTotal
		batch := make([]fileRow, 0, 512)
		for i := range rows {
			r := &rows[i]
			prev, seen := known[r.Path]
			needHash := !seen || prev.key != r.statKey() || rehashDue(r.Path, gen, rehashDenom)
			if !needHash {
				r.SHA256 = prev.sha
				res.Skipped++
			} else {
				sum, truncated, herr := hashFile(filepath.Join(root, r.Path), r.Size)
				if herr != nil {
					_ = p.st.noteSuspect(ctx, r.Path, r.Class, "unreadable", herr.Error())
					res.Suspect++
					continue
				}
				if truncated {
					// Recorded, and still indexed: refusing to back up a file
					// because it looks damaged guarantees the damaged copy is
					// the only one left.
					_ = p.st.noteSuspect(ctx, r.Path, r.Class, "truncated",
						"file does not end with its format's end marker")
					res.Suspect++
				}
				r.SHA256 = sum
				res.Hashed++
			}
			ct.Files++
			ct.Bytes += r.Size
			batch = append(batch, *r)
			if len(batch) >= 512 {
				if err := p.st.upsertFiles(ctx, gen, batch); err != nil {
					_ = p.st.failGeneration(ctx, gen, err.Error())
					return res, fmt.Errorf("record %s: %w", c.Slug, err)
				}
				batch = batch[:0]
			}
		}
		if len(batch) > 0 {
			if err := p.st.upsertFiles(ctx, gen, batch); err != nil {
				_ = p.st.failGeneration(ctx, gen, err.Error())
				return res, fmt.Errorf("record %s: %w", c.Slug, err)
			}
		}
		if err := p.st.recordClassTotal(ctx, gen, c.Slug, ct.Files, ct.Bytes); err != nil {
			return res, fmt.Errorf("class totals %s: %w", c.Slug, err)
		}
		res.PerClass[c.Slug] = ct
		res.Files += ct.Files
		res.Bytes += ct.Bytes
	}

	if err := p.st.sealGeneration(ctx, gen, res.Files, res.Bytes, res.Hashed); err != nil {
		return res, fmt.Errorf("seal generation: %w", err)
	}
	return res, nil
}

// shrinkReport describes a class that lost a suspicious share of its content
// against the previous sealed generation.
type shrinkReport struct {
	Class      string
	WasFiles   int64
	NowFiles   int64
	PctDropped float64
}

// detectShrink compares this generation's per-class totals with the previous
// sealed one.
//
// This is the guard against the worst failure a backup has: a missing bind
// mount is indistinguishable from an empty class. The boot check creates the
// directory if absent, the walk finds it empty, the generation seals with zero
// files, and once older generations age out the only copy is gone — with every
// step reporting success. A class that collapses must stop the pipeline, not
// flow through it.
func detectShrink(prev, cur map[string]classTotal, maxDropPct float64) []shrinkReport {
	var out []shrinkReport
	for class, was := range prev {
		if was.Files == 0 {
			continue
		}
		now := cur[class]
		if now.Files >= was.Files {
			continue
		}
		dropped := 100 * float64(was.Files-now.Files) / float64(was.Files)
		if dropped > maxDropPct {
			out = append(out, shrinkReport{
				Class: class, WasFiles: was.Files, NowFiles: now.Files, PctDropped: dropped,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PctDropped > out[j].PctDropped })
	return out
}

// sinceHours is a small helper for the job log.
func sinceHours(t time.Time) float64 { return time.Since(t).Hours() }
