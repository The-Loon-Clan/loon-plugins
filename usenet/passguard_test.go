package usenet

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// unguardedGoroutines are spawned methods that deliberately do NOT contain
// their own panics, with the reason. Keep this list short and argued.
var unguardedGoroutines = map[string]string{
	// Telemetry is a Redis publish loop with its own error handling and no
	// job to report against; a panic here is a bug in the publisher, and
	// containing it would leave the plugin reporting stale counters forever
	// while looking healthy. Loud is the right failure for a metrics writer.
	"publishTelemetry": "telemetry publisher: no job to report against, and silent staleness is worse than a crash",
	// The lease renewals must keep running or the fleet loses its term. They
	// are three lines of UPDATE with no parsing and no external input.
	"renewLease":     "lease renewal: must keep running or the fleet loses its term",
	"renewTermLease": "same as renewLease",
}

// TestEveryPassContainsPanics is the same mechanism as
// TestEveryPassAsksTheWriteGate, pointed at the other thing that escapes
// through this plugin's many dispatch routes.
//
// An unrecovered panic in ANY goroutine ends the whole process. loon's
// SetTriggerAsync protects the route through the host's /admin/jobs button —
// but that is ONE of at least five ways a pass starts here, and protecting
// only it produced the worse shape it was meant to remove: the same job,
// recovered when run from /admin/jobs and process-fatal when run from this
// plugin's own Jobs tab, which is the button an operator is more likely to
// press for these six.
//
// writegate.go already wrote the rule this follows: "Guarding those call sites
// means finding all four and remembering the fifth. The pass entry point is
// the one place every route converges, so the check goes there." Same routes,
// same convergence point, same enforcement — because a comment saying "every
// pass recovers" is exactly what was true of the write gate while none of them
// asked.
func TestEveryPassContainsPanics(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	spawned := map[string]bool{}
	bodies := map[string]*ast.FuncDecl{}

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
		if reason, ok := unguardedGoroutines[name]; ok {
			if reason == "" {
				t.Errorf("%s is exempt from panic containment with no reason given", name)
			}
			continue
		}
		fn, ok := bodies[name]
		if !ok || fn.Body == nil {
			continue // spawned on another receiver, or declared elsewhere
		}
		if !recoversFirst(fn) {
			t.Errorf("goroutine p.%s does not `defer p.recoverPass(...)` as its FIRST statement.\n"+
				"    A panic in a spawned pass ends the whole process, and this plugin starts its\n"+
				"    passes from at least five places — only one of which (the host's /admin/jobs\n"+
				"    button) is protected by loon. If %s genuinely must not contain its panics, add\n"+
				"    it to unguardedGoroutines with a reason.",
				name, name)
		}
	}
}

// recoversFirst requires the guard to be the FIRST statement, not merely an
// early one.
//
// Stricter than the write gate's within-the-first-four, and deliberately: a
// deferred recover only catches panics raised AFTER it is registered, so a
// guard placed below even the nil-ctx check leaves that check unprotected. The
// ordering is not style, it is the difference between working and not.
func recoversFirst(fn *ast.FuncDecl) bool {
	if len(fn.Body.List) == 0 {
		return false
	}
	def, ok := fn.Body.List[0].(*ast.DeferStmt)
	if !ok {
		return false
	}
	sel, ok := def.Call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel != nil && sel.Sel.Name == "recoverPass"
}
