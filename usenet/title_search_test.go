package usenet

import (
	"reflect"
	"strings"
	"testing"
)

// Scene releases are dot- or underscore-separated; people and *arr clients
// type spaces. A contiguous substring match means the two spellings never
// meet — measured on a live index, "Fear the Walking Dead" matched 154 rows
// of which NONE were dot-named, and "Fear.the.Walking.Dead" matched 316 of
// which none were space-named. Neither told the member what was there.
func TestTitleTokensSplitsEverySeparator(t *testing.T) {
	want := []string{"%Fear%", "%the%", "%Walking%", "%Dead%"}
	for _, q := range []string{
		"Fear the Walking Dead",
		"Fear.the.Walking.Dead",
		"Fear_the_Walking_Dead",
		"Fear.the Walking_Dead",
		"  Fear   the  Walking Dead  ",
	} {
		if got := titleTokens(q); !reflect.DeepEqual(got, want) {
			t.Errorf("titleTokens(%q) = %v, want %v", q, got, want)
		}
	}
}

// The tokens are ANDed, so word order and interior text stop mattering —
// "Breaking Bad 1080p" matched nothing before even though such releases exist.
func TestTitleClauseAndsItsTokens(t *testing.T) {
	clause, args := titleClause("Breaking Bad", 1)
	if strings.Count(clause, "title ILIKE") != 2 || !strings.Contains(clause, " AND ") {
		t.Errorf("clause = %q, want two ANDed predicates", clause)
	}
	if !strings.Contains(clause, "$1") || !strings.Contains(clause, "$2") {
		t.Errorf("clause = %q, want sequential placeholders", clause)
	}
	if len(args) != 2 {
		t.Fatalf("args = %v, want two", args)
	}

	// startIdx offsets the placeholders, because the feed's caller appends
	// limit and offset after them.
	clause, _ = titleClause("Breaking Bad", 5)
	if !strings.Contains(clause, "$5") || !strings.Contains(clause, "$6") {
		t.Errorf("clause = %q, want placeholders starting at $5", clause)
	}
}

// A '%' or '_' typed into a search box is a character somebody meant, not an
// operator. They leaked into the pattern before.
func TestTitleTokensEscapesMetacharacters(t *testing.T) {
	got := titleTokens("50%")
	if len(got) != 1 || got[0] != `%50\%%` {
		t.Errorf("titleTokens(%q) = %v, want the percent escaped", "50%", got)
	}
	// Backslash is escaped FIRST, or escaping '%' manufactures one.
	if got := titleTokens(`a\b`); len(got) != 1 || got[0] != `%a\\b%` {
		t.Errorf(`titleTokens("a\b") = %v, want the backslash doubled`, got)
	}
	// '_' cannot survive: it is a separator, so it never reaches the pattern.
	if got := titleTokens("a_b"); !reflect.DeepEqual(got, []string{"%a%", "%b%"}) {
		t.Errorf("titleTokens(%q) = %v, want it split rather than escaped", "a_b", got)
	}
}

// Each token is a leading-wildcard ILIKE that no index accelerates, so a
// pasted paragraph must not become a hundred sequential scans.
func TestTitleTokensAreBounded(t *testing.T) {
	q := strings.Repeat("word ", 200)
	if got := titleTokens(q); len(got) != maxTitleTokens {
		t.Errorf("titleTokens of 200 words returned %d, want the %d cap",
			len(got), maxTitleTokens)
	}
}

// No usable terms reads as "match everything" — the answer an empty query
// already gave, so the feed's recent-all form is unchanged.
func TestTitleClauseEmptyForNoTerms(t *testing.T) {
	for _, q := range []string{"", "   ", "..._..."} {
		if clause, args := titleClause(q, 1); clause != "" || args != nil {
			t.Errorf("titleClause(%q) = %q/%v, want empty", q, clause, args)
		}
	}
}
