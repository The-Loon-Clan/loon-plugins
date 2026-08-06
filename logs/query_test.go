package logs

import (
	"testing"
	"time"
)

func TestParseQuery(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	t.Run("terms and exclusions", func(t *testing.T) {
		q := ParseQuery(`timeout "connection refused" -flapping`, now)
		if len(q.Terms) != 2 || q.Terms[0] != "timeout" || q.Terms[1] != "connection refused" {
			t.Fatalf("terms = %#v", q.Terms)
		}
		if len(q.NotTerms) != 1 || q.NotTerms[0] != "flapping" {
			t.Fatalf("notTerms = %#v", q.NotTerms)
		}
	})

	t.Run("op exact and prefix", func(t *testing.T) {
		q := ParseQuery(`op:usenet/assemble op:usenet/*`, now)
		if len(q.Ops) != 2 || q.Ops[0] != "usenet/assemble" || q.Ops[1] != "usenet/*" {
			t.Fatalf("ops = %#v", q.Ops)
		}
	})

	t.Run("severity aliases", func(t *testing.T) {
		q := ParseQuery(`severity:err sev:warning severity:bogus`, now)
		if len(q.Severities) != 2 || q.Severities[0] != "error" || q.Severities[1] != "warning" {
			t.Fatalf("severities = %#v (bogus should drop)", q.Severities)
		}
	})

	t.Run("user path count", func(t *testing.T) {
		q := ParseQuery(`user:42 path:/admin/users count:>=5`, now)
		if q.UserID == nil || *q.UserID != 42 {
			t.Fatalf("userID = %v", q.UserID)
		}
		if len(q.Paths) != 1 || q.Paths[0] != "/admin/users" {
			t.Fatalf("paths = %#v", q.Paths)
		}
		if q.MinCount != 5 {
			t.Fatalf("minCount = %d", q.MinCount)
		}
	})

	t.Run("count gt vs gte vs bare", func(t *testing.T) {
		if got := ParseQuery(`count:>5`, now).MinCount; got != 6 {
			t.Fatalf("count:>5 => %d, want 6", got)
		}
		if got := ParseQuery(`count:10`, now).MinCount; got != 10 {
			t.Fatalf("count:10 => %d, want 10", got)
		}
	})

	t.Run("relative since windows", func(t *testing.T) {
		for in, want := range map[string]time.Time{
			"since:24h": now.Add(-24 * time.Hour),
			"since:7d":  now.Add(-7 * 24 * time.Hour),
			"since:2w":  now.Add(-14 * 24 * time.Hour),
			"since:30m": now.Add(-30 * time.Minute),
		} {
			q := ParseQuery(in, now)
			if q.From == nil || !q.From.Equal(want) {
				t.Fatalf("%s => %v, want %v", in, q.From, want)
			}
		}
	})

	t.Run("absolute date", func(t *testing.T) {
		q := ParseQuery(`since:2026-07-01 until:2026-07-10`, now)
		if q.From == nil || q.From.Format("2006-01-02") != "2026-07-01" {
			t.Fatalf("from = %v", q.From)
		}
		if q.To == nil || q.To.Format("2006-01-02") != "2026-07-10" {
			t.Fatalf("to = %v", q.To)
		}
	})

	t.Run("is archived and sort", func(t *testing.T) {
		q := ParseQuery(`boom is:archived sort:count`, now)
		if !q.IncludeArchived {
			t.Fatal("IncludeArchived should be true")
		}
		if q.Sort != "count" {
			t.Fatalf("sort = %q", q.Sort)
		}
		if len(q.Terms) != 1 || q.Terms[0] != "boom" {
			t.Fatalf("terms = %#v", q.Terms)
		}
	})

	t.Run("negated fields route to exclusion filters", func(t *testing.T) {
		q := ParseQuery(`-severity:warning -op:usenet/* -path:/health`, now)
		if len(q.NotSeverities) != 1 || q.NotSeverities[0] != "warning" {
			t.Fatalf("notSeverities = %#v", q.NotSeverities)
		}
		if len(q.NotOps) != 1 || q.NotOps[0] != "usenet/*" {
			t.Fatalf("notOps = %#v", q.NotOps)
		}
		if len(q.NotPaths) != 1 || q.NotPaths[0] != "/health" {
			t.Fatalf("notPaths = %#v", q.NotPaths)
		}
		// The positive filters must stay empty — this was the inversion bug.
		if len(q.Severities) != 0 || len(q.Ops) != 0 || len(q.Paths) != 0 {
			t.Fatalf("negation leaked into positive filters: %#v", q)
		}
	})

	t.Run("negated scalar field falls to a NotTerm, never an inverted filter", func(t *testing.T) {
		q := ParseQuery(`-user:42 -since:24h`, now)
		if q.UserID != nil || q.From != nil {
			t.Fatalf("negated scalar must not apply a positive filter: %#v", q)
		}
		if len(q.NotTerms) != 2 {
			t.Fatalf("notTerms = %#v", q.NotTerms)
		}
	})

	t.Run("unknown field falls through to a term", func(t *testing.T) {
		q := ParseQuery(`request_id:abc123`, now)
		if len(q.Terms) != 1 || q.Terms[0] != "request_id:abc123" {
			t.Fatalf("unknown field should stay a term, got %#v", q.Terms)
		}
	})

	t.Run("empty query is the zero filter", func(t *testing.T) {
		q := ParseQuery("   ", now)
		if len(q.Terms) != 0 || q.From != nil || q.IncludeArchived {
			t.Fatalf("blank query should be empty, got %#v", q)
		}
	})
}
