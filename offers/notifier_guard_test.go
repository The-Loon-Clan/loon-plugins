package offers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every optional notifier must be nil-checked against the field it CALLS.
//
// Deps lets a host wire some notifiers and not others — that is the contract,
// not an oversight. Three call sites guarded on deps.NotifyRequest and then
// invoked NotifyClaimed, NotifyDelivered and NotifyFailed. A host that wired
// only NotifyRequest would nil-func panic on the first claim, and because two
// of those calls are inside a bare `go func`, nothing recovers it: the panic
// takes down the whole web process, not the request.
//
// It was latent only because ameNZB happens to wire all four
// (indexer-site/cmd/offers_wiring.go). "Latent" is not a property of the code,
// it is a property of the one consumer that exists today, so it is pinned here
// instead of being left to the next host to discover.
//
// Checked structurally rather than by exercising the handlers: the failure is a
// missing guard, and a test that called them would need every handler's
// scaffolding while still only covering the paths it happened to drive.
func TestOptionalNotifiersAreGuardedOnTheFieldTheyCall(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()

	type call struct {
		field string
		pos   token.Position
	}
	var unguarded []call
	var checked int

	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		// Walk with a stack so each call knows which if-conditions enclose it.
		// A closure body inherits the guards around the `go func(...)` that
		// created it, which is exactly the shape here.
		var guards []string
		var visit func(n ast.Node) bool
		visit = func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.IfStmt:
				cond := exprText(src, fset, v.Cond)
				guards = append(guards, cond)
				ast.Inspect(v.Body, visit)
				guards = guards[:len(guards)-1]
				// The else branch is NOT covered by this condition.
				if v.Else != nil {
					saved := guards
					guards = nil
					ast.Inspect(v.Else, visit)
					guards = saved
				}
				return false

			case *ast.CallExpr:
				sel, ok := v.Fun.(*ast.SelectorExpr)
				if !ok || !strings.HasPrefix(sel.Sel.Name, "Notify") {
					return true
				}
				// Only fields reached through a deps value. A method named
				// Notify* on something else is not part of this contract.
				recv := exprText(src, fset, sel.X)
				if !strings.HasSuffix(recv, "deps") && !strings.HasSuffix(recv, "Deps") {
					return true
				}
				checked++
				for _, g := range guards {
					if strings.Contains(g, sel.Sel.Name) {
						return true
					}
				}
				unguarded = append(unguarded, call{sel.Sel.Name, fset.Position(v.Pos())})
				return true
			}
			return true
		}
		ast.Inspect(f, visit)
	}

	if checked == 0 {
		t.Fatal("found no deps.Notify* call sites at all — the walker is broken, " +
			"not the code, and a green result here would mean nothing")
	}
	for _, c := range unguarded {
		t.Errorf("%s: deps.%s is called without a nil check on deps.%s — "+
			"a host that does not wire it panics here, unrecovered if this is in a goroutine",
			c.pos, c.field, c.field)
	}
	t.Logf("checked %d optional-notifier call site(s)", checked)
}

func exprText(src []byte, fset *token.FileSet, e ast.Expr) string {
	start := fset.Position(e.Pos()).Offset
	end := fset.Position(e.End()).Offset
	if start < 0 || end > len(src) || start >= end {
		return ""
	}
	return string(src[start:end])
}
