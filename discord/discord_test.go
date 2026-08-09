package discord

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// stubLinks / stubUsers satisfy the seam interfaces via embedding so a
// non-nil value exists for the Provision nil-checks. No method is ever
// called: every guard case below leaves exactly one required field nil, so
// Provision returns at the validation guard before touching the *core.Core
// or any repository method.
type stubLinks struct{ LinkStore }
type stubUsers struct{ UserStore }

// fakeHub is a ChatHub that goes nowhere — Provision only stores it.
type fakeHub struct{}

func (fakeHub) Start()                                               {}
func (fakeHub) Publish(context.Context, pluginapi.ChatMessage) error { return nil }
func (fakeHub) Recent(context.Context, int) ([]pluginapi.ChatMessage, error) {
	return nil, nil
}
func (fakeHub) Subscribe(string) chan pluginapi.ChatMessage {
	return make(chan pluginapi.ChatMessage)
}
func (fakeHub) Unsubscribe(chan pluginapi.ChatMessage) {}

// fakeSettings is a map-backed Settings. Only the accessors the tests
// exercise carry state; the rest are inert.
type fakeSettings struct{ m map[string]string }

func newFakeSettings() *fakeSettings { return &fakeSettings{m: map[string]string{}} }

func (f *fakeSettings) get(k string) string                    { return f.m[k] }
func (f *fakeSettings) set(k, v string) error                  { f.m[k] = v; return nil }
func (f *fakeSettings) GetDiscordEnabled(context.Context) bool { return f.get("enabled") == "1" }
func (f *fakeSettings) SetDiscordEnabled(_ context.Context, v bool) error {
	if v {
		return f.set("enabled", "1")
	}
	return f.set("enabled", "")
}
func (f *fakeSettings) GetDiscordBotToken(context.Context) string { return f.get("bot_token") }
func (f *fakeSettings) SetDiscordBotToken(_ context.Context, v string) error {
	return f.set("bot_token", v)
}
func (f *fakeSettings) GetDiscordGuildID(context.Context) string { return f.get("guild_id") }
func (f *fakeSettings) SetDiscordGuildID(_ context.Context, v string) error {
	return f.set("guild_id", v)
}
func (f *fakeSettings) GetDiscordReleasesChannelID(context.Context) string {
	return f.get("releases_channel")
}
func (f *fakeSettings) SetDiscordReleasesChannelID(_ context.Context, v string) error {
	return f.set("releases_channel", v)
}
func (f *fakeSettings) GetDiscordChatChannelID(context.Context) string { return f.get("chat_channel") }
func (f *fakeSettings) SetDiscordChatChannelID(_ context.Context, v string) error {
	return f.set("chat_channel", v)
}
func (f *fakeSettings) GetDiscordVerifyChannelID(context.Context) string {
	return f.get("verify_channel")
}
func (f *fakeSettings) SetDiscordVerifyChannelID(_ context.Context, v string) error {
	return f.set("verify_channel", v)
}
func (f *fakeSettings) GetDiscordOpsChannelID(context.Context) string {
	return f.get("ops_channel")
}
func (f *fakeSettings) SetDiscordOpsChannelID(_ context.Context, v string) error {
	return f.set("ops_channel", v)
}
func (f *fakeSettings) GetDiscordChatWebhookURL(context.Context) string { return f.get("webhook") }
func (f *fakeSettings) SetDiscordChatWebhookURL(_ context.Context, v string) error {
	return f.set("webhook", v)
}
func (f *fakeSettings) GetDiscordInviteURL(context.Context) string { return f.get("invite_url") }
func (f *fakeSettings) SetDiscordInviteURL(_ context.Context, v string) error {
	return f.set("invite_url", v)
}
func (f *fakeSettings) GetDiscordMemberRoleID(context.Context) string { return f.get("role_member") }
func (f *fakeSettings) SetDiscordMemberRoleID(_ context.Context, v string) error {
	return f.set("role_member", v)
}
func (f *fakeSettings) GetDiscordRoleID(_ context.Context, name string) string {
	return f.get("role_" + name)
}
func (f *fakeSettings) SetDiscordRoleID(_ context.Context, name, roleID string) error {
	return f.set("role_"+name, roleID)
}

func validDeps() Deps {
	return Deps{
		Links:     stubLinks{},
		Users:     stubUsers{},
		Settings:  newFakeSettings(),
		NewHub:    func() pluginapi.ChatHub { return fakeHub{} },
		Viewer:    func(*gin.Context) *Viewer { return nil },
		CSRFToken: func(*gin.Context) string { return "test-csrf" },
	}
}

// withDeps snapshots and restores the package-level `deps` so
// tests that mutate it don't leak into one another.
func withDeps(t *testing.T, d *Deps) {
	t.Helper()
	orig := deps
	t.Cleanup(func() { deps = orig })
	deps = d
}

