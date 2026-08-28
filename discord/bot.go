package discord

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon/schedule"
)

// DiscordBotService manages the Discord bot lifecycle, slash commands, role sync,
// and release notifications. Runs in the worker process.
type DiscordBotService struct {
	discordLinks LinkStore
	users        UserStore
	settings     Settings
	baseURL      string
	siteName     string
	chat         pluginapi.ChatHub                          // optional — set via SetChatHub
	createInvite func(context.Context, int) (string, error) // optional — set via SetCreateInvite
	display      pluginapi.GroupDisplay                     // optional — set via SetGroupDisplay
	job          *schedule.JobInfo

	mu      sync.Mutex
	session *discordgo.Session
	running bool

	// stopSync ends the role-sync loop on Shutdown. The loop is owned by
	// Start, not connect(): spawned per-connect it leaked one goroutine per
	// admin-triggered reconnect, and none of them ever exited.
	stopSync chan struct{}
	stopOnce sync.Once

	// reconnectMu serialises admin-triggered reconnects; see reconnect.
	reconnectMu sync.Mutex
}

// SetCreateInvite wires the invite seam so the /invite Discord command can
// generate invite codes for linked users.
func (d *DiscordBotService) SetCreateInvite(f func(context.Context, int) (string, error)) {
	d.createInvite = f
}

func NewDiscordBotService(links LinkStore, users UserStore, settings Settings, baseURL string) *DiscordBotService {
	return &DiscordBotService{discordLinks: links, users: users, settings: settings, baseURL: baseURL}
}

// SetGroupDisplay wires the badge capability, resolved from the extension
// registry at Start (ENTITLEMENTS.md Stage 3.4). Nil is normal: a host built
// without the ranks plugin syncs account roles and bridges chat with no rank
// label, which is the cosmetic degrade that contract mandates.
func (d *DiscordBotService) SetGroupDisplay(gd pluginapi.GroupDisplay) {
	d.mu.Lock()
	d.display = gd
	d.mu.Unlock()
}

// groupDisplay reads the capability under the lock.
func (d *DiscordBotService) groupDisplay() pluginapi.GroupDisplay {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.display
}

// badgesFor resolves a user's badges, absorbing both degraded cases: no
// capability in this process, and a failed read. Chat is a firehose, so a
// failure is not reported per message — the line carries no rank label and the
// next message tries again.
func (d *DiscordBotService) badgesFor(ctx context.Context, userID int) []pluginapi.Badge {
	gd := d.groupDisplay()
	if gd == nil {
		return nil
	}
	badges, err := gd.BadgesFor(ctx, int64(userID))
	if err != nil {
		return nil
	}
	return badges
}

// SetChatHub wires the chat bridge so MessageCreate events from the
// configured chat channel are published to the site's chat history.
func (d *DiscordBotService) SetChatHub(h pluginapi.ChatHub) {
	d.chat = h
}

// Start connects to Discord and begins the role sync loop.
// Registered as a job so logs are visible in the admin Jobs page.
func (d *DiscordBotService) Start() {
	d.job = schedule.RegisterService("Discord Bot", "Maintains a persistent WebSocket to Discord for chat bridge, slash commands, role sync, and release notifications.")
	d.job.SetTriggerAsync(func() { d.reconnect() })
	d.stopSync = make(chan struct{})
	go d.connect()
	go d.roleSyncLoop()
}

// roleSyncLoop reconciles Discord roles every five minutes for as long as the
// bot service lives. One loop per service lifetime, whatever reconnect does:
// syncRoles no-ops while disconnected, and Shutdown ends it.
func (d *DiscordBotService) roleSyncLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-d.stopSync:
			return
		case <-t.C:
			d.syncRoles()
		}
	}
}

