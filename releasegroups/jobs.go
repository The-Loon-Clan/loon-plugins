package releasegroups

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/httpclient"
	"github.com/the-loon-clan/loon/schedule"
)

const (
	// Historical job names, kept exactly: the admin's interval overrides
	// are stored under "job_interval:<name>".
	scraperJobName = "Release Group Scraper"
	archiveJobName = "Release Group Archive Sweep"

	scraperDefaultInterval = 7 * 24 * time.Hour // weekly
	archiveDefaultInterval = 24 * time.Hour     // daily

	nekobtAPIBase    = "https://nekobt.to"
	scraperHTTPLimit = 30 * time.Second
	nekobtPageLimit  = 50
	nekobtMaxPages   = 20 // hard cap: 1000 groups max per run
	nekobtRequestGap = 1500 * time.Millisecond
	interGroupGap    = 5 * time.Second
	scraperBootDelay = 10 * time.Minute
	archiveBootDelay = 30 * time.Minute
)

// scraperService is the weekly nekoBT group-profile scrape. It walks
// /api/v1/groups/search and merges profiles into the host's release_groups
// table — promoting auto-detected 'unknown' rows to 'confirmed' with a
// logo, website, and description; new rows arrive pre-confirmed with
// source='scraped'.
type scraperService struct {
	deps JobDeps
	errs core.ErrorReporter
	job  *schedule.JobInfo
}

func newScraperService(d JobDeps, errs core.ErrorReporter) *scraperService {
	s := &scraperService{deps: d, errs: errs}
	s.job = schedule.RegisterJob(scraperJobName,
		"Scrapes release-group profiles (name, website, logo) from nekobt.to's "+
			"JSON API and merges them into the release_groups table — promoting "+
			"auto-detected 'unknown' rows to 'confirmed'.").
		MarkWrites()
	s.job.IntervalMin = int(scraperDefaultInterval.Minutes())
	s.job.SetTriggerAsync(func() { s.run(context.Background()) })
	return s
}

// nekobtGroup is one entry in the /api/v1/groups/search response.
type nekobtGroup struct {
	ID          json.Number `json:"id"`
	Name        string      `json:"name"`         // lowercase internal name
	DisplayName string      `json:"display_name"` // proper-cased display name
	Tag         string      `json:"display_tag"`  // short tag (e.g. "GJM")
	PfpHash     string      `json:"pfp_hash"`     // avatar hash, stored at /cdn/pfp/<hash>
	Tagline     string      `json:"tagline"`      // short one-liner
	Description string      `json:"description"`  // long HTML blurb, often contains social links
}

// nekobtGroupResponse wraps the paginated groups list.
// Actual shape: {"error": false, "data": {"results": [...], "more": true}}
type nekobtGroupResponse struct {
	Error bool `json:"error"`
	Data  struct {
		Results []nekobtGroup `json:"results"`
		More    bool          `json:"more"`
	} `json:"data"`
}