func TestProvision_NilDeps_FailsFast(t *testing.T) {
	withDeps(t, nil) // SetDeps never called

	err := (&Plugin{}).Provision(nil)
	if err == nil {
		t.Fatal("Provision with nil deps: want error, got nil")
	}
	if !strings.Contains(err.Error(), "SetDeps") {
		t.Errorf("error should mention SetDeps, got %q", err.Error())
	}
}

func TestProvision_MissingRequiredField(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Deps)
	}{
		{"missing Links", func(d *Deps) { d.Links = nil }},
		{"missing Users", func(d *Deps) { d.Users = nil }},
		{"missing Settings", func(d *Deps) { d.Settings = nil }},
		{"missing NewHub", func(d *Deps) { d.NewHub = nil }},
		{"missing Viewer", func(d *Deps) { d.Viewer = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validDeps()
			tc.mutate(&d)
			withDeps(t, &d)

			err := (&Plugin{}).Provision(nil)
			if err == nil {
				t.Fatalf("Provision(%s): want error, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "required field") {
				t.Errorf("Provision(%s): error should mention the missing required field, got %q", tc.name, err.Error())
			}
		})
	}
}

func TestSetDeps_PopulatesPackageVar(t *testing.T) {
	// keep the global clean regardless of what SetDeps writes
	orig := deps
	t.Cleanup(func() { deps = orig })

	deps = nil
	d := validDeps()
	d.BaseURL = "https://example.test"
	SetDeps(d)

	if deps == nil {
		t.Fatal("SetDeps did not populate the package deps var")
	}
	if deps.BaseURL != "https://example.test" {
		t.Errorf("SetDeps stored wrong payload: BaseURL=%q", deps.BaseURL)
	}
	if deps.Links == nil || deps.Users == nil {
		t.Error("SetDeps dropped required repositories")
	}
}

// The plugin used to be worker-only. It has a web leg now because the /profile
// account-link card is its UI: the bot belongs on the worker, but the card and
// its unlink were only ever the host's because a worker-only plugin had nowhere
// to put them.
func TestMetadata_RunsOnWebAndWorker(t *testing.T) {
	m := (&Plugin{}).Metadata()
	if m.Name != "discord" {
		t.Errorf("Name = %q, want discord", m.Name)
	}
	want := map[string]bool{"web": false, "worker": false}
	for _, p := range m.Processes {
		if _, ok := want[p]; !ok {
			t.Errorf("unexpected process %q", p)
			continue
		}
		want[p] = true
	}
	for proc, seen := range want {
		if !seen {
			t.Errorf("Processes = %v, missing %q", m.Processes, proc)
		}
	}
}

