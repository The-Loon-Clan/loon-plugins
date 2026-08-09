package roadmap

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/the-loon-clan/loon/schedule"
)

const (
	flowSnapshotIntervalMin = 15
	flowSnapshotKeepDays    = 7
)

// flowSnapshots runs "Flow Snapshots": a periodic JSONB snapshot of the /flow
// graph for rollback, pruning automatic snapshots older than the keep window.
// It needs no host seams at all — the flow store is plugin-owned.
type flowSnapshots struct {
	flow FlowStore
	job  *schedule.JobInfo
	mu   sync.Mutex
}

func newFlowSnapshots(flow FlowStore) *flowSnapshots {
	s := &flowSnapshots{flow: flow}
	s.job = schedule.RegisterJob("Flow Snapshots",
		"Periodic JSONB snapshot of the /flow graph for rollback. "+
			"Keeps "+fmt.Sprintf("%d", flowSnapshotKeepDays)+" days of "+
			"automatic snapshots; manual / pre-restore ones are kept "+
			"indefinitely. Cheap operation — graph fits in a few KB.")
	s.job.IntervalMin = flowSnapshotIntervalMin
	// Background, not the trigger's request context: an /admin/jobs POST
	// must not cancel the run it fired.
	s.job.SetTrigger(func() { go s.run(context.Background()) })
	return s
}

// start launches the loop. Bare ServiceLoop: the host's global hooks provide
// the admin interval override, and ctx is the root context, so the loop dies
// at SIGTERM.
func (s *flowSnapshots) start(ctx context.Context) {
	go schedule.ServiceLoop(ctx, s.job,
		5*time.Minute,
		time.Duration(flowSnapshotIntervalMin)*time.Minute,
		s.run)
}

func (s *flowSnapshots) run(ctx context.Context) {
	if !s.mu.TryLock() {
		s.job.Log("Skipped: another run is in progress")
		return
	}
	defer s.mu.Unlock()
	if s.job.IsPaused() {
		return
	}
	s.job.SetRunning()
	start := time.Now()

	g, err := s.flow.GetFlowGraph(ctx)
	if err != nil {
		s.job.SetError("get-graph: " + err.Error())
		return
	}
	payload, err := json.Marshal(g)
	if err != nil {
		s.job.SetError("marshal: " + err.Error())
		return
	}
	if _, err := s.flow.CreateFlowSnapshot(ctx, payload, len(g.Nodes), len(g.Edges), "periodic"); err != nil {
		s.job.SetError("create-snapshot: " + err.Error())
		return
	}
	pruned, err := s.flow.PruneFlowSnapshots(ctx, time.Now().AddDate(0, 0, -flowSnapshotKeepDays))
	if err != nil {
		s.job.Log("prune failed (continuing): %v", err)
	}
	s.job.Log("Snapshot %d nodes / %d edges in %s; pruned %d old",
		len(g.Nodes), len(g.Edges), time.Since(start).Round(time.Millisecond), pruned)
	// ServiceLoop announces the true next run (with any admin override)
	// right after this returns; the default is the manual-trigger
	// placeholder.
	s.job.SetIdle(time.Now().Add(time.Duration(flowSnapshotIntervalMin) * time.Minute))
}