func (s *scraperService) run(parentCtx context.Context) {
	if s.job.IsPaused() {
		return
	}
	s.job.SetRunning()
	start := time.Now()

	ctx, cancel := s.job.RunContext(parentCtx)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			s.job.SetError(fmt.Sprintf("panic: %v", r))
			s.errs.Report(ctx, "release-group/scraper-panic", fmt.Errorf("panic: %v", r))
		}
	}()

	s.job.Log("Starting nekobt.to release-group scrape")

	client := httpclient.NewWithTimeout(scraperHTTPLimit)
	var (
		totalScraped  int
		totalInserted int
		totalUpdated  int
		totalLogos    int
		totalSkipped  int
	)
	// Set of slugs we saw on nekoBT this run. Used after the loop (if we
	// completed without being stopped) to stamp nekobt_status='not_found'
	// on confirmed groups that didn't appear in any page. Slugs are what
	// the merge keys on, so the same slug normalization that drives the
	// upsert drives the not-found set.
	scrapedSlugs := map[string]struct{}{}
	stoppedEarly := false

	for page := 0; page < nekobtMaxPages; page++ {
		if ctx.Err() != nil {
			s.job.Log("Stopped mid-run at page %d", page)
			stoppedEarly = true
			break
		}
		if s.job.IsPaused() {
			s.job.Log("Paused mid-run at page %d", page)
			stoppedEarly = true
			break
		}
		// Intentionally NO WaitForCPU() here: the scraper is network-bound
		// (just HTTP GETs and a sleep between requests) so it adds no real
		// CPU load. The previous version blocked the loop indefinitely on
		// busy hosts with no visible log output.

		offset := page * nekobtPageLimit
		url := fmt.Sprintf("%s/api/v1/groups/search?query=&offset=%d&limit=%d&sort=uploads&order=desc",
			nekobtAPIBase, offset, nekobtPageLimit)
		s.job.SetProgress("page %d: GET %s", page+1, url)
		s.job.Log("Page %d: GET %s", page+1, url)

		groups, err := fetchGroups(ctx, client, url)
		if err != nil {
			s.job.Log("  fetch failed: %v", err)
			schedule.SleepCtx(ctx, nekobtRequestGap)
			continue
		}
		schedule.SleepCtx(ctx, nekobtRequestGap)

		s.job.Log("  got %d groups", len(groups))
		if len(groups) == 0 {
			s.job.Log("  empty page — done")
			break
		}

		for i, g := range groups {
			if ctx.Err() != nil {
				stoppedEarly = true
				break
			}
			if s.job.IsPaused() {
				stoppedEarly = true
				break
			}

			// Prefer display_name ("ToonsHub") over the lowercase internal
			// name ("toonshub") so tag matching on NZB titles still works.
			name := strings.TrimSpace(g.DisplayName)
			if name == "" {
				name = strings.TrimSpace(g.Name)
			}
			if name == "" {
				totalSkipped++
				continue
			}
			// Record this slug so the post-loop finalize knows we saw it on
			// nekoBT. The merge uses the same slugifier so the set lines up
			// with what the UPDATE will compare against. Skipped names
			// (empty) intentionally never land here so they don't lock out
			// a future scrape.
			scrapedSlugs[s.deps.Slugify(name)] = struct{}{}
			totalScraped++

			s.job.SetProgress("page %d entry %d/%d: %s (new=%d upd=%d)",
				page+1, i+1, len(groups), name, totalInserted, totalUpdated)

			// Build avatar URL from the pfp_hash (e.g. /cdn/pfp/<hash>).
			logoURL := ""
			if g.PfpHash != "" {
				logoURL = nekobtAPIBase + "/cdn/pfp/" + g.PfpHash
			}

			// Website lives on the group profile page, not in the API. Link
			// back to the nekobt profile so users can see the full description.
			website := fmt.Sprintf("%s/groups/%s", nekobtAPIBase, g.ID.String())

			// Prefer the short tagline over the long HTML description.
			description := strings.TrimSpace(g.Tagline)
			if description == "" {
				description = strings.TrimSpace(stripHTMLTags(g.Description))
			}

			// Download + re-encode the logo locally (host seam — the image
			// stack stays host-side).
			localLogo := ""
			if logoURL != "" {
				local, err := s.deps.FetchLogo(ctx, logoURL, s.deps.Slugify(name))
				if err != nil {
					s.job.Log("  logo %q failed: %v", name, err)
				} else {
					localLogo = local
					totalLogos++
				}
				schedule.SleepCtx(ctx, nekobtRequestGap)
			}

			groupID, inserted, err := s.deps.Groups.MergeScrapedReleaseGroup(ctx,
				ScrapedGroup{
					Name:        name,
					WebsiteURL:  website,
					Description: description,
					LogoURL:     localLogo,
				})
			if err != nil {
				s.job.Log("  merge %q: %v", name, err)
				continue
			}
			if inserted {
				totalInserted++
			} else {
				totalUpdated++
			}
			// Cache the upstream snowflake so the daily archive sweep has
			// something to do for every group the weekly scrape sees.
			// Idempotent — the setter no-ops when the ID hasn't changed.
			// Also stamps nekobt_status='linked' so the Torrents-tab empty
			// state knows this row is connected.
			if nekobtID := g.ID.String(); nekobtID != "" && groupID > 0 {
				if err := s.deps.Groups.SetReleaseGroupNekobtID(ctx, groupID, nekobtID); err != nil {
					s.job.Log("  set nekobt_group_id %q: %v", name, err)
				}
				if err := s.deps.Groups.SetReleaseGroupNekobtStatus(ctx, groupID, "linked"); err != nil {
					s.job.Log("  set nekobt_status linked %q: %v", name, err)
				}
			}
		}
	}

	stopped := ctx.Err() != nil
	// Auto-mark `not_found` on confirmed groups that didn't appear in any
	// page of this run. Only safe when the run completed fully — a partial
	// sweep would falsely mark unreached groups. We use stoppedEarly (set
	// by the in-loop break paths) AND ctx.Err() so a successful run hits
	// both.
	if !stopped && !stoppedEarly && len(scrapedSlugs) > 0 {
		slugs := make([]string, 0, len(scrapedSlugs))
		for sl := range scrapedSlugs {
			slugs = append(slugs, sl)
		}
		if n, err := s.deps.Groups.MarkConfirmedGroupsNotFoundOnNekobt(parentCtx, slugs); err != nil {
			s.job.Log("mark not_found: %v", err)
		} else if n > 0 {
			s.job.Log("Marked %d confirmed group(s) as not_found on nekoBT", n)
		}
	}
	if !stopped {
		if err := s.deps.Groups.RefreshReleaseGroupNzbCounts(parentCtx); err != nil {
			s.job.Log("refresh group counts: %v", err)
		}
	}

	verb := "Done"
	if stopped {
		verb = "Stopped"
	}
	s.job.Log("%s: %d scraped, %d new, %d updated, %d logos, %d skipped — %s",
		verb, totalScraped, totalInserted, totalUpdated, totalLogos, totalSkipped,
		time.Since(start).Round(time.Millisecond))
	// ServiceLoop announces the true next run (with any admin override)
	// right after this returns; this default is the manual-trigger
	// placeholder.
	s.job.SetIdle(time.Now().Add(scraperDefaultInterval))
}

