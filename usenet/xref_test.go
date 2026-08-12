package usenet

import (
	"reflect"
	"testing"
)

// Xref is the protocol's own crosspost signal (RFC 5536 s3.2.14) and normally
// arrives free in overview data. Reading it is the difference between knowing a
// posting is crossposted before we fetch it and rediscovering that afterwards
// from content sketches, having already paid to stage and assemble each copy.
func TestXrefGroups(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string
	}{
		{
			name: "server token then group:number pairs",
			in:   "news.example.com alt.binaries.teevee:12345 alt.binaries.mom:98765",
			want: []string{"alt.binaries.teevee", "alt.binaries.mom"},
		}, {
			// The leading token is the SERVER, not a group. Treating it as one
			// would put a hostname in the NZB's <groups>.
			name: "server name is never a group",
			in:   "news.example.com alt.binaries.misc:1",
			want: []string{"alt.binaries.misc"},
		}, {
			// Article numbers are per-(server, group) and mean nothing anywhere
			// else (RFC 3977 s6), so only the names survive.
			name: "numbers are stripped",
			in:   "srv alt.binaries.boneless:99999999",
			want: []string{"alt.binaries.boneless"},
		}, {
			// A binary crosspost routinely also lands in discussion groups we
			// neither crawl nor want in a member's NZB.
			name: "non-binary groups are dropped",
			in:   "srv alt.binaries.teevee:1 alt.tv.discussion:2 rec.arts.anime:3",
			want: []string{"alt.binaries.teevee"},
		}, {
			name: "foreign binary hierarchies are kept",
			in:   "srv es.binarios.cine:1 nl.binaer.film:2 alt.binaries.x:3",
			want: []string{"es.binarios.cine", "nl.binaer.film", "alt.binaries.x"},
		}, {
			name: "duplicates collapse",
			in:   "srv alt.binaries.mom:1 alt.binaries.mom:2",
			want: []string{"alt.binaries.mom"},
		}, {
			name: "empty header yields nothing",
			in:   "",
			want: nil,
		}, {
			// Absence must degrade to "we only know the crawled group", never to
			// a parse error that drops the article.
			name: "garbage yields nothing rather than junk groups",
			in:   "!!! ??? :::",
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := xrefGroups(c.in); !reflect.DeepEqual(got, c.want) {
				t.Errorf("xrefGroups(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// The assembled document must name every group the posting lives in, and must
// always include the group we crawled — so a server that sends no Xref produces
// exactly the previous behaviour rather than an empty <groups>, which NNTmux
// treats as a hard failure and which no downloader can use.
func TestFileGroupsUnionIncludesCrawledGroup(t *testing.T) {
	arts := []stagedArticle{
		{Group: "alt.binaries.teevee", XrefGroups: []string{"alt.binaries.teevee", "alt.binaries.mom"}, PartNum: 1},
		{Group: "alt.binaries.teevee", XrefGroups: []string{"alt.binaries.boneless"}, PartNum: 2},
	}
	want := []string{"alt.binaries.boneless", "alt.binaries.mom", "alt.binaries.teevee"}
	if got := fileGroups(arts); !reflect.DeepEqual(got, want) {
		t.Errorf("fileGroups = %v, want %v (sorted union, crawled group always present)", got, want)
	}

	// No Xref at all: the crawled group, and nothing invented.
	bare := []stagedArticle{{Group: "alt.binaries.misc", PartNum: 1}}
	if got := fileGroups(bare); !reflect.DeepEqual(got, []string{"alt.binaries.misc"}) {
		t.Errorf("fileGroups with no Xref = %v, want just the crawled group", got)
	}
}

// The group list reaches the document, which is the only place a downloader can
// see it. NZBGet with JoinGroup=yes tries each listed group in turn and stops at
// the first success, so more groups is more chances to hit one the member's
// provider carries.
func TestBuildNZBEmitsEveryCrosspostGroup(t *testing.T) {
	arts := []stagedArticle{
		{MessageID: "<a@n>", Group: "alt.binaries.teevee", PartNum: 1, TotalParts: 2, Bytes: 10,
			XrefGroups: []string{"alt.binaries.teevee", "alt.binaries.mom"}},
		{MessageID: "<b@n>", Group: "alt.binaries.teevee", PartNum: 2, TotalParts: 2, Bytes: 10,
			XrefGroups: []string{"alt.binaries.teevee", "alt.binaries.mom"}},
	}
	doc, _, err := buildNZB(arts)
	if err != nil {
		t.Fatalf("buildNZB: %v", err)
	}
	for _, want := range []string{"<group>alt.binaries.mom</group>", "<group>alt.binaries.teevee</group>"} {
		if !containsStr(string(doc), want) {
			t.Errorf("document does not contain %s — the crosspost groups never reach the file a member downloads", want)
		}
	}
}

func containsStr(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
