package usenet

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

type fakeSiteState struct{ mode core.SiteMode }

func (f fakeSiteState) Mode(context.Context) core.SiteMode { return f.mode }
func (f fakeSiteState) Reason(context.Context) string      { return "upgrading the database" }

func TestMayWriteFollowsSiteMode(t *testing.T) {
	cases := []struct {
		mode core.SiteMode
		want bool
	}{
		{core.SiteNormal, true},
		{core.SiteReadOnly, false},
		{core.SiteMaintenance, false},
		{"", true}, // unset is normal, per core.SiteMode.Writable
	}
	for _, c := range cases {
		p := &Plugin{core: &core.Core{SiteState: fakeSiteState{mode: c.mode}}}
		if got := p.mayWrite(context.Background(), nil); got != c.want {
			t.Errorf("mode %q: mayWrite = %v, want %v", c.mode, got, c.want)
		}
	}
}

// FAIL OPEN is a deliberate choice, not an oversight, so it is pinned: a host that
// never wired SiteState must get a working crawler rather than one that silently
// indexes nothing. The backstop for that case is pg18_migrate.sh's quiesce check,
// which measures pg_stat_user_tables instead of trusting any flag.
func TestMayWriteFailsOpenWithNoSiteState(t *testing.T) {
	for name, p := range map[string]*Plugin{
		"nil core":      {core: nil},
		"no site state": {core: &core.Core{}},
	} {
		if !p.mayWrite(context.Background(), nil) {
			t.Errorf("%s: mayWrite = false, want true (fail open)", name)
		}
	}
}

// Methods spawned with `go p.X(...)` that legitimately do NOT write to Postgres.
// Anything not on this list and not guarded fails the test below, which is the
// point: a new background goroutine has to be classified, not remembered.
var nonWritingGoroutines = map[string]string{
	"publishTelemetry": "publishes counters to Redis + the plugin's own status; no PG writes",
	"renewLease":       "lease renewal writes, but it must keep running or the fleet loses its term",
	"renewTermLease":   "same as renewLease",
}

// TestEveryPassAsksTheWriteGate is the mechanism that replaces remembering.
//
// The bug this exists to prevent already happened: all six pipeline jobs carried
// MarkWrites() and the comment above them said they held back while the site was
// read-only, and none of them did, because this plugin dispatches from its own
// loops and never touches ServiceLoop. A reviewer reading the registration would
// have agreed it was correct. So correctness here cannot live in a comment.
//
// Any *Plugin method started as a goroutine must either ask p.mayWrite near the
// top of its body, or be listed in nonWritingGoroutines with a reason.
func TestEveryPassAsksTheWriteGate(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	spawned := map[string]bool{}         // methods started with `go p.X(...)`
	bodies := map[string]*ast.FuncDecl{} // every method on *Plugin

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.FuncDecl:
					if node.Recv != nil && node.Name != nil {
						bodies[node.Name.Name] = node
					}
				case *ast.GoStmt:
					if call, ok := node.Call.Fun.(*ast.SelectorExpr); ok {
						if ident, ok := call.X.(*ast.Ident); ok && ident.Name == "p" {
							spawned[call.Sel.Name] = true
						}
					}
				}
				return true
			})
		}
	}

	if len(spawned) == 0 {
		t.Fatal("found no `go p.X(...)` dispatches — the scan is broken, not the code")
	}

	for name := range spawned {
		if reason, ok := nonWritingGoroutines[name]; ok {
			if reason == "" {
				t.Errorf("%s is exempt with no reason given", name)
			}
			continue
		}
		fn, ok := bodies[name]
		if !ok || fn.Body == nil {
			continue // spawned on another receiver, or declared elsewhere
		}
		if !asksTheGateEarly(fn) {
			t.Errorf("goroutine p.%s does not call p.mayWrite near the top of its body.\n"+
				"    Every pass must ask the read-only write gate, because this plugin has four\n"+
				"    different dispatch paths and only TriggerJob ever consulted it. If %s genuinely\n"+
				"    writes nothing to Postgres, add it to nonWritingGoroutines with a reason.",
				name, name)
		}
	}
}

// Early = within the first few statements, so the guard cannot be buried after the
// work has already begun.
func asksTheGateEarly(fn *ast.FuncDecl) bool {
	const maxStmts = 4
	for i, stmt := range fn.Body.List {
		if i >= maxStmts {
			return false
		}
		found := false
		ast.Inspect(stmt, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == "mayWrite" {
				found = true
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}
