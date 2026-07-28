package usenet

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"
)

// The census exists to tell three failure modes apart. These pin that each one
// is actually distinguishable from the numbers, because the whole point is that
// they were indistinguishable before.
func TestCensusSeparatesTheThreeFailureModes(t *testing.T) {
	// 1. Redis is destroying keys to stay under maxmemory: evictions climbing.
	evicting := censusRow{EvictedKeys: 4_120_000, EvictedDelta: 8_400,
		MemUsedBytes: 8 << 30, MemMaxBytes: 8 << 30, MaxMemoryPolicy: "allkeys-lru"}
	if evicting.EvictedDelta == 0 || !evicting.EvictionRisk() {
		t.Error("an evicting Redis must read as both climbing and at risk")
	}
	if got := evicting.MemPct(); got < 99 {
		t.Errorf("memory at the ceiling reads %.0f%%", got)
	}

	// 2. The queue is deeper than the draw: completed work waits its turn.
	starved := censusRow{ReadyDepth: 41_000, Sampled: 500, LiveCandidates: 500}
	if !starved.Starved() {
		t.Error("41,000 waiting against a 500 draw must read as starved")
	}
	if starved.EvictedDelta != 0 || starved.FossilDropped != 0 {
		t.Error("starvation alone must not look like eviction or expiry")
	}

	// 3. Entries expired before their turn: fossils, with nothing else wrong.
	fossils := censusRow{ReadyDepth: 900, Sampled: 500, LiveCandidates: 460, FossilDropped: 40}
	if fossils.FossilDropped == 0 || fossils.EvictedDelta != 0 {
		t.Error("expiry must be visible without implying eviction")
	}

	// Healthy: nothing waiting, nothing lost, no eviction risk.
	healthy := censusRow{ReadyDepth: 12, Sampled: 12, LiveCandidates: 12, MaxMemoryPolicy: "noeviction"}
	if healthy.Starved() || healthy.EvictionRisk() || healthy.FossilDropped != 0 {
		t.Error("a healthy pass must trip none of the three signals")
	}
}

// An unbounded Redis has no ceiling to be at. Reporting 0% would read as
// "empty", which is the opposite of the truth and would send a diagnosis the
// wrong way — so it is explicitly negative and the template renders it as text.
func TestUnboundedMemoryIsNotZeroPercent(t *testing.T) {
	if got := (censusRow{MemUsedBytes: 5 << 30, MemMaxBytes: 0}).MemPct(); got >= 0 {
		t.Errorf("unbounded memory reported as %.0f%%, want a negative sentinel", got)
	}
	if got := (censusRow{MemUsedBytes: 1 << 30, MemMaxBytes: 4 << 30}).MemPct(); got != 25 {
		t.Errorf("MemPct = %v, want 25", got)
	}
}

// noeviction is the one policy that cannot silently destroy staged work: it
// refuses the write, which surfaces as a reported error. Every other policy
// deletes quietly, which is what made this invisible for a day.
func TestOnlyNoevictionIsSafe(t *testing.T) {
	for _, policy := range []string{"allkeys-lru", "allkeys-lfu", "volatile-ttl", "allkeys-random"} {
		if !(censusRow{MaxMemoryPolicy: policy}).EvictionRisk() {
			t.Errorf("%q silently deletes keys but reads as safe", policy)
		}
	}
	if (censusRow{MaxMemoryPolicy: "noeviction"}).EvictionRisk() {
		t.Error("noeviction must not read as a risk")
	}
	// Unknown/unreadable policy must not raise a false alarm.
	if (censusRow{MaxMemoryPolicy: ""}).EvictionRisk() {
		t.Error("an unread policy must not claim risk")
	}
}

// candidateStats.Starved drives a log line during the pass, before any row
// exists, so it must agree with the census row rendered afterwards.
func TestDrawAndRowAgreeOnStarvation(t *testing.T) {
	draw := candidateStats{ReadyDepth: 41_000, Sampled: 500}
	row := censusRow{ReadyDepth: draw.ReadyDepth, Sampled: draw.Sampled}
	if draw.Starved() != row.Starved() {
		t.Error("the pass log and the census disagree about starvation")
	}
}

// The card is the whole deliverable — the numbers existing in a table nobody
// reads would leave the next diagnosis exactly where this one started.
func TestStagingHealthCardRenders(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 29, 14, 3, 9, 0, time.UTC)
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "jobs.html", map[string]any{
		"Jobs":   []jobPaneVM{{crawlerJobVM: crawlerJobVM{Name: "Usenet Builder", Status: "idle"}, Slug: "builder", Short: "Builder"}},
		"Schema": "024_staging_census.sql", "Pending": []pendingSet{}, "ReadyGroups": int64(0),
		"Census": []censusRow{
			{At: at, ReadyDepth: 41000, Sampled: 500, LiveCandidates: 463, FossilDropped: 37,
				PendingSets: 15, MemUsedBytes: 8 << 30, MemMaxBytes: 8 << 30,
				EvictedKeys: 4120000, EvictedDelta: 8400, ExpiredKeys: 900, ExpiredDelta: 12,
				MaxMemoryPolicy: "allkeys-lru"},
			{At: at.Add(-2 * time.Minute), ReadyDepth: 12, Sampled: 12, LiveCandidates: 12,
				MemUsedBytes: 1 << 30, MemMaxBytes: 4 << 30, MaxMemoryPolicy: "noeviction"},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Staging health",
		"024_staging_census.sql", // the plugin deploy marker
		"allkeys-lru",
		"may DELETE staged releases", // the warning that names the danger
		"41000", "500",               // ready / drew
		// The fossil cell specifically. A bare "37" would be a weak assertion
		// and a bare "40" was an actively vacuous one: it matched the "+8400"
		// delta elsewhere on the page, so the column could be deleted and the
		// test still passed. Mutation testing caught that.
		`class="text-end small text-danger">37</td>`,
		"4120000", "+8400", // cumulative AND the delta that matters
		"100%",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("staging health card missing %q", want)
		}
	}
}

// With no samples the card must vanish rather than render an empty shell that
// reads as "measured, nothing wrong".
func TestStagingHealthCardHiddenWithoutSamples(t *testing.T) {
	tmpl, err := template.ParseFS(viewFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "jobs.html", map[string]any{
		"Jobs": []jobPaneVM{{crawlerJobVM: crawlerJobVM{Name: "Usenet Builder"}, Slug: "builder", Short: "Builder"}}, "Pending": []pendingSet{}, "ReadyGroups": int64(0),
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(buf.String(), "Staging health") {
		t.Error("card rendered with no samples")
	}
}
