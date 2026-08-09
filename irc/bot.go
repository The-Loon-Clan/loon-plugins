package irc

// IRCBotService — IRC bridge bot. Mirrors DiscordBotService's role:
// joins a single channel, bridges chat in both directions, handles
// account linking via PM, and runs a small command set (invite, stats,
// help, link, whisper).
//
// Lifecycle:
//   - Started from the worker process alongside the Discord bot (both
//     register as services via schedule.RegisterService so they share the
//     admin Jobs page surface).
//   - Connects via SASL if credentials are configured; falls back to
//     plain NICK/USER registration otherwise. Supports SOCKS5 dial
//     (for the gluetun-or-VPS-B egress) when irc_socks5_addr is set.
//   - Reconnect on disconnect via the hand-rolled runWithBackoff loop,
//     exponential backoff capped at 5 minutes. (girc's own reconnect is
//     not used — each attempt builds a fresh client so config changes
//     apply.)
//
// Chat bridge:
//   - PRIVMSG to the configured channel → hub Publish with source="irc".
//     Linked users get their site role colour; unlinked users render as
//     anonymous "[irc:nick]".
//   - Foreign messages on the hub (source != "irc") get forwarded to the
//     IRC channel as "[<source>:<author>] body". Anti-loop: messages we
//     just sent (tracked by target+body short-window dedup) are skipped
//     on the subscribe side.
//
// Linking:
//   - User runs `/msg <bot> link <token>` where token comes from
//     their profile page. Bot looks up the token, creates the
//     link row, clears the token, confirms the link in PM.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lrstanley/girc"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/schedule"
	"golang.org/x/net/proxy"
)

// ircSetting reads one settings row as a string with a fallback.
func (b *IRCBotService) ircSetting(ctx context.Context, key, fallback string) string {
	v, _ := b.settings.GetSetting(ctx, key)
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	return v
}

