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
	return p.frag("spots.html", map[string]any{
		"Group":        SpotGroup,
		"MinKeyBits":   MinSpotKeyBits,
		"Probe":        probe,
		"HaveProbe":    have,
		"ProbeAge":     humanSince(probe.At),
		"HaveProvider": haveProvider,
		// The importer is the missing half. Stated as data rather than prose
		// in the template so that wiring it later flips one boolean.
		"ImportWired": false,
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
