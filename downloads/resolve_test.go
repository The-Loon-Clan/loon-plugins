package downloads

import (
	"context"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Matching a job to a release is the part that can be wrong without looking
// wrong: a bad match attaches one member's failure to somebody else's release,
// and nothing downstream can tell it apart from a real report.

func TestReleaseIDFromURL(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want int64
	}{
		// The Newznab download link, which is what Sonarr and the API use.
		{"https://example.org/api?t=get&id=12345&apikey=deadbeef", 12345},
		{"https://example.org/api?apikey=deadbeef&t=get&id=7", 7},
		// The plain link the download button uses, with and without the
		// extension a client may have appended.
		{"https://example.org/nzb/12345", 12345},
		{"https://example.org/nzb/12345.nzb", 12345},
		// Nothing to read.
		{"", 0},
		{"https://example.org/browse", 0},
		{"https://example.org/api?t=get&apikey=x", 0},
		// Not an id: a zero and a negative are both "no", never a release.
		{"https://example.org/api?t=get&id=0", 0},
		{"https://example.org/api?t=get&id=-3", 0},
		{"https://example.org/api?t=get&id=banana", 0},
		// A URL that will not parse must not panic or guess.
		{"://nonsense", 0},
	} {
		t.Run(tc.url, func(t *testing.T) {
			if got := releaseIDFromURL(tc.url); got != tc.want {
				t.Errorf("releaseIDFromURL(%q) = %d, want %d", tc.url, got, tc.want)
			}
		})
	}
}

// A download client rewrites job names according to its own settings, and every
// one of these is the same release the member grabbed.
func TestFoldTitleSurvivesAClientsRenaming(t *testing.T) {
	want := foldTitle("Some.Show.S01E02.1080p.WEB-DL.x264-GRP")
	for _, variant := range []string{
		"Some Show S01E02 1080p WEB-DL x264-GRP",
		"some_show_s01e02_1080p_web_dl_x264_grp",
		"Some.Show.S01E02.1080p.WEB-DL.x264-GRP.nzb",
		"  Some.Show.S01E02.1080p.WEB-DL.x264-GRP  ",
	} {
		if got := foldTitle(stripNZBExt(variant)); got != want {
			t.Errorf("%q folded to %q, want %q", variant, got, want)
		}
	}
	// And the fold must still tell genuinely different releases apart —
	// dropping punctuation must not drop the episode number with it.
	if foldTitle("Some.Show.S01E02.1080p") == foldTitle("Some.Show.S01E03.1080p") {
		t.Error("two different episodes folded to one key")
	}
}

// fakeGrabs is the host's grab history.
type fakeGrabs struct {
	rows []pluginapi.GrabbedRelease
	err  error
}

func (f fakeGrabs) RecentGrabs(context.Context, int64, int) ([]pluginapi.GrabbedRelease, error) {
	return f.rows, f.err
}

func TestResolveReleasePrefersWhatItCanTrust(t *testing.T) {
	p := &Plugin{grabs: fakeGrabs{rows: []pluginapi.GrabbedRelease{
		{ID: 99, Title: "Some.Show.S01E02.1080p.WEB-DL.x264-GRP"},
	}}}
	ctx := context.Background()

	// An explicit id wins over everything, including a name that would have
	// matched something else.
	got, _ := p.resolveRelease(ctx, 1, reportRequest{ID: 5, Name: "Some Show S01E02 1080p WEB-DL x264-GRP"})
	if got != 5 {
		t.Errorf("id = %d, want the explicit 5", got)
	}
	// Then the URL, which is our own link read back.
	got, _ = p.resolveRelease(ctx, 1, reportRequest{
		URL: "https://x/api?t=get&id=42", Name: "Some Show S01E02 1080p WEB-DL x264-GRP"})
	if got != 42 {
		t.Errorf("id = %d, want 42 from the URL", got)
	}
	// Then the name, against the member's own grabs — and the match is
	// disclosed, because a fuzzy match the member cannot see is one they
	// cannot question.
	got, how := p.resolveRelease(ctx, 1, reportRequest{Name: "Some Show S01E02 1080p WEB-DL x264-GRP"})
	if got != 99 {
		t.Errorf("id = %d, want 99 matched by name", got)
	}
	if how == "" {
		t.Error("a name match was not disclosed in the message")
	}
}

// A name that matches nothing the member downloaded is answered as unmatched.
// Guessing would attach their failure to somebody else's release.
func TestResolveReleaseRefusesToGuess(t *testing.T) {
	p := &Plugin{grabs: fakeGrabs{rows: []pluginapi.GrabbedRelease{
		{ID: 99, Title: "Some.Show.S01E02.1080p.WEB-DL.x264-GRP"},
	}}}
	if got, _ := p.resolveRelease(context.Background(), 1,
		reportRequest{Name: "Completely.Different.Thing.2024.1080p"}); got != 0 {
		t.Errorf("matched an unrelated job to release %d", got)
	}
}

// A host with no grab history still resolves the reports that carry an id, and
// simply cannot resolve the rest — it must not panic reaching for the seam.
func TestResolveReleaseWithoutGrabHistory(t *testing.T) {
	p := &Plugin{}
	if got, _ := p.resolveRelease(context.Background(), 1, reportRequest{ID: 8}); got != 8 {
		t.Error("an explicit id needs no grab history")
	}
	if got, _ := p.resolveRelease(context.Background(), 1, reportRequest{Name: "anything"}); got != 0 {
		t.Errorf("matched %d with no grab history to match against", got)
	}
}

// A failing grab lookup is not a wrong answer — it is no answer.
func TestResolveReleaseSurvivesAFailingLookup(t *testing.T) {
	p := &Plugin{grabs: fakeGrabs{err: context.DeadlineExceeded}}
	if got, _ := p.resolveRelease(context.Background(), 1, reportRequest{Name: "x"}); got != 0 {
		t.Errorf("got %d from a failed lookup", got)
	}
}

// Every client has one word for success and a dozen for failure, so the
// unknown case must fall to 'failed': a wrong 'failed' costs one re-check
// nobody needed, and a wrong 'ok' silently discards the report the whole
// feature exists for.
func TestNormaliseStatus(t *testing.T) {
	for _, s := range []string{"ok", "OK", "success", "SUCCESS", "0", "93", "completed", " true "} {
		if got := normaliseStatus(s); got != statusOK {
			t.Errorf("normaliseStatus(%q) = %q, want ok", s, got)
		}
	}
	for _, s := range []string{"", "1", "2", "3", "-1", "FAILURE", "FAILURE/UNPACK", "banana", "DELETED"} {
		if got := normaliseStatus(s); got != statusFailed {
			t.Errorf("normaliseStatus(%q) = %q, want failed", s, got)
		}
	}
}

func TestBearerToken(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Bearer abc123", "abc123"},
		{"bearer abc123", "abc123"},
		{"BEARER  abc123 ", "abc123"},
		{"abc123", ""},
		{"Basic abc123", ""},
		{"", ""},
		{"Bearer", ""},
	} {
		if got := bearerToken(tc.in); got != tc.want {
			t.Errorf("bearerToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