func (d *DiscordBotService) connect() {
	ctx := context.Background()
	if !d.settings.GetDiscordEnabled(ctx) {
		d.job.Log("Disabled in admin settings — not connecting")
		d.job.SetProgress("Disabled")
		return
	}
	token := d.settings.GetDiscordBotToken(ctx)
	if token == "" {
		d.job.Log("No bot token configured")
		d.job.SetError("No bot token — configure in Admin > Settings > Discord")
		return
	}

	d.job.SetRunning()
	d.job.Log("Connecting to Discord...")

	session, err := discordgo.New("Bot " + token)
	if err != nil {
		d.job.Log("Failed to create session: %v", err)
		d.job.SetError(fmt.Sprintf("Session error: %v", err))
		return
	}

	// IntentsGuildMessages + IntentsMessageContent are required for the
	// chat bridge — without MessageContent the bot only sees message
	// metadata, not the actual text. MessageContent is a privileged intent
	// and must be enabled in the Discord Developer Portal under the bot's
	// settings; without that toggle the bot will fail to connect.
	session.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMembers |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsMessageContent
	session.AddHandler(d.handleInteraction)
	session.AddHandler(d.handleMessage)

	if err := session.Open(); err != nil {
		d.job.Log("Failed to connect: %v", err)
		d.job.SetError(fmt.Sprintf("Connect failed: %v", err))
		return
	}

	// Enumerate channels once the gateway is up. On connect rather than on a
	// timer: the list changes when an operator adds a channel, which is rare,
	// and a reconnect is exactly when the bridge should re-check what it can
	// see.
	go func() {
		if gid := d.settings.GetDiscordGuildID(context.Background()); gid != "" {
			d.syncChannels(context.Background(), session, gid)
		}
	}()

	d.mu.Lock()
	d.session = session
	d.running = true
	d.mu.Unlock()

	d.job.Log("Connected as %s", session.State.User.Username)
	d.job.SetProgress("Connected as %s", session.State.User.Username)

	// Register slash commands. The /verify text command was retired in
	// favour of a button + modal flow: an admin runs /setup-verify in a
	// channel, the bot posts a "Verify" button, and clicks open a modal
	// that takes the verification code. Keeps codes out of the message
	// transcript and matches the pattern most servers use.
	guildID := d.settings.GetDiscordGuildID(ctx)
	manageGuildPerm := int64(discordgo.PermissionManageServer)
	commands := []*discordgo.ApplicationCommand{
		{
			Name:                     "setup-verify",
			Description:              "Post the Verify button in this channel (admin only)",
			DefaultMemberPermissions: &manageGuildPerm,
		},
		{
			Name:        "invite",
			Description: "Generate a site invite code (uses one of your invites)",
		},
	}
	for _, cmd := range commands {
		if _, err := session.ApplicationCommandCreate(session.State.User.ID, guildID, cmd); err != nil {
			d.job.Log("Failed to register /%s command: %v", cmd.Name, err)
		}
	}
	d.job.Log("Registered %d slash commands (guild %s)", len(commands), guildID)

	// Retire the old /verify slash command — its arg-typed shape leaked
	// codes into the channel transcript. The button+modal flow that
	// replaces it has the code in an ephemeral modal that Discord
	// doesn't log or scrollback. Best-effort: ignore errors so a fresh
	// install (where /verify never existed) doesn't log spurious
	// failures.
	if existing, err := session.ApplicationCommands(session.State.User.ID, guildID); err == nil {
		for _, cmd := range existing {
			if cmd.Name == "verify" {
				if err := session.ApplicationCommandDelete(session.State.User.ID, guildID, cmd.ID); err == nil {
					d.job.Log("Deleted obsolete /verify slash command")
				}
			}
		}
	}

	chatChannel := d.settings.GetDiscordChatChannelID(ctx)
	if chatChannel != "" {
		d.job.Log("Chat bridge active — listening on channel %s", chatChannel)
	} else {
		d.job.Log("Chat channel not configured — chat bridge disabled")
	}

	d.job.SetIdle(time.Time{}) // idle with no next-run — persistent service
	d.job.SetProgress("Connected as %s", session.State.User.Username)

	// Backfill recent chat history from Discord so site users see messages
	// from before the bot connected.
	if d.chat != nil {
		if gid := d.settings.GetDiscordGuildID(context.Background()); gid != "" {
			go d.backfillChatHistory(session, gid)
		}
	}
}

// backfillChatHistory walks every visible channel and fills in history.
//
// The original fetched 50 messages from ONE configured channel and gave up if
// the Redis ring already held anything — it existed to seed the ring on a cold
// start, not to build history. Now that the bridge mirrors every channel and
// keeps a durable copy in Postgres, the ring is the wrong thing to check: it is
// emptied by any Redis restart and holds only the most recent chatRingSize
// messages regardless.
//
// It also carried its OWN copy of the message conversion, which by this point
// had drifted: it set neither ChannelID nor Public, so every message it wrote
// would have been stored with public=false and been invisible on the page. That
// copy is gone — both paths now go through buildChatMessage, which is the only
// way they stay honest about each other.
//
// Stops per channel at the first message already stored. Discord returns newest
// first, so walking backwards means the first hit is the boundary of what we
// have; everything older was fetched on a previous run.
func (d *DiscordBotService) backfillChatHistory(s *discordgo.Session, guildID string) {
	ctx := context.Background()
	if d.chat == nil || guildID == "" {
		return
	}
	chans, err := s.GuildChannels(guildID)
	if err != nil {
		d.job.Log("Chat history: cannot list channels: %v", err)
		return
	}
	memberRole := d.settings.GetDiscordMemberRoleID(ctx)

	total, skipped := 0, 0
	for _, ch := range chans {
		if ch == nil {
			continue
		}
		if ch.Type != discordgo.ChannelTypeGuildText && ch.Type != discordgo.ChannelTypeGuildNews {
			continue
		}
		// Only channels a member could see. Backfilling a staff channel would
		// write history nobody may read — and the whole point of storing it is
		// that somebody can.
		if !memberCanView(s, guildID, memberRole, ch.ID) {
			skipped++
			continue
		}
		n := d.backfillChannel(ctx, s, guildID, ch)
		total += n
	}
	d.job.Log("Chat history: backfilled %d message(s) across %d channel(s), %d skipped as not member-visible",
		total, len(chans)-skipped, skipped)
}

