package usenet

import "testing"

// Admin handlers run in the WEB process, where the job pointers are nil. This
// panicked the settings page: the reset had already committed, so the operator
// saw a 500 for work that succeeded. The zero-value Plugin is exactly the
// hazard — no core, no registered jobs.
func TestLogActionSurvivesMissingJobAndLogger(t *testing.T) {
	var p Plugin // crawlJob nil, core nil — the web process, at its emptiest
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("logAction panicked with no job and no logger: %v", r)
		}
	}()
	p.logAction("%s: reset %d -> %d", "alt.binaries.example", 100, 50)
}
