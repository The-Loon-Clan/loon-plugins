package irc

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/core"
)

// Stubs satisfy the seam interfaces via embedding so a non-nil value exists
// for the Provision nil-checks. No method is ever called: every guard case
// leaves exactly one required field nil, so Provision returns at the
// validation guard before touching the *core.Core or any store method.
type stubDMs struct{ DMStore }
type stubLinks struct{ LinkStore }
type stubUsers struct{ UserStore }

type stubSettings struct{}

func (stubSettings) GetSetting(context.Context, string) (string, error) { return "", nil }

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

// validDeps returns a Deps with every Provision-required field satisfied by
// an inert double. The optional fields are left nil on purpose — Provision
// only stores them, so this proves the guard's boundary exactly.
func validDeps() Deps {
	return Deps{
		DMs:      stubDMs{},
		Links:    stubLinks{},
		Settings: stubSettings{},
		Users:    stubUsers{},
		NewHub:   func() pluginapi.ChatHub { return fakeHub{} },
		Viewer:   func(*gin.Context) *Viewer { return nil },
		BaseURL:  "https://example.test",
	}
}

func TestMetadata(t *testing.T) {
	p := &Plugin{}
	m := p.Metadata()

	if m.Name != "irc" {
		t.Errorf("Name = %q, want %q", m.Name, "irc")
	}
	if m.Version == "" {
		t.Error("Version is empty")
	}
	// Dual-leg: the bot is worker work, but the /profile account-link card is
	// this plugin's UI and rides a web-leg view slot. It lived in the host
	// only because a worker-only plugin had nowhere to put a page fragment.
	want := map[string]bool{"web": false, "worker": false}
	for _, proc := range m.Processes {
		if _, ok := want[proc]; !ok {
			t.Errorf("unexpected process %q", proc)
			continue
		}
		want[proc] = true
	}
	for proc, seen := range want {
		if !seen {
			t.Errorf("Processes = %v, missing %q", m.Processes, proc)
		}
	}
}

func TestProvision_MissingSetDeps(t *testing.T) {
	deps = nil // simulate SetDeps never having run
	p := &Plugin{}

	err := p.Provision(nil) // Provision never dereferences the Core
	if err == nil {
		t.Fatal("expected error when SetDeps was not called, got nil")
	}
	if !strings.Contains(err.Error(), "SetDeps") {
		t.Errorf("error should mention SetDeps, got: %v", err)
	}
}

func TestProvision_MissingRequiredField(t *testing.T) {
	cases := []struct {
		name string
		mut  func(d *Deps)
	}{
		{"nil DMs", func(d *Deps) { d.DMs = nil }},
		{"nil Links", func(d *Deps) { d.Links = nil }},
		{"nil Settings", func(d *Deps) { d.Settings = nil }},
		{"nil Users", func(d *Deps) { d.Users = nil }},
		{"nil NewHub", func(d *Deps) { d.NewHub = nil }},
		{"nil Viewer", func(d *Deps) { d.Viewer = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validDeps()
			tc.mut(&d)
			SetDeps(d)

			err := (&Plugin{}).Provision(nil)
			if err == nil {
				t.Fatalf("%s: expected error, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "missing a required field") {
				t.Errorf("%s: unexpected error: %v", tc.name, err)
			}
		})
	}
}

func TestProvision_Success(t *testing.T) {
	SetDeps(validDeps())
	p := &Plugin{}

	// The Core is dereferenced now (Provision reads c.Process to pick its
	// leg), so this passes a worker Core rather than nil.
	if err := p.Provision(&core.Core{Process: "worker"}); err != nil {
		t.Fatalf("Provision with valid deps: %v", err)
	}
	// Provision must fully wire the services so Start has something to
	// launch (it does no I/O itself).
	if p.bot == nil {
		t.Error("bot service not constructed")
	}
	if p.hub == nil {
		t.Error("chat hub not constructed")
	}
}

// The web leg must not build a bot: a second IRC connection from the web
// process would join the channel twice and echo every relayed message.
func TestProvision_WebLegHasNoBot(t *testing.T) {
	d := validDeps()
	d.NewHub = func() pluginapi.ChatHub {
		t.Error("the web leg constructed a chat hub; the worker owns the relay")
		return fakeHub{}
	}
	SetDeps(d)
	p := &Plugin{}

	c := &core.Core{
		Process: "web",
		Router:  core.NewRouter(core.RouterAdapter{}),
		Auth:    core.NewAuth(core.AuthAdapter{}),
	}
	if err := p.Provision(c); err != nil {
		t.Fatalf("Provision(web): %v", err)
	}
	if p.bot != nil {
		t.Error("the web leg built an IRC bot — that is a duplicate connection")
	}
	if p.hub != nil {
		t.Error("the web leg holds a chat hub; the worker owns the relay")
	}
	if err := p.Start(context.Background()); err != nil {
		t.Errorf("Start on the web leg: %v", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Errorf("Stop on the web leg: %v", err)
	}
}

// The bridge caps forwarded bodies at a byte budget but must never split a
// multibyte rune — a byte slice through the middle of a CJK character
// manufactures invalid UTF-8 (the msg[:4000] lesson, in miniature).
func TestCapForIRC_IsRuneSafe(t *testing.T) {
	// 200 three-byte runes = 600 bytes; a naive [:380] lands mid-rune.
	long := strings.Repeat("あ", 200)
	capped := capForIRC(long)
	if !strings.HasSuffix(capped, "…") {
		t.Fatal("cap did not apply to an over-budget body")
	}
	if !utf8.ValidString(capped) {
		t.Fatal("capping produced invalid UTF-8 — the cut split a rune")
	}
	if len(capped) > ircBodyCap+len("…") {
		t.Errorf("capped body is %d bytes, budget is %d", len(capped), ircBodyCap)
	}

	short := "hello"
	if got := capForIRC(short); got != short {
		t.Errorf("under-budget body was modified: %q", got)
	}
}

// The sentCache anti-loop guard: what we sent recently is suppressed, and
// the cache is bounded.
func TestSentCache_SuppressesEchoesAndStaysBounded(t *testing.T) {
	b := NewIRCBotService(stubDMs{}, stubLinks{}, stubSettings{}, stubUsers{}, "")

	b.rememberSent("#chan", "hello")
	if !b.recentlySent("#chan", "hello") {
		t.Error("a just-sent message should be suppressed")
	}
	if b.recentlySent("#chan", "different") {
		t.Error("an unsent message must not be suppressed")
	}
	for i := 0; i < ircSentCacheSize*2; i++ {
		b.rememberSent("#chan", strings.Repeat("x", i%7)+string(rune('a'+i%26)))
	}
	if len(b.sentCache) > ircSentCacheSize {
		t.Errorf("sentCache grew to %d, cap is %d", len(b.sentCache), ircSentCacheSize)
	}
}

// Not exercised here — present so fakeHub satisfies pluginapi.ChatHub after
// the bridge gained channel enumeration.
func (fakeHub) SyncChannels(context.Context, []pluginapi.ChatChannel) error { return nil }

func (fakeHub) PublishHistory(context.Context, pluginapi.ChatMessage) error { return nil }
