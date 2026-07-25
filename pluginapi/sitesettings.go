package pluginapi

import "context"

// SiteSettings is the narrow slice of host business-config the agent
// subsystem reads at dispatch + completion time. The host implements it
// over its settings service; the agent plugin consumes it (via its Deps)
// so the plugin never imports the settings package or its ~40 other
// admin-tunable knobs.
//
// Deliberately NOT *services.SettingsService: the agent needs exactly two
// numbers off it, and dragging the whole service in would recouple the
// plugin to the host's config layer it exists to stay out of — the same
// stance ReleaseNotifier takes toward the Discord bot.
type SiteSettings interface {
	// CalculatePoints returns the upload reward for a fulfilled request,
	// given the release size (MB), the request's age (days), and its vote
	// count. It mirrors the host's points formula so the agent owner earns
	// an identical amount whether the award is computed here or on the
	// site's own upload paths.
	CalculatePoints(ctx context.Context, sizeMB float64, requestAgeDays, voteCount int) int

	// AgentMaxConcurrent is the site-wide default cap on an agent's
	// simultaneous in-flight locks, applied when a token carries no
	// per-agent override. Admin-tunable.
	AgentMaxConcurrent(ctx context.Context) int
}