// backfillChannelPages bounds one channel's walk.
//
// 100 is Discord's per-request maximum, so 20 pages is 2,000 messages per
// channel per run. A cap rather than "until the beginning of time" because this
// runs on connect: an unbounded first run against a busy channel would spend
// thousands of API calls before the bridge did anything else, and the next run
// resumes from where this one stopped anyway.
const (
	backfillChannelPages = 20
	backfillPageSize     = 100
)

func (d *DiscordBotService) backfillChannel(ctx context.Context, s *discordgo.Session,
	guildID string, ch *discordgo.Channel) int {
	inserted := 0
	before := ""
	for page := 0; page < backfillChannelPages; page++ {
		msgs, err := s.ChannelMessages(ch.ID, backfillPageSize, before, "", "")
		if err != nil {
			// Missing Access on a channel the permission check thought was
			// visible is not fatal to the whole sweep — log and move on.
			d.job.Log("Chat history: %s: %v", ch.Name, err)
			return inserted
		}
		if len(msgs) == 0 {
			return inserted
		}
		// Newest first from Discord; walk oldest-first so the durable copy is
		// written in the order it was said.
		for i := len(msgs) - 1; i >= 0; i-- {
			cm, ok := d.buildChatMessage(ctx, s, msgs[i], guildID)
			if !ok {
				continue
			}
			// PublishHistory, not Publish: these are old messages. Ringing them
			// would push live chat out of the buffer and fan them out to every
			// open SSE stream as if they had just been said.
			if err := d.chat.PublishHistory(ctx, cm); err != nil {
				d.job.Log("Chat history: store failed for %s: %v", cm.ID, err)
				continue
			}
			inserted++
		}
		before = msgs[len(msgs)-1].ID
	}
	return inserted
}

// reconnect disconnects (if connected) and reconnects. Used by the admin
// "Trigger" button to pick up settings changes without restarting the worker.
// The role-sync loop is untouched — it belongs to the service, not the
// connection. Serialised: two concurrent triggers used to each open a live
// session, and the orphaned one doubled every bridged message. Also refuses
// to resurrect the connection after Shutdown.
func (d *DiscordBotService) reconnect() {
	d.reconnectMu.Lock()
	defer d.reconnectMu.Unlock()
	select {
	case <-d.stopSync:
		return // shut down — a trigger racing SIGTERM must not reconnect
	default:
	}
	d.job.Log("Reconnecting (triggered by admin)...")
	d.Stop()
	time.Sleep(2 * time.Second)
	d.connect()
}

// Stop cleanly disconnects the bot. Reconnect-safe: the service keeps
// running (role-sync loop, job registration) — Shutdown is the full stop.
func (d *DiscordBotService) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.session != nil {
		d.session.Close()
		d.session = nil
		d.running = false
		log.Println("discord bot: disconnected")
	}
}

// Shutdown disconnects AND ends the role-sync loop. Called from the plugin's
// Stop on process shutdown.
func (d *DiscordBotService) Shutdown() {
	d.stopOnce.Do(func() {
		if d.stopSync != nil {
			close(d.stopSync)
		}
	})
	d.Stop()
}

// handleInteraction processes slash command interactions.
// Component custom IDs. Stable identifiers Discord echoes back so we
// can route a button click or modal submission to the right handler.
const (
	verifyButtonID = "verify_button"
	verifyModalID  = "verify_modal"
	verifyInputID  = "verify_code"
)

func (d *DiscordBotService) handleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		data := i.ApplicationCommandData()
		switch data.Name {
		case "setup-verify":
			d.handleSetupVerify(s, i)
		case "invite":
			d.handleInvite(s, i)
		}
	case discordgo.InteractionMessageComponent:
		switch i.MessageComponentData().CustomID {
		case verifyButtonID:
			d.handleVerifyButton(s, i)
		}
	case discordgo.InteractionModalSubmit:
		switch i.ModalSubmitData().CustomID {
		case verifyModalID:
			d.handleVerifyModalSubmit(s, i)
		}
	}
}

