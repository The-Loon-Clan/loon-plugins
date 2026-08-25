// grabdispatch.go is a PROPOSED seam: the host hands a chosen torrent to the
// fleet, which fetches it, re-uploads to Usenet, and returns the result
// through content.pipeline. It is the OUTBOUND half of the loop whose inbound
// half ContentPipeline already is.
//
// WHERE IT SITS IN THE PIPELINE. The schedule finds an aired episode, the gap
// join finds the index has nothing, the tracker search picks the best copy --
// and this dispatches that copy to an agent. The agent downloads the torrent,
// produces the NZB with its screenshots, metadata and subtitles, and calls
// ContentPipeline.Ingest(DeliveryTorrent, ...) to publish it. So Dispatch and
// Ingest are the two ends of one flow: Dispatch says "go get this", Ingest
// says "here is what I got".
//
// WHY IT IS A PROPOSAL, AND HOST-SIDE. The agent RUNTIME -- the /api/agent/*
// poll surface agents pull work from, the task queue, the upload flow -- lives
// in the host, not a plugin (the agent plugin is surfaces-only; its README is
// explicit that the runtime stays with the host). So a GrabDispatcher is
// implemented by whatever owns that queue: dispatching is enqueuing a task an
// agent later polls, not pushing to a waiting process. This reference demo
// wires no agent runtime, so nothing here implements it and the host consumer
// (the auto-grab) computes what it WOULD dispatch and dispatches nothing --
// exactly as the request filer is dormant until a board wires one.
//
// A SEPARATE seam from requests.filer, because they are two different entry
// points to the fleet. The filer files a community REQUEST an agent (or a
// person) sources later; this dispatches a SPECIFIC already-chosen torrent to
// fetch now. The auto-request path and the auto-grab path both exist because a
// site may want either: ask the community, or just go and get it.
package pluginapi

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// GrabDispatchName is where the host's agent dispatcher publishes itself.
// `agent.dispatch`, in the agent runtime's namespace.
const GrabDispatchName = "agent.dispatch"

// GrabRequest is one torrent to fetch, and enough about the release for the
// agent to name it and the ingest to identify it.
type GrabRequest struct {
	// Delivery is how the content will arrive. DeliveryTorrent today; the same
	// dispatcher could carry a DeliveryDirect grab (a direct download URL)
	// without a new contract, which is why it reuses the pipeline's kind.
	Delivery DeliveryKind

	// ── The artifact to fetch ──────────────────────────────────────────
	// A magnet OR a download URL, and the info hash for dedup. A public
	// source gives a magnet; a private tracker gives an authenticated
	// DownloadURL whose link already carries the member's key.
	Magnet      string
	DownloadURL string
	InfoHash    string
	// TrackerSlug is the trackerdir identity of the source, so the agent can
	// apply that tracker's politeness and, for a private one, its stored key.
	TrackerSlug string

	// ── The release it will become ─────────────────────────────────────
	// Carried so the produced NZB is identified precisely and can fulfil the
	// gap: the same fields the request filer sends, for the same reason.
	Title    string
	Category string
	Season   int
	Episode  int
	ImdbID   string
	TvdbID   string
	TmdbID   string

	// RequestID links the grab to an originating community request when there
	// is one, so completing the fetch fulfils that request. 0 for a direct
	// auto-grab that no member asked for.
	RequestID int64
}

// GrabResult reports what the dispatcher did.
type GrabResult struct {
	// Queued is false when the dispatcher declined -- most often because the
	// same info hash is already downloading or already indexed, which the
	// dispatcher dedups so a six-hourly pass does not queue a fetch twice.
	Queued bool
	// TaskID identifies the enqueued work, for a status page. 0 when not
	// queued.
	TaskID int64
}

// GrabDispatcher accepts a grab for the fleet to fetch.
type GrabDispatcher interface {
	// Dispatch enqueues one torrent for an agent to fetch and re-upload.
	//
	// DEDUP IS THE DISPATCHER'S, on info hash: the auto-grab runs every pass
	// and would otherwise re-queue a fetch still in flight. A non-error
	// GrabResult with Queued false is the ordinary "already have it or already
	// getting it" answer, not a failure.
	Dispatch(ctx context.Context, req GrabRequest) (GrabResult, error)
}

// LookupGrabDispatcher resolves the dispatcher, or false when no agent runtime
// publishes one -- which is this demo, and every host until the fleet is wired.
func LookupGrabDispatcher(c *core.Core) (GrabDispatcher, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(GrabDispatchName)
	if !ok {
		return nil, false
	}
	d, ok := v.(GrabDispatcher)
	return d, ok
}
