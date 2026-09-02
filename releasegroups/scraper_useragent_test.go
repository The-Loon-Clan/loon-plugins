package releasegroups

import (
	"strings"
	"testing"
)

// The scrape job panicked on every run with a nil pointer dereference, and
// nothing caught it: the User-Agent is built inside a job, in a process that
// wires only the worker leg, so no compiler check, no vet pass and no existing
// test ever executed the line. The stack was the only evidence, and it took a
// production error_logs row to find.
//
// These tests pin the two halves of the contract that was violated.

// TestScraperUserAgentSurvivesUnwiredHost is the regression proper. Before the
// fix, scraperUserAgent read the WEB-leg `deps` global; the scrape job runs in
// the worker, which calls SetJobDeps and never SetDeps, so that global is nil
// there by contract and calling a value-receiver method through it panicked.
//
// A User-Agent is not worth killing a scrape over, so the unwired case must
// degrade to a truthful fallback rather than crash.
func TestScraperUserAgentSurvivesUnwiredHost(t *testing.T) {
	restore := jobDeps
	t.Cleanup(func() { jobDeps = restore })

	jobDeps = nil // exactly what the worker process looked like

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("scraperUserAgent panicked with no job deps wired: %v", r)
		}
	}()

	got := scraperUserAgent()
	if got == "" {
		t.Fatal("scraperUserAgent returned empty")
	}
	if !strings.Contains(got, "release-group scraper") {
		t.Errorf("User-Agent lost its purpose string: %q", got)
	}
}

// TestScraperUserAgentNamesTheWorkerLegSite guards the reason the function
// exists at all: it must name THIS deployment, and it must read that name from
// the leg the job actually runs in. A test that set Deps.SiteName and passed
// would be re-testing the bug.
func TestScraperUserAgentNamesTheWorkerLegSite(t *testing.T) {
	restoreJob, restoreWeb := jobDeps, deps
	t.Cleanup(func() { jobDeps, deps = restoreJob, restoreWeb })

	// Web leg says one thing; worker leg says another. The job must report the
	// worker leg's answer, because that is the only one its process has.
	deps = &Deps{SiteName: "web-leg-name"}
	jobDeps = &JobDeps{SiteName: "worker-leg-name"}

	got := scraperUserAgent()
	if !strings.HasPrefix(got, "worker-leg-name/1.0") {
		t.Errorf("User-Agent = %q, want it to lead with the worker-leg site name", got)
	}
	if strings.Contains(got, "web-leg-name") {
		t.Errorf("User-Agent reached into the web leg: %q", got)
	}
}

// TestJobSiteNameFallsBackToHost pins the fallback shape shared with the web
// leg: a bare host is at least true, where a generic phrase is useless to
// whoever reads their server logs.
func TestJobSiteNameFallsBackToHost(t *testing.T) {
	restore := jobDeps
	t.Cleanup(func() { jobDeps = restore })

	jobDeps = &JobDeps{BaseURL: "https://example.test/some/path"}
	if got := jobSiteName(); got != "example.test" {
		t.Errorf("jobSiteName() = %q, want the BaseURL host", got)
	}

	jobDeps = &JobDeps{}
	if got := jobSiteName(); got != "this indexer" {
		t.Errorf("jobSiteName() with nothing set = %q, want the generic fallback", got)
	}
}
