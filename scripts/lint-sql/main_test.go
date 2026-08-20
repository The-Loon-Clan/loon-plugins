package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This linter is what keeps SQL in this repo constant-only, and until now
// nothing checked the checker. Both directions matter and the second is the one
// that kills a linter: a rule that flags ordinary safe code gets baselined
// wholesale or switched off, and then the first direction stops mattering too.

// scan writes src to a temporary .go file and runs the scanner over it.
func scan(t *testing.T, src string) []finding {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.go")
	if err := os.WriteFile(path, []byte("package fixture\n\n"+src), 0o644); err != nil {
		t.Fatal(err)
	}
	var out []finding
	scanFile(path, &out)
	return out
}

func mustFlag(t *testing.T, name, src string) finding {
	t.Helper()
	got := scan(t, src)
	if len(got) != 1 {
		t.Fatalf("%s: got %d findings, want 1\n%s", name, len(got), src)
	}
	return got[0]
}

func mustNotFlag(t *testing.T, name, src string) {
	t.Helper()
	if got := scan(t, src); len(got) != 0 {
		t.Errorf("%s: flagged safe code — %q\n%s", name, got[0].reason, src)
	}
}

// ── what it must catch ──────────────────────────────────────────────

func TestCatchesConcatenatedSQL(t *testing.T) {
	f := mustFlag(t, "concat", `
func del(tx T, id string) {
	tx.ExecContext(ctx, "DELETE FROM users WHERE id = " + id)
}
`)
	if !strings.Contains(f.reason, "concat") {
		t.Errorf("reason = %q, want it to name the concatenation", f.reason)
	}
	if f.method != "ExecContext" || f.funcName != "del" {
		t.Errorf("identity = %s/%s, want del/ExecContext", f.funcName, f.method)
	}
}

func TestCatchesSprintfSQL(t *testing.T) {
	f := mustFlag(t, "sprintf", `
func find(tx T, name string) {
	tx.GetContext(ctx, &dest, fmt.Sprintf("SELECT * FROM users WHERE name = '%s'", name))
}
`)
	if !strings.Contains(f.reason, "Sprintf") {
		t.Errorf("reason = %q, want it to name Sprintf", f.reason)
	}
}

// TestCatchesEveryWatchedMethod. A method missing from the watchlist is a hole
// with no symptom — the query is never looked at and the linter passes.
func TestCatchesEveryWatchedMethod(t *testing.T) {
	for method := range dbMethods {
		src := `
func q(tx T, v string) {
	tx.` + method + `(ctx, "SELECT 1 WHERE x = " + v)
}
`
		if got := scan(t, src); len(got) != 1 {
			t.Errorf("%s: got %d findings, want 1", method, len(got))
		}
	}
}

// TestCatchesItInsideAClosure — a query built inside a WithTx callback is the
// normal shape in this repo, so walking only the top level would miss almost
// everything.
func TestCatchesItInsideAClosure(t *testing.T) {
	mustFlag(t, "closure", `
func del(id string) {
	db.WithTx(ctx, func(tx *sqlx.Tx) error {
		return tx.ExecContext(ctx, "DELETE FROM users WHERE id = " + id)
	})
}
`)
}

// TestCatchesAMixedChain. One interpolated leaf in an otherwise literal chain
// is the realistic version of this bug, not a wholly dynamic string.
func TestCatchesAMixedChain(t *testing.T) {
	mustFlag(t, "mixed chain", `
const cols = "id, name"

func q(tx T, order string) {
	tx.SelectContext(ctx, &dest, "SELECT " + cols + " FROM t ORDER BY " + order)
}
`)
}

// ── what it must NOT catch ──────────────────────────────────────────

func TestLeavesOrdinarySafeQueriesAlone(t *testing.T) {
	mustNotFlag(t, "a plain literal", `
func q(tx T, id int64) {
	tx.GetContext(ctx, &dest, "SELECT * FROM users WHERE id = $1", id)
}
`)

	mustNotFlag(t, "a raw-string literal", "\nfunc q(tx T, id int64) {\n\ttx.GetContext(ctx, &dest, `\n\t\tSELECT *\n\t\t  FROM users\n\t\t WHERE id = $1`, id)\n}\n")

	mustNotFlag(t, "a chain of literals", `
func q(tx T) {
	tx.ExecContext(ctx, "UPDATE t " + "SET x = 1 " + "WHERE id = $1")
}
`)

	// The shared column-list idiom this whole repo is built on.
	mustNotFlag(t, "a const column list", `
const appCols = "id, email, status"

func q(tx T) {
	tx.SelectContext(ctx, &dest, "SELECT " + appCols + " FROM applications WHERE id = $1")
}
`)

	mustNotFlag(t, "a var column list", `
var appCols = "id, email"

func q(tx T) {
	tx.SelectContext(ctx, &dest, "SELECT " + appCols + " FROM t")
}
`)

	// A static name built from other static names, declared in one block.
	mustNotFlag(t, "a chain of static names", `
const (
	base  = "id, email"
	extra = ", status"
	all   = base + extra
)

func q(tx T) {
	tx.SelectContext(ctx, &dest, "SELECT " + all + " FROM t")
}
`)
}

