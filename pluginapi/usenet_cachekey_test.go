package pluginapi

import (
	"strings"
	"testing"
)

// Every field the rendered XML embeds must partition the cache. BaseURL was
// the miss that mattered: both hosts derive it from the request's Host
// header and the plugin bakes it into every download link, so two hostnames
// sharing one Redis served each other's links — and a keyless cached t=caps
// could leak an internal-only hostname to public callers.
func TestNewznabCacheKeyPartitionsByResponseFields(t *testing.T) {
	base := NewznabRequest{
		Function: "tvsearch", Query: "frieren", Categories: []int{5070},
		Limit: 50, Offset: 0, ID: "", APIKey: "k1",
		BaseURL: "https://a.example", Title: "loon indexer",
	}
	baseKey := NewznabCacheKey(base)

	if got := NewznabCacheKey(base); got != baseKey {
		t.Fatal("identical requests produced different keys")
	}
	if !strings.HasPrefix(baseKey, NewznabCachePrefix) {
		t.Fatalf("key %q does not carry the %q prefix DeletePrefix invalidation clears", baseKey, NewznabCachePrefix)
	}

	mutate := []struct {
		name string
		mut  func(r NewznabRequest) NewznabRequest
	}{
		{"BaseURL", func(r NewznabRequest) NewznabRequest { r.BaseURL = "https://b.example"; return r }},
		{"Title", func(r NewznabRequest) NewznabRequest { r.Title = "loon api"; return r }},
		{"APIKey", func(r NewznabRequest) NewznabRequest { r.APIKey = "k2"; return r }},
		{"Function", func(r NewznabRequest) NewznabRequest { r.Function = "search"; return r }},
		{"Query", func(r NewznabRequest) NewznabRequest { r.Query = "other"; return r }},
		{"Categories", func(r NewznabRequest) NewznabRequest { r.Categories = []int{5000}; return r }},
		{"Limit", func(r NewznabRequest) NewznabRequest { r.Limit = 25; return r }},
		{"Offset", func(r NewznabRequest) NewznabRequest { r.Offset = 50; return r }},
		{"ID", func(r NewznabRequest) NewznabRequest { r.ID = "42"; return r }},
		// Season / Episode were added to NewznabRequest late and omitted here
		// at first, which is the failure this table exists to catch: for one
		// release `tvsearch&q=X&season=4&ep=1` and `tvsearch&q=X` hashed the
		// same, so whichever ran first answered for both.
		{"Season", func(r NewznabRequest) NewznabRequest { r.Season = cacheKeyIntp(4); return r }},
		{"Episode", func(r NewznabRequest) NewznabRequest { r.Episode = cacheKeyIntp(1); return r }},
	}
	for _, m := range mutate {
		if NewznabCacheKey(m.mut(base)) == baseKey {
			t.Errorf("changing %s did not change the key — a cached response would be served for different bytes", m.name)
		}
	}

	// The episode fields are POINTERS so that "did not ask" and "asked for
	// zero" are different questions; the key has to keep them different too.
	s0, s4, s5 := base, base, base
	s0.Season, s4.Season, s5.Season = cacheKeyIntp(0), cacheKeyIntp(4), cacheKeyIntp(5)
	if NewznabCacheKey(s0) == baseKey {
		t.Error("season=0 and no season hash the same — a request that asked " +
			"would be served the answer to one that did not")
	}
	if NewznabCacheKey(s4) == NewznabCacheKey(s5) {
		t.Error("season 4 and season 5 share a key")
	}
}

func cacheKeyIntp(v int) *int { return &v }
