package usenet

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// Every registered job must be REACHABLE without a human pressing a button.
//
// Registering a job and giving it a SetTrigger makes it appear on /admin/jobs
// and makes the manual trigger work — and that is exactly enough to look
// finished. Two jobs shipped that way on 2026-08-14: they ran once each when
// triggered by hand, reported sensible results, and then never ran again. The
// symptom is silent and easy to misread, because the job list shows them
// present and idle with a zero next_run, which is indistinguishable from "has
// not ticked yet".
//
// A job is reachable if it is on a Scheduler.RunLoop, or if some other code
// path dispatches it (NFO is deliberately dispatched from an idle crawl pass
// rather than a timer, because it should only spend connections the crawler
// does not want). This test allows either, and fails on neither.
func TestEveryRegisteredJobIsScheduledOrDispatched(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	// Collect: jobs given a trigger (p.xJob.SetTrigger), jobs put on a loop
	// (RunLoop(ctx, p.xJob, ...)), and every run* function referenced anywhere
	// outside its own declaration.
	triggered := map[string]bool{}
	looped := map[string]bool{}
	dispatched := map[string]bool{}

	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "SetTrigger":
					if inner, ok := sel.X.(*ast.SelectorExpr); ok {
						triggered[inner.Sel.Name] = true
					}
				case "RunLoop":
					// RunLoop(ctx, p.xJob, boot, interval, fn)
					if len(call.Args) >= 2 {
						if job, ok := call.Args[1].(*ast.SelectorExpr); ok {
							looped[job.Sel.Name] = true
						}
					}
				}
				return true
			})
			// A job whose run function is called from ordinary code (the NFO
			// pattern) is reachable too.
			//
			// The SetTrigger body must be EXCLUDED, and this is the whole
			// subtlety: every triggered job contains
			// `SetTrigger(func(){ go p.runX(...) })`, so counting that as a
			// dispatch makes the check vacuous — it passes for exactly the jobs
			// it is supposed to fail. Verified by deleting the RunLoop and
			// watching the first version of this test stay green.
			var skip []ast.Node
			ast.Inspect(f, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "SetTrigger" {
						for _, a := range call.Args {
							skip = append(skip, a)
						}
					}
				}
				return true
			})
			inSkip := func(n ast.Node) bool {
				for _, s := range skip {
					if n.Pos() >= s.Pos() && n.End() <= s.End() {
						return true
					}
				}
				return false
			}
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || !strings.HasPrefix(sel.Sel.Name, "run") {
					return true
				}
				if !inSkip(sel) {
					dispatched[sel.Sel.Name] = true
				}
				return true
			})
		}
	}

	if len(triggered) == 0 {
		t.Fatal("found no SetTrigger calls at all — the walker is broken, not the code")
	}

	// Map job field -> the run function it triggers, by naming convention, and
	// accept any of the three routes to reachability.
	unreachable := []string{}
	for job := range triggered {
		if looped[job] {
			continue
		}
		// nfoJob -> runNFO, junkProbeJob -> runJunkProbe, rot18Job -> runRot18Repair
		base := strings.TrimSuffix(job, "Job")
		found := false
		for fn := range dispatched {
			if strings.EqualFold(fn, "run"+base) {
				found = true
				break
			}
		}
		if !found {
			unreachable = append(unreachable, job)
		}
	}
	for _, job := range unreachable {
		t.Errorf("%s has a manual trigger but is on no RunLoop and nothing dispatches it — "+
			"it will run only when a human presses the button, and its interval setting does nothing", job)
	}
	t.Logf("%d job(s) triggerable, %d on a loop", len(triggered), len(looped))
}