// handleSetupVerify (re)posts the Verify button into the channel
// configured in admin settings as Discord Verify Channel ID. Wipes
// prior bot posts in that channel first so successive runs after
// copy/button edits don't pile up duplicates — operator can re-run
// /setup-verify as many times as they like and the channel always
// ends up with exactly one bot post.
//
// Gated to ManageServer via DefaultMemberPermissions on the command;
// also requires the verify channel to be configured.
func (d *DiscordBotService) handleSetupVerify(s *discordgo.Session, i *discordgo.InteractionCreate) {
	ctx := context.Background()
	channelID := d.settings.GetDiscordVerifyChannelID(ctx)
	if channelID == "" {
		d.respond(s, i, "Verify channel isn't configured. Set it on /admin/settings → Discord → Verify Channel ID, then run this command again.")
		return
	}

	// Purge our own prior posts in the channel. Discord's bulk-delete
	// endpoint requires Manage Messages and only handles messages
	// younger than 14 days; we fall back to single-message deletes
	// for anything older. Either way we only touch messages authored
	// by this bot — never user content — so a misconfigured channel
	// can't accidentally nuke conversation.
	deleted, err := d.purgeOwnMessages(s, channelID)
	if err != nil {
		// Best-effort: the post still happens, but warn the operator if
		// cleanup didn't run so they can notice stale messages. The error
		// text itself stays in the job log — ManageServer gates this command
		// only by DEFAULT, and a guild admin can loosen that per-role.
		d.respond(s, i, "Posted, but couldn't fully clear prior bot messages — see the Discord Bot job log.")
		d.job.Log("setup-verify: purge of prior bot messages failed: %v", err)
	}

	embed := &discordgo.MessageEmbed{
		Title: d.site(),
		Description: "**To gain access to the server, click the button below!**\n\n" +
			"_Find your verify code on your Profile page after creating your " +
			d.site() + " account._",
		Color: 0x6f42c1, // brand purple
	}
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Verify",
					Style:    discordgo.PrimaryButton,
					CustomID: verifyButtonID,
					Emoji: &discordgo.ComponentEmoji{
						Name: "✅",
					},
				},
			},
		},
	}
	if _, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Embeds:     []*discordgo.MessageEmbed{embed},
		Components: components,
	}); err != nil {
		d.respond(s, i, "Failed to post the Verify message — see the Discord Bot job log.")
		d.job.Log("setup-verify: posting the Verify message failed: %v", err)
		return
	}
	d.respond(s, i, fmt.Sprintf("Verify message posted in <#%s> (cleared %d prior bot message(s)).", channelID, deleted))
}

// purgeOwnMessages deletes every message authored by this bot in the
// given channel. Walks pages of 100 newest-first, filtering by
// Author.ID == bot's user, and uses bulk-delete for batches < 14
// days old (Discord constraint), single-message delete for older.
// Stops once a page returns no own-messages. Returns the count
// removed.
func (d *DiscordBotService) purgeOwnMessages(s *discordgo.Session, channelID string) (int, error) {
	botID := s.State.User.ID
	twoWeeksAgo := time.Now().Add(-14 * 24 * time.Hour)
	total := 0
	beforeID := ""
	for {
		batch, err := s.ChannelMessages(channelID, 100, beforeID, "", "")
		if err != nil {
			return total, err
		}
		if len(batch) == 0 {
			break
		}
		var bulkRecent []string
		var oldOwn []string
		for _, m := range batch {
			if m.Author == nil || m.Author.ID != botID {
				continue
			}
			if m.Timestamp.After(twoWeeksAgo) {
				bulkRecent = append(bulkRecent, m.ID)
			} else {
				oldOwn = append(oldOwn, m.ID)
			}
		}
		if len(bulkRecent) >= 2 {
			if err := s.ChannelMessagesBulkDelete(channelID, bulkRecent); err != nil {
				return total, fmt.Errorf("bulk delete: %w", err)
			}
			total += len(bulkRecent)
		} else if len(bulkRecent) == 1 {
			if err := s.ChannelMessageDelete(channelID, bulkRecent[0]); err == nil {
				total++
			}
		}
		for _, id := range oldOwn {
			if err := s.ChannelMessageDelete(channelID, id); err == nil {
				total++
			}
		}
		// Page back: paginate using the oldest message in the batch.
		// Stop when we've walked an entire page with no own-messages
		// — anything past that is older history we don't care about
		// (nothing to delete, and no need to keep paging forever).
		if len(bulkRecent) == 0 && len(oldOwn) == 0 {
			break
		}
		beforeID = batch[len(batch)-1].ID
		if len(batch) < 100 {
			break
		}
	}
	return total, nil
}

// handleVerifyButton answers a click on the "Verify" button by
// opening a modal. The modal lives entirely in the user's client —
// the typed code never appears in the channel transcript.
func (d *DiscordBotService) handleVerifyButton(s *discordgo.Session, i *discordgo.InteractionCreate) {
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: verifyModalID,
			Title:    "Verify",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    verifyInputID,
							Label:       "Verification Code",
							Style:       discordgo.TextInputShort,
							Placeholder: "Please input your verification code from the site!",
							Required:    true,
							MinLength:   4,
							MaxLength:   64,
						},
					},
				},
			},
		},
	})
	if err != nil {
		d.job.Log("Failed to open verify modal: %v", err)
	}
}

