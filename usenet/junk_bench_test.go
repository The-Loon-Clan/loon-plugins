package usenet

import (
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// Per-rule cost against REAL subjects.
//
// The literal pre-filter turned out to gate only one shipped rule, which said
// the 42% of worker CPU the junk engine burns is not sitting where a cheap
// necessary-substring check can reach it. This measures where it actually
// sits, so the next change is aimed rather than hopeful.
//
// The corpus is a sample of production subjects (usenet.subject_corpus, the
// instrument added for exactly this kind of question). Run it with:
//
//	go test ./usenet -run TestJunkRuleCostRanking -v
func loadCorpus(t testing.TB) []string {
	blob, err := os.ReadFile("testdata/corpus.txt")
	if err != nil {
		t.Skipf("no corpus staged: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(blob), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		t.Skip("corpus is empty")
	}
	return out
}

func TestJunkRuleCostRanking(t *testing.T) {
	subjects := loadCorpus(t)
	m, err := loadEmbeddedJunkMatcher()
	if err != nil {
		t.Fatal(err)
	}

	// Pre-strip once, as the real path does, so the measurement is of rule
	// cost and not of stripAllMarkers.
	stripped := make([]string, len(subjects))
	for i, s := range subjects {
		stripped[i] = stripAllMarkers(s)
	}

	type row struct {
		name  string
		dur   time.Duration
		hits  int
		gated bool
	}
	var rows []row
	const reps = 40

	for _, r := range m.rules {
		start := time.Now()
		hits := 0
		for rep := 0; rep < reps; rep++ {
			for i, s := range subjects {
				in := s
				if r.params.LightInput {
					in = stripped[i]
				}
				if r.re != nil {
					if !r.gate.pass(in) {
						continue
					}
					if r.re.MatchString(in) && gatesPass(in, r.params) {
						hits++
					}
					continue
				}
				if runJunkHeuristic(r.heuristic, in, r.params) {
					hits++
				}
			}
		}
		rows = append(rows, row{r.name, time.Since(start), hits / reps, len(r.gate.Any) > 0})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].dur > rows[j].dur })

	var total time.Duration
	for _, r := range rows {
		total += r.dur
	}
	t.Logf("%d subjects x %d reps — total %s across %d rules", len(subjects), reps, total, len(rows))
	t.Logf("%-34s %10s %7s %6s %s", "rule", "time", "share", "hits", "gated")
	for _, r := range rows {
		if r.dur*100/total == 0 && r.dur < total/50 {
			continue // tail noise
		}
		t.Logf("%-34s %10s %6d%% %6d %v",
			r.name, r.dur.Round(time.Microsecond), r.dur*100/total, r.hits, r.gated)
	}
}

// BenchmarkJunkMatch is the whole engine on real subjects — the number to
// watch when changing rule evaluation.
func BenchmarkJunkMatch(b *testing.B) {
	subjects := loadCorpus(b)
	m, err := loadEmbeddedJunkMatcher()
	if err != nil {
		b.Fatal(err)
	}
	stripped := make([]string, len(subjects))
	for i, s := range subjects {
		stripped[i] = stripAllMarkers(s)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := i % len(subjects)
		_ = m.match(subjects[s], stripped[s], 0)
	}
}

// BenchmarkStripAllMarkers isolates the second-biggest slice of parseOverviews
// (17% of the worker) from the junk engine's 42%.
func BenchmarkStripAllMarkers(b *testing.B) {
	subjects := loadCorpus(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = stripAllMarkers(subjects[i%len(subjects)])
	}
}
