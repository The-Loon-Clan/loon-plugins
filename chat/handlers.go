package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon/httpclient"
)

// Handlers serves the chat API behind the /chat page.
type Handlers struct {
	// Simple per-user rate limiter: max 1 message per 2 seconds.
	rateMu  sync.Mutex
	rateMap map[int]time.Time
	// core is the mediator, for announcing sends. Nil in tests.
	core *core.Core
}

func NewHandlers() *Handlers {
	return &Handlers{rateMap: map[int]time.Time{}}
}

// Send posts a message to the Discord chat channel via the configured webhook.
// The webhook's username/avatar override makes it look like the site user posted
// natively in Discord.
func (h *Handlers) Send(c *gin.Context) {
	user := deps.Viewer(c)
	if user == nil {
		deps.JSONError(c, http.StatusUnauthorized, "not logged in")
		return
	}

	body := strings.TrimSpace(c.PostForm("message"))
	if body == "" {
		deps.JSONError(c, http.StatusBadRequest, "empty message")
		return
	}
	if len(body) > 2000 {
		deps.JSONError(c, http.StatusBadRequest, "message too long (max 2000 chars)")
		return
	}

	// Rate limit: 1 message per 2 seconds per user.
	h.rateMu.Lock()
	if last, ok := h.rateMap[user.ID]; ok && time.Since(last) < 2*time.Second {
		h.rateMu.Unlock()
		deps.JSONError(c, http.StatusTooManyRequests, "slow down")
		return
	}
	h.rateMap[user.ID] = time.Now()
	h.rateMu.Unlock()

	// Route to the channel the member is actually looking at.
	//
	// A Discord webhook is bound to ONE channel, so the single configured hook
	// put every message into that channel whatever the member had open. The
	// bridge now creates a webhook per channel, and this picks the right one.
	//
	// Falls back to the configured hook when the channel has none, so a guild
	// where the bot lacks MANAGE_WEBHOOKS keeps working exactly as it did.
	channelID := c.PostForm("channel")
	if channelID == "" {
		channelID = c.Query("channel")
	}
	webhookURL := ""
	if channelID != "" && deps.ChannelWebhook != nil {
		webhookURL = deps.ChannelWebhook(c.Request.Context(), channelID)
	}
	if webhookURL == "" {
		webhookURL = deps.WebhookURL(c.Request.Context())
	}
	if webhookURL == "" {
		deps.JSONError(c, http.StatusServiceUnavailable, "chat sending not configured")
		return
	}

	// Thread replies are the SAME webhook plus a thread_id — the parent
	// channel's hook addresses the thread, which is why chat_messages stores
	// both ids as a pair. Discord takes it as a query parameter, not in the body.
	sendURL := webhookURL + "?wait=true"
	if threadID := c.PostForm("thread"); threadID != "" {
		sendURL += "&thread_id=" + url.QueryEscape(threadID)
	}

	// Build the avatar URL for the webhook override.
	avatarURL := ""
	if user.AvatarPath != "" {
		avatarURL = deps.BaseURL + user.AvatarPath
	}

	// Discord webhook payload — the username and avatar_url overrides make
	// the message appear as if the site user posted it directly in Discord.
	// allowed_mentions: parse:[] prevents @everyone/@here abuse.
	payload, _ := json.Marshal(map[string]interface{}{
		"username":   user.Username,
		"avatar_url": avatarURL,
		"content":    body,
		"allowed_mentions": map[string]interface{}{
			"parse": []string{},
		},
	})

	// Discord webhooks are always discord.com — whitelist + SSRF-block so an
	// admin-configured webhook URL can't be pointed at internal/metadata IPs.
	client := httpclient.NewWhitelisted(10*time.Second, "discord.com", "discordapp.com")
	resp, err := client.Post(sendURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		deps.JSONError(c, http.StatusBadGateway, "failed to send to Discord")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		deps.JSONError(c, http.StatusBadGateway, fmt.Sprintf("Discord returned %d", resp.StatusCode))
		return
	}

	h.emit(c.Request.Context(), EventMessageSent, user.ID, MessageSent{Length: len(body)})
	deps.JSONOK(c, nil)
}

// Recent returns the last N messages as JSON. Used by the chat page on
// initial load before the SSE stream takes over for live updates.
func (h *Handlers) Recent(c *gin.Context) {
	// channel filters the stream; before pages backwards through it.
	//
	// The sidebar already linked ?channel=, and the page already changed its
	// header to match — but the messages never filtered, so selecting a channel
	// renamed the room and showed the same combined stream. That is worse than
	// having no filter, because it looks like it worked.
	//
	// Both are passed through as opaque strings and parsed at the storage
	// boundary. The channel id reaches a query parameter from a member, so the
	// one place that builds SQL should not be the place that trusts it.
	msgs, err := deps.Recent(c.Request.Context(), 100,
		c.Query("channel"), c.Query("before"))
	if err != nil {
		deps.JSONError(c, http.StatusInternalServerError, "failed to load history")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"messages": msgs,
		"online":   deps.OnlineCount(),
		"users":    deps.OnlineUsers(),
	})
}

// Online returns the current connected chat viewers.
func (h *Handlers) Online(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"online": deps.OnlineCount(),
		"users":  deps.OnlineUsers(),
	})
}

// Stream is a Server-Sent Events endpoint. The client opens an EventSource
// to /api/chat/stream and receives one event per new chat message. The
// connection stays open until the client disconnects or the server shuts
// down. Each subscriber is fanned out from the host's chat hub; cross-process
// delivery (worker bot → web SSE) goes through Redis pub/sub.
//
// In addition to chat messages, the stream sends an "online" event every
// 15 seconds with the current viewer count.
func (h *Handlers) Stream(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering
	c.Writer.Flush()

	username := ""
	if u := deps.Viewer(c); u != nil {
		username = u.Username
	}
	sub, cancel := deps.Subscribe(username)
	// Not optional: without this the host keeps fanning out to a channel
	// nobody reads, and the presence list keeps counting a viewer who left.
	defer cancel()

	onlineTicker := time.NewTicker(15 * time.Second)
	defer onlineTicker.Stop()

	// Send initial online data immediately.
	onlineData, _ := json.Marshal(gin.H{"count": deps.OnlineCount(), "users": deps.OnlineUsers()})
	fmt.Fprintf(c.Writer, "event: online\ndata: %s\n\n", onlineData)
	c.Writer.Flush()

	clientGone := c.Request.Context().Done()
	for {
		select {
		case <-clientGone:
			return
		case data, ok := <-sub:
			if !ok {
				return
			}
			// Already encoded by the host — the plugin never decoded it.
			fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			c.Writer.Flush()
		case <-onlineTicker.C:
			od, _ := json.Marshal(gin.H{"count": deps.OnlineCount(), "users": deps.OnlineUsers()})
			fmt.Fprintf(c.Writer, "event: online\ndata: %s\n\n", od)
			c.Writer.Flush()
		}
	}
}