// handleVerifyModalSubmit runs the same account-link logic the old
// /verify slash command used, but receives the code from a modal
// field instead of a slash-command argument.
func (d *DiscordBotService) handleVerifyModalSubmit(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ModalSubmitData()
	var key string
	for _, row := range data.Components {
		ar, ok := row.(*discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, c := range ar.Components {
			if input, ok := c.(*discordgo.TextInput); ok && input.CustomID == verifyInputID {
				key = strings.TrimSpace(input.Value)
			}
		}
	}
	if key == "" {
		d.respond(s, i, "No verification code received. Click Verify and try again.")
		return
	}

	ctx := context.Background()
	_, user, err := d.discordLinks.GetDiscordLinkByToken(ctx, key)
	if err != nil || user == nil {
		d.respond(s, i, "Invalid or expired verification code. Check your Profile page for the current code.")
		return
	}

	if i.Member == nil || i.Member.User == nil {
		d.respond(s, i, "Could not identify your Discord account. Please try again.")
		return
	}
	discordUser := i.Member.User
	if err := d.discordLinks.CreateDiscordLink(ctx, user.ID, discordUser.ID, discordUser.Username); err != nil {
		d.respond(s, i, "Failed to link account. Please try again.")
		return
	}

	// One-shot token: clear it so a leaked code can't be reused.
	_ = d.discordLinks.SetDiscordVerifyToken(ctx, user.ID, "")

	d.respond(s, i, fmt.Sprintf("Successfully linked to **%s**! Your rank roles will sync shortly.", user.Username))

	// Sync roles immediately for this user.
	go d.syncRolesForUser(discordUser.ID)
}

// handleInvite generates a site invite code for a Discord user whose account
// is linked. The code is returned as an ephemeral reply (only visible to the
// invoker). Uses one of the user's available invite slots.
func (d *DiscordBotService) handleInvite(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if d.createInvite == nil {
		d.respond(s, i, "Invite system is not available.")
		return
	}
	if i.Member == nil || i.Member.User == nil {
		d.respond(s, i, "Could not identify your Discord account.")
		return
	}

	ctx := context.Background()
	discordID := i.Member.User.ID

	// Look up the linked site account.
	link, err := d.discordLinks.GetDiscordLinkByDiscordID(ctx, discordID)
	if err != nil || link == nil {
		d.respond(s, i, "Your Discord account is not linked to a "+d.site()+" account. Click the **Verify** button in the verification channel first.")
		return
	}

	token, err := d.createInvite(ctx, link.UserID)
	if err != nil {
		if errors.Is(err, ErrNoInvites) {
			d.respond(s, i, "You have no invite codes remaining. Earn more by contributing!")
		} else {
			// Internal error text stays server-side; the host used to echo
			// err.Error() into the Discord reply.
			d.respond(s, i, "Failed to create invite — please try again later.")
			d.job.Log("Invite create failed for site user %d: %v", link.UserID, err)
		}
		return
	}

	registerURL := fmt.Sprintf("%s/register?invite=%s", d.baseURL, token)
	d.respond(s, i, fmt.Sprintf(
		"Here's your invite code: `%s`\n\nShare it or send this link:\n%s\n\nThis code can be used once.",
		token, registerURL))

	if d.job != nil {
		d.job.Log("Invite code generated by Discord user %s (site user %d)", i.Member.User.Username, link.UserID)
	}
}

// handleMessage bridges a Discord chat message into the site's chat history
// when it lands in the configured chat channel. Bot/webhook echoes are
// included so site users see the messages they themselves sent (which we
// post via webhook in Phase 2).
func (d *DiscordBotService) handleMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	if d.chat == nil {
		return
	}
	ctx := context.Background()
	// EVERY channel the bot can see, not one configured id.
	//
	// This used to be `m.ChannelID != chatChannel { return }`, which dropped
	// everything said in the guild except one room — including help-desk threads
	// members were waiting in, which never reached the site or the database and
	// so were invisible to anything counting who needed a reply.
	//
	// Visibility comes from Discord instead of from configuration: @everyone can
	// read it there, members can read it here. See channels.go for why that is
	// the rule and not an allowlist.
	guildID := m.GuildID
	if guildID == "" {
		return // DM or group DM: not the guild's conversation, not mirrored
	}
	cm, ok := d.buildChatMessage(ctx, s, m.Message, guildID)
	if !ok {
		return
	}
	if err := d.chat.Publish(ctx, cm); err != nil {
		log.Printf("discord bot: chat publish failed: %v", err)
	}
}

