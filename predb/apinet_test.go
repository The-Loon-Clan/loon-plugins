package predb

import (
	"encoding/json"
	"testing"
	"time"
)

// Decoding a real api.predb.net response.
//
// The payload below is captured from the live endpoint, not written by hand:
// the field names are theirs and a rename on their side should fail here
// rather than silently produce empty releases.
const netSample = `{"status":"success","results":2,"time":"0.002s","results_total":14242239,"data":[
{"id":14377200,"pretime":1786685576,"release":"Alton_Miller-Summer_Bliss-(HOUSEWAX041)-WEB-2026-BB","section":"MP3-WEB","files":4,"size":64,"status":0,"reason":"","group":"BB","genre":"christian","url":"\/rls\/Alton_Miller-Summer_Bliss-(HOUSEWAX041)-WEB-2026-BB"},
{"id":14377199,"pretime":1786685562,"release":"Shirt.Pocket.SuperDuper.v3.12.MacOS.UB.Incl.Keymaker-CORE","section":"APPS-0DAY","files":2,"size":9,"status":1,"reason":"dupe","group":"CORE","genre":"","url":"\/rls\/x"}]}`

func TestNetEnvelopeDecodesTheirFieldNames(t *testing.T) {
	var env netEnvelope
	if err := json.Unmarshal([]byte(netSample), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Status != "success" || env.Results != 2 || env.Total != 14242239 {
		t.Fatalf("envelope: status=%q results=%d total=%d", env.Status, env.Results, env.Total)
	}
	if len(env.Data) != 2 {
		t.Fatalf("got %d rows", len(env.Data))
	}

	r := env.Data[0]
	if r.ID != 14377200 {
		t.Errorf("id = %d", r.ID)
	}
	if r.Release != "Alton_Miller-Summer_Bliss-(HOUSEWAX041)-WEB-2026-BB" {
		t.Errorf("release = %q — this is the field the whole feature exists for", r.Release)
	}
	if r.Section != "MP3-WEB" || r.Group != "BB" || r.Genre != "christian" {
		t.Errorf("section=%q group=%q genre=%q", r.Section, r.Group, r.Genre)
	}
	if r.Files != 4 || r.Size != 64 {
		t.Errorf("files=%d size=%d", r.Files, r.Size)
	}
	want := time.Unix(1786685576, 0).UTC()
	if !r.At().Equal(want) {
		t.Errorf("At() = %v, want %v", r.At(), want)
	}
	if r.Nuked() {
		t.Error("a status-0 row with no reason was read as nuked")
	}
}

// A nuke has to be visible: a nuked release is still a real release and still
// de-obfuscates, but a caller deciding whether to trust the name wants to know.
func TestNetRowNukeDetection(t *testing.T) {
	var env netEnvelope
	if err := json.Unmarshal([]byte(netSample), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	r := env.Data[1]
	if !r.Nuked() {
		t.Error("a row with status=1 and reason=dupe was not read as nuked")
	}
	if r.Reason != "dupe" {
		t.Errorf("reason = %q", r.Reason)
	}
}

// Both halves of the nuke signal count. Trusting only `reason` misses a status
// set with an empty reason; trusting only `status` misses the reverse.
func TestNetRowNukeUsesBothSignals(t *testing.T) {
	cases := map[string]struct {
		row  NetRow
		want bool
	}{
		"clean":             {NetRow{Status: 0, Reason: ""}, false},
		"status only":       {NetRow{Status: 2, Reason: ""}, true},
		"reason only":       {NetRow{Status: 0, Reason: "nuked.for.something"}, true},
		"whitespace reason": {NetRow{Status: 0, Reason: "   "}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := c.row.Nuked(); got != c.want {
				t.Errorf("Nuked() = %v, want %v", got, c.want)
			}
		})
	}
}

// A missing pretime must not become 1970.
func TestNetRowZeroTime(t *testing.T) {
	if got := (NetRow{PreTime: 0}).At(); !got.IsZero() {
		t.Errorf("At() = %v for a zero pretime, want the zero time", got)
	}
}
