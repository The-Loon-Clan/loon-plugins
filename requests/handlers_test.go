package requests

import "testing"

func TestBoostCostBreakdown(t *testing.T) {
	tests := []struct {
		name      string
		baseCost  int
		seedCount int
		sizeBytes int64
		perGB     int
		wantCost  int
		wantSeed  int // seed mult percentage
		wantSize  int // size mult percentage
	}{
		{"base only, healthy seeds", 5, 10, 0, 0, 5, 0, 0},
		{"no seeders = +300%", 5, 0, 0, 0, 20, 300, 0},
		{"2 seeders = +200%", 5, 2, 0, 0, 15, 200, 0},
		{"1 seeder = +200%", 5, 1, 0, 0, 15, 200, 0},
		{"5 seeders = +100%", 5, 5, 0, 0, 10, 100, 0},
		{"6 seeders = no penalty", 5, 6, 0, 0, 5, 0, 0},
		{"size 10GB at 2pts/GB", 5, 10, 10 * 1024 * 1024 * 1024, 2, 25, 0, 400},
		{"size 1GB at 1pt/GB", 10, 10, 1 * 1024 * 1024 * 1024, 1, 11, 0, 10},
		{"combined seed+size", 5, 0, 10 * 1024 * 1024 * 1024, 2, 40, 300, 400},
		{"zero base cost", 0, 0, 0, 0, 1, 300, 0}, // minimum 1
		{"no size penalty when perGB=0", 5, 10, 100 * 1024 * 1024 * 1024, 0, 5, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := boostCostBreakdown(tt.baseCost, tt.seedCount, tt.sizeBytes, tt.perGB)
			if b.FinalCost != tt.wantCost {
				t.Errorf("FinalCost = %d, want %d", b.FinalCost, tt.wantCost)
			}
			if b.SeedMult != tt.wantSeed {
				t.Errorf("SeedMult = %d, want %d", b.SeedMult, tt.wantSeed)
			}
			if b.SizeMult != tt.wantSize {
				t.Errorf("SizeMult = %d, want %d", b.SizeMult, tt.wantSize)
			}
			if b.BaseCost != tt.baseCost {
				t.Errorf("BaseCost = %d, want %d", b.BaseCost, tt.baseCost)
			}
		})
	}
}

func TestBoostCostForRequest(t *testing.T) {
	// Legacy wrapper should return the same as breakdown.FinalCost
	tests := []struct {
		base  int
		seeds int
		want  int
	}{
		{5, 10, 5},
		{5, 0, 20},
		{5, 2, 15},
	}
	for _, tt := range tests {
		got := boostCostForRequest(tt.base, tt.seeds)
		if got != tt.want {
			t.Errorf("boostCostForRequest(%d, %d) = %d, want %d", tt.base, tt.seeds, got, tt.want)
		}
	}
}

func TestValidateRequestURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "missing_url"},
		{"whitespace only", "   ", "missing_url"},
		{"nyaa", "https://nyaa.si/view/1234567", ""},
		{"nyaa subdomain", "https://old.nyaa.si/view/1234567", ""},
		{"tokyotosho", "https://tokyotosho.info/details.php?id=42", ""},
		{"nekobt", "https://nekobt.to/torrents/abc", ""},
		{"animetosho", "https://animetosho.org/view/some-release", ""},
		{"magnet ok", "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01", ""},
		{"magnet no btih", "magnet:?xt=urn:sha1:foo", "invalid_url"},
		{"random site", "https://example.com/torrent", "host_not_allowed"},
		{"random forum post", "https://reddit.com/r/anime/comments/xyz", "host_not_allowed"},
		{"raw filename", "Attack on Titan S04 1080p.mkv", "invalid_url"},
		{"missing scheme but allowed host", "nyaa.si/view/123", "invalid_url"},
		{"port stripped", "https://nyaa.si:443/view/1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validateRequestURL(tc.in); got != tc.want {
				t.Errorf("validateRequestURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
