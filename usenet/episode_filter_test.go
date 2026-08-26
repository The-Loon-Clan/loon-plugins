package usenet

import (
	"strings"
	"testing"
)

func ip(v int) *int { return &v }

// tvsearch is the single query Sonarr runs most, and it silently ignored
// season and ep: a member asking for one episode was handed every episode of
// the series, every time, with nothing in the response admitting it.
func TestEpisodeClauseNarrows(t *testing.T) {
	clause, args := episodeClause(feedFilter{Season: ip(4), Episode: ip(1)}, 3)
	if !strings.Contains(clause, "season = $3") {
		t.Errorf("clause = %q, want the season bound at $3", clause)
	}
	if !strings.Contains(clause, "episode = $4") {
		t.Errorf("clause = %q, want the episode bound at $4", clause)
	}
	if len(args) != 2 || args[0] != 4 || args[1] != 1 {
		t.Errorf("args = %v, want [4 1]", args)
	}
}

// A client asking for S04E01 and handed only single-episode releases would
// miss the complete-season upload, which is often the only one there.
func TestEpisodeClauseMatchesSeasonPacks(t *testing.T) {
	clause, _ := episodeClause(feedFilter{Episode: ip(1)}, 1)
	if !strings.Contains(clause, "is_pack") {
		t.Errorf("clause = %q, want season packs to satisfy an episode query", clause)
	}
}

// season and episode are NOT NULL DEFAULT 0 and 0 means UNPARSED here, not
// "specials". Filtering on it would match every release the parser has never
// read -- the opposite of narrowing.
func TestEpisodeClauseIgnoresZero(t *testing.T) {
	for _, f := range []feedFilter{
		{Season: ip(0)},
		{Episode: ip(0)},
		{Season: ip(0), Episode: ip(0)},
	} {
		if clause, args := episodeClause(f, 1); clause != "" || args != nil {
			t.Errorf("episodeClause(%+v) = %q/%v, want no filter: zero means "+
				"unparsed in this schema, so it would match everything unread",
				f, clause, args)
		}
	}
}

// Absent is not the same as zero, which is why the fields are pointers: a host
// that never populates them gets exactly today's behaviour.
func TestEpisodeClauseEmptyWhenNotAsked(t *testing.T) {
	if clause, args := episodeClause(feedFilter{}, 1); clause != "" || args != nil {
		t.Errorf("episodeClause(nil,nil) = %q/%v, want no filter", clause, args)
	}
}

// The placeholders must continue the title clause's numbering, or the values
// land on the wrong parameters.
func TestEpisodeClauseContinuesPlaceholders(t *testing.T) {
	title, targs := titleClause("Breaking Bad", 1)
	ep, eargs := episodeClause(feedFilter{Season: ip(4)}, len(targs)+1)
	if !strings.Contains(title, "$1") || !strings.Contains(title, "$2") {
		t.Fatalf("title clause = %q", title)
	}
	if !strings.Contains(ep, "$3") {
		t.Errorf("episode clause = %q, want it to start at $3 after two title tokens", ep)
	}
	if len(eargs) != 1 {
		t.Errorf("eargs = %v", eargs)
	}
}
