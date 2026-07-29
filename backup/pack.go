package backup

import (
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
)

// Packs: many small files concatenated into one transferable unit.
//
// A pack is a ZIP, STORED (uncompressed) only, with members named by their real
// relative path. That last choice is what makes this format defensible as a
// hand-rolled one: `unzip` is a complete break-glass restore path with no
// bespoke tool, on a machine with nothing installed, years from now.
//
//	while read id; do unzip -o -q "packs/$id.zip" -d /srv/indexer/; done < order.txt
//
// One caveat on that command, found by actually running it rather than by
// reading the spec: Info-ZIP unzip 6.00 (2009, still what Debian ships) mangles
// non-ASCII member names whatever you pass it — -U and -UU included — even
// though the archive marks them UTF-8 correctly and libarchive, Go and Python
// all restore them byte-identically. Every one of the 417,826 paths in this
// corpus is ASCII, so `unzip` is a sound restore path as things stand. If a
// non-ASCII asset name ever appears, use `bsdtar xf` instead; it was verified
// to round-trip the same archive exactly.
//
// The alternative — members named by content hash — buys deduplication, which
// is worth approximately nothing here: the corpus is immutable
// already-compressed images, so there is no duplicate content to find. Trading
// a working `unzip` for a dedup ratio near zero is a bad deal.
//
// STORED rather than deflate for determinism: deflate output depends on the
// zlib/Go version, so a stdlib upgrade would silently change pack bytes and
// therefore pack IDs, and every previously-transferred pack would look new.
// The files are already-compressed images; deflate would gain nothing anyway.
//
// The headers are written explicitly rather than through archive/zip for the
// same reason — this pins the on-disk format against a stdlib change. It is
// about 150 lines and a golden-file test.

const (
	// packTargetBytes is the size a pack is filled toward. 64 MiB rather than
	// the 10 MB first sketched: the only advantage of smaller packs is bounded
	// re-fetch waste on an interrupted transfer, and Range resume already
	// covers that. 64 MiB means ~500 packs for this corpus instead of ~3,000.
	packTargetBytes = 64 << 20

	// packMaxMembers keeps a pack inside the classic ZIP central-directory
	// limit. A pack of many tiny files could otherwise exceed 65,535 entries
	// and need zip64, which is exactly the complexity this format avoids.
	packMaxMembers = 20000
)

// packMember is one file's placement inside a pack.
type packMember struct {
	Path   string // real relative path, and the name inside the zip
	SHA256 string
	Size   int64
	CRC32  uint32
	// Offset is where the member's DATA begins in the pack — not its header.
	// This is what makes restoring one file a single Range GET rather than a
	// 64 MiB download, and it is the reason the usual objection to packing
	// does not apply here.
	Offset int64
}

// packPlan is a sealed pack: a fixed, ordered member list. Given the plan, the
// bytes are a pure function of the members' contents, so the pack can be
// assembled on demand and will hash identically every time.
type packPlan struct {
	Class   string
	Members []packMember
	Bytes   int64
}