// respond sends an ephemeral reply to a slash command.
func (d *DiscordBotService) respond(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: msg,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

// NotifyRelease posts a new release embed to the configured channel.
// NotifyRelease implements pluginapi.ReleaseNotifier.
func (d *DiscordBotService) NotifyRelease(nzb pluginapi.ReleaseAnnouncement) {
	d.mu.Lock()
	s := d.session
	d.mu.Unlock()
	if s == nil {
		return
	}

	ctx := context.Background()
	channelID := d.settings.GetDiscordReleasesChannelID(ctx)
	if channelID == "" {
		return
	}

	// Build description.
	var desc strings.Builder
	if nzb.Category != "" {
		desc.WriteString("**Category:** " + nzb.Category + "\n")
	}
	if nzb.Resolution != "" || nzb.Source != "" {
		parts := []string{}
		if nzb.Resolution != "" {
			parts = append(parts, nzb.Resolution)
		}
		if nzb.Source != "" {
			parts = append(parts, nzb.Source)
		}
		desc.WriteString(strings.Join(parts, " | ") + "\n")
	}
	if nzb.Size > 0 {
		desc.WriteString(fmt.Sprintf("**Size:** %.2f GB\n", float64(nzb.Size)/1073741824))
	}

	url := fmt.Sprintf("%s/release/%d", d.baseURL, nzb.ID)

	embed := &discordgo.MessageEmbed{
		Title:       nzb.Title,
		URL:         url,
		Description: desc.String(),
		Color:       0x5b8af5,
		Footer: &discordgo.MessageEmbedFooter{
			Text: d.site(),
		},
		Timestamp: nzb.CreatedAt.Format(time.RFC3339),
	}

	// Add cover image if available.
	if cover := nzb.CoverURL; cover != "" && strings.HasPrefix(cover, "/") {
		embed.Thumbnail = &discordgo.MessageEmbedThumbnail{
			URL: d.baseURL + cover,
		}
	}

	_, err := s.ChannelMessageSendEmbed(channelID, embed)
	if err != nil {
		log.Printf("discord bot: failed to send release notification: %v", err)
	}
}

// NotifyOps posts an operational digest to the configured ops channel.
// NotifyOps implements pluginapi.OpsNotifier: it must not block, so the send
// runs on its own goroutine and owns its own failures.
func (d *DiscordBotService) NotifyOps(title, body string) {
	d.mu.Lock()
	s := d.session
	d.mu.Unlock()
	if s == nil {
		return
	}
	channelID := d.settings.GetDiscordOpsChannelID(context.Background())
	if channelID == "" {
		return
	}
	go func() {
		embed := &discordgo.MessageEmbed{
			Title:       title,
			Description: body,
			Color:       0x8a5bf5,
			Footer:      &discordgo.MessageEmbedFooter{Text: d.site() + " ops"},
			Timestamp:   time.Now().Format(time.RFC3339),
		}
		if _, err := s.ChannelMessageSendEmbed(channelID, embed); err != nil {
			log.Printf("discord bot: failed to send ops digest: %v", err)
		}
	}()
}

// syncRoles updates Discord roles for all linked users based on their active rank.
func (d *DiscordBotService) syncRoles() {
	d.mu.Lock()
	s := d.session
	d.mu.Unlock()
	if s == nil {
		return
	}

	ctx := context.Background()
	guildID := d.settings.GetDiscordGuildID(ctx)
	if guildID == "" {
		return
	}

	links, err := d.discordLinks.GetDiscordLinksWithRoles(ctx)
	if err != nil {
		log.Printf("discord bot: role sync query failed: %v", err)
		return
	}

	// Two independent role-management axes — group membership (whatever the
	// groups catalog holds) and account role (contributor / moderator / admin,
	// a fixed enum). Each axis is reconciled separately so an admin who also
	// holds a paid rank keeps both roles.
	rankRoles := d.getRankRoleMap(ctx)
	rankSlugs := d.rankSlugsFor(ctx, links)
	accountRoles := d.getAccountRoleMap(ctx)
	memberRoleID := d.settings.GetDiscordMemberRoleID(ctx)
	rankRoleIDs := nonEmptyRoleIDs(rankRoles)
	accountRoleIDs := nonEmptyRoleIDs(accountRoles)

	if len(rankRoleIDs) == 0 && len(accountRoleIDs) == 0 && memberRoleID == "" {
		return // no roles configured at all
	}

	for _, link := range links {
		member, err := s.GuildMember(guildID, link.DiscordID)
		if err != nil {
			continue // user left the server
		}

		// Baseline member role: every linked user gets it, regardless
		// of rank or role. Added if missing, never removed by this
		// loop — stripping it would lock the user out of the server.
		if memberRoleID != "" {
			has := false
			for _, mr := range member.Roles {
				if mr == memberRoleID {
					has = true
					break
				}
			}
			if !has {
				_ = s.GuildMemberRoleAdd(guildID, link.DiscordID, memberRoleID)
			}
		}

		// Axis 1: group membership. No badge → no visible group, so every
		// paid-rank role gets removed.
		d.reconcileAxis(s, guildID, link.DiscordID, member.Roles,
			rankRoles[rankSlugs[link.UserID]], rankRoleIDs)

		// Axis 2: account role. "user" maps to no Discord role on
		// this axis (baseline Member role above is sufficient);
		// "banned" / "disabled" / "" all fall through with empty
		// target so any prior contributor/mod/admin role gets
		// stripped — matches the site-side authority shift.
		d.reconcileAxis(s, guildID, link.DiscordID, member.Roles,
			accountRoles[strings.ToLower(link.AccountRole)], accountRoleIDs)
	}
}

// nonEmptyRoleIDs converts a name→role-id map to a set of just the
// non-empty role IDs — what reconcileAxis needs to know which roles
// are "owned" by this axis (and therefore eligible for removal when
// the user shouldn't have them).
func nonEmptyRoleIDs(m map[string]string) map[string]bool {
	out := make(map[string]bool, len(m))
	for _, rid := range m {
		if rid != "" {
			out[rid] = true
		}
	}
	return out
}

// syncRolesForUser syncs roles for a single user immediately.
func (d *DiscordBotService) syncRolesForUser(discordID string) {
	d.mu.Lock()
	s := d.session
	d.mu.Unlock()
	if s == nil {
		return
	}

	ctx := context.Background()
	guildID := d.settings.GetDiscordGuildID(ctx)
	if guildID == "" {
		return
	}

	links, err := d.discordLinks.GetDiscordLinksWithRoles(ctx)
	if err != nil {
		return
	}

	rankRoles := d.getRankRoleMap(ctx)
	rankSlugs := d.rankSlugsFor(ctx, links)
	accountRoles := d.getAccountRoleMap(ctx)
	memberRoleID := d.settings.GetDiscordMemberRoleID(ctx)
	rankRoleIDs := nonEmptyRoleIDs(rankRoles)
	accountRoleIDs := nonEmptyRoleIDs(accountRoles)

	for _, link := range links {
		if link.DiscordID != discordID {
			continue
		}
		member, err := s.GuildMember(guildID, discordID)
		if err != nil {
			return
		}
		// Baseline member role first — gating access to the rest of
		// the server. Added on link, never removed here.
		if memberRoleID != "" {
			has := false
			for _, mr := range member.Roles {
				if mr == memberRoleID {
					has = true
					break
				}
			}
			if !has {
				_ = s.GuildMemberRoleAdd(guildID, discordID, memberRoleID)
			}
		}
		// Reconcile both axes (paid rank + account role) — same
		// helper as the batch sync, kept identical so behaviour
		// doesn't drift between manual and scheduled sync.
		d.reconcileAxis(s, guildID, discordID, member.Roles,
			rankRoles[rankSlugs[link.UserID]], rankRoleIDs)
		d.reconcileAxis(s, guildID, discordID, member.Roles,
			accountRoles[strings.ToLower(link.AccountRole)], accountRoleIDs)
		return
	}
}

// getRankRoleMap returns the Discord role id configured for each group, keyed
// by group SLUG.
//
// Built from the group catalog, not a literal. It used to hardcode the four
// ranks that happened to exist — kirisame/shigure/samidare/arashi — so a fifth
// rank created later could never sync to Discord, silently: the user gets the
// rank, pays for it, and Discord never hears.
//
// Keyed by slug rather than lowercased name since Stage 3.4. Slugs survive a
// rename and names do not, so renaming a tier used to orphan its Discord role
// exactly as quietly: the old settings key kept the role id, the bot looked up
// the new name and found nothing. Host migration 280 re-keys the settings.
//
// An unconfigured group maps to "", which reconcileAxis already reads as "no
// role on this axis" — so a new group is simply not synced until an admin fills
// its field, rather than being unrepresentable.
func (d *DiscordBotService) getRankRoleMap(ctx context.Context) map[string]string {
	gd := d.groupDisplay()
	if gd == nil {
		return nil // no ranks plugin in this process: account roles still sync
	}
	groups, err := gd.Catalog(ctx)
	if err != nil {
		// Nil is the safe failure: nonEmptyRoleIDs yields an empty set, and
		// reconcileAxis only ever touches roles IN that set — so this sync pass
		// adds and removes nothing rather than acting on a half-known world.
		// The caller already returns early when no roles resolve.
		log.Printf("discord: rank role map: Catalog: %v", err)
		return nil
	}
	m := make(map[string]string, len(groups))
	for _, g := range groups {
		m[g.Slug] = d.settings.GetDiscordRoleID(ctx, g.Slug)
	}
	return m
}

// rankSlugsFor resolves the badge slug to use for each linked user's rank axis,
// in one batch call rather than one per user — this runs over every linked
// member of the guild. A user with no visible group is absent from the map,
// which reconcileAxis reads as "strip every paid-rank role".
func (d *DiscordBotService) rankSlugsFor(ctx context.Context, links []LinkWithRole) map[int]string {
	gd := d.groupDisplay()
	if gd == nil || len(links) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(links))
	for _, l := range links {
		ids = append(ids, int64(l.UserID))
	}
	byUser, err := gd.BadgesForBatch(ctx, ids)
	if err != nil {
		log.Printf("discord: rank slugs: BadgesForBatch: %v", err)
		return nil
	}
	out := make(map[int]string, len(byUser))
	for uid, badges := range byUser {
		if len(badges) > 0 {
			out[int(uid)] = badges[0].Slug
		}
	}
	return out
}

