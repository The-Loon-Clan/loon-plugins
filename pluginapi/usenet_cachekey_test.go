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
	}
	for _, m := range mutate {
		if NewznabCacheKey(m.mut(base)) == baseKey {
			t.Errorf("changing %s did not change the key — a cached response would be served for different bytes", m.name)
		}
	}
}
