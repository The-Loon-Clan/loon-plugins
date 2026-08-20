# lint-sql

Flags any SQL query whose string argument is built with `+` concatenation or
`fmt.Sprintf` — the usual way SQL injection creeps in. Every value should reach
the driver through a `$N` placeholder instead.

It also flags the indirect form, where the query is assembled into a local and
the call sees only its name:

    q := fmt.Sprintf("SELECT ... '%s'", name)   // flagged
    tx.ExecContext(ctx, q)

A local counts as SQL when it is **both** dynamically built **and** opens with a
SQL verb. The second half is what keeps the rule usable — a Sprint'ed *value*
passed as a bind parameter beside a constant query is the common shape here and
is not a finding:

    userTarget := fmt.Sprintf("user:%d", id)    // not flagged
    tx.SelectContext(ctx, &m, `SELECT ... WHERE target = $1`, userTarget)

Run from the module root:

    go run ./scripts/lint-sql ./...

Exit 1 on any unsuppressed finding, 0 otherwise. (Ported from the ameNZB indexer.)

## Suppressing a finding

Only for dynamic IDENTIFIERS (table/column names, `ORDER BY` direction, an
int-only `IN (...)` list) where the value comes from a hard-coded allowlist —
NEVER for user input. Put on the call line or the line above:

    // sqllint:allow <reason>

The token reaches its own line and **one** line after it, so in a multi-line
justification the token goes **last**. A token on the first line of a two-line
comment suppresses nothing — and the comment above the finding reads as though
it should, which is the trap.

Or record a batch of reviewed-safe sites in `baseline.txt` with
`--update-baseline` (only after manually verifying each is safe).
