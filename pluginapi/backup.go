package pluginapi

import (
	"context"
	"io"
)

// Backup capability contract. The backup plugin publishes this on the core
// extension registry; the host mounts HTTP routes over it so a remote puller
// can fetch packs without the host importing the plugin.

// BackupPacksName is the extension registry key for the pack server.
const BackupPacksName = "backup.packs"

// BackupPack is one transferable unit in a generation's manifest.
type BackupPack struct {
	ID    string `json:"id"`
	Class string `json:"class"`
	// Bytes is the WIRE length: exactly what streaming this pack emits,
	// ZIP structure included. A client uses this for Content-Length and to
	// know when a transfer is complete. It is deliberately NOT the sum of the
	// member file sizes — that figure is always short by the archive's own
	// headers, and treating it as the end mark truncates every pack.
	Bytes int64 `json:"bytes"`
	// Content is the sum of the member file sizes: what a restore writes out,
	// as opposed to what the transfer costs.
	Content int64 `json:"content_bytes"`
	Members int   `json:"members"`

	// Raw marks a pack streamed as its member's own bytes, with no ZIP
	// container. Classic ZIP header fields are 32-bit, so a member over 4 GiB
	// cannot be represented — and `pg_dump -Fd` writes one file per table, so
	// the database class produces exactly that. A client MUST honour this: the
	// body is not an archive, and opening it as one fails.
	Raw bool `json:"raw,omitempty"`
	// Path and SHA256 are the sole member's, set only for a raw pack. A ZIP
	// carries its members' names and CRCs in its own directory; a raw pack has
	// nowhere to put them, so they travel here instead — without them a client
	// holds bytes it can neither name nor check.
	Path   string `json:"path,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// BackupManifest is everything a puller needs to decide what to fetch.
//
// Generation matters as much as the pack list. Packs are planned per sealed
// generation, and a transfer of this corpus runs for hours while the index
// seals a new one daily — so a puller must pin the generation it planned
// against and pass it back when fetching, or its still-valid pack IDs start
// disappearing mid-transfer.
type BackupManifest struct {
	Generation int64        `json:"generation"`
	SealedAt   string       `json:"sealed_at"`
	Files      int64        `json:"files"`
	Bytes      int64        `json:"bytes"`
	Packs      []BackupPack `json:"packs"`
}

// BackupPacks serves backup content without staging any of it.
//
// The whole reason this capability exists: the archive job writes a full second
// copy to the volume it is protecting, which needs free space equal to the data
// and therefore cannot run on a box whose assets already fill it. Streaming
// moves that cost to the puller's disk instead, so the server's peak overhead
// is one open file.
type BackupPacks interface {
	// Manifest plans the newest sealed generation.
	Manifest(ctx context.Context) (BackupManifest, error)
	// WritePack streams one pack. gen pins the generation the id belongs to;
	// 0 means the newest sealed one. skip discards that many leading bytes,
	// which is how a dropped transfer resumes — packs are byte-deterministic,
	// so an offset means the same thing on every attempt.
	WritePack(ctx context.Context, w io.Writer, gen int64, id string, skip int64) error
	// Ack records that a puller holds a complete, verified copy of a
	// generation.
	//
	// Without it the server can only say what is AVAILABLE to pull, and the
	// question an operator actually has — "is my backup happening?" — has no
	// answer on the site at all. A backup that silently stopped a month ago
	// looks exactly like one that ran last night.
	//
	// The claim is the puller's, and the server records it as such: it means
	// "a puller reported this", not "the server verified it". The puller only
	// sends it after every pack's length and CRC checked out.
	Ack(ctx context.Context, a BackupAck) error
}

// BackupAck is a puller's report that it holds a generation completely.
type BackupAck struct {
	Generation int64 `json:"generation"`
	Packs      int64 `json:"packs"`
	Bytes      int64 `json:"bytes"`
	// Source names the puller, so several backup targets can be distinguished
	// (and so one going quiet is visible rather than masked by another).
	Source string `json:"source"`
}
