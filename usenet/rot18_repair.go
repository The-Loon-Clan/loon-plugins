package usenet

import (
	"context"
	"strconv"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Repairing the releases stored before the crawler could read a rotated subject.
//
// The ingest fix (crawl.go, 2026-08-12) decodes ROT18 before parseSubject sees
// it. That fixed the future and nothing else: roughly a thousand rows were
// already in the catalogue with gibberish titles, and a re-crawl cannot correct
// them because the dedup key is a content hash over message-ids — the corrected
// re-assembly hashes to the same row and is discarded by ON CONFLICT DO
// NOTHING. The only way back is to rewrite the stored title, which is what this
// does.
//
// WHY THE DECODE IS SAFE TO DO BLIND. rot18 is its own inverse, so the repair
// is the same operation as the damage. That cuts both ways: applying it to a
// row that was never rotated produces convincing-looking nonsense with no
// signal that anything went wrong. So the job never guesses. It decodes only
// rows that isRot18Subject matches on a literal marker — a token that cannot
// occur by accident in a real title — and never a row already flagged
// obfuscated, because a second pass would re-encode a correct title.
//
// WHAT IT DOES NOT DO. The rotation covered digits, so counters and sizes were
// parsed wrong too, and some sets staged with a total-parts of zero that
// completeness can never satisfy. A rename does not make those buildable. The
// job counts them and leaves them alone rather than re-deriving structure from
// a decoded subject, which is the kind of inference that turns a repair into
// the next incident.

// rot18ScanBatch is how many titles one host round-trip fetches. The walk is
// unfiltered by design, so this is a plain indexed range scan on the primary
// key; the batch is sized to keep each call short rather than to bound work.
const rot18ScanBatch = 5000

// rot18CursorKey stores how far the walk has read. Job STATE, not operator
// config, so it is deliberately absent from the settings UI: a walk that
// restarts from zero on every pass re-reads a million rows to find nothing,
// which is the difference between a repair that finishes and one that runs
// forever.
const rot18CursorKey = "rot18_repair_cursor"

type rot18Outcome struct {
	Scanned  int
	Repaired int
	// Broken counts repaired rows that are ALSO structurally damaged
	// (total_segments == 0). They are searchable again but still unbuildable,
	// and saying so is the point — a job that reported them as fixed would be
	// lying about the part that matters.
	Broken int
	// AlreadyFlagged counts rows carrying the obfuscated flag already. Skipped,
	// because decoding twice re-encodes.
	AlreadyFlagged int
	Cursor         int64
	Done           bool
}

// repairRot18Batch walks one batch and repairs what it finds.
//
// Split from the job so the decision — which rows are rotated, what they should
// say, what must be left alone — is testable without a host or a database.
func repairRot18Batch(rows []pluginapi.ReleaseTitle, retitle func(id int64, title string) error) (rot18Outcome, error) {
	var out rot18Outcome
	out.Scanned = len(rows)
	for _, r := range rows {
		if r.Obfuscated {
			// Already decoded on a previous pass or at ingest. Rotating again
			// would turn a correct title back into the poster's gibberish.
			out.AlreadyFlagged++
			if r.ID > out.Cursor {
				out.Cursor = r.ID
			}
			continue
		}
		decoded, was := deobfuscateSubject(r.Title)
		if !was || decoded == r.Title {
			if r.ID > out.Cursor {
				out.Cursor = r.ID
			}
			continue
		}
		if err := retitle(r.ID, decoded); err != nil {
			// The cursor stops SHORT of a failed write. ReleaseTitlesAfter is
			// strictly greater-than, so a cursor that has crossed a row is a
			// promise never to look at it again — and a row whose retitle
			// failed is not a row this job is done with. Persisting the
			// failed row's own ID is how a transient host error used to turn
			// into a title that stayed gibberish forever: every later pass
			// fetched ID > N and reported a clean walk. A PERMANENTLY failing
			// row now wedges the walk loudly (one failed write and one
			// reportErr per pass) instead of vanishing silently — that shape
			// of failure is a host-store defect worth surfacing.
			return out, err
		}
		out.Repaired++
		if r.TotalSegments == 0 {
			out.Broken++
		}
		if r.ID > out.Cursor {
			out.Cursor = r.ID
		}
	}
	return out, nil
}

// runRot18Repair is the job entry point.
func (p *Plugin) runRot18Repair(ctx context.Context) {
	cfg := p.effective(ctx)
	if !cfg.Rot18RepairEnabled {
		p.rot18Job.Log("disabled in settings")
		p.rot18Job.SetIdle(p.nextRot18(cfg))
		return
	}
	if !p.mayWrite(ctx, p.rot18Job) {
		return
	}
	if !p.rot18Mu.TryLock() {
		p.rot18Job.Log("title repair already running — skipping overlap")
		return
	}
	defer p.rot18Mu.Unlock()

	if !p.withLease(ctx, leaseScopeJob, jobNameRot18Repair, p.leaseTTL(cfg), func(ctx context.Context) {
		p.rot18RepairLocked(ctx, cfg)
	}) {
		p.rot18Job.Log("title repair skipped — another worker holds this job")
		p.rot18Job.SetIdle(p.nextRot18(cfg))
	}
}

func (p *Plugin) rot18RepairLocked(ctx context.Context, cfg Config) {
	p.rot18Job.SetRunning()

	store, ok := pluginapi.LookupReleaseRetitleStore(p.core)
	if !ok {
		// Not an error state: a host that registered no retitle store has not
		// asked for this, and an internal-sink install has nothing to repair.
		p.rot18Job.Log("no retitle store registered by the host — nothing to do")
		p.rot18Job.SetIdle(p.nextRot18(cfg))
		return
	}

	cursor := p.rot18Cursor(ctx)
	var total rot18Outcome
	deadline := time.Now().Add(time.Duration(cfg.Rot18RepairMaxMin) * time.Minute)

	for {
		if ctx.Err() != nil {
			return
		}
		rows, err := store.ReleaseTitlesAfter(ctx, cursor, rot18ScanBatch)
		if err != nil {
			p.rot18Job.SetError(err.Error())
			p.reportErr(ctx, "usenet/rot18-scan", err)
			return
		}
		if len(rows) == 0 {
			total.Done = true
			break
		}
		batch, err := repairRot18Batch(rows, func(id int64, title string) error {
			// obfuscated=true always: the flag records that the STORED title is
			// a decode rather than what the poster wrote, and it is what stops
			// the next pass rotating it back.
			return store.RetitleRelease(ctx, id, title, true)
		})
		total.Scanned += batch.Scanned
		total.Repaired += batch.Repaired
		total.Broken += batch.Broken
		total.AlreadyFlagged += batch.AlreadyFlagged
		if batch.Cursor > cursor {
			cursor = batch.Cursor
		}
		// Persisted per batch, not at the end. A pass cut short by a deploy or
		// a deadline must not re-read what it has already walked.
		p.setRot18Cursor(ctx, cursor)
		if err != nil {
			p.rot18Job.SetError(err.Error())
			p.reportErr(ctx, "usenet/rot18-retitle", err)
			return
		}
		if time.Now().After(deadline) {
			p.rot18Job.Log("time budget reached (%d min) — paused at id %d, %d scanned, %d repaired",
				cfg.Rot18RepairMaxMin, cursor, total.Scanned, total.Repaired)
			break
		}
	}

	switch {
	case total.Done && total.Scanned == 0:
		// The steady state once the catalogue has been walked: the cursor is
		// past the newest row and the job has nothing left to do. Said out loud
		// so "finished" and "never started" do not look the same.
		p.rot18Job.Log("nothing new to check — the walk has reached the end of the catalogue (id %d)", cursor)
	case total.Repaired == 0:
		p.rot18Job.Log("scanned %d title(s) to id %d, none rotated", total.Scanned, cursor)
	default:
		p.rot18Job.Log("repaired %d title(s) of %d scanned to id %d — %d of them are ALSO structurally broken "+
			"(total_segments=0) and remain unbuildable; %d were already flagged",
			total.Repaired, total.Scanned, cursor, total.Broken, total.AlreadyFlagged)
	}
	p.rot18Job.SetIdle(p.nextRot18(cfg))
}

func (p *Plugin) rot18Cursor(ctx context.Context) int64 {
	s, err := p.st.getSettings(ctx)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(s[rot18CursorKey], 10, 64)
	return n
}

func (p *Plugin) setRot18Cursor(ctx context.Context, cursor int64) {
	if err := p.st.setSetting(ctx, rot18CursorKey, strconv.FormatInt(cursor, 10)); err != nil {
		p.reportErr(ctx, "usenet/rot18-cursor", err)
	}
}

func (p *Plugin) nextRot18(cfg Config) time.Time {
	return time.Now().Add(time.Duration(cfg.Rot18RepairIntervalMin) * time.Minute)
}
