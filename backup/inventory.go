package backup

import (
	"bytes"
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
	// Regenerable marks a class that can be rebuilt from something the site
	// still has — derived thumbnails, extracted frames — as opposed to one
	// whose only copy is this directory.
	//
	// It exists because the archive job stages a full local copy before it
	// writes anything, and on this install one regenerable class is 116 GB of
	// the 129 GB total. Including it pushes the staging requirement past the
	// free space on the box, so the pre-flight refuses and NOTHING is backed
	// up — the 13 GB that actually matters included. Excluding it turns an
	// impossible backup into a routine one.
	//
	// The host sets this: only it knows what it can rebuild.
	Regenerable bool
}

// fileRow is one indexed file.
type fileRow struct {
	// Rehashed records that this pass actually READ the file, as opposed to
	// carrying its hash forward on an unchanged stat. Only a real read may
	// advance hashed_at, or "last verified" degrades into "last seen".
	Rehashed bool
	Path     string
	Class    string
	SHA256   string
	Size     int64
	MtimeNS  int64
	CtimeNS  int64
	Inode    int64
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
	Cleared  int64 // previously flagged, read cleanly this pass
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
	// Keep the last tailWindow bytes to look for the format's end marker
	// without a second read. A window rather than the final two bytes because
	// encoders and CDNs legitimately append padding, EXIF or junk AFTER the
	// marker — requiring the file to END with it reports those as damaged, and
	// a check that cries wolf is one that gets deleted.
	tail := make([]byte, 0, tailWindow)
	// The first few bytes identify the format. Kept from the same pass, so the
	// sniff costs nothing on top of the hash.
	var head []byte
	buf := make([]byte, 256<<10)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			chunk := buf[:n]
			if head == nil {
				k := len(chunk)
				if k > headWindow {
					k = headWindow
				}
				head = append([]byte(nil), chunk[:k]...)
			}
			if len(chunk) > tailWindow {
				chunk = chunk[len(chunk)-tailWindow:]
			}
			tail = append(tail, chunk...)
			if len(tail) > tailWindow {
				tail = tail[len(tail)-tailWindow:]
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", false, rerr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), !hasCompleteTail(head, tail, size, path), nil
}

// tailWindow is how much of a file's end is kept for the completeness check.
// Wide enough to survive trailing padding, EXIF blocks and CDN junk after the
// end marker; small enough to cost nothing.
const tailWindow = 512

// headWindow is how much of a file's start is kept to identify its format.
// The longest magic checked is 8 bytes; 16 leaves room without another read.
const headWindow = 16

// imageFormat identifies a file by its CONTENT, ignoring the name.
//
// The extension is not evidence of anything. On this install 3,954 covers and
// 173 banners are complete, valid PNGs stored with a .jpg extension: the
// upstream serves PNG bytes from a .jpg URL and the fetchers keep the URL's
// name. Dispatching on the extension meant hunting for a JPEG end-marker inside
// a PNG, never finding one, and reporting 27% of the cover art as damaged —
// while every one of those files ended with a correct IEND chunk and CRC.
//
// Sniffing also survives the failure it is meant to catch, because a partial
// download still has its header: the bytes arrive in order, so the magic is
// present long before the end marker would be.
func imageFormat(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return "jpeg"
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(head, []byte("GIF8")):
		return "gif"
	case bytes.HasPrefix(head, []byte("RIFF")):
		return "webp"
	}
	return ""
}

// hasCompleteTail reports whether the file carries its format's end marker near
// the end. Unknown formats and empty files pass — this must never be the reason
// a file is excluded from a backup, only a reason to flag it.
//
// It SEARCHES the tail window rather than demanding the final bytes match.
// Requiring an exact ending reported 4,163 of 14,817 production covers as
// truncated, because JPEG encoders and CDNs routinely append bytes after FFD9.
// Fixing that left 4,128 still flagged, and the second cause turned out to be
// the format dispatch rather than the window — see imageFormat. Both times the
// tell was the same: flagged files averaged roughly five times the size of
// healthy ones, and truncation makes files smaller.
func hasCompleteTail(head, tail []byte, size int64, path string) bool {
	// A zero-byte image is never valid — it is a download that created the
	// file and then failed. There is no content to sniff, so the name is the
	// only signal left; an empty file of an unknown type is left alone,
	// because plenty of things are legitimately empty.
	if size == 0 {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".jpg", ".jpeg", ".png", ".gif", ".webp":
			return false
		}
		return true
	}
	if len(tail) == 0 {
		return true
	}

	switch imageFormat(head) {
	case "jpeg":
		return bytes.Contains(tail, []byte{0xFF, 0xD9}) // end-of-image
	case "png":
		return bytes.Contains(tail, []byte("IEND"))
	case "gif":
		return bytes.Contains(tail, []byte{0x3B})
	default:
		// webp has no cheap trailing marker, and anything unrecognised is not
		// evidence of damage — only of a format this check does not know.
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

	// Always re-read what is currently flagged, whatever its stat says. Two
	// reasons, and the second is why the suspect count could not be trusted:
	// a file may have been repaired, and the DETECTOR may have been corrected.
	// Without this, a flagged row survives every future pass — the stat gate
	// skips the file, so nothing ever re-examines it — and the table converges
	// on the worst historical moment rather than on the truth.
	suspect := map[string]bool{}
	if paths, err := p.st.suspectPaths(ctx); err == nil {
		for _, sp := range paths {
			suspect[sp] = true
		}
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
			needHash := !seen || prev.key != r.statKey() ||
				suspect[r.Path] || rehashDue(r.Path, gen, rehashDenom)
			if !needHash {
				r.SHA256 = prev.sha
				res.Skipped++
			} else {
				r.Rehashed = true
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
						fmt.Sprintf("no end-of-format marker in the last %d bytes", tailWindow))
					res.Suspect++
				} else if suspect[r.Path] {
					// It read cleanly this time — repaired, or previously
					// misjudged. Either way the flag is stale and must go, or
					// the count only ever grows.
					_ = p.st.clearSuspect(ctx, r.Path)
					res.Cleared++
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