// TestDoesNotFlagLookalikeMethods. Get on an HTTP client, Exec on a template,
// Query on url.Values — the watchlist is name-matched with no type information,
// so its restraint is the only thing keeping this usable.
func TestDoesNotFlagLookalikeMethods(t *testing.T) {
	mustNotFlag(t, "http client", `
func fetch(client T, id string) {
	client.Get("https://example.com/thing?id=" + id)
}
`)
	mustNotFlag(t, "a bare Query", `
func q(u T, k string) {
	u.Query().Get("a" + k)
}
`)
}

// ── the suppression ─────────────────────────────────────────────────

func TestSuppressionOnTheSameLine(t *testing.T) {
	mustNotFlag(t, "same line", `
func q(tx T, col string) {
	tx.SelectContext(ctx, &dest, "SELECT * FROM t ORDER BY " + col) // sqllint:allow column comes from a switch
}
`)
}

func TestSuppressionOnTheLineAbove(t *testing.T) {
	mustNotFlag(t, "line above", `
func q(tx T, col string) {
	// sqllint:allow column comes from a switch
	tx.SelectContext(ctx, &dest, "SELECT * FROM t ORDER BY " + col)
}
`)
}

// TestSuppressionReachesNoFurtherThanTwoLines. A comment that silenced a whole
// function would be an off switch nobody could see the extent of.
func TestSuppressionReachesNoFurtherThanTwoLines(t *testing.T) {
	got := scan(t, `
func q(tx T, col string) {
	// sqllint:allow this covers the next line only

	tx.SelectContext(ctx, &dest, "SELECT * FROM t ORDER BY " + col)
}
`)
	if len(got) != 1 {
		t.Errorf("got %d findings, want 1 — a blank line must end the suppression", len(got))
	}
}

// TestSuppressionMustBeOnTheLastCommentLine. A two-line justification with the
// token on the FIRST line does not suppress anything — the token's line and the
// one after it are marked, and the one after it is the rest of the comment.
//
// This is a trap rather than a rule: the comment reads as if it applies, the
// linter reports anyway, and the natural next move is to assume the suppression
// is broken. Write the prose first and the token last.
func TestSuppressionMustBeOnTheLastCommentLine(t *testing.T) {
	got := scan(t, `
func q(tx T, col string) {
	// sqllint:allow col comes from a switch
	// and here is a second line of justification
	tx.SelectContext(ctx, &dest, "SELECT * FROM t ORDER BY " + col)
}
`)
	if len(got) != 1 {
		t.Errorf("got %d findings, want 1 — the token only reaches one line past itself", len(got))
	}

	// The same comment with the token last does suppress it.
	mustNotFlag(t, "token on the last line", `
func q(tx T, col string) {
	// here is the justification, at whatever length it needs
	// sqllint:allow col comes from a switch
	tx.SelectContext(ctx, &dest, "SELECT * FROM t ORDER BY " + col)
}
`)
}

// ── the baseline ratchet ────────────────────────────────────────────

// TestBaselineRoundTrips. The identity is what the ratchet compares, so it has
// to survive a write and a read unchanged. If the format ever drifts, every
// baselined entry silently becomes a NEW finding — or worse, a genuine new
// finding silently matches an old entry.
func TestBaselineRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.txt")
	findings := []finding{
		{file: "a/store.go", funcName: "*PGStore.List", method: "SelectContext", reason: "string concat (`+`) used to build SQL", line: 10, snippet: "irrelevant"},
		{file: "b/store.go", funcName: "q", method: "ExecContext", reason: "fmt.Sprintf used to build SQL", line: 20},
	}
	if err := writeBaseline(path, findings); err != nil {
		t.Fatal(err)
	}
	loaded := loadBaseline(path)
	for _, f := range findings {
		if !loaded[f.id()] {
			t.Errorf("%q did not survive the round trip", f.id())
		}
	}
	if len(loaded) != 2 {
		t.Errorf("loaded %d entries, want 2", len(loaded))
	}
}

// TestBaselineIdentityIgnoresLineNumbers is the reason the identity is
// file|func|method|reason and not a position: a baseline keyed on line numbers
// churns on every refactor, which trains everybody to regenerate it without
// reading it — and a regenerated baseline suppresses the real finding that
// arrived in the same commit.
func TestBaselineIdentityIgnoresLineNumbers(t *testing.T) {
	a := finding{file: "s.go", funcName: "q", method: "ExecContext", reason: "concat", line: 10}
	b := a
	b.line = 400
	b.snippet = "totally different text"
	if a.id() != b.id() {
		t.Errorf("moving a line changed the identity:\n%s\n%s", a.id(), b.id())
	}

	// But a DIFFERENT reason at the same site is a different finding — one site
	// can hold both a concat and a Sprintf, and baselining one must not
	// suppress the other.
	c := a
	c.reason = "fmt.Sprintf used to build SQL"
	if a.id() == c.id() {
		t.Error("two different reasons at one site share an identity")
	}
}

