package wiki

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// maxUploadSize bounds admin image uploads; the destination directory and
// public URL prefix come from Deps (host filesystem layout).
const maxUploadSize = 5 << 20 // 5 MB

var allowedMIME = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// UploadImage stores an admin-uploaded image under
// /static/wiki-uploads/ and returns its public URL for embedding
// in post markdown. MIME is sniffed from the first 512 bytes, not
// trusted from the filename.
func (h *Handlers) UploadImage(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadSize)

	file, _, err := c.Request.FormFile("file")
	if err != nil {
		jsonError(c, http.StatusBadRequest, "no file uploaded")
		return
	}
	defer file.Close()

	// Read first 512 bytes to detect MIME type
	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil || n == 0 {
		jsonError(c, http.StatusBadRequest, "could not read file")
		return
	}
	mime := http.DetectContentType(buf[:n])
	ext, ok := allowedMIME[mime]
	if !ok {
		jsonError(c, http.StatusBadRequest, fmt.Sprintf("unsupported file type: %s", mime))
		return
	}

	// Seek back to start (multipart.File supports Seek)
	type seeker interface {
		Seek(int64, int) (int64, error)
	}
	if s, ok2 := file.(seeker); ok2 {
		s.Seek(0, 0)
	}

	if err := os.MkdirAll(deps.UploadDir, 0755); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not create upload directory")
		return
	}

	filename := uuid.New().String() + ext
	destPath := filepath.Join(deps.UploadDir, filename)

	data, err := io.ReadAll(file)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "could not read file")
		return
	}
	if err := os.WriteFile(destPath, data, 0644); err != nil {
		jsonError(c, http.StatusInternalServerError, "could not save file")
		return
	}

	c.JSON(http.StatusOK, gin.H{"url": deps.UploadURL + filename})
}
