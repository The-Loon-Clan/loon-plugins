package tvmaze

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// One day of schedule, shaped exactly as TVmaze returns it — including the two
// entries this must DROP: one with no show, one whose airstamp will not parse.
const scheduleFixture = `[
 {"id":1,"name":"Pilot","season":1,"number":1,"airstamp":"2026-08-24T09:00:00+00:00",
  "url":"https://www.tvmaze.com/episodes/1/pilot","show":{"id":15308,"name":"Way Too Early"}},
 {"id":2,"name":"Late One","season":3,"number":7,"airstamp":"2026-08-25T03:30:00+00:00",
  "url":"https://www.tvmaze.com/episodes/2/late","show":{"id":82368,"name":"Tom Green Country"}},
 {"id":3,"name":"Orphan","season":1,"number":2,"airstamp":"2026-08-24T10:00:00+00:00",
  "url":"","show":{"id":0,"name":""}},
 {"id":4,"name":"Undated","season":1,"number":3,"airstamp":"","url":"","show":{"id":99,"name":"X"}}
]`

func TestScheduleParsesAndDropsWhatItCannotPlace(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(scheduleFixture))
	}))
	defer srv.Close()

	s := New(srv.URL)
	day := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	got, err := s.Schedule(context.Background(), day, "US")
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if gotQuery != "country=US&date=2026-08-24" {
		t.Errorf("asked %q, want the date and country as query params", gotQuery)
	}
	if len(got) != 2 {
		t.Fatalf("kept %d entries, want 2 — the show-less and the undated ones must go", len(got))
	}
	if got[0].ShowID != "15308" || got[0].ShowTitle != "Way Too Early" {
		t.Errorf("first = %+v, want the tvmaze show id carried as a string", got[0])
	}
	// THE POINT OF USING airstamp: this one airs 23:30 US Eastern on the 24th
	// and is the 25th in UTC. A calendar keyed on the requested DATE would put
	// it in the wrong cell.
	if got[1].AirsAt.Day() != 25 {
		t.Errorf("late-night episode landed on day %d, want 25 — the airstamp decides "+
			"the cell, not the date we asked for", got[1].AirsAt.Day())
	}
	if !got[1].AirsAt.Equal(got[1].AirsAt.UTC()) {
		t.Error("AirsAt is not UTC; a calendar comparing it against a window would drift")
	}
}

func TestScheduleReportsUpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	if _, err := New(srv.URL).Schedule(context.Background(), time.Now(), "US"); err == nil {
		t.Fatal("a 429 returned no error; the job would record an empty day as a real one")
	}
}

// An empty day is not a failure — most countries have quiet days.
func TestScheduleEmptyDay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]scheduleDay{})
	}))
	defer srv.Close()
	got, err := New(srv.URL).Schedule(context.Background(), time.Now(), "US")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty day gave (%v, %v), want (nothing, no error)", got, err)
	}
}
