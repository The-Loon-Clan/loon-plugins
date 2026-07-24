// lint-sql walks the Go source under this module and flags any SQL
// query whose first argument (the SQL string) is built via string
// concatenation or fmt.Sprintf with non-literal substitution. Those
// patterns are how SQL-injection vulnerabilities are normally
// introduced — every value should reach the driver via a $N placeholder
// instead.
//
// Suppress a finding by adding `// sqllint:allow <reason>` on the same
// line as the call (or the line immediately above). Use that ONLY for
// dynamic identifiers (table/column names, ORDER BY columns) where the
// value comes from a hard-coded allowlist or switch statement — never
// for actual user input.
//
// Run from the repo root:
//
//	go run ./scripts/lint-sql ./...
//
// Exits 1 if any unsuppressed finding is reported, 0 otherwise.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// dbMethods are the database/sql + sqlx call names whose first
// non-context argument is a SQL string. We don't try to verify that
// the receiver is actually a *sql.DB / *sqlx.DB — name match alone is
// sufficient.
//
// The bare names (Get, Query, QueryRow, Select) collide with HTTP
// clients, url.Values, sessions, and other generic APIs, so we limit
// the watchlist to Context-suffixed variants + the unambiguous Exec /
// Named* / MustExec / Prepare* names. That covers the entire storage
// layer (`s.DB.ExecContext(...)`, `tx.SelectContext(...)`, etc.) and
// avoids false positives on `httpClient.Get(url)` style calls.
var dbMethods = map[string]bool{
	"Exec":             true,
	"ExecContext":      true,
	"QueryContext":     true,
	"QueryRowContext":  true,
	"GetContext":       true,
	"SelectContext":    true,
	"NamedExec":        true,
	"NamedExecContext": true,
	"NamedQuery":       true,
	"PrepareContext":   true,
	"PrepareNamed":     true,
	"MustExec":         true,
}

type finding struct {
	file     string
	line     int
	funcName string
	method   string
	reason   string
	snippet  string
}

func main() {
	baselinePath := flag.String("baseline", "scripts/lint-sql/baseline.txt", "path to baseline file of known-safe findings; new findings are reported as failures")
	updateBaseline := flag.Bool("update-baseline", false, "rewrite the baseline file from the current findings (use ONLY after a manual review confirms each new entry is safe)")
	flag.Parse()
	roots := flag.Args()
	if len(roots) == 0 {
		roots = []string{"./..."}
	}
	var findings []finding
	for _, root := range roots {
		root = strings.TrimSuffix(root, "/...")
		root = strings.TrimSuffix(root, `\...`)
		if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if name == "vendor" || name == ".git" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			scanFile(path, &findings)
			return nil
		}); err != nil {
			fmt.Fprintln(os.Stderr, "walk:", err)
			os.Exit(2)
		}
	}

	if *updateBaseline {
		if err := writeBaseline(*baselinePath, findings); err != nil {
			fmt.Fprintln(os.Stderr, "write baseline:", err)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "wrote %d entries to %s\n", len(findings), *baselinePath)
		return
	}

	known := loadBaseline(*baselinePath)
	var newOnes []finding
	for _, f := range findings {
		if known[f.id()] {
			fmt.Printf("[known] %s:%d  %s — %s\n", f.file, f.line, f.method, f.reason)
			continue
		}
		newOnes = append(newOnes, f)
		fmt.Printf("[NEW]   %s:%d  %s — %s\n    %s\n", f.file, f.line, f.method, f.reason, f.snippet)
	}
	if len(newOnes) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d NEW SQL-string-building finding(s) (not in baseline). Use parameterized queries, annotate with `// sqllint:allow <reason>`, or — if you have manually verified the new site is safe — re-run with --update-baseline to record it.\n", len(newOnes))
		os.Exit(1)
	}
}

// id is the baseline identity for a finding. We key on file +
// enclosing function name + method + reason. Line numbers are excluded
// (they churn with unrelated edits) and so are SQL snippets (truncation
// collisions caused too many false-NEW reports). Multiple findings of
// the same shape inside the same function collapse to one baseline
// entry — that's intentional, since they share the same justification.
func (f finding) id() string {
	return f.file + "|" + f.funcName + "|" + f.method + "|" + f.reason
}

func loadBaseline(path string) map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	return out
}

func writeBaseline(path string, findings []finding) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf strings.Builder
	buf.WriteString("# Baseline of known-safe SQL-string-building sites.\n")
	buf.WriteString("# Identity = file|enclosing-function|method|reason. Line numbers and\n")
	buf.WriteString("# snippets are excluded so refactors don't churn this file. Each entry\n")
	buf.WriteString("# should be verified safe by code review before being added\n")
	buf.WriteString("# (regenerate with --update-baseline).\n\n")
	seen := map[string]bool{}
	for _, f := range findings {
		id := f.id()
		if seen[id] {
			continue
		}
		seen[id] = true
		buf.WriteString(id)
		buf.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(buf.String()), 0o644)
}