// getAccountRoleMap returns the Discord role IDs configured for each
// site account role. Plain "user" maps to no role here — the baseline
// Member role is granted on link and isn't part of this axis. Banned /
// disabled users have no Discord role assigned (the unlink path strips
// access separately).
func (d *DiscordBotService) getAccountRoleMap(ctx context.Context) map[string]string {
	return map[string]string{
		"contributor": d.settings.GetDiscordRoleID(ctx, "contributor"),
		"mod":         d.settings.GetDiscordRoleID(ctx, "moderator"),
		"admin":       d.settings.GetDiscordRoleID(ctx, "admin"),
	}
}

// reconcileAxis applies one role-management axis to a Discord member.
// targetRoleID is the role the user *should* have on this axis (empty
// = none); allRoleIDs is the full set of roles owned by the axis (so
// the user gets stripped of every other one). Member role is handled
// outside — never touched by this routine.
//
// Both axes use this so the per-user and per-batch sync paths share
// the same logic and stay in step when behaviour evolves.
func (d *DiscordBotService) reconcileAxis(s *discordgo.Session, guildID, discordID string, memberRoles []string, targetRoleID string, allRoleIDs map[string]bool) {
	for roleID := range allRoleIDs {
		hasRole := false
		for _, mr := range memberRoles {
			if mr == roleID {
				hasRole = true
				break
			}
		}
		if roleID == targetRoleID && !hasRole {
			_ = s.GuildMemberRoleAdd(guildID, discordID, roleID)
		} else if roleID != targetRoleID && hasRole {
			_ = s.GuildMemberRoleRemove(guildID, discordID, roleID)
		}
	}
}

