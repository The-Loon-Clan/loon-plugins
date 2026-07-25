package wiki

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/the-loon-clan/loon/blob"
)

// maxUploadSize bounds admin image uploads; where the bytes land and
// what URL they serve under is the host's blob.Store (Deps.Files).
const maxUploadSize = 5 << 20 // 5 MB

// UploadImage stores an admin-uploaded image under the store's
// wiki-uploads/ namespace and returns its public URL for embedding
// in post markdown. MIME is sniffed from the payload, not trusted
// from the filename.
func (h *Handlers) UploadImage(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadSize)

	file, _, err := c.Request.FormFile("file")
	if err != nil {
		jsonError(c, http.StatusBadRequest, "no file uploaded")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil || len(data) == 0 {
		jsonError(c, http.StatusBadRequest, "could not read file")
		return
	}
	_, ext, err := blob.SniffImage(data)
	if err != nil {
		jsonError(c, http.StatusBadRequest, err.Error())
		return
	}

	url, err := deps.Files.Save(c.Request.Context(), "wiki-uploads/"+uuid.New().String()+ext, data)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save file")
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": url})
}
