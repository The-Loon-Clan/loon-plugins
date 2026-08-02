package communities

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"io"

	_ "golang.org/x/image/webp"
	_ "image/gif" // register decoders
	_ "image/png"

	"golang.org/x/image/draw"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/the-loon-clan/loon/blob"
)

// communityMaxUpload caps the raw upload before processing. Source
// images can be large (a 4 MB PNG banner is common); we re-encode to
// a much smaller JPEG below, so this is just an anti-OOM ceiling.
const communityMaxUpload = 12 << 20 // 12 MB

// communityMaxInputPixels guards the decode against image bombs (a
// tiny file claiming 30k x 30k dimensions allocates W*H*4 bytes on
// decode). Same defense and value as the avatar pipeline.
const communityMaxInputPixels = 4096 * 4096

// communityJPEGQuality balances size vs artefacts for the re-encoded
// banner/icon. 82 is the usual "good enough, much smaller" sweet spot.
const communityJPEGQuality = 82

// imageKind sizing — banners are wide, icons are square. maxW caps the
// output width (height scales proportionally); the source is never
// upscaled, so a small upload stays small.
type imageKind struct {
	field string
	maxW  int
}

var (
	bannerKind = imageKind{field: "banner", maxW: 1600}
	iconKind   = imageKind{field: "icon", maxW: 256}
)

// saveCommunityImage reads one multipart file field, decodes + resizes
// it to the kind's max width, re-encodes as a compressed JPEG, and
// stores it under the community/ namespace with a uuid name. Returns
// the public URL. Returns ("", nil) when the field is absent (the
// common case — the settings form submits without re-uploading every
// time), so callers keep the existing URL on an empty field.
//
// Compression is the point: a multi-MB PNG banner becomes a ~100–250
// KB JPEG, and oversized uploads are scaled down to a sane width so
// the header strip isn't shipping a 4000px image.
func saveCommunityImage(c *gin.Context, kind imageKind, slug string) (string, error) {
	fh, err := c.FormFile(kind.field)
	if err != nil {
		// No upload this submit — not an error; caller keeps current value.
		return "", nil
	}
	if fh.Size > communityMaxUpload {
		return "", fmt.Errorf("%s too large (max %d MB)", kind.field, communityMaxUpload>>20)
	}

	src, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()
	raw, err := io.ReadAll(io.LimitReader(src, communityMaxUpload+1))
	if err != nil {
		return "", err
	}

	// MIME sniff — don't trust the filename extension.
	if _, _, err := blob.SniffImage(raw); err != nil {
		return "", fmt.Errorf("%s: unsupported image type", kind.field)
	}
	// Pixel-bomb defense before the full decode allocates W*H*4.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("%s: could not decode image", kind.field)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 ||
		int64(cfg.Width)*int64(cfg.Height) > int64(communityMaxInputPixels) {
		return "", fmt.Errorf("%s: image dimensions too large", kind.field)
	}

	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("%s: could not decode image", kind.field)
	}
	out := resizeToWidth(img, kind.maxW)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: communityJPEGQuality}); err != nil {
		return "", err
	}
	name := fmt.Sprintf("community/%s-%s-%s.jpg", slug, kind.field, uuid.New().String()[:8])
	if deps.Files == nil {
		// Uploads are optional (see Provision): say so plainly rather than
		// panicking on a nil interface.
		return "", fmt.Errorf("communities: no upload store configured on this host")
	}
	return deps.Files.Save(c.Request.Context(), name, buf.Bytes())
}

// resizeToWidth scales img down so its width is at most maxW,
// preserving aspect ratio. Never upscales — a source narrower than
// maxW is returned unchanged. Uses CatmullRom (the same high-quality
// kernel the avatar pipeline uses).
func resizeToWidth(img image.Image, maxW int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxW || maxW <= 0 {
		return img
	}
	newW := maxW
	newH := h * maxW / w
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// randomInviteCode returns a 16-char URL-safe random code for an
// invite link.
func randomInviteCode() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
