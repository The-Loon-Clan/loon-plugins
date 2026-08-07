package lists

import (
	"archive/zip"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Handlers serves /lists* + /community/watchlists.
type Handlers struct{}

// UserLists shows the current user's lists.
func (h *Handlers) UserLists(c *gin.Context) {
	userID, _, _ := deps.Viewer(c)
	owned, followed, _ := deps.UserLists(c.Request.Context(), userID)
	page(c, "My Lists", "user_lists.html", userListsVM{Lists: owned, Followed: followed})
}

// CreateList creates a new user list.
func (h *Handlers) CreateList(c *gin.Context) {
	userID, _, _ := deps.Viewer(c)
	name := strings.TrimSpace(c.PostForm("name"))
	desc := strings.TrimSpace(c.PostForm("description"))
	public := c.PostForm("public") == "1"
	if name == "" {
		c.Redirect(http.StatusFound, "/lists")
		return
	}
	_ = deps.Create(c.Request.Context(), userID, name, desc, public)
	c.Redirect(http.StatusFound, "/lists")
}

// DeleteList removes a user list.
func (h *Handlers) DeleteList(c *gin.Context) {
	listID, _ := strconv.Atoi(c.Param("id"))
	userID, _, _ := deps.Viewer(c)
	_ = deps.Delete(c.Request.Context(), listID, userID)
	c.Redirect(http.StatusFound, "/lists")
}

// SetListPublic toggles the public flag on a list.
func (h *Handlers) SetListPublic(c *gin.Context) {
	listID, _ := strconv.Atoi(c.Param("id"))
	userID, _, _ := deps.Viewer(c)
	public := c.PostForm("public") == "1"
	_ = deps.SetPublic(c.Request.Context(), listID, userID, public)
	c.Redirect(http.StatusFound, "/lists/"+strconv.Itoa(listID))
}

func (h *Handlers) FollowList(c *gin.Context) {
	listID, _ := strconv.Atoi(c.Param("id"))
	userID, username, _ := deps.Viewer(c)
	ctx := c.Request.Context()
	if err := deps.Follow(ctx, userID, listID); err == nil {
		// UserID is the FOLLOWER. The owner is notified below and is
		// deliberately not the event's subject: they did not do anything.
		emit(ctx, EventFollowed, userID, strconv.Itoa(listID), Followed{ListID: listID})

		if deps.NotifyFollow != nil {
			// Resolve the list owner so we know who to notify. Skipped on
			// any error so a slow lookup doesn't break the redirect.
			if list, err := deps.ByID(ctx, listID); err == nil && list != nil {
				deps.NotifyFollow(ctx, list.UserID, userID, username, list.Name, int64(listID))
			}
		}
	}
	c.Redirect(http.StatusFound, "/lists/"+strconv.Itoa(listID))
}

func (h *Handlers) UnfollowList(c *gin.Context) {
	listID, _ := strconv.Atoi(c.Param("id"))
	userID, _, _ := deps.Viewer(c)
	_ = deps.Unfollow(c.Request.Context(), userID, listID)
	c.Redirect(http.StatusFound, "/lists")
}

func (h *Handlers) CopyList(c *gin.Context) {
	listID, _ := strconv.Atoi(c.Param("id"))
	userID, username, _ := deps.Viewer(c)
	newID, err := deps.Copy(c.Request.Context(), listID, userID, username)
	if err != nil {
		c.Redirect(http.StatusFound, "/lists")
		return
	}
	c.Redirect(http.StatusFound, "/lists/"+strconv.Itoa(newID))
}

// ViewList shows the contents of a list.
func (h *Handlers) ViewList(c *gin.Context) {
	listID, _ := strconv.Atoi(c.Param("id"))
	list, err := deps.ByID(c.Request.Context(), listID)
	if err != nil || list == nil {
		c.String(http.StatusNotFound, "list not found")
		return
	}
	// Only owner can view private lists
	userID, _, _ := deps.Viewer(c)
	if !list.Public && list.UserID != userID {
		c.String(http.StatusForbidden, "this list is private")
		return
	}
	items, _ := deps.Items(c.Request.Context(), listID)
	isFollowing, _ := deps.IsFollowing(c.Request.Context(), userID, listID)
	page(c, list.Name, "list_detail.html", listDetailVM{
		List:        list,
		Items:       items,
		IsFollowing: isFollowing,
		IsOwner:     list.UserID == userID,
		ViewerID:    userID,
		NzbCardCSS:  deps.NzbCardCSS(),
		ReportModal: deps.ReportModal(c),
	})
}

// DownloadAllList serves all NZBs in a list as a ZIP archive.
func (h *Handlers) DownloadAllList(c *gin.Context) {
	ctx := c.Request.Context()
	listID, _ := strconv.Atoi(c.Param("id"))
	userID, _, _ := deps.Viewer(c)

	if !deps.DownloadAllowed(c) {
		deps.RenderError(c, http.StatusForbidden,
			"Download blocked: your IP does not match your pinned browse IP.")
		return
	}

	list, err := deps.ByID(ctx, listID)
	if err != nil || list == nil {
		c.String(http.StatusNotFound, "list not found")
		return
	}
	if !list.Public && list.UserID != userID {
		c.String(http.StatusForbidden, "this list is private")
		return
	}

	items, err := deps.Items(ctx, listID)
	if err != nil || len(items) == 0 {
		c.String(http.StatusNotFound, "no items in list")
		return
	}

	c.Header("Content-Type", "application/zip")
	safeName := sanitizeListFilename(list.Name)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, safeName))

	zw := zip.NewWriter(c.Writer)
	defer zw.Close()

	for _, nzb := range items {
		compressed, err := deps.NzbData(ctx, nzb.ID)
		if err != nil || len(compressed) == 0 {
			continue
		}
		raw, err := deps.Gunzip(compressed)
		if err != nil {
			continue
		}
		fname := nzb.Filename
		if !strings.HasSuffix(strings.ToLower(fname), ".nzb") {
			fname += ".nzb"
		}
		w, err := zw.Create(fname)
		if err != nil {
			continue
		}
		_, _ = w.Write(raw)
	}

	_ = deps.IncrementDownloads(ctx, listID)
}