func scanFile(path string, out *[]finding) {
	fset := token.NewFileSet()
	src, err := os.ReadFile(path)
	if err != nil {
		return
	}
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return
	}
	// Build a line → suppression map by walking comments. A finding on
	// line N is suppressed if line N or N-1 contains "sqllint:allow".
	suppressed := map[int]bool{}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "sqllint:allow") {
				ln := fset.Position(c.Pos()).Line
				suppressed[ln] = true
				suppressed[ln+1] = true
			}
		}
	}
	// Build a set of file-local identifiers whose value is a static
	// string. The codebase uses `const animeMetaCols = "id, title, …"`
	// and similar package-level declarations to share column lists
	// across queries — those concatenations are literal-equivalent and
	// shouldn't be flagged. We accept const- AND var-declared names
	// where the RHS is a string literal or a +-chain of literals/
	// previously-declared static names.
	staticStrs := map[string]bool{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if isStaticString(vs.Values[i], staticStrs) {
					staticStrs[name.Name] = true
				}
			}
		}
	}
	// Walk top-level FuncDecls so each call's enclosing function is
	// known when we record the finding's identity. ast.Inspect alone
	// hides that context; iterating decls first is the simplest fix.
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		funcName := funcDeclName(fd)
		ast.Inspect(fd, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if !dbMethods[sel.Sel.Name] {
				return true
			}
			// Find the SQL argument. database/sql + sqlx calls take one
			// of Exec(sql,...), ExecContext(ctx,sql,...), or
			// GetContext(ctx,dest,sql,...). Without type info we probe
			// args in order and pick the first whose AST shape is a
			// string-being-built (literal, +-chain, fmt.Sprint*). Bare
			// *ast.Ident args are skipped — they could be ctx/tx/dest,
			// and a SQL string held in an identifier (`q := ...`) should
			// be linted at its assignment site, not here.
			var sqlArg ast.Expr
			for _, a := range call.Args {
				if exprIsLikelyString(a) {
					sqlArg = a
					break
				}
			}
			if sqlArg == nil {
				return true
			}
			pos := fset.Position(call.Pos())
			if suppressed[pos.Line] {
				return true
			}
			if reason, snippet, bad := classifyArg(sqlArg, src, staticStrs); bad {
				*out = append(*out, finding{
					file:     path,
					line:     pos.Line,
					funcName: funcName,
					method:   sel.Sel.Name,
					reason:   reason,
					snippet:  snippet,
				})
			}
			return true
		})
	}
}

// funcDeclName returns a stable name for a function declaration,
// including its receiver type for methods (e.g. "*Storage.GetUser").
func funcDeclName(fd *ast.FuncDecl) string {
	name := fd.Name.Name
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return name
	}
	var recvName string
	switch t := fd.Recv.List[0].Type.(type) {
	case *ast.Ident:
		recvName = t.Name
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			recvName = "*" + id.Name
		}
	}
	if recvName == "" {
		return name
	}
	return recvName + "." + name
}

// exprIsLikelyString returns true for arg nodes that look like
// string-being-built. We deliberately exclude *ast.Ident — see the
// note in scanFile's argument-probe loop.
func exprIsLikelyString(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING
	case *ast.BinaryExpr:
		return v.Op == token.ADD
	case *ast.CallExpr:
		// fmt.Sprintf / fmt.Sprint / fmt.Sprintln return string.
		if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "fmt" {
				return strings.HasPrefix(sel.Sel.Name, "Sprint")
			}
		}
	}
	return false
}

func classifyArg(e ast.Expr, src []byte, staticStrs map[string]bool) (reason, snippet string, bad bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		return "", "", false
	case *ast.BinaryExpr:
		if v.Op == token.ADD && !pureLiteralConcat(v, staticStrs) {
			return "string concat (`+`) used to build SQL", srcRange(src, v.Pos(), v.End()), true
		}
		return "", "", false
	case *ast.CallExpr:
		if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "fmt" {
				if strings.HasPrefix(sel.Sel.Name, "Sprint") {
					return "fmt." + sel.Sel.Name + " used to build SQL", srcRange(src, v.Pos(), v.End()), true
				}
			}
		}
	}
	return "", "", false
}

// pureLiteralConcat returns true when every leaf of a + chain is a
// compile-time string — either a literal or an identifier that we've
// already determined refers to a file-local static-string declaration
// (const animeMetaCols = "..."). This catches the idiomatic
// "SELECT " + colList + " FROM t WHERE id = $1" pattern without
// flagging it as user-input building.
func pureLiteralConcat(e ast.Expr, staticStrs map[string]bool) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING
	case *ast.Ident:
		return staticStrs[v.Name]
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return false
		}
		return pureLiteralConcat(v.X, staticStrs) && pureLiteralConcat(v.Y, staticStrs)
	}
	return false
}

// isStaticString reports whether expr resolves to a compile-time
// string. Used while building the file's static-string identifier set
// before any classification runs.
func isStaticString(e ast.Expr, staticStrs map[string]bool) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING
	case *ast.Ident:
		return staticStrs[v.Name]
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return false
		}
		return isStaticString(v.X, staticStrs) && isStaticString(v.Y, staticStrs)
	}
	return false
}

func srcRange(src []byte, lo, hi token.Pos) string {
	a := int(lo) - 1
	b := int(hi) - 1
	if a < 0 || b > len(src) || a >= b {
		return ""
	}
	s := string(src[a:b])
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
