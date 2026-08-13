package roadmap

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/the-loon-clan/loon/blob"
)

// maxProposalUpload bounds one attached image.
//
// Smaller than the wiki's 5 MB because the audiences differ: wiki uploads
// come from mods, these come from any signed-in member. The images are
// screenshots and mockups illustrating a request, which do not need more.
const maxProposalUpload = 2 << 20 // 2 MB

// UploadProposalImage stores an image for a feature request and returns its
// URL for embedding in the description markdown.
//
// This is the plugin's only member-writable storage path, so the checks are
// the point rather than an afterthought:
//
//   - Signed in. Anonymous upload to disk is how a site becomes a file host.
//   - MaxBytesReader before reading, so an oversized body is refused as it
//     arrives instead of after it has been buffered.
//   - Type sniffed from the bytes via blob.SniffImage, never taken from the
//     filename. The extension it returns is what names the stored file, so a
//     .png that is actually a script is stored as neither.
//   - A UUID name. The uploader does not choose the path, which is what keeps
//     one member from overwriting another's file or escaping the namespace.
//
// Returns 501 when the host wired no Files store: attachments are optional,
// and a host that did not opt in should say so plainly rather than 500.
func (h *Handlers) UploadProposalImage(c *gin.Context) {
	if h.requireAuthed(c) {
		return
	}
	if deps.Files == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"ok": false, "error": "attachments are not enabled on this site"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxProposalUpload)

	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "no file uploaded, or it is over 2 MB"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil || len(data) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "could not read file"})
		return
	}
	_, ext, err := blob.SniffImage(data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
		return
	}
	url, err := deps.Files.Save(c.Request.Context(), "proposal-uploads/"+uuid.New().String()+ext, data)
	if err != nil {
		h.errs.HandlerError(c, "flow/proposals/upload", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "url": url})
}
