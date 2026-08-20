// audit-sentinels finds ownership checks that a zero or empty sentinel could
// satisfy.
//
// THE BUG IT LOOKS FOR. User id 0 means two things at once in this codebase:
// the id a request carries when nobody is signed in, and the id reserved for
// the system (achievements refuses to grant a badge to user 0 on that ground).
// So `record.UserID == viewerID` is TRUE for an anonymous viewer whenever the
// record's owner is 0 — and most plugin routes mount behind Authenticate(),
// which lets anonymous requests through in the site's public access mode.
//
// It was found four times in two days, in four plugins, always by accident:
// playlists.owned, playlists.Show, comments.Delete (which used 0 to MEAN
// "staff", so 0 also meant "everyone"), and tickets.ticketVisibleTo. Each was
// correct in the code around it and wrong on the one input nobody passes by
// hand. That is four more times than a thing should be found by accident.
//
// TWO RULES:
//
//	widen  — SQL of the form `($3 = 0 OR user_id = $3)`, where one parameter
//	         carries both "match anyone" and an identity. This is the shape
//	         that removes anybody's comment. `(user_id = $2 OR $3)`, with the
//	         privilege as its own boolean, is the correct form and passes.
//	compare — a Go comparison between a stored owner and a viewer id, with no
//	         positivity guard and not going through pluginapi.OwnedBy or
//	         VisibleTo.
//
// Baselined like scripts/lint-sql: identity is file|function|rule, so line
// numbers do not churn it. A new finding fails; regenerate only after reading
// each entry.
//
//	go run ./scripts/audit-sentinels ./...
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
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// identityCol are the column names that name a person.
var identityCol = regexp.MustCompile(
	`\b(user_id|created_by|author_id|owner_id|actor_user_id|posted_by|` +
		`uploader_id|member_id|owner_user_id|author_user_id|recipient_id|sender_id)\b`)

// sentinelWiden is `$3 = 0 OR` — one parameter meaning both things.
var sentinelWiden = regexp.MustCompile(`\$(\d+)\s*=\s*0\s+OR\b`)

// boolWiden is `OR $3)` — the privilege as its own boolean. The correct shape.
var boolWiden = regexp.MustCompile(`OR\s+\$\d+\s*\)`)

// identityField are the Go struct fields that hold a stored owner.
var identityField = regexp.MustCompile(
	`^(UserID|CreatedBy|AuthorID|OwnerID|OwnerUserID|AuthorUserID|UploaderID|` +
		`MemberID|PosterID|ActorUserID)$`)

// viewerName are the identifiers a viewer id is usually held in. A comparison
// between two STORED ids is not what this is about.
var viewerName = regexp.MustCompile(
	`(?i)^(viewer|viewerid|userid|currentuserid|me|u|user|caller|callerid|byuser|actor)$`)

type finding struct {
	file, fn, rule, detail string
	line                   int
}

func (f finding) id() string { return f.file + "|" + f.fn + "|" + f.rule }

func main() {
	baselinePath := flag.String("baseline", "scripts/audit-sentinels/baseline.txt", "known-reviewed findings")
	update := flag.Bool("update-baseline", false, "rewrite the baseline (only after reading every new entry)")
	flag.Parse()
	roots := flag.Args()
	if len(roots) == 0 {
		roots = []string{"./..."}
	}

	var found []finding
	for _, root := range roots {
		root = strings.TrimSuffix(strings.TrimSuffix(root, "/..."), `\...`)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if n := d.Name(); n == ".git" || n == "vendor" || n == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				scan(filepath.ToSlash(path), &found)
			}
			return nil
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "walk:", err)
			os.Exit(2)
		}
	}

	if *update {
		if err := writeBaseline(*baselinePath, found); err != nil {
			fmt.Fprintln(os.Stderr, "write baseline:", err)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "wrote %d entries to %s\n", len(found), *baselinePath)
		return
	}

	known := loadBaseline(*baselinePath)
	var fresh []finding
	for _, f := range found {
		if known[f.id()] {
			continue
		}
		fresh = append(fresh, f)
		fmt.Printf("[NEW] %s:%d  %s — %s\n      %s\n", f.file, f.line, f.fn, f.rule, f.detail)
	}
	if len(fresh) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d new sentinel finding(s). Use pluginapi.OwnedBy / VisibleTo, "+
			"or pass privilege as its own boolean parameter. If a site is genuinely safe, read it, "+
			"say why in the commit, and re-run with --update-baseline.\n", len(fresh))
		os.Exit(1)
	}
	fmt.Printf("sentinels: %d known, no new findings\n", len(known))
}

func scan(path string, out *[]finding) {
	fset := token.NewFileSet()
	src, err := os.ReadFile(path)
	if err != nil {
		return
	}
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return
	}
	suppressed := map[int]bool{}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "sentinel:allow") {
				ln := fset.Position(c.Pos()).Line
				suppressed[ln], suppressed[ln+1] = true, true
			}
		}
	}

	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		name := funcName(fd)
		ast.Inspect(fd, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BasicLit:
				if v.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(v.Value)
				if err != nil {
					s = v.Value
				}
				if !identityCol.MatchString(s) || !sentinelWiden.MatchString(s) || boolWiden.MatchString(s) {
					return true
				}
				line := fset.Position(v.Pos()).Line
				if suppressed[line] {
					return true
				}
				*out = append(*out, finding{
					file: path, line: line, fn: name, rule: "widen",
					detail: "one parameter carries both `match anyone` and an identity; " +
						"0 means staff AND means nobody is signed in",
				})
			case *ast.BinaryExpr:
				if v.Op != token.EQL && v.Op != token.NEQ {
					return true
				}
				owner, viewer, ok := ownerViewer(v)
				if !ok {
					return true
				}
				// A guard in the SAME expression is the common correct form:
				//
				//	viewerID != 0 && r.UserID == viewerID
				//
				// Flagging those would be flagging the fix, and an audit that
				// reports its own remedy gets baselined wholesale.
				if guarded(fd, v, viewer, owner) {
					return true
				}
				line := fset.Position(v.Pos()).Line
				if suppressed[line] {
					return true
				}
				*out = append(*out, finding{
					file: path, line: line, fn: name, rule: "compare",
					detail: fmt.Sprintf("%s vs %s with no positivity guard — "+
						"use pluginapi.OwnedBy", owner, viewer),
				})
			}
			return true
		})
	}
}

