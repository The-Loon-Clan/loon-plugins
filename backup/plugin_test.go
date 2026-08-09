package backup

import (
	"testing"

	"github.com/the-loon-clan/loon/schedule"
)

// The invariant loops() exists to make testable: every job this plugin
// REGISTERS must also be SCHEDULED. It was broken once and stayed broken
// silently — the index job had a Run button and an interval in /admin/jobs
// but no loop, so the inventory only advanced when someone pressed the
// button. The README promised this test for months before it existed; the
// 2026-08-09 consistency audit caught the phantom claim.
func TestEveryRegisteredJobIsScheduled(t *testing.T) {
	p := &Plugin{
		indexJob: schedule.RegisterJob("test-backup-index-invariant", "test"),
		dumpJob:  schedule.RegisterJob("test-backup-dump-invariant", "test"),
	}
	scheduled := map[*schedule.JobInfo]bool{}
	for _, l := range p.loops() {
		if l.job == nil {
			t.Fatal("loops() entry with a nil job — ServiceLoop would nil-panic at Start")
		}
		if l.run == nil {
			t.Fatalf("job %q scheduled with a nil run func", l.job.Name)
		}
		if l.interval <= 0 {
			t.Fatalf("job %q scheduled with a non-positive interval", l.job.Name)
		}
		scheduled[l.job] = true
	}
	for name, j := range map[string]*schedule.JobInfo{
		"index": p.indexJob,
		"dump":  p.dumpJob,
	} {
		if !scheduled[j] {
			t.Errorf("%s job is registered but never scheduled — it would only run from its Run button", name)
		}
	}
}