// planPacks groups files into packs, class-pure and sorted by path.
//
// Class-pure so a policy can transfer or retain one class without dragging
// another along — the tiers here differ by four orders of magnitude, and mixing
// 30 MB of irreplaceable artwork into a pack with 117 GB of regenerable frames
// would make the cheap thing as expensive as the dear one.
//
// Sorted by path so packs are stable across runs: an unchanged corpus produces
// the same grouping, and therefore the same pack IDs, and therefore no
// transfer.
//
// The caller must supply files NEWEST FIRST per path. `files` is keyed
// (path, sha256), so a file edited in place leaves several rows sharing a path,
// and only the newest is current — see currentStats, which does the same thing
// with DISTINCT ON. Duplicates are not merely wasteful here: two ZIP members
// with the same name make `unzip -o` write whichever came last, and because the
// sort key is the path alone their relative order is unspecified, so the pack
// bytes — and therefore the pack ID — would differ between runs over identical
// content.
func planPacks(class string, files []fileRow, targetBytes int64, maxMembers int) []packPlan {
	if targetBytes <= 0 {
		targetBytes = packTargetBytes
	}
	if maxMembers <= 0 {
		maxMembers = packMaxMembers
	}
	sorted := make([]fileRow, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, f := range files {
		if _, dup := seen[f.Path]; dup {
			continue
		}
		seen[f.Path] = struct{}{}
		sorted = append(sorted, f)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	var (
		out  []packPlan
		cur  packPlan
		size int64
	)
	cur.Class = class
	flush := func() {
		if len(cur.Members) > 0 {
			out = append(out, cur)
		}
		cur = packPlan{Class: class}
		size = 0
	}
	// A file is never split across packs — the offset contract is that one
	// member lives in one place — so the target is a fill line, not a hard cap:
	// a single file bigger than the target becomes a pack of its own. That falls
	// out of the rule below rather than needing a case of its own, because a
	// pack at or over the target forces a flush on the very next file.
	for _, f := range sorted {
		if size+f.Size > targetBytes || len(cur.Members) >= maxMembers {
			flush()
		}
		cur.Members = append(cur.Members, packMember{Path: f.Path, SHA256: f.SHA256, Size: f.Size})
		size += f.Size
	}
	flush()

	for i := range out {
		var total int64
		for _, m := range out[i].Members {
			total += m.Size
		}
		out[i].Bytes = total
	}
	return out
}

// Fixed DOS timestamp for every member: 1980-01-01 00:00:00, the ZIP epoch.
//
// Zeroing the timestamp is what makes a pack byte-deterministic. Real mtimes
// would change the bytes whenever a file was touched without its content
// changing, which would change the pack ID and force a re-transfer of data that
// is identical.
const (
	dosTime = 0
	dosDate = 1<<5 | 1 // month 1, day 1, year 1980

	// General purpose bit 11: the filename is UTF-8.
	//
	// Without it a reader falls back to CP437, and `unzip` restores a non-ASCII
	// name as mojibake — a different file from the one that was backed up. It is
	// worth knowing how this was found: `unzip -t` reported OK, because it
	// verifies the CRC of member DATA and never looks at the name, and
	// archive/zip round-tripped it too, because Go assumes UTF-8 either way.
	// Only an actual `unzip` into a directory and a diff against the originals
	// showed the mangling. ASCII is unchanged by this flag, so it is set for
	// every member rather than conditionally, which keeps the bytes a pure
	// function of the content.
	flagUTF8 = 0x0800
)

// writePack assembles a pack, returning the member list with offsets filled in.
//
// The member ORDER is the plan's order and is never re-derived here: the pack
// ID is the hash of these bytes, so anything that could reorder them would
// change the ID of a pack whose contents are unchanged.
func writePack(w io.Writer, root string, plan packPlan) ([]packMember, int64, error) {
	members := append([]packMember(nil), plan.Members...)
	cw := &countingWriter{w: w}

	// The classic ZIP header fields are 32-bit. Going over silently truncates
	// modulo 2^32 and produces an archive that opens, lists plausible sizes, and
	// restores garbage — the one failure mode a backup must never have. zip64
	// would lift the limit at the cost of the break-glass compatibility this
	// format exists for, so refuse instead. Nothing in this corpus is close:
	// the largest class averages under a megabyte per file.
	for _, m := range plan.Members {
		if m.Size > math.MaxUint32 {
			return nil, 0, fmt.Errorf("%s is %d bytes: a member over 4 GiB needs zip64, which this format deliberately does not use", m.Path, m.Size)
		}
	}
	if plan.Bytes > math.MaxUint32 {
		return nil, 0, fmt.Errorf("pack is %d bytes: the central directory offset is 32-bit, so a pack over 4 GiB needs zip64", plan.Bytes)
	}

	type centralEntry struct {
		m           packMember
		localOffset int64
	}
	var central []centralEntry

	for i := range members {
		m := &members[i]
		full := filepath.Join(root, m.Path)
		f, err := os.Open(full)
		if err != nil {
			return nil, 0, fmt.Errorf("open %s: %w", m.Path, err)
		}
		localOffset := cw.n

		// The CRC and size must be in the local header, which means reading the
		// file before writing it. Data descriptors would avoid that, but they
		// make the archive unreadable by some tools — and break-glass
		// compatibility is the entire justification for using ZIP.
		crc, size, err := crcOfFile(f)
		if err != nil {
			f.Close()
			return nil, 0, fmt.Errorf("crc %s: %w", m.Path, err)
		}
		if size != m.Size {
			f.Close()
			return nil, 0, fmt.Errorf("%s changed size between index and pack (%d -> %d)", m.Path, m.Size, size)
		}
		m.CRC32 = crc

		name := []byte(m.Path)
		hdr := make([]byte, 0, 30+len(name))
		hdr = append(hdr, 0x50, 0x4b, 0x03, 0x04) // local file header signature
		hdr = putU16(hdr, 10)                     // version needed: 1.0, stored
		hdr = putU16(hdr, flagUTF8)
		hdr = putU16(hdr, 0) // method: stored
		hdr = putU16(hdr, dosTime)
		hdr = putU16(hdr, dosDate)
		hdr = putU32(hdr, crc)
		hdr = putU32(hdr, uint32(size)) // compressed == uncompressed
		hdr = putU32(hdr, uint32(size))
		hdr = putU16(hdr, uint16(len(name)))
		hdr = putU16(hdr, 0) // extra length
		hdr = append(hdr, name...)
		if _, err := cw.Write(hdr); err != nil {
			f.Close()
			return nil, 0, err
		}

		m.Offset = cw.n
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			f.Close()
			return nil, 0, err
		}
		if _, err := io.Copy(cw, f); err != nil {
			f.Close()
			return nil, 0, fmt.Errorf("copy %s: %w", m.Path, err)
		}
		f.Close()
		central = append(central, centralEntry{m: *m, localOffset: localOffset})
	}

	// The authoritative limit check. The pre-flight above uses plan.Bytes, which
	// is only as good as the plan; this is the offset actually about to be
	// written into the end-of-central-directory record.
	cdOffset := cw.n
	if cdOffset > math.MaxUint32 {
		return nil, 0, fmt.Errorf("pack reached %d bytes: the central directory offset is 32-bit", cdOffset)
	}
	for _, e := range central {
		name := []byte(e.m.Path)
		rec := make([]byte, 0, 46+len(name))
		rec = append(rec, 0x50, 0x4b, 0x01, 0x02) // central directory header
		rec = putU16(rec, 20)                     // version made by
		rec = putU16(rec, 10)                     // version needed
		rec = putU16(rec, flagUTF8)
		rec = putU16(rec, 0) // method: stored
		rec = putU16(rec, dosTime)
		rec = putU16(rec, dosDate)
		rec = putU32(rec, e.m.CRC32)
		rec = putU32(rec, uint32(e.m.Size))
		rec = putU32(rec, uint32(e.m.Size))
		rec = putU16(rec, uint16(len(name)))
		rec = putU16(rec, 0) // extra
		rec = putU16(rec, 0) // comment
		rec = putU16(rec, 0) // disk number
		rec = putU16(rec, 0) // internal attrs
		rec = putU32(rec, 0o644<<16)
		rec = putU32(rec, uint32(e.localOffset))
		rec = append(rec, name...)
		if _, err := cw.Write(rec); err != nil {
			return nil, 0, err
		}
	}
	cdSize := cw.n - cdOffset

	eocd := make([]byte, 0, 22)
	eocd = append(eocd, 0x50, 0x4b, 0x05, 0x06)
	eocd = putU16(eocd, 0) // this disk
	eocd = putU16(eocd, 0) // disk with central directory
	eocd = putU16(eocd, uint16(len(central)))
	eocd = putU16(eocd, uint16(len(central)))
	eocd = putU32(eocd, uint32(cdSize))
	eocd = putU32(eocd, uint32(cdOffset))
	eocd = putU16(eocd, 0) // comment length
	if _, err := cw.Write(eocd); err != nil {
		return nil, 0, err
	}
	return members, cw.n, nil
}

func crcOfFile(f *os.File) (uint32, int64, error) {
	h := crc32.NewIEEE()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, 0, err
	}
	return h.Sum32(), n, nil
}

func putU16(b []byte, v uint16) []byte { return append(b, byte(v), byte(v>>8)) }
func putU32(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

// countingWriter tracks the byte offset, which is how member offsets are
// discovered without buffering the pack.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
