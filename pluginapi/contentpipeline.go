package pluginapi

import "context"

// ContentPipeline turns a finished piece of delivered content into a
// published release. It is deliberately delivery-agnostic: the Usenet agent
// feeds it today, and the future BitTorrent tracker and direct-download
// paths will feed the SAME pipeline. Everything downstream of "the bytes
// arrived" is identical across delivery methods —
//
//	dedup on content identity -> store the delivery artifact ->
//	media-info / audio-catalog / palette / OCR / pipeline-stage / NFO
//	metadata -> async screenshot decode -> release-group link ->
//	fulfil the originating request -> award the uploader points ->
//	announce the release
//
// — so it lives behind one contract instead of being copy-pasted into each
// delivery handler. Only the artifact-storage step is delivery-specific
// (an nzbs row for Usenet; a torrent/magnet row for BT; a file/URL row for
// direct), keyed off IngestRequest.Delivery.
//
// This is a published capability, not an agent-private helper: the agent
// plugin consumes it via the extension registry (Lookup(ContentPipelineName)),
// and the BT-tracker and direct-download plugins will consume the same one.
// It is a strong candidate to graduate into its own plugin
// (loon-plugins/pipeline) once the releases/release_artifacts tier lands —
// the contract here is shaped so that extraction is a move, not a redesign.
//
// Implementation note: the host adapter's Ingest body is the EXISTING agent
// /complete NZB-creation block moved verbatim (same statements, same order).
// This is the revenue path; a behaviour-preserving relocation plus a golden
// test on the resulting nzbs row + points_ledger entry is the safe way to
// extract it. Screenshot PNG decode stays inside the adapter (it's the slow
// part); callers forward the raw parts only.
const ContentPipelineName = "content.pipeline"

// DeliveryKind identifies how a release's content reached the site. Usenet
// is the only kind the pipeline stores today; Torrent and Direct are named
// now so the tracker and direct-download work slot in as new artifact-store
// branches without changing this contract.
type DeliveryKind string

const (
	DeliveryUsenet  DeliveryKind = "usenet"
	DeliveryTorrent DeliveryKind = "torrent"
	DeliveryDirect  DeliveryKind = "direct"
)

// ContentPipeline is the delivery-agnostic release-ingestion contract.
type ContentPipeline interface {
	// Ingest creates-or-dedups the release for a fulfilled request and
	// returns what happened. It honours the keep-private branch (bytes
	// land on the request row, no public release, no points). A non-nil
	// error means the caller should demote its delivery lock to "failed";
	// a non-nil IngestResult.Announce means the caller should fire the
	// release notification.
	Ingest(ctx context.Context, req IngestRequest) (IngestResult, error)
}

// IngestRequest is everything the pipeline needs to publish one release.
// The release metadata (the *JSON sidecars, screenshots, anonymity, points)
// is delivery-agnostic; only Delivery + the artifact bytes are specific to
// how the content arrived.
type IngestRequest struct {
	// Delivery selects the artifact-storage branch. Empty defaults to
	// DeliveryUsenet.
	Delivery DeliveryKind

	// RequestID is the originating nzb_request this content fulfils (the
	// pipeline loads it for title/category/external-ids and marks it
	// fulfilled). Delivery methods that ingest without a request will pass
	// 0 once that path exists.
	RequestID int64
	// UploaderUserID owns the resulting artifact and receives the points.
	UploaderUserID int64

	// ── Delivery artifact (delivery-specific) ──────────────────────────
	// Usenet: the raw (or gzipped) NZB XML exactly as sent; the adapter
	// normalises, hashes, size-audits and compresses it. Torrent/Direct
	// artifact fields are added alongside when those paths land.
	NzbData []byte

	// ── Release metadata (delivery-agnostic sidecars) ──────────────────
	// Empty strings mean "not provided"; the corresponding setter is
	// skipped.
	MediaInfoJSON         string
	AudioTracksJSON       string
	AudioFingerprintsJSON string
	DominantPaletteJSON   string
	PipelineStagesJSON    string
	OCRText               string
	OCRLang               string
	Password              string
	ScreenshotBlobs       [][]byte

	// AnonymousMode is the per-source upload-anonymity setting, passed
	// through so the adapter applies it without reaching back into the
	// caller's own config table.
	AnonymousMode string

	// AwardPoints gates the uploader points award: true on the fulfil
	// path, false when backfilling an already-published release.
	// Keep-private uploads never award regardless.
	AwardPoints bool
}

// IngestResult reports the outcome so the caller can respond + notify.
type IngestResult struct {
	NzbID       int64 // created or deduped public release id; 0 for keep-private / no public row
	Deduped     bool  // an identical content identity already existed
	KeptPrivate bool  // bytes landed on the request row instead of a public release
	// Announce is non-nil only for a freshly created public release; the
	// caller fires it through a ReleaseNotifier it looked up.
	Announce *ReleaseAnnouncement
}
