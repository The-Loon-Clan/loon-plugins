package wiki

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// testContext builds a minimal *gin.Context carrying a request so
// handler helpers that read c.Request.Context() work off a MemStore.
func testContext() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/wiki", nil)
	return c
}

// postsByTopicMap folds the flat AllPosts projection into a
// topic_id-keyed map for the sidebar tree. This is real handler
// logic (grouping + the content-blanked projection), not PGStore
// delegation.
func TestPostsByTopicMap_GroupsByTopic(t *testing.T) {
	s := NewMemStore()
	h := NewHandlers(s, nil)
	c := testContext()

	a := seedTopic(s, t, "a", 0)
	b := seedTopic(s, t, "b", 1)
	empty := seedTopic(s, t, "empty", 2)
	seedPost(s, t, a.ID, "a1")
	seedPost(s, t, a.ID, "a2")
	seedPost(s, t, b.ID, "b1")

	got := h.postsByTopicMap(c)

	if len(got[a.ID]) != 2 {
		t.Errorf("topic a: want 2 posts, got %d", len(got[a.ID]))
	}
	if len(got[b.ID]) != 1 {
		t.Errorf("topic b: want 1 post, got %d", len(got[b.ID]))
	}
	// A topic with no posts must not appear as a key.
	if _, ok := got[empty.ID]; ok {
		t.Errorf("empty topic should not be a key in the map")
	}
	// AllPosts blanks the content column — the folded rows inherit that.
	for _, p := range got[a.ID] {
		if p.Content != "" {
			t.Errorf("sidebar payload should have blank content, got %q", p.Content)
		}
	}
}

func TestPostsByTopicMap_EmptyStore(t *testing.T) {
	h := NewHandlers(NewMemStore(), nil)
	got := h.postsByTopicMap(testContext())
	if got == nil {
		t.Fatal("want non-nil empty map, got nil")
	}
	if len(got) != 0 {
		t.Errorf("want empty map, got %d keys", len(got))
	}
}

func TestBuildWikiLandingStats(t *testing.T) {
	topics := []*Topic{{ID: 1}, {ID: 2}}
	posts := map[int][]*Post{
		1: {
			{ID: 1, CreatedBy: 7, ViewCount: 12},
			{ID: 2, CreatedBy: 9, ViewCount: 5},
		},
		2: {{ID: 3, CreatedBy: 7, ViewCount: 3}},
	}

	got := buildWikiLandingStats(topics, posts)
	if got.Topics != 2 || got.Articles != 3 || got.Contributors != 2 || got.Views != 20 {
		t.Fatalf("unexpected stats: %+v", got)
	}
}

func TestBuildWikiLandingStatsFallsBackToTopicCounts(t *testing.T) {
	topics := []*Topic{{ID: 1, PostCount: 4}, {ID: 2, PostCount: 3}}
	got := buildWikiLandingStats(topics, map[int][]*Post{})
	if got.Articles != 7 {
		t.Fatalf("articles: want 7, got %d", got.Articles)
	}
}