// fetchGroups hits the nekobt API and returns the parsed group list.
func fetchGroups(ctx context.Context, client *http.Client, apiURL string) ([]nekobtGroup, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", scraperUserAgent())
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	var wrapped nekobtGroupResponse
	if err := json.Unmarshal(body, &wrapped); err != nil {
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		return nil, fmt.Errorf("parse response: %w (body: %s)", err, snippet)
	}
	return wrapped.Data.Results, nil
}

// stripHTMLTags returns the plain text of an HTML fragment. Good enough for
// a short description field — we don't need full sanitization because the
// value goes into a DB column, not back into a rendered page.
func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			b.WriteRune(' ')
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// archiveService is the daily sweep refreshing the external-archive snapshot
// for every eligible group. The owner-triggered refresh on the archive page
// bypasses this entirely; this loop is for "show fresh data when nobody's
// clicked the button."
type archiveService struct {
	deps JobDeps
	errs core.ErrorReporter
	job  *schedule.JobInfo
}

func newArchiveService(d JobDeps, errs core.ErrorReporter) *archiveService {
	s := &archiveService{deps: d, errs: errs}
	s.job = schedule.RegisterJob(archiveJobName,
		"Daily refresh of per-group nekoBT torrent listings for claimed "+
			"release groups (migration 229).").
		MarkWrites()
	s.job.IntervalMin = int(archiveDefaultInterval.Minutes())
	// Off-peak gate: this calls an external API in a loop and writes to PG;
	// we don't want it competing with site traffic on a loaded box.
	s.job.MarkOffPeak()
	s.job.SetTriggerAsync(func() { s.run(context.Background()) })
	return s
}

// run executes one full sweep: list eligible groups, scrape each in turn
// with a polite gap between them. Best-effort per group — one failure logs
// + continues so a misconfigured group id on group #3 doesn't stop the
// sweep from refreshing group #4.
func (s *archiveService) run(parentCtx context.Context) {
	if s.job.IsPaused() {
		return
	}
	s.job.SetRunning()
	start := time.Now()

	ctx, cancel := s.job.RunContext(parentCtx)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			s.job.SetError("panic during sweep")
			log.Printf("release-group archive sweep panic: %v", r)
			s.errs.Report(ctx, "release-group/archive-sweep-panic", fmt.Errorf("panic: %v", r))
		}
	}()

	groups, err := s.deps.Groups.ListGroupsForArchiveSweep(ctx)
	if err != nil {
		s.job.SetError("list groups: " + err.Error())
		s.errs.Report(ctx, "release-group/archive-sweep-list", err)
		return
	}
	s.job.Log("Eligible groups: %d", len(groups))

	var (
		totalGroups   int
		totalTorrents int
		totalErrors   int
	)
	for i, g := range groups {
		if ctx.Err() != nil {
			s.job.Log("Stopped mid-sweep at group %d/%d", i+1, len(groups))
			break
		}
		if s.job.IsPaused() {
			s.job.Log("Paused mid-sweep at group %d/%d", i+1, len(groups))
			break
		}
		s.job.SetProgress("group %d/%d: %s", i+1, len(groups), g.Slug)

		n, err := s.deps.ScrapeArchive(ctx, g.ID)
		if err != nil {
			s.job.Log("  %s: %v", g.Slug, err)
			s.errs.Report(ctx, "release-group/archive-sweep-scrape", err)
			totalErrors++
		} else {
			s.job.Log("  %s: refreshed (%d torrents)", g.Slug, n)
			totalTorrents += n
		}
		totalGroups++

		if i < len(groups)-1 {
			schedule.SleepCtx(ctx, interGroupGap)
		}
	}

	stopped := ctx.Err() != nil
	verb := "Done"
	if stopped {
		verb = "Stopped"
	}
	s.job.Log("%s: %d group(s), %d torrent(s), %d error(s) — %s",
		verb, totalGroups, totalTorrents, totalErrors,
		time.Since(start).Round(time.Millisecond))
	s.job.SetIdle(time.Now().Add(archiveDefaultInterval))
}

// scraperUserAgent identifies this crawler to the sites it visits.
//
// A function rather than a const because it reads the deployment's name, and it
// reads it because the const said "AmeNZB/1.0" on every host that installed
// this plugin. A User-Agent naming the wrong site is a false statement about
// who is crawling you, which is the one thing a UA exists to get right.
func scraperUserAgent() string {
	return deps.siteName() + "/1.0 (+release-group scraper; contact via site)"
}