// The web leg must not construct a bot: a second gateway connection from the
// web process would double every Discord event the worker already handles.
func TestProvision_WebLegHasNoBot(t *testing.T) {
	d := validDeps()
	d.NewHub = func() pluginapi.ChatHub {
		t.Error("the web leg constructed a chat hub; the worker owns the relay")
		return fakeHub{}
	}
	withDeps(t, &d)

	p := &Plugin{}
	// A Core shaped like the real web one minus a gin engine: NewRouter with an
	// empty adapter yields Engine() == nil, so route registration is skipped and
	// this exercises the leg split rather than gin. (A bare &core.Core{} would
	// nil-deref on Router — every real web Core has one.)
	c := &core.Core{
		Process: "web",
		Router:  core.NewRouter(core.RouterAdapter{}),
		Auth:    core.NewAuth(core.AuthAdapter{}),
	}
	if err := p.Provision(c); err != nil {
		t.Fatalf("Provision(web): %v", err)
	}
	if p.bot != nil {
		t.Error("the web leg built a Discord bot — that is a duplicate gateway connection")
	}
	if p.hub != nil {
		t.Error("the web leg holds a chat hub; the worker owns the relay")
	}
	// Start/Stop must tolerate the nil bot rather than panic on the web leg.
	if err := p.Start(context.Background()); err != nil {
		t.Errorf("Start on the web leg: %v", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Errorf("Stop on the web leg: %v", err)
	}
}

// fakeDisplay stands in for the ranks plugin's GroupDisplay. The bot holds the
// capability, not a repository, since Stage 3.4.
type fakeDisplay struct {
	catalog []pluginapi.Badge
	byUser  map[int64][]pluginapi.Badge
	err     error
}

func (f *fakeDisplay) Catalog(context.Context) ([]pluginapi.Badge, error) {
	return f.catalog, f.err
}
func (f *fakeDisplay) BadgesFor(_ context.Context, uid int64) ([]pluginapi.Badge, error) {
	return f.byUser[uid], f.err
}
func (f *fakeDisplay) BadgesForBatch(_ context.Context, ids []int64) (map[int64][]pluginapi.Badge, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[int64][]pluginapi.Badge{}
	for _, id := range ids {
		if b, ok := f.byUser[id]; ok {
			out[id] = b
		}
	}
	return out, nil
}

// getRankRoleMap used to be a literal naming the four ranks that existed when
// it was written, so a fifth rank created later could never sync to Discord —
// silently: the user buys the rank and Discord never hears. It reads the group
// catalog now, keyed by SLUG. This pins both, because either failure is
// invisible.
func TestGetRankRoleMap_FollowsTheCatalogAndKeysBySlug(t *testing.T) {
	settings := newFakeSettings()
	ctx := context.Background()

	// A slug that differs from the lowercased name is the case the old
	// name-keyed map got wrong: rename a tier and its role is orphaned.
	if err := settings.SetDiscordRoleID(ctx, "kirisame", "role-kiri"); err != nil {
		t.Fatal(err)
	}
	if err := settings.SetDiscordRoleID(ctx, "high-tide", "role-tide"); err != nil {
		t.Fatal(err)
	}

	d := &DiscordBotService{settings: settings, display: &fakeDisplay{catalog: []pluginapi.Badge{
		{Slug: "kirisame", Name: "Kirisame"},
		{Slug: "high-tide", Name: "High Tide"},
	}}}
	got := d.getRankRoleMap(ctx)

	if got["kirisame"] != "role-kiri" {
		t.Errorf("kirisame = %q, want role-kiri", got["kirisame"])
	}
	if got["high-tide"] != "role-tide" {
		t.Errorf("high-tide = %q, want role-tide — the key is the slug, not %q",
			got["high-tide"], "high tide")
	}
}

// A group with no Discord role configured maps to "", which nonEmptyRoleIDs
// drops — so it is simply not synced, rather than being unrepresentable.
func TestGetRankRoleMap_UnconfiguredGroupIsEmpty(t *testing.T) {
	d := &DiscordBotService{
		settings: newFakeSettings(),
		display:  &fakeDisplay{catalog: []pluginapi.Badge{{Slug: "shigure", Name: "Shigure"}}},
	}

	got := d.getRankRoleMap(context.Background())
	if _, ok := got["shigure"]; !ok {
		t.Fatal("the group is missing from the map entirely")
	}
	if got["shigure"] != "" {
		t.Errorf("shigure = %q, want empty", got["shigure"])
	}
}

// No ranks plugin in this process must not break account-role sync: the rank
// axis simply has no roles to manage.
func TestGetRankRoleMap_NoCapabilityYieldsNoRankAxis(t *testing.T) {
	d := &DiscordBotService{settings: newFakeSettings()}

	if got := d.getRankRoleMap(context.Background()); got != nil {
		t.Errorf("got %+v, want nil so nonEmptyRoleIDs manages nothing", got)
	}
}

// A failing catalog read must also manage nothing rather than act on a
// half-known world — an empty map would strip every user's rank role.
func TestGetRankRoleMap_ReadFailureManagesNothing(t *testing.T) {
	d := &DiscordBotService{
		settings: newFakeSettings(),
		display:  &fakeDisplay{err: errors.New("store down")},
	}

	if got := d.getRankRoleMap(context.Background()); got != nil {
		t.Errorf("got %+v, want nil on a failed read", got)
	}
}

// rankSlugsFor is one batch call for every linked guild member, not one per
// user — this runs over the whole guild on a schedule.
func TestRankSlugsFor_BatchesAndTakesTheTopBadge(t *testing.T) {
	d := &DiscordBotService{display: &fakeDisplay{byUser: map[int64][]pluginapi.Badge{
		1764: {{Slug: "arashi"}, {Slug: "kirisame"}},
		2141: {{Slug: "shigure"}},
	}}}

	got := d.rankSlugsFor(context.Background(), []LinkWithRole{
		{DiscordID: "d1", UserID: 1764},
		{DiscordID: "d2", UserID: 2141},
		{DiscordID: "d3", UserID: 9999},
	})

	if got[1764] != "arashi" {
		t.Errorf("user 1764 = %q, want arashi (the most prominent badge)", got[1764])
	}
	if got[2141] != "shigure" {
		t.Errorf("user 2141 = %q, want shigure", got[2141])
	}
	// Absent, not empty-string: reconcileAxis reads a missing entry as "strip
	// every paid-rank role", which is what a user with no group should get.
	if _, ok := got[9999]; ok {
		t.Errorf("user with no badge is present as %q; it should be absent", got[9999])
	}
}

// The invite command must never echo internal error text to a Discord user —
// only the ErrNoInvites sentinel has a user-facing translation.
func TestErrNoInvites_IsAMatchableSentinel(t *testing.T) {
	wrapped := errors.Join(errors.New("outer"), ErrNoInvites)
	if !errors.Is(wrapped, ErrNoInvites) {
		t.Error("ErrNoInvites should survive wrapping — the host adapter may wrap it")
	}
}
