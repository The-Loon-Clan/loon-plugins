package downloads

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// The report endpoint: POST /api/downloads/report.
//
// Called by a script running inside the member's own download client, so every
// decision here is made for a caller that is a shell process on somebody
// else's machine:
//
//   - It takes FORM ENCODING as well as JSON, because the smallest possible
//     script in any language can post a form and not every one of them can
//     build JSON.
//   - It answers JSON with a human "message", because that message is the only
//     feedback the member will ever see — it lands in their client's log.
//   - It uses real status codes, because the script has to tell "you are not
//     signed in" from "I could not work out which release that was", and a
//     200-with-ok-false makes every failure look the same to a retry loop.
//
// What it does NOT do is act on the report beyond recording it and asking for
// a re-check. See the package comment: a report is a signal.

// reportRequest is what a download client sends.
type reportRequest struct {
	// APIKey identifies the member. Also accepted as an Authorization: Bearer
	// header, because some clients make it easier to set a header than to keep
	// a secret out of a logged command line.
	APIKey string `form:"apikey" json:"apikey"`
	// Status is the member's outcome: anything that is not a success is a
	// failure. Parsed leniently — see normaliseStatus.
	Status string `form:"status" json:"status"`
	// ID is the release id when the client knows it. Everything else here
	// exists to work it out when the client does not.
	ID int64 `form:"id" json:"id"`
	// URL is where the NZB was fetched from, which for a job added by URL
	// carries the id. The single most reliable field after ID itself.
	URL string `form:"url" json:"url"`
	// Name is the job's name as the client knows it, and Filename the NZB file
	// it came from. Either may have been rewritten by the client.
	Name     string `form:"name" json:"name"`
	Filename string `form:"filename" json:"filename"`
	// Detail is the client's own wording for what went wrong. Stored as
	// evidence; nothing branches on it.
	Detail string `form:"detail" json:"detail"`
	// Client names the software, for the staff view.
	Client string `form:"client" json:"client"`
}

// reportResponse is what the script prints into the member's download log.
type reportResponse struct {
	OK bool `json:"ok"`
	// Release is the id this was matched to, 0 when nothing matched.
	Release int64 `json:"release,omitempty"`
	// Action is what the site did: "recorded", "recorded+recheck", or
	// "unmatched". Named rather than implied, because the member reading their
	// client's log is entitled to know whether anything happened.
	Action  string `json:"action"`
	Message string `json:"message"`
}

const (
	actionRecorded  = "recorded"
	actionRecheck   = "recorded+recheck"
	actionUnmatched = "unmatched"
)

func (p *Plugin) handleReport(c *gin.Context) {
	if p.keys == nil {
		// Refused, not opened. An endpoint that cannot validate a key accepts
		// every key, and this one can ask for real NNTP work.
		c.JSON(http.StatusServiceUnavailable, reportResponse{
			Action:  actionUnmatched,
			Message: "This site is not accepting download reports.",
		})
		return
	}

	var req reportRequest
	// ShouldBind picks form or JSON off the content type. A bad body is a
	// client bug worth naming rather than a silent empty request.
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, reportResponse{
			Action:  actionUnmatched,
			Message: "Could not read the report body.",
		})
		return
	}
	key := strings.TrimSpace(req.APIKey)
	if key == "" {
		key = bearerToken(c.GetHeader("Authorization"))
	}
	if key == "" {
		c.JSON(http.StatusUnauthorized, reportResponse{
			Action:  actionUnmatched,
			Message: "No API key. Put your key in the script, or send it as apikey=.",
		})
		return
	}

	ctx := c.Request.Context()
	userID, ok, err := p.keys.ResolveAPIKey(ctx, key)
	if err != nil {
		// The lookup itself failed, which is ours and not theirs. 503 so a
		// retrying client comes back rather than deciding its key is bad.
		c.JSON(http.StatusServiceUnavailable, reportResponse{
			Action:  actionUnmatched,
			Message: "Could not check the API key just now — try again later.",
		})
		return
	}
	if !ok {
		c.JSON(http.StatusUnauthorized, reportResponse{
			Action:  actionUnmatched,
			Message: "That API key is not valid here. Copy a fresh one from your account page.",
		})
		return
	}

	releaseID, how := p.resolveRelease(ctx, userID, req)
	if releaseID == 0 {
		// 404, and a message that says what to do. This is the failure a
		// member will actually hit — a job their client renamed — and "no"
		// with no explanation is what makes somebody give up on the feature.
		c.JSON(http.StatusNotFound, reportResponse{
			Action: actionUnmatched,
			Message: "Could not match that job to a release here. Reports work best when the " +
				"NZB was added by URL from this site; a renamed job may not match.",
		})
		return
	}

	status := normaliseStatus(req.Status)
	rep, err := p.st.Record(ctx, Report{
		UserID: userID, ReleaseID: releaseID, Status: status,
		Detail: trimTo(req.Detail, 500), Client: trimTo(req.Client, 40),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, reportResponse{
			Action:  actionUnmatched,
			Message: "Could not record the report.",
		})
		return
	}

	action, msg := actionRecorded, "Thanks — recorded."
	if status == statusFailed {
		// The whole point of a failure report. Best-effort: a re-check that
		// could not be requested must not turn a recorded report into an
		// error, because the record is the part that is already true.
		if p.recheck != nil {
			if accepted, rerr := p.recheck.RequestRecheck(ctx, releaseID, userID); rerr == nil && accepted {
				action = actionRecheck
				msg = "Thanks — recorded, and this release is queued for a health re-check."
			} else if rerr == nil {
				// Declined, which the host does for a release re-checked
				// moments ago. Said plainly: the member has not been ignored,
				// the work is already happening.
				msg = "Thanks — recorded. This release was checked very recently, so no new check was queued."
			}
		}
	}
	if rep.Reports > 1 {
		msg += " (report " + itoa(rep.Reports) + " from you for this release.)"
	}

	c.JSON(http.StatusOK, reportResponse{
		OK: true, Release: releaseID, Action: action,
		Message: msg + how,
	})
}

const (
	statusOK     = "ok"
	statusFailed = "failed"
)

// normaliseStatus maps every client's vocabulary onto the one distinction this
// site can act on.
//
// FAILURE IS THE DEFAULT for anything unrecognised, and that is deliberate. A
// script that sends a status this does not know is more likely reporting a
// problem than a success — clients have one word for "fine" and a dozen for
// the ways a job can end badly — and the cost of the two mistakes is not
// symmetrical: a wrong 'failed' asks for one re-check nobody needed, while a
// wrong 'ok' silently discards the report the feature exists for.
func normaliseStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ok", "success", "0", "93", "completed", "true":
		return statusOK
	default:
		return statusFailed
	}
}

// bearerToken pulls the credential out of an Authorization header.
func bearerToken(h string) string {
	const prefix = "bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

func trimTo(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
