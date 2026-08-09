package feeds

import (
	"sync"
	"time"

	lpapi "github.com/the-loon-clan/loon-plugins/pluginapi"
)

// statusBook is the importer's operational memory, published as the
// feeds.status capability and read by the host's GET /ops/feeds. It records
// what the last poll of each source did and how the last run's items were
// judged — the questions ("did Nyaa stop working", "why did nothing get
// created") that /admin/jobs cannot answer.
type statusBook struct {
	mu      sync.Mutex
	sources []lpapi.FeedsSource
	idx     map[string]int
	lastRun *time.Time
	runErr  string
	totals  lpapi.FeedsTotals
}

func newStatusBook(nekoBTEnabled bool) *statusBook {
	b := &statusBook{idx: map[string]int{}}
	for _, s := range []lpapi.FeedsSource{
		{Source: "nyaa", Enabled: true},
		{Source: "anirena", Enabled: true},
		{Source: "tokyotosho", Enabled: true},
		{Source: "nekobt", Enabled: nekoBTEnabled},
	} {
		b.idx[s.Source] = len(b.sources)
		b.sources = append(b.sources, s)
	}
	return b
}

func (b *statusBook) pollOK(source string, items int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	i, ok := b.idx[source]
	if !ok {
		return
	}
	now := time.Now()
	b.sources[i].LastPollAt = &now
	b.sources[i].LastOKAt = &now
	b.sources[i].LastItems = items
	b.sources[i].LastError = ""
	b.sources[i].LastErrorAt = nil
}

func (b *statusBook) pollFailed(source string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	i, ok := b.idx[source]
	if !ok {
		return
	}
	now := time.Now()
	b.sources[i].LastPollAt = &now
	b.sources[i].LastError = err.Error()
	b.sources[i].LastErrorAt = &now
}

// runFinished closes out one import run. runErr is empty on success; a run
// that failed before judging items keeps the previous totals visible rather
// than zeroing them, so a transient failure doesn't erase the last good
// numbers an operator is comparing against.
func (b *statusBook) runFinished(totals *lpapi.FeedsTotals, runErr string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.lastRun = &now
	b.runErr = runErr
	if totals != nil {
		b.totals = *totals
	}
}

// FeedsStatus implements lpapi.FeedsStatus. The sources slice is copied under
// the lock; the *time.Time fields are replaced (never mutated in place) by the
// writers above, so sharing the pointed-to values is race-free.
func (b *statusBook) FeedsStatus() lpapi.FeedsSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	src := make([]lpapi.FeedsSource, len(b.sources))
	copy(src, b.sources)
	return lpapi.FeedsSnapshot{
		LastRunAt:    b.lastRun,
		LastRunError: b.runErr,
		Totals:       b.totals,
		Sources:      src,
	}
}

var _ lpapi.FeedsStatus = (*statusBook)(nil)