// AddToList adds a release to a user list (AJAX).
func (h *Handlers) AddToList(c *gin.Context) {
	nzbID, _ := strconv.ParseInt(c.PostForm("nzb_id"), 10, 64)
	listID, _ := strconv.Atoi(c.PostForm("list_id"))
	userID, _, _ := deps.Viewer(c)
	err := deps.AddItem(c.Request.Context(), listID, nzbID, userID)
	if err != nil {
		deps.JSONError(c, http.StatusBadRequest, err.Error())
		return
	}
	emit(c.Request.Context(), EventItemAdded, userID, strconv.FormatInt(nzbID, 10),
		ItemAdded{ListID: listID, NzbID: nzbID})
	deps.JSONOK(c, nil)
}

// RemoveFromList removes a release from a user list (AJAX).
func (h *Handlers) RemoveFromList(c *gin.Context) {
	nzbID, _ := strconv.ParseInt(c.PostForm("nzb_id"), 10, 64)
	listID, _ := strconv.Atoi(c.PostForm("list_id"))
	userID, _, _ := deps.Viewer(c)
	_ = deps.RemoveItem(c.Request.Context(), listID, nzbID, userID)
	deps.JSONOK(c, nil)
}

// ReleaseLists shows all lists containing a release.
func (h *Handlers) ReleaseLists(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	nzb, ls, err := deps.ListsForNzb(c.Request.Context(), id)
	if err != nil || nzb == nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	page(c, "Lists containing "+nzb.Title, "release_lists.html",
		releaseListsVM{Nzb: nzb, Lists: ls})
}

// WatchLists renders the public-watchlists index as a unified poster
// grid. Loads a generous slice from each axis (new / top by item
// count / most grabbed) and merges them into a single deduped slice
// the template renders as cover-art cards. Sort + search are client-
// side over this combined set; with a few hundred public lists the
// payload stays small enough that server-side pagination would be
// premature.
func (h *Handlers) WatchLists(c *gin.Context) {
	const fetchPerAxis = 60
	newLists, topLists, topGrabbed, _ := deps.Discovery(c.Request.Context(), fetchPerAxis)

	// Dedup by id so a list that appears on multiple axes only renders
	// once. Insertion order = "new first" because that's the most
	// useful default for spotting fresh activity; the template's sort
	// dropdown can re-order client-side.
	combined := dedupPublicLists(newLists, topLists, topGrabbed)
	page(c, "Watchlists", "community_watchlists.html",
		watchlistsVM{Lists: combined, NzbCardCSS: deps.NzbCardCSS()})
}

// sanitizeListFilename strips characters that are illegal in a
// Content-Disposition filename / on common filesystems, replacing
// each with an underscore. Pure — used to derive the ZIP name from a
// user-supplied list name in DownloadAllList.
func sanitizeListFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '<' || r == '>' || r == '|' {
			return '_'
		}
		return r
	}, name)
}

// dedupPublicLists merges the discovery axes into a single slice,
// keeping the first occurrence of each list ID and preserving the
// order in which axes (and lists within them) are supplied.
func dedupPublicLists(axes ...[]List) []List {
	seen := map[int]bool{}
	combined := make([]List, 0)
	for _, axis := range axes {
		for _, l := range axis {
			if seen[l.ID] {
				continue
			}
			seen[l.ID] = true
			combined = append(combined, l)
		}
	}
	return combined
}
