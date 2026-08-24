package tvmaze

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The broadcast schedule: what airs on a given day.
//
// A DIFFERENT QUESTION from the rest of this file. Everything else here
// answers "what show is this release?" -- one lookup per release, cached by
// series name. This answers "what is on", which nothing about a release
// triggers and which the calendar asks for a whole window at a time.
//
// It lives beside them because of the RATE LIMIT, which is the only genuinely
// scarce thing in this package. TVmaze documents 20 calls per 10 seconds per
// IP and answers 429 beyond it, with no key to raise it -- so every caller on
// one address shares one budget. A second client with its own pacing would be
// two clients each politely staying under the limit and together exceeding it.
// Schedule goes through the same wait() as Search for that reason alone.

// scheduleDay is one entry as TVmaze returns it. Only the fields the calendar
// needs; the payload also carries summaries and images that would be a few
// hundred KB a day to keep for nothing.
type scheduleDay struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Season   int    `json:"season"`
	Number   int    `json:"number"`
	Airstamp string `json:"airstamp"`
	URL      string `json:"url"`
	Show     struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"show"`
}

// Airing is one episode on the schedule, upstream-shaped and id-carrying.
//
// Deliberately NOT pluginapi.TVEpisode: this package is a catalog source and
// does not import the plugin API. The caller converts, which is one small
// function in the plugin against two packages that stay independent.
type Airing struct {
	ShowID    string
	ShowTitle string
	Season    int
	Number    int
	Title     string
	AirsAt    time.Time
	URL       string
}

// Schedule returns every episode airing on one date in one country.
//
// ONE CALL PER DAY, and that is the whole reason this is driven by a job
// rather than by a page render: a month view is thirty-one days, which at the
// pacing above is nineteen seconds of waiting. Nobody watches a calendar load
// for nineteen seconds, and doing it per viewer would multiply one polite
// client into a rude one.
//
// country is an ISO code ("US"); empty asks for every country, which is a much
// larger answer and rarely what a site wants.
//
// A day with nothing on it is (nil, nil). An upstream failure IS an error --
// the caller is a job that can log it and try the next day, which is different
// from a render path that must not fail.
func (s *Source) Schedule(ctx context.Context, date time.Time, country string) ([]Airing, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("date", date.UTC().Format("2006-01-02"))
	if country != "" {
		q.Set("country", country)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.baseURL+"/schedule?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 429 included. The caller backs off by simply not asking again until
		// its next tick; retrying inside a rate limit is how a client that was
		// merely throttled becomes one that is blocked.
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("tvmaze schedule %s: %s", q.Get("date"), resp.Status)
	}
	// Capped: a day is a few hundred KB and a runaway response should not be
	// held in memory by a background job nobody is watching.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var days []scheduleDay
	if err := json.Unmarshal(body, &days); err != nil {
		return nil, fmt.Errorf("tvmaze schedule %s: %w", q.Get("date"), err)
	}

	out := make([]Airing, 0, len(days))
	for _, d := range days {
		if d.Show.ID == 0 {
			continue // an entry with no show cannot be matched to anything
		}
		at, err := time.Parse(time.RFC3339, d.Airstamp)
		if err != nil {
			// Airstamp is the only field that decides which calendar cell this
			// lands in. An entry without a usable one is dropped rather than
			// defaulted to the request date: a show airing 23:00 US Eastern is
			// the NEXT day in UTC, and guessing would put half of late-night
			// television in the wrong cell.
			continue
		}
		out = append(out, Airing{
			ShowID:    strconv.FormatInt(d.Show.ID, 10),
			ShowTitle: d.Show.Name,
			Season:    d.Season,
			Number:    d.Number,
			Title:     d.Name,
			AirsAt:    at.UTC(),
			URL:       d.URL,
		})
	}
	return out, nil
}