// The agent handler consumes this through the contract, never the type — the
// host looks it up under pluginapi.ReleaseNotifierName after Boot. Asserted
// here so a signature drift fails the build in the package that owns the
// implementation, not at a Lookup in the host.
var _ pluginapi.ReleaseNotifier = (*DiscordBotService)(nil)

// buildChatMessage converts a Discord message into the site's envelope.
//
// ONE conversion, shared by the live handler and the history backfill. They each
// had their own copy, and the copies had already drifted: the backfill's set
// neither ChannelID nor Public, so with the visibility gate in place every
// message it wrote would have been stored private and never shown. Two copies of
// "what a message means" is the shape of that bug, so there is now one.
//
// ok is false for messages that must not be mirrored at all: the bot's own
// posts, and empty bodies — an attachment-only post carries no text to display.
func (d *DiscordBotService) buildChatMessage(ctx context.Context, s *discordgo.Session,
	m *discordgo.Message, guildID string) (pluginapi.ChatMessage, bool) {
	if m == nil || m.Author == nil {
		return pluginapi.ChatMessage{}, false
	}
	// The bot's own posts: release notifications and slash-command replies.
	// Webhook messages DO come through, deliberately — those are the site's own
	// messages echoing back, and they carry a different author id.
	if s.State != nil && s.State.User != nil && m.Author.ID == s.State.User.ID {
		return pluginapi.ChatMessage{}, false
	}
	body := strings.TrimSpace(m.Content)
	if body == "" {
		return pluginapi.ChatMessage{}, false
	}

	memberRole := d.settings.GetDiscordMemberRoleID(ctx)
	public := memberCanView(s, guildID, memberRole, m.ChannelID)
	channelID, channelName, threadID, threadName := channelDisplay(s, m.ChannelID)

	displayName := m.Author.GlobalName
	if displayName == "" {
		displayName = m.Author.Username
	}
	source := "discord"
	if m.WebhookID != "" {
		source = "site"
		displayName = m.Author.Username
	}

	avatarURL := m.Author.AvatarURL("64")
	role, rankName, rankColor := "", "", ""
	if link, err := d.discordLinks.GetDiscordLinkByDiscordID(ctx, m.Author.ID); err == nil && link != nil {
		if siteUser, err := d.users.GetUserByID(ctx, link.UserID); err == nil && siteUser != nil {
			role = siteUser.Role
			if siteUser.AvatarPath != "" {
				avatarURL = d.baseURL + siteUser.AvatarPath
			}
			displayName = siteUser.Username
			if badges := d.badgesFor(ctx, siteUser.ID); len(badges) > 0 {
				rankName = badges[0].Name
				rankColor = badges[0].TitleColor
			}
		}
	}

	return pluginapi.ChatMessage{
		ID:          m.ID,
		Author:      displayName,
		AvatarURL:   avatarURL,
		Body:        body,
		At:          m.Timestamp,
		Source:      source,
		Role:        role,
		RankName:    rankName,
		RankColor:   rankColor,
		ChannelID:   channelID,
		ChannelName: channelName,
		ThreadID:    threadID,
		ThreadName:  threadName,
		Public:      public,
	}, true
}

// SetSiteName supplies what this deployment calls itself, for the strings this
// bot sends to people who are not on it. Optional, like SetChatHub beside it.
func (d *DiscordBotService) SetSiteName(name string) { d.siteName = strings.TrimSpace(name) }

// site is the deployment's name, or the host part of its base URL.
//
// The fallback is a host name rather than a phrase like "this site", because
// every caller is read by somebody elsewhere — in another server's channel, or
// in a stranger's Discord — where "this site" says nothing at all.
func (d *DiscordBotService) site() string {
	if d.siteName != "" {
		return d.siteName
	}
	if u, err := url.Parse(d.baseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "this indexer"
}