// ircSettingInt parses an int with fallback.
func (b *IRCBotService) ircSettingInt(ctx context.Context, key string, fallback int) int {
	v := b.ircSetting(ctx, key, "")
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// ircSettingBool returns true for "1", "true", "yes".
func (b *IRCBotService) ircSettingBool(ctx context.Context, key string, fallback bool) bool {
	v := strings.ToLower(b.ircSetting(ctx, key, ""))
	if v == "" {
		return fallback
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

const (
	ircReconnectMin  = 5 * time.Second
	ircReconnectMax  = 5 * time.Minute
	ircSentCacheSize = 64  // track the last N outbound bodies to suppress loop echoes
	ircBodyCap       = 380 // bytes; forwarded chat lines are truncated to fit one PRIVMSG
)

// IRCBotService manages the IRC bot lifecycle + chat bridge + link
// command + whisper delivery. Mirror of DiscordBotService.
type IRCBotService struct {
	dms          DMStore
	ircLinks     LinkStore
	settings     SettingReader
	users        UserStore
	baseURL      string
	chat         pluginapi.ChatHub
	display      pluginapi.GroupDisplay
	createInvite func(context.Context, int) (string, error)
	job          *schedule.JobInfo

	mu      sync.Mutex
	client  *girc.Client
	running bool
	stopCh  chan struct{}

	// bridgeCh is the hub subscription feeding bridgeFromChat. Held so
	// Shutdown can Unsubscribe (which closes it and ends the goroutine) —
	// the host version never did, leaving the bridge blocked on the hub
	// until process exit.
	bridgeCh chan pluginapi.ChatMessage

	// sentCache holds the last N (target, body) pairs we sent so the
	// chat bridge can suppress echoes of our own forwarded messages.
	// girc fires PRIVMSG for our own privmsgs too.
	sentMu    sync.Mutex
	sentCache []string
}

func NewIRCBotService(dms DMStore, ircLinks LinkStore, settings SettingReader, users UserStore, baseURL string) *IRCBotService {
	return &IRCBotService{
		dms: dms, ircLinks: ircLinks, settings: settings, users: users,
		baseURL:   baseURL,
		sentCache: make([]string, 0, ircSentCacheSize),
	}
}

// badgesFor resolves a user's badges, absorbing both degraded cases: no
// capability (no ranks plugin in this process) and a failed read. Chat is a
// firehose, so a failure is not reported per message — the line simply carries
// no rank, and the next message tries again.
func (b *IRCBotService) badgesFor(ctx context.Context, userID int) []pluginapi.Badge {
	b.mu.Lock()
	d := b.display
	b.mu.Unlock()
	if d == nil {
		return nil
	}
	badges, err := d.BadgesFor(ctx, int64(userID))
	if err != nil {
		return nil
	}
	return badges
}

// SetGroupDisplay wires the badge capability, resolved from the extension
// registry at Start (ENTITLEMENTS.md Stage 3.4). Nil is normal — a host built
// without the ranks plugin bridges chat with no rank label, which is the
// cosmetic degrade that contract mandates.
func (b *IRCBotService) SetGroupDisplay(d pluginapi.GroupDisplay) {
	b.mu.Lock()
	b.display = d
	b.mu.Unlock()
}

// SetChatHub wires the chat hub. Without it, chat bridging is
// disabled (the bot still handles link / whisper / commands).
func (b *IRCBotService) SetChatHub(h pluginapi.ChatHub) { b.chat = h }

// SetCreateInvite wires the invite seam for the PM `invite` command.
func (b *IRCBotService) SetCreateInvite(f func(context.Context, int) (string, error)) {
	b.createInvite = f
}

// Start launches the connect loop with backoff. Safe to call once at boot.
func (b *IRCBotService) Start() {
	b.job = schedule.RegisterService("IRC Bot", "Maintains a persistent connection to the configured IRC server for chat bridge, account linking, and whisper delivery.")
	b.job.SetTrigger(func() { go b.reconnect() })
	b.stopCh = make(chan struct{})
	go b.runWithBackoff()
	// Subscribe SYNCHRONOUSLY, then hand the channel to the goroutine: if the
	// goroutine subscribed itself, a Shutdown racing boot could read a nil
	// bridgeCh, skip the Unsubscribe, and leak the bridge forever — the very
	// leak this machinery exists to close.
	if b.chat != nil {
		ch := b.chat.Subscribe("irc-bot")
		b.mu.Lock()
		b.bridgeCh = ch
		b.mu.Unlock()
		go b.bridgeFromChat(ch)
	}
}

// Stop disconnects + tells the run loop to exit. Reconnect-safe: the bridge
// subscription survives so an admin-triggered reconnect keeps relaying —
// Shutdown is the full stop.
func (b *IRCBotService) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopCh != nil {
		close(b.stopCh)
		b.stopCh = nil
	}
	if b.client != nil {
		b.client.Close()
		b.client = nil
	}
	b.running = false
}

// Shutdown disconnects AND ends the bridge goroutine (Unsubscribe closes its
// channel). Called from the plugin's Stop on process shutdown.
func (b *IRCBotService) Shutdown() {
	b.Stop()
	b.mu.Lock()
	ch := b.bridgeCh
	b.bridgeCh = nil
	b.mu.Unlock()
	if ch != nil && b.chat != nil {
		b.chat.Unsubscribe(ch)
	}
}

func (b *IRCBotService) reconnect() {
	b.job.Log("Reconnecting (triggered by admin)...")
	b.Stop()
	b.stopCh = make(chan struct{})
	go b.runWithBackoff()
}

// runWithBackoff is the connect + reconnect loop. Each successful
// connection runs the girc client's blocking loop until disconnect;
// disconnect triggers a backoff sleep then retry.
func (b *IRCBotService) runWithBackoff() {
	backoff := ircReconnectMin
	for {
		select {
		case <-b.stopCh:
			return
		default:
		}
		ctx := context.Background()
		if !b.settingEnabled(ctx) {
			b.job.SetProgress("Disabled")
			b.job.Log("Disabled in admin settings — not connecting")
			return
		}
		if err := b.runOnce(ctx); err != nil {
			b.job.Log("Connection ended: %v — reconnecting in %s", err, backoff.Round(time.Second))
			b.job.SetError(fmt.Sprintf("Disconnected: %v", err))
			select {
			case <-b.stopCh:
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > ircReconnectMax {
				backoff = ircReconnectMax
			}
			continue
		}
		// Clean exit (e.g. server QUIT). Reset backoff.
		backoff = ircReconnectMin
	}
}

func (b *IRCBotService) runOnce(ctx context.Context) error {
	server := b.ircSetting(ctx, "irc_server", "")
	if server == "" {
		b.job.SetError("No IRC server configured")
		return fmt.Errorf("no irc_server configured")
	}
	port := b.ircSettingInt(ctx, "irc_port", 6697)
	useTLS := b.ircSettingBool(ctx, "irc_use_tls", true)
	nick := b.ircSetting(ctx, "irc_nick", "ameNZB")
	realname := b.ircSetting(ctx, "irc_realname", "ameNZB bridge bot")
	channel := b.ircSetting(ctx, "irc_channel", "")
	if channel == "" {
		b.job.SetError("No IRC channel configured")
		return fmt.Errorf("no irc_channel configured")
	}
	saslUser := b.ircSetting(ctx, "irc_sasl_user", "")
	saslPass := b.ircSetting(ctx, "irc_sasl_pass", "")
	socks5 := b.ircSetting(ctx, "irc_socks5_addr", "")

	cfg := girc.Config{
		Server:    server,
		Port:      port,
		Nick:      nick,
		User:      nick,
		Name:      realname,
		Version:   "ameNZB-bridge",
		SSL:       useTLS,
		TLSConfig: &tls.Config{ServerName: server, MinVersion: tls.VersionTLS12},
	}
	if saslUser != "" && saslPass != "" {
		cfg.SASL = &girc.SASLPlain{User: saslUser, Pass: saslPass}
	}

	client := girc.New(cfg)
	b.attachHandlers(client, channel)

	b.mu.Lock()
	b.client = client
	b.running = true
	b.mu.Unlock()
	b.job.SetRunning()
	b.job.SetProgress("Connecting to %s:%d as %s", server, port, nick)

	var connectErr error
	if socks5 != "" {
		// SOCKS5 dial via golang.org/x/net/proxy. Used when the bot
		// connects to an EXTERNAL IRC network (Libera / Rizon) via
		// a proxy for IP hiding. For a self-hosted IRCd reachable
		// over a private network, leave irc_socks5_addr empty.
		d, derr := proxy.SOCKS5("tcp", socks5, nil, &net.Dialer{Timeout: 15 * time.Second})
		if derr != nil {
			b.mu.Lock()
			b.running = false
			b.client = nil
			b.mu.Unlock()
			return fmt.Errorf("socks5 dialer %q: %w", socks5, derr)
		}
		b.job.Log("Connecting via SOCKS5 %s", socks5)
		connectErr = client.DialerConnect(gircDialer{d: d})
	} else {
		connectErr = client.Connect()
	}
	if connectErr != nil {
		b.mu.Lock()
		b.running = false
		b.client = nil
		b.mu.Unlock()
		return connectErr
	}
	// Connect() returns when the connection drops.
	b.mu.Lock()
	b.running = false
	b.client = nil
	b.mu.Unlock()
	return nil
}

// gircDialer adapts a proxy.Dialer to girc's Dialer interface.
type gircDialer struct{ d proxy.Dialer }

func (g gircDialer) Dial(network, address string) (net.Conn, error) {
	return g.d.Dial(network, address)
}

// attachHandlers wires the girc callbacks for the chat bridge,
// connect-ready (join channel), and PM commands.
func (b *IRCBotService) attachHandlers(c *girc.Client, channel string) {
	c.Handlers.AddBg(girc.CONNECTED, func(client *girc.Client, e girc.Event) {
		b.job.Log("Connected as %s — joining %s", client.GetNick(), channel)
		b.job.SetProgress("Connected as %s in %s", client.GetNick(), channel)
		client.Cmd.Join(channel)
	})

	c.Handlers.AddBg(girc.PRIVMSG, func(client *girc.Client, e girc.Event) {
		// Two flavours: channel messages (chat bridge) and direct
		// PMs to the bot (command handlers).
		if len(e.Params) == 0 {
			return
		}
		target := e.Params[0]
		body := e.Last()
		from := e.Source.Name
		if from == client.GetNick() {
			return // ignore our own echoes
		}
		if strings.EqualFold(target, channel) {
			b.handleChannelMessage(from, body, channel)
			return
		}
		if strings.EqualFold(target, client.GetNick()) {
			b.handlePrivateMessage(from, body)
			return
		}
	})

	c.Handlers.AddBg(girc.ERROR, func(client *girc.Client, e girc.Event) {
		b.job.Log("Server error: %s", e.Last())
	})
	c.Handlers.AddBg(girc.KICK, func(client *girc.Client, e girc.Event) {
		if len(e.Params) >= 2 && e.Params[1] == client.GetNick() {
			b.job.Log("Kicked from %s — rejoining", e.Params[0])
			time.Sleep(3 * time.Second)
			client.Cmd.Join(e.Params[0])
		}
	})
}

// handleChannelMessage publishes one IRC channel message into the hub so
// site SSE subscribers + the Discord bridge see it. Looks up the IRC nick →
// site user mapping so linked users render with their site username + rank
// colour.
func (b *IRCBotService) handleChannelMessage(from, body, channel string) {
	if b.chat == nil {
		return
	}
	if !b.ircSettingBool(context.Background(), "irc_chat_bridge", true) {
		return
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}

	ctx := context.Background()
	cm := pluginapi.ChatMessage{
		ID:     fmt.Sprintf("irc-%d-%s", time.Now().UnixNano(), from),
		Author: from,
		Body:   body,
		At:     time.Now().UTC(),
		Source: "irc",
	}
	// Linked-user enrichment: rank colour + site role for nicer
	// rendering in the site chat widget.
	if link, err := b.ircLinks.GetIRCLinkByNick(ctx, from); err == nil && link != nil {
		if user, uerr := b.users.GetUserByID(ctx, link.UserID); uerr == nil && user != nil {
			cm.Author = user.Username
			cm.Role = user.Role
			if user.AvatarPath != "" {
				cm.AvatarURL = b.baseURL + user.AvatarPath
			}
			// One call for the label and the tint, where this used to be an
			// active-subscription lookup followed by a rank fetch. The head of
			// the slice is the most prominent badge, matching what
			// `ORDER BY sort_order DESC LIMIT 1` returned.
			if badges := b.badgesFor(ctx, user.ID); len(badges) > 0 {
				cm.RankName = badges[0].Name
				cm.RankColor = badges[0].TitleColor
			}
		}
	}
	if err := b.chat.Publish(ctx, cm); err != nil {
		b.job.Log("Publish irc msg failed: %v", err)
	}
}

// handlePrivateMessage routes a PM to the bot to the correct command.
// Supported:
//
//	help                  → command reference
//	link <token>          → verify + create irc_link
//	invite                → mint a site invite code (requires link)
//	stats                 → user stats (requires link)
//	whisper <user> <body> → send a site whisper (requires link)
//
// Anything else gets a help hint.
func (b *IRCBotService) handlePrivateMessage(from, body string) {
	parts := strings.Fields(body)
	if len(parts) == 0 {
		return
	}
	cmd := strings.ToLower(parts[0])
	args := parts[1:]
	switch cmd {
	case "help", "?":
		b.cmdHelp(from)
	case "link":
		b.cmdLink(from, args)
	case "invite":
		b.cmdInvite(from)
	case "stats":
		b.cmdStats(from)
	case "whisper", "msg", "tell":
		b.cmdWhisper(from, args)
	default:
		b.pmReply(from, "Unknown command. Try: help, link <token>, invite, stats, whisper <user> <body>")
	}
}

// cmdHelp lists the bot's PM commands.
func (b *IRCBotService) cmdHelp(from string) {
	b.pmReply(from, "Commands:")
	b.pmReply(from, "  link <token>          — link your site account (get token at "+b.baseURL+"/account-settings#irc)")
	b.pmReply(from, "  invite                — mint a site invite code (linked users only)")
	b.pmReply(from, "  stats                 — your account stats (linked users only)")
	b.pmReply(from, "  whisper <user> <body> — send a site whisper to <user> (linked users only)")
}

// cmdLink — `link <token>`. Looks up the verify token, creates the
// link row, clears the token. One-shot.
func (b *IRCBotService) cmdLink(from string, args []string) {
	if len(args) == 0 {
		b.pmReply(from, "Usage: link <token>. Get your token at "+b.baseURL+"/account-settings#irc")
		return
	}
	token := strings.TrimSpace(args[0])
	ctx := context.Background()
	_, user, err := b.ircLinks.GetIRCLinkByToken(ctx, token)
	if err != nil || user == nil {
		b.pmReply(from, "Invalid or expired verification code. Get a fresh one on your account-settings page.")
		return
	}
	if err := b.ircLinks.CreateIRCLink(ctx, user.ID, from, ""); err != nil {
		b.pmReply(from, "Failed to link account. Please try again.")
		b.job.Log("CreateIRCLink failed user=%d nick=%s: %v", user.ID, from, err)
		return
	}
	_ = b.ircLinks.SetIRCVerifyToken(ctx, user.ID, "") // one-shot
	b.pmReply(from, fmt.Sprintf("Linked to %s! You'll see your username + rank in chat now.", user.Username))
}

// cmdInvite mints a site invite code for the linked user.
func (b *IRCBotService) cmdInvite(from string) {
	if b.createInvite == nil {
		b.pmReply(from, "Invite system unavailable.")
		return
	}
	ctx := context.Background()
	link, err := b.ircLinks.GetIRCLinkByNick(ctx, from)
	if err != nil || link == nil {
		b.pmReply(from, "Your IRC nick isn't linked. Use `link <token>` first.")
		return
	}
	token, err := b.createInvite(ctx, link.UserID)
	if err != nil {
		if errors.Is(err, ErrNoInvites) {
			b.pmReply(from, "You have no invite codes remaining.")
		} else {
			// Internal error text stays server-side; the host used to PM
			// err.Error() to a remote IRC user.
			b.pmReply(from, "Invite create failed — please try again later.")
			b.job.Log("Invite create failed for site user %d: %v", link.UserID, err)
		}
		return
	}
	b.pmReply(from, fmt.Sprintf("Your invite: %s/r/%s", b.baseURL, token))
}

// cmdStats reports basic account stats for the linked user.
func (b *IRCBotService) cmdStats(from string) {
	ctx := context.Background()
	link, err := b.ircLinks.GetIRCLinkByNick(ctx, from)
	if err != nil || link == nil {
		b.pmReply(from, "Your IRC nick isn't linked. Use `link <token>` first.")
		return
	}
	user, err := b.users.GetUserByID(ctx, link.UserID)
	if err != nil || user == nil {
		b.pmReply(from, "Couldn't load your account.")
		return
	}
	b.pmReply(from, fmt.Sprintf("%s — role=%s points=%d invites=%d",
		user.Username, user.Role, user.Points, user.InviteCount))
}

// cmdWhisper accepts `whisper <username> <body>` from a linked user
// and sends a site whisper (DirectMessage) to <username>. The
// recipient sees it in their site inbox; if they have an IRC link
// too AND irc_whisper_delivery=1, the bot also PMs them.
func (b *IRCBotService) cmdWhisper(from string, args []string) {
	if len(args) < 2 {
		b.pmReply(from, "Usage: whisper <username> <body>")
		return
	}
	ctx := context.Background()
	link, err := b.ircLinks.GetIRCLinkByNick(ctx, from)
	if err != nil || link == nil {
		b.pmReply(from, "Your IRC nick isn't linked. Use `link <token>` first.")
		return
	}
	toUsername := args[0]
	body := strings.TrimSpace(strings.Join(args[1:], " "))
	if body == "" {
		b.pmReply(from, "Empty body — nothing to send.")
		return
	}
	toUser, err := b.users.GetUserByUsername(ctx, toUsername)
	if err != nil || toUser == nil {
		b.pmReply(from, fmt.Sprintf("No such user: %s", toUsername))
		return
	}
	// Persist the whisper to the site DM store via the
	// thread+message API: GetOrCreate the (lo, hi) thread, then
	// insert the message. Mirrors what the site's /api/dm POST does.
	// Failures PM a generic line — internal error text does not leave
	// the server (the host used to reply with err.Error()).
	threadID, _, terr := b.dms.EnsureDMThread(ctx, link.UserID, toUser.ID)
	if terr != nil {
		b.pmReply(from, "Whisper failed — please try again later.")
		b.job.Log("Whisper EnsureDMThread failed from=%d to=%d: %v", link.UserID, toUser.ID, terr)
		return
	}
	if _, err := b.dms.CreateDMMessage(ctx, threadID, link.UserID, body); err != nil {
		b.pmReply(from, "Whisper failed — please try again later.")
		b.job.Log("Whisper CreateDMMessage failed from=%d to=%d: %v", link.UserID, toUser.ID, err)
		return
	}
	b.pmReply(from, fmt.Sprintf("Whisper sent to %s.", toUser.Username))
	// Cross-platform delivery: if recipient has IRC, PM them too.
	if b.ircSettingBool(ctx, "irc_whisper_delivery", true) {
		if toLink, _ := b.ircLinks.GetIRCLinkByUserID(ctx, toUser.ID); toLink != nil {
			b.pmReply(toLink.IRCNick, fmt.Sprintf("[whisper from %s] %s", link.IRCNick, body))
		}
	}
}

// bridgeFromChat drains the hub subscription for cross-platform messages
// (source != "irc") and forwards them to the IRC channel as
// "[<source>:<author>] body". Anti-loop check uses sentCache. Ends when
// Shutdown unsubscribes (the hub closes the channel). The subscription is
// made in Start, before this goroutine exists — see the comment there.
func (b *IRCBotService) bridgeFromChat(ch chan pluginapi.ChatMessage) {
	for m := range ch {
		if strings.EqualFold(m.Source, "irc") {
			continue // don't loop our own messages back
		}
		b.mu.Lock()
		client := b.client
		b.mu.Unlock()
		if client == nil {
			continue
		}
		if !b.ircSettingBool(context.Background(), "irc_chat_bridge", true) {
			continue
		}
		channel := b.ircSetting(context.Background(), "irc_channel", "")
		if channel == "" {
			continue
		}
		// Sanitize body for IRC: collapse newlines, cap length.
		body := capForIRC(strings.ReplaceAll(m.Body, "\n", " "))
		text := fmt.Sprintf("[%s:%s] %s", m.Source, m.Author, body)
		if b.recentlySent(channel, text) {
			continue
		}
		client.Cmd.Message(channel, text)
		b.rememberSent(channel, text)
	}
}

// capForIRC truncates a forwarded chat line to fit one PRIVMSG. The cap is
// a byte budget, but the cut backs up to a rune boundary — a byte slice
// through a multibyte rune manufactures invalid UTF-8 from a valid CJK
// string (the msg[:4000] lesson, in miniature).
func capForIRC(body string) string {
	if len(body) <= ircBodyCap {
		return body
	}
	cut := ircBodyCap
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	return body[:cut] + "…"
}

// pmReply sends a PRIVMSG to a single nick. Silently drops when
// disconnected.
func (b *IRCBotService) pmReply(to, msg string) {
	b.mu.Lock()
	client := b.client
	b.mu.Unlock()
	if client == nil {
		return
	}
	client.Cmd.Message(to, msg)
}

// rememberSent / recentlySent — bounded-cache anti-loop guard.
func (b *IRCBotService) rememberSent(target, body string) {
	b.sentMu.Lock()
	defer b.sentMu.Unlock()
	key := target + "|" + body
	b.sentCache = append(b.sentCache, key)
	if len(b.sentCache) > ircSentCacheSize {
		b.sentCache = b.sentCache[len(b.sentCache)-ircSentCacheSize:]
	}
}

func (b *IRCBotService) recentlySent(target, body string) bool {
	b.sentMu.Lock()
	defer b.sentMu.Unlock()
	key := target + "|" + body
	for _, k := range b.sentCache {
		if k == key {
			return true
		}
	}
	return false
}

func (b *IRCBotService) settingEnabled(ctx context.Context) bool {
	return b.ircSettingBool(ctx, "irc_enabled", false)
}
