package usenet

import (
	"bytes"
	"fmt"
	"testing"
)

// yencEncodePart wraps payload bytes as one yEnc article body, with =ypart
// offsets when begin > 0 — the multipart form a proof JPG actually posts as.
func yencEncodePart(payload []byte, part, total int, begin, whole int64) []byte {
	var enc []byte
	if begin > 0 {
		enc = append(enc, []byte(fmt.Sprintf(
			"=ybegin part=%d total=%d line=128 size=%d name=proof.jpg\r\n", part, total, whole))...)
		enc = append(enc, []byte(fmt.Sprintf(
			"=ypart begin=%d end=%d\r\n", begin, begin+int64(len(payload))-1))...)
	} else {
		enc = append(enc, []byte(fmt.Sprintf(
			"=ybegin line=128 size=%d name=proof.jpg\r\n", len(payload)))...)
	}
	for i := 0; i < len(payload); i++ {
		c := payload[i] + 42
		switch c {
		case 0x00, 0x0A, 0x0D, 0x3D:
			enc = append(enc, '=', c+64)
		default:
			enc = append(enc, c)
		}
	}
	enc = append(enc, []byte(fmt.Sprintf("\r\n=yend size=%d\r\n", len(payload)))...)
	return enc
}

// A multipart image reassembles into the original bytes — and =ypart offsets
// win over arrival order, because a document listing parts out of order would
// otherwise produce a scrambled file that still begins with a valid magic.
func TestAssembleImageJoinsPartsByOffset(t *testing.T) {
	img := append(append([]byte{}, jpegMagic...), bytes.Repeat([]byte("payload"), 100)...)
	cut := len(img) / 2
	p1 := yencEncodePart(img[:cut], 1, 2, 1, int64(len(img)))
	p2 := yencEncodePart(img[cut:], 2, 2, int64(cut+1), int64(len(img)))

	got, ok := assembleImage([][]byte{p1, p2})
	if !ok {
		t.Fatal("in-order parts did not assemble")
	}
	if !bytes.Equal(got, img) {
		t.Errorf("assembled %d bytes, want %d", len(got), len(img))
	}

	// Same parts, listed backwards.
	got, ok = assembleImage([][]byte{p2, p1})
	if !ok {
		t.Fatal("out-of-order parts did not assemble")
	}
	if !bytes.Equal(got, img) {
		t.Error("out-of-order parts assembled scrambled — =ypart offsets were ignored")
	}
}

func TestAssembleImageSinglePartPNG(t *testing.T) {
	img := append(append([]byte{}, pngMagic...), []byte("png-ish body")...)
	got, ok := assembleImage([][]byte{yencEncodePart(img, 0, 0, 0, 0)})
	if !ok {
		t.Fatal("single-part image rejected")
	}
	if !bytes.Equal(got, img) {
		t.Errorf("got %d bytes, want %d", len(got), len(img))
	}
}

// The filename promised an image and the bytes are something else — the
// mislabelling check, mirroring looksLikeAnotherFormat on the NFO side.
func TestAssembleImageRejectsNonImages(t *testing.T) {
	if _, ok := assembleImage([][]byte{yencEncodePart([]byte("RIFF not an image"), 0, 0, 0, 0)}); ok {
		t.Error("non-image payload accepted")
	}
	if _, ok := assembleImage([][]byte{[]byte("no ybegin here at all")}); ok {
		t.Error("non-yEnc body accepted")
	}
}
