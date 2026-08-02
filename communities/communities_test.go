package communities

import (
	"image"
	"strings"
	"testing"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// The communities plugin ships PGStore only (no MemStore), so these
// tests cover the package's pure logic: slug validation, the join-gate
// message helper, invite usability, viewer-role, the numeric clamps,
// image resize, and invite-code generation. The PGStore SQL methods
// need a live-DB integration test and are intentionally not mocked here.

func TestCommunitySlugValidation(t *testing.T) {
	cases := []struct {
		slug string
		ok   bool
	}{
		{"anime", true},
		{"a_b-c", true},
		{"abc", true},
		{"abcdefghijklmnopqrstuvwxyz012345", true}, // 32 chars
		{"ab", false}, // too short (min 3)
		{"abcdefghijklmnopqrstuvwxyz0123456", false}, // 33 chars, too long
		{"1abc", false}, // must start with a letter
		{"_abc", false}, // must start with a letter
		{"Abc", false},  // uppercase not allowed
		{"a b", false},  // space
		{"a.b", false},  // punctuation
		{"", false},
	}
	for _, tc := range cases {
		if got := communitySlugRE.MatchString(tc.slug); got != tc.ok {
			t.Errorf("communitySlugRE(%q) = %v, want %v", tc.slug, got, tc.ok)
		}
	}
}

func TestCommunitySlugReserved(t *testing.T) {
	// Every reserved word is a valid slug shape but must be blocked so
	// it can't shadow a static /c subpath.
	for word := range communitySlugReserved {
		if !communitySlugRE.MatchString(word) {
			t.Errorf("reserved word %q is not a valid slug shape — reservation is dead", word)
		}
	}
	if communitySlugReserved["anime"] {
		t.Error("normal slug should not be reserved")
	}
	if !communitySlugReserved["new"] || !communitySlugReserved["settings"] {
		t.Error("expected /c/new and /c/settings collisions to be reserved")
	}
}

func TestJoinRequirementError(t *testing.T) {
	now := time.Now()
	oldUser := &core.User{Role: core.RoleUser, CreatedAt: now.AddDate(0, 0, -30)} // 30 days old
	newUser := &core.User{Role: core.RoleUser, CreatedAt: now.AddDate(0, 0, -1)}  // 1 day old
	modUser := &core.User{Role: core.RoleMod, CreatedAt: now.AddDate(0, 0, -30)}

	cases := []struct {
		name    string
		user    *core.User
		balance int
		comm    *Community
		wantErr bool
	}{
		{"no gates passes", oldUser, 0, &Community{}, false},
		{"age gate fail", newUser, 0, &Community{MinAccountAgeDays: 7}, true},
		{"age gate pass", oldUser, 0, &Community{MinAccountAgeDays: 7}, false},
		{"role gate fail", oldUser, 0, &Community{MinRoleLevel: int(core.RoleMod)}, true},
		{"role gate pass", modUser, 0, &Community{MinRoleLevel: int(core.RoleMod)}, false},
		{"points gate fail", oldUser, 5, &Community{JoinPointsCost: 100}, true},
		{"points gate pass", oldUser, 100, &Community{JoinPointsCost: 100}, false},
		{"points exact boundary", oldUser, 100, &Community{JoinPointsCost: 100}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := joinRequirementError(tc.user, tc.balance, tc.comm)
			if (msg != "") != tc.wantErr {
				t.Errorf("joinRequirementError = %q, wantErr=%v", msg, tc.wantErr)
			}
		})
	}
}

