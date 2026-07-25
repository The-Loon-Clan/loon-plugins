package pluginapi

import "time"

// ReleaseNotifierName is the Core extension-registry key under which a chat
// bridge publishes its ReleaseNotifier. Consumers Lookup this name and
// type-assert the result to ReleaseNotifier.
const ReleaseNotifierName = "notify.release"

// ReleaseAnnouncement is what announcing a release actually needs: a title, a
// few badges, a size, and something to link and illustrate it with.
//
// Deliberately NOT models.Nzb. The row carries ~50 fields including HiddenAt,
// DeletedAt, UploaderID, MediaInfo blobs and a dozen external ids — none of
// which a chat bridge has any business seeing, and all of which would drag this
// package's contracts into the domain model it exists to stay out of. CoverURL
// is a string here because it is a method on the row: the host resolves it and
// passes the answer, rather than exporting the row so the plugin can ask.
type ReleaseAnnouncement struct {
	ID         int64
	Title      string
	Category   string
	Resolution string
	Source     string
	Size       int64
	CreatedAt  time.Time
	// CoverURL may be empty; publishers must render without an image.
	CoverURL string
}

// ReleaseNotifier announces a freshly indexed release to a chat bridge. The
// discord plugin publishes it; the host's agent handler consumes it when an
// agent completes an upload.
//
// This exists to unstrand the bot. The agent handler needed exactly one method
// off *services.DiscordBotService, and because web/handlers cannot import
// plugins/, that single call kept 893 lines of Discord bot in pkg/services —
// the plugin's own comment said the type lived there only "because the agent
// handler pushes completion notifications through it". One method holding a
// whole subsystem hostage.
//
// Note the direction: the host is the CONSUMER here, not a publisher. A host
// cannot declare Metadata.Requires, so it degrades rather than failing boot —
// no bridge configured means no announcement, which is correct for a
// notification and is why callers nil-check instead of asserting.
//
// Not named "discord": nothing about announcing a release is Discord-specific,
// and the IRC bridge could publish the same contract tomorrow. The name says
// what it does, not who does it.
type ReleaseNotifier interface {
	// NotifyRelease announces the release. Implementations MUST NOT block: the
	// agent handler calls this on the request path, so a publisher owns its own
	// goroutine and its own failures.
	NotifyRelease(a ReleaseAnnouncement)
}
