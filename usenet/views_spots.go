package usenet

// The Spots tab: what Spotnet support currently is, and what the live index
// looks like.
//
// The honest framing matters more than the numbers here. The reader and the
// verifier exist; nothing imports spots yet. A status page that showed only
// "spots imported: 0" would read as a broken feature rather than an unfinished
// one, so this leads with what IS wired and what is not, and fills the rest
// with a probe whose answers are real today.

import (
	"context"
	"encoding/json"
	"html/template"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// spotProbeKey stores the last probe in the plugin's settings table, so the
// tab shows a result rather than an empty card until someone clicks. Probing
// on page load would spend connections the crawler is already short of.
const spotProbeKey = "spot_last_probe"

func (p *Plugin) lastSpotProbe(ctx context.Context) (spotProbe, bool) {
	s, err := p.st.getSettings(ctx)
	if err != nil {
		return spotProbe{}, false
	}
	raw := s[spotProbeKey]
	if raw == "" {
		return spotProbe{}, false
	}
	var pr spotProbe
	if err := json.Unmarshal([]byte(raw), &pr); err != nil {
		return spotProbe{}, false
	}
	return pr, true
}

func (p *Plugin) renderSpots(ctx context.Context) (template.HTML, error) {
	probe, have := p.lastSpotProbe(ctx)
	// Whether a provider exists to probe WITH. Without one the button can only
	// fail, and an error flash is a worse explanation than a disabled button.
	var haveProvider bool
	if servers, err := p.st.listServers(ctx); err == nil {
		for _, s := range servers {
			if s.Enabled {
				haveProvider = true
				break
			}
		}
	}
	// Best-effort, both of them: a counts query that fails should not blank a
	// page whose other half explains how to turn the feature on.
	counts, cerr := p.st.countSpots(ctx)
	if cerr != nil {
		p.reportErr(ctx, "usenet/spot-counts", cerr)
	}
	groups, gerr := p.st.spotGroups(ctx)
	if gerr != nil {
		p.reportErr(ctx, "usenet/spot-groups-view", gerr)
	}
	return p.frag("spots.html", map[string]any{
		"Group":        SpotGroup,
		"MinKeyBits":   MinSpotKeyBits,
		"Probe":        probe,
		"HaveProbe":    have,
		"ProbeAge":     humanSince(probe.At),
		"HaveProvider": haveProvider,
		"Counts":       counts,
		"Groups":       spotGroupVMs(groups),
		"Indexing":     len(groups) > 0,
		// The fetch pass is the missing half. Stated as data rather than prose
		// so that wiring it later flips one boolean.
		"FetchWired": false,
	})
}

// humanSince renders a probe's age. "never" reads better than a zero time, and
// the age is the difference between a live reading and a stale one.
func humanSince(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	}
}

// actionProbeSpots samples free.pt against the first enabled provider.
func (p *Plugin) actionProbeSpots(gc *gin.Context) (template.HTML, error) {
	// Generous but bounded: a probe HEADs up to `sample` articles serially, and
	// the alternative to a timeout here is a wedged admin request.
	ctx, cancel := context.WithTimeout(gc.Request.Context(), 90*time.Second)
	defer cancel()

	servers, err := p.st.listServers(ctx)
	if err != nil {
		return settingsRedirect(gc, "err", err.Error())
	}
	var srv *provider
	for i := range servers {
		if servers[i].Enabled {
			srv = &servers[i]
			break
		}
	}
	if srv == nil {
		return settingsRedirect(gc, "err", "no enabled provider to probe with")
	}
	ps := pluginapi.Server{Host: srv.Host, Port: srv.Port, TLS: srv.TLS, Username: srv.Username}
	if pw, err := p.st.serverPassword(ctx, srv.ID); err == nil {
		ps.Password = pw
	}

	sample := 25
	if n, err := strconv.Atoi(gc.PostForm("sample")); err == nil && n > 0 && n <= 200 {
		sample = n
	}
	probe := probeSpots(ctx, ps, sample)

	// Persist before reporting, so a probe that found bad news is still on the
	// page after the redirect rather than only in a flash the operator can
	// dismiss by reloading.
	if b, err := json.Marshal(probe); err == nil {
		if err := p.st.setSetting(ctx, spotProbeKey, string(b)); err != nil {
			p.reportErr(ctx, "usenet/spot-probe-save", err)
		}
	}
	if probe.Err != "" {
		return settingsRedirect(gc, "err", "spot probe: "+probe.Err)
	}
	return settingsRedirect(gc, "msg", probe.Summary())
}

// spotGroupVM is one spot group's progress, as the tab shows it.
type spotGroupVM struct {
	Name      string
	Indexed   string
	Remaining int64
	Done      bool
	Percent   int
}

// spotGroupVMs turns watermark state into "how much of the history is in".
//
// Measured against what the SERVER still holds (server_high - server_low), not
// against the group's whole numbering: articles below server_low expired years
// ago and can never be read, so counting them would pin the bar short of 100%
// forever and make a finished backfill look stuck.
func spotGroupVMs(gs []spotGroup) []spotGroupVM {
	out := make([]spotGroupVM, 0, len(gs))
	for _, g := range gs {
		vm := spotGroupVM{Name: g.Name, Done: g.BackfillDone}
		span := g.ServerHigh - g.ServerLow
		if span <= 0 {
			out = append(out, vm)
			continue
		}
		back := g.BackWatermark
		if back <= 0 {
			back = g.ServerHigh
		}
		read := g.ServerHigh - back
		if g.BackfillDone {
			read = span
		}
		if read < 0 {
			read = 0
		}
		vm.Remaining = back - g.ServerLow
		if vm.Remaining < 0 || g.BackfillDone {
			vm.Remaining = 0
		}
		vm.Percent = int(read * 100 / span)
		if vm.Percent > 100 {
			vm.Percent = 100
		}
		vm.Indexed = humanCount(read)
		out = append(out, vm)
	}
	return out
}

// humanCount keeps six- and seven-figure article counts readable in a table.
func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M"
	case n >= 1_000:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64) + "k"
	default:
		return strconv.FormatInt(n, 10)
	}
}

// actionEnableSpots marks the Spotnet index group as one, and activates it.
//
// A button rather than documentation: the group has to exist in newsgroups
// with kind='spots' before the pass will look at it, and "add free.pt on the
// Newsgroups tab, then set a column we do not expose" is not an instruction
// anyone should have to follow.
func (p *Plugin) actionEnableSpots(gc *gin.Context) (template.HTML, error) {
	ctx := gc.Request.Context()
	if err := p.st.setGroupKind(ctx, SpotGroup, "spots"); err != nil {
		return settingsRedirect(gc, "err", err.Error())
	}
	return settingsRedirect(gc, "msg", SpotGroup+" is now indexed as a Spotnet source — the Spot Index job will pick it up on its next pass")
}