func TestMissingBaselineIsEmptyNotFatal(t *testing.T) {
	if got := loadBaseline(filepath.Join(t.TempDir(), "nope.txt")); len(got) != 0 {
		t.Errorf("got %v, want an empty set", got)
	}
}

func TestBaselineIgnoresCommentsAndBlankLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.txt")
	body := "# a comment\n\n   \na/store.go|q|ExecContext|concat\n  b/store.go|q|Exec|concat  \n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := loadBaseline(path)
	if len(got) != 2 {
		t.Fatalf("loaded %d entries, want 2: %v", len(got), got)
	}
	if !got["b/store.go|q|Exec|concat"] {
		t.Error("a padded line was not trimmed")
	}
}

// ── SQL built into a variable ───────────────────────────────────────

// TestCatchesSQLBuiltIntoAVariable closes what was an open door.
//
// The scanner skips bare identifier arguments on purpose — an arg that is just
// a name could be ctx, tx or dest. That meant naming your string was enough to
// step around the whole linter:
//
//	q := fmt.Sprintf("... %s", userInput)
//	tx.ExecContext(ctx, q)
//
// A local now counts as SQL when it is BOTH dynamically built and opens with a
// SQL verb; the second half is what keeps ordinary Sprint'ed values out of it.
func TestCatchesSQLBuiltIntoAVariable(t *testing.T) {
	f := mustFlag(t, "sprintf into a variable", `
func q(tx T, name string) {
	sql := fmt.Sprintf("SELECT * FROM users WHERE name = '%s'", name)
	tx.ExecContext(ctx, sql)
}
`)
	if !strings.Contains(f.reason, "sql") {
		t.Errorf("reason = %q, want it to name the variable", f.reason)
	}

	mustFlag(t, "concat into a variable", `
func q(tx T, id string) {
	sql := "DELETE FROM users WHERE id = " + id
	tx.ExecContext(ctx, sql)
}
`)

	mustFlag(t, "var declaration", `
func q(tx T, id string) {
	var sql = "DELETE FROM users WHERE id = " + id
	tx.ExecContext(ctx, sql)
}
`)

	// Leading whitespace and a newline are how every multi-line query in this
	// repo starts, so the verb check has to see past them.
	mustFlag(t, "an indented multi-line query", `
func q(tx T, id string) {
	sql := `+"`"+`
		SELECT *
		  FROM users
		 WHERE id = `+"`"+` + id
	tx.SelectContext(ctx, &dest, sql)
}
`)
}

// TestDoesNotFlagASprintedValue is the false positive this rule would have if
// it only checked "dynamically built". Both of these are real shapes from this
// repo: a Sprint'ed VALUE passed as a bind parameter beside a constant query.
func TestDoesNotFlagASprintedValue(t *testing.T) {
	mustNotFlag(t, "a value bound as $1", `
func q(tx T, userID int) {
	userTarget := fmt.Sprintf("user:%d", userID)
	tx.SelectContext(ctx, &dest, "SELECT * FROM messages WHERE target = $1", userTarget)
}
`)
	mustNotFlag(t, "a summary string bound as $4", `
func q(tx T, id int, actor string) {
	summary := fmt.Sprintf("merged proposal #%d by %s", id, actor)
	tx.ExecContext(ctx, "INSERT INTO revisions (node_id, summary) VALUES ($1, $2)", id, summary)
}
`)
	// The same value, with the query itself held in a constant from another
	// file — so there is no string-shaped argument to anchor on. The value must
	// still not be mistaken for the query.
	mustNotFlag(t, "a value beside a named query", `
func q(tx T, userID int) {
	userTarget := fmt.Sprintf("user:%d", userID)
	tx.SelectContext(ctx, &dest, messagesForUserQuery, userTarget)
}
`)
	// A dynamic string that is not SQL at all.
	mustNotFlag(t, "a path", `
func q(tx T, name string) {
	key := "avatars/" + name
	tx.ExecContext(ctx, storeAvatarQuery, key)
}
`)
}

// TestSuppressionCoversTheVariableForm too — the escape hatch has to reach the
// new rule, or a safe assembled query has no way to be signed off.
func TestSuppressionCoversTheVariableForm(t *testing.T) {
	mustNotFlag(t, "suppressed at the call", `
func q(tx T, col string) {
	sql := "SELECT * FROM t ORDER BY " + col
	// sqllint:allow col comes from a switch
	tx.SelectContext(ctx, &dest, sql)
}
`)
}