// guarded reports whether the viewer identifier is checked against zero
// anywhere in the smallest enclosing boolean expression, or in an if-statement
// guarding the block the comparison sits in.
//
// Deliberately generous. A false NEGATIVE here costs one missed site that a
// reader can still find; a false POSITIVE costs trust in the whole audit, and
// an audit nobody trusts gets its baseline regenerated without being read.
func guarded(fd *ast.FuncDecl, target *ast.BinaryExpr, names ...string) bool {
	// The bare identifier, so `user.ID` matches a guard written on `user.ID`
	// and `viewerID` matches one on `viewerID`.
	found := false
	var walk func(n ast.Node) bool
	walk = func(n ast.Node) bool {
		if found || n == nil {
			return false
		}
		be, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		if isZeroGuard(be, names...) {
			found = true
			return false
		}
		return true
	}

	// Any enclosing expression or statement that CONTAINS the comparison.
	var stack []ast.Node
	ast.Inspect(fd, func(n ast.Node) bool {
		if found {
			return false
		}
		if n == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return true
		}
		if n == target {
			// Walk every ancestor looking for a guard beside us.
			for _, anc := range stack {
				switch a := anc.(type) {
				case *ast.BinaryExpr:
					ast.Inspect(a, walk)
				case *ast.IfStmt:
					if a.Cond != nil {
						ast.Inspect(a.Cond, walk)
					}
				}
			}
			return false
		}
		stack = append(stack, n)
		return true
	})
	return found
}

// isZeroGuard matches `x != 0`, `x > 0`, `x <= 0`, `x < 1` and the mirrored
// forms, for any of the names given.
//
// EITHER side counts. A guard on the owner is as good as one on the viewer: a
// stored owner proven non-zero cannot match a viewer of zero, and on a `!=`
// comparison it resolves in the safe direction. tracker's announce handler is
// written that way, and it is not wrong.
func isZeroGuard(be *ast.BinaryExpr, names ...string) bool {
	switch be.Op {
	case token.NEQ, token.GTR, token.LEQ, token.LSS, token.EQL, token.GEQ:
	default:
		return false
	}
	name := func(e ast.Expr) string { return render(e) }
	isNum := func(e ast.Expr) bool {
		bl, ok := e.(*ast.BasicLit)
		return ok && bl.Kind == token.INT && (bl.Value == "0" || bl.Value == "1")
	}
	for _, want := range names {
		if name(be.X) == want && isNum(be.Y) {
			return true
		}
		if name(be.Y) == want && isNum(be.X) {
			return true
		}
	}
	return false
}

// ownerViewer reports whether a binary expression compares a stored owner field
// against something that looks like a viewer id.
func ownerViewer(v *ast.BinaryExpr) (owner, viewer string, ok bool) {
	fieldName := func(e ast.Expr) (string, bool) {
		sel, is := e.(*ast.SelectorExpr)
		if !is {
			return "", false
		}
		if !identityField.MatchString(sel.Sel.Name) {
			return "", false
		}
		return render(sel), true
	}
	viewerish := func(e ast.Expr) (string, bool) {
		switch t := e.(type) {
		case *ast.Ident:
			if viewerName.MatchString(t.Name) {
				return t.Name, true
			}
		case *ast.SelectorExpr:
			// user.ID, u.ID, viewer.ID
			if t.Sel.Name == "ID" {
				if id, is := t.X.(*ast.Ident); is && viewerName.MatchString(id.Name) {
					return render(t), true
				}
			}
		}
		return "", false
	}
	if o, is := fieldName(v.X); is {
		if w, is2 := viewerish(v.Y); is2 {
			return o, w, true
		}
	}
	if o, is := fieldName(v.Y); is {
		if w, is2 := viewerish(v.X); is2 {
			return o, w, true
		}
	}
	return "", "", false
}

func render(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return render(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + render(t.X)
	case *ast.IndexExpr:
		return render(t.X) + "[…]"
	case *ast.CallExpr:
		return render(t.Fun) + "()"
	}
	return "?"
}

func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	return render(fd.Recv.List[0].Type) + "." + fd.Name.Name
}

func loadBaseline(path string) map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			out[line] = true
		}
	}
	return out
}

func writeBaseline(path string, found []finding) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Ownership comparisons reviewed and found safe.\n")
	b.WriteString("# Identity = file|function|rule. Line numbers are excluded so a\n")
	b.WriteString("# refactor does not churn this file.\n")
	b.WriteString("#\n")
	b.WriteString("# An entry here is a claim that the site cannot be reached with a viewer\n")
	b.WriteString("# id of 0 — usually because both sides are STORED ids, or the value came\n")
	b.WriteString("# from a route behind RequireUser. Read it before you add it.\n\n")
	ids := make([]string, 0, len(found))
	seen := map[string]bool{}
	for _, f := range found {
		if !seen[f.id()] {
			seen[f.id()] = true
			ids = append(ids, f.id())
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		b.WriteString(id + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