func TestCommunityInviteIsUsable(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	cases := []struct {
		name string
		inv  CommunityInvite
		want bool
	}{
		{"unlimited never expires", CommunityInvite{MaxUses: 0, UseCount: 999}, true},
		{"under cap", CommunityInvite{MaxUses: 5, UseCount: 4}, true},
		{"at cap", CommunityInvite{MaxUses: 5, UseCount: 5}, false},
		{"over cap", CommunityInvite{MaxUses: 5, UseCount: 6}, false},
		{"expired", CommunityInvite{MaxUses: 0, ExpiresAt: &past}, false},
		{"not yet expired", CommunityInvite{MaxUses: 0, ExpiresAt: &future}, true},
		{"expired and capped", CommunityInvite{MaxUses: 1, UseCount: 1, ExpiresAt: &past}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv := tc.inv
			if got := inv.IsUsable(now); got != tc.want {
				t.Errorf("IsUsable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCommunityViewerRoleCanModerate(t *testing.T) {
	cases := []struct {
		name string
		role CommunityViewerRole
		want bool
	}{
		{"owner", CommunityViewerRole{IsOwner: true}, true},
		{"mod", CommunityViewerRole{IsMod: true}, true},
		{"subscriber only", CommunityViewerRole{IsSubscriber: true}, false},
		{"nobody", CommunityViewerRole{}, false},
	}
	for _, tc := range cases {
		if got := tc.role.CanModerate(); got != tc.want {
			t.Errorf("%s: CanModerate = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestClampInt(t *testing.T) {
	cases := []struct {
		v, lo, hi, want int
	}{
		{5, 0, 10, 5},
		{-1, 0, 10, 0},
		{11, 0, 10, 10},
		{0, 0, 10, 0},
		{10, 0, 10, 10},
	}
	for _, tc := range cases {
		if got := clampInt(tc.v, tc.lo, tc.hi); got != tc.want {
			t.Errorf("clampInt(%d,%d,%d) = %d, want %d", tc.v, tc.lo, tc.hi, got, tc.want)
		}
	}
}

func TestAtoiDefault(t *testing.T) {
	cases := []struct {
		in   string
		def  int
		want int
	}{
		{"42", 0, 42},
		{"  7 ", 0, 7},
		{"", 99, 99},
		{"notanumber", 5, 5},
		{"-3", 0, -3},
	}
	for _, tc := range cases {
		if got := atoiDefault(tc.in, tc.def); got != tc.want {
			t.Errorf("atoiDefault(%q,%d) = %d, want %d", tc.in, tc.def, got, tc.want)
		}
	}
}

func TestResizeToWidth(t *testing.T) {
	// 2000x1000 source; resize to 1000 wide should halve both dims.
	src := image.NewRGBA(image.Rect(0, 0, 2000, 1000))
	out := resizeToWidth(src, 1000)
	if b := out.Bounds(); b.Dx() != 1000 || b.Dy() != 500 {
		t.Errorf("downscale got %dx%d, want 1000x500", b.Dx(), b.Dy())
	}

	// Source narrower than maxW is never upscaled — returned unchanged.
	small := image.NewRGBA(image.Rect(0, 0, 100, 50))
	out2 := resizeToWidth(small, 1000)
	if b := out2.Bounds(); b.Dx() != 100 || b.Dy() != 50 {
		t.Errorf("no-upscale got %dx%d, want 100x50", b.Dx(), b.Dy())
	}

	// maxW <= 0 is a no-op guard.
	out3 := resizeToWidth(src, 0)
	if b := out3.Bounds(); b.Dx() != 2000 {
		t.Errorf("maxW=0 should be a no-op, got width %d", b.Dx())
	}
}

func TestRandomInviteCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		code, err := randomInviteCode()
		if err != nil {
			t.Fatalf("randomInviteCode: %v", err)
		}
		// 12 random bytes, RawURLEncoding => 16 chars.
		if len(code) != 16 {
			t.Errorf("code %q length = %d, want 16", code, len(code))
		}
		// URL-safe alphabet only (no +, /, =).
		if strings.ContainsAny(code, "+/=") {
			t.Errorf("code %q contains non-URL-safe chars", code)
		}
		if seen[code] {
			t.Errorf("duplicate code generated: %q", code)
		}
		seen[code] = true
	}
}
