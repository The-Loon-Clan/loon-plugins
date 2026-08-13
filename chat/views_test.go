package chat

import (
	"strings"
	"testing"
	"time"
)

// The chat page renders, and the hooks its JavaScript depends on are present.
//
// pageTmpl is built with template.Must at package init, so a PARSE error is
// already fatal — but only to something that loads the package, and until this
// file existed nothing in the test suite did. A broken template shipped as a
// panic on first request.
//
// Parsing is the smaller half anyway. html/template resolves fields at EXECUTE
// time, so a renamed or misspelled field parses perfectly and fails only when a
// member opens the page. Worse, it fails MID-WRITE: the page streams, so the
// output already sent stays on screen and the reader gets a page that stops
// halfway with no error visible — the failure signature that cost real time on
// the forum lift.

// channelVM mirrors the host's storage.ChatChannelRow by field name. Declared
// here rather than imported because a plugin cannot import the site; the point
// is that the TEMPLATE's field references stay satisfiable, and a drift between
// this and the real row shows up as a template that no longer executes.
//
// Note what is absent: WebhookURL. The real row does not carry it either, which
// is what keeps a credential from being one {{.WebhookURL}} away from a page
// every logged-in member can read.
type channelVM struct {
	ChannelID   string
	ChannelName string
	Messages    int
	LastAt      time.Time
}

func render(t *testing.T, vm pageVM) string {
	t.Helper()
	var sb strings.Builder
	if err := pageTmpl.Execute(&sb, vm); err != nil {
		t.Fatalf("chat.html failed to execute: %v", err)
	}
	return sb.String()
}

func testVM() pageVM {
	return pageVM{
		DiscordInviteURL: "https://discord.gg/example",
		Channels: []channelVM{
			{ChannelID: "1", ChannelName: "general", Messages: 12, LastAt: time.Unix(0, 0)},
			{ChannelID: "2", ChannelName: "suggestions", Messages: 40, LastAt: time.Unix(0, 0)},
		},
	}
}

func TestChatPageRenders(t *testing.T) {
	out := render(t, testVM())
	for _, want := range []string{"general", "suggestions", "chat-channel"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page is missing %q", want)
		}
	}
}

// Every branch executes. The anonymous half in particular had never run: the
// page read IsAnon and nothing supplied it, so the "log in to chat" prompt was
// unreachable and would have surfaced its first bug the day /chat went public.
func TestChatPageRendersEveryBranch(t *testing.T) {
	vms := map[string]pageVM{
		"anonymous":      {IsAnon: true, Channels: testVM().Channels},
		"no channels":    {DiscordInviteURL: "https://discord.gg/example"},
		"no invite":      {Channels: testVM().Channels},
		"active channel": {Channels: testVM().Channels, Active: "2"},
	}
	for name, vm := range vms {
		t.Run(name, func(t *testing.T) { render(t, vm) })
	}
}

// The contract between the markup and the click handler.
//
// Switching channels used to be an ordinary link, so every click reloaded the
// whole page — scroll position lost, SSE torn down and rebuilt to receive the
// stream it already had. The handler switches in place instead, and it finds
// the channel by data-channel and its label by data-name.
//
// Those attributes carry no meaning to a reader and nothing else references
// them, which is exactly why they would be tidied away. Removing one does not
// break the page: it silently reverts to a full navigation, which looks like
// the feature was never built.
func TestChannelLinksCarryTheHooksTheClickHandlerNeeds(t *testing.T) {
	out := render(t, testVM())

	for _, want := range []string{
		`data-channel="1"`, `data-name="general"`,
		`data-channel="2"`, `data-name="suggestions"`,
		`data-channel="" data-name="all"`, // the "all" entry clears the filter
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s — the sidebar reverts to full page loads without it", want)
		}
	}

	// The header is retitled in place on switch, by id.
	if !strings.Contains(out, `id="chat-header-name"`) {
		t.Error(`missing id="chat-header-name" — switching leaves the header ` +
			`naming the previous channel, so the page reads as the wrong room`)
	}

	// The links stay real links. Middle-click, ctrl-click and no-JS all depend
	// on the href, and the handler deliberately does not consume those clicks.
	if !strings.Contains(out, `href="/chat?channel=1"`) {
		t.Error("channel link lost its href — open-in-new-tab and the no-JS " +
			"path both stop working, and both still filter server-side")
	}
}

// A live message must be filtered client-side, because the SSE stream carries
// every public channel over ONE connection. Without this the loaded history was
// filtered and the live tail was not: sitting in #suggestions, a line from
// #general appended as though it had been said there, and the longer the page
// stayed open the more wrong it got.
func TestLiveStreamIsFilteredByChannel(t *testing.T) {
	out := render(t, testVM())
	if !strings.Contains(out, "m.channel_id !== activeChannel") {
		t.Error("the SSE handler no longer filters by channel — messages from " +
			"other channels will appear in whichever channel is open")
	}
}

// The webhook URL embeds its token. It has no route into this template — the
// row type does not carry it — and this asserts that it stays that way, since
// the sidebar is rendered for every logged-in member.
func TestPageNeverRendersAWebhookURL(t *testing.T) {
	out := render(t, testVM())
	if strings.Contains(out, "discord.com/api/webhooks") {
		t.Error("a webhook URL reached the chat page — that is a send credential " +
			"published to every member who opens /chat")
	}
}
