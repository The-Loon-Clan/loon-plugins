package usenet

import (
	"bufio"
	"bytes"
	"crypto/rsa"
	"crypto/sha1"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
)

// liveSpot is one real spot, reduced to the three fields verification touches.
type liveSpot struct {
	messageID string
	key       string
	signature string
}

// loadLiveSpots reads testdata/spot_signatures.txt — genuine articles captured
// from free.pt on 2026-08-16 with indexer-tools/spot_capture.py.
//
// Only the message-id, key and signature are kept. The titles, descriptions and
// NZB pointers are deliberately NOT in the repo: none of them is what these
// tests are about, and there is no reason for a public repository to carry a
// list of release names.
func loadLiveSpots(t *testing.T) []liveSpot {
	t.Helper()
	f, err := os.Open("testdata/spot_signatures.txt")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	defer f.Close()

	var out []liveSpot
	var cur liveSpot
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		name, val, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		switch strings.ToLower(name) {
		case "message-id":
			cur.messageID = val
		case "x-user-key":
			cur.key = val
		case "x-user-signature":
			cur.signature = val
			out = append(out, cur)
			cur = liveSpot{}
		}
	}
	if len(out) < 2 {
		t.Fatalf("fixture holds only %d spots", len(out))
	}
	return out
}

// THE ONE THAT MATTERS. Real spots, real keys, real signatures. Before the
// escaping in DecodeSpotBase64 was understood, 11 of 12 of these failed — and
// they failed as "bad signature", which is indistinguishable from a forged feed
// unless something checks known-good articles.
func TestVerifySpotAgainstLiveSpots(t *testing.T) {
	spots := loadLiveSpots(t)
	var strong, weak int
	for _, s := range spots {
		pub, err := ParseSpotKey(s.key)
		if err != nil {
			t.Fatalf("%s: key did not parse: %v", s.messageID, err)
		}
		sig, err := SpotSignatureBytes(s.signature)
		if err != nil {
			t.Fatalf("%s: signature did not decode: %v", s.messageID, err)
		}
		// A PKCS#1 v1.5 signature is exactly as long as the modulus. This is
		// the assertion that catches a decoding bug directly, rather than
		// letting it surface as an indistinguishable "bad signature".
		if got, want := len(sig), (pub.N.BitLen()+7)/8; got != want {
			t.Errorf("%s: signature is %d bytes for a %d-bit modulus (want %d) — the base64 escaping is wrong",
				s.messageID, got, pub.N.BitLen(), want)
			continue
		}

		err = VerifySpot(pub, s.messageID, sig)
		switch {
		case pub.N.BitLen() < MinSpotKeyBits:
			weak++
			if !errors.Is(err, ErrSpotWeakKey) {
				t.Errorf("%s: %d-bit key gave %v, want ErrSpotWeakKey", s.messageID, pub.N.BitLen(), err)
			}
			// The signature must still be CRYPTOGRAPHICALLY sound — otherwise
			// this test would pass on a broken decoder simply because the key
			// was rejected before the maths ran. crypto/rsa will not do it:
			// Go refuses sub-1024-bit keys outright, which is the standard
			// library reaching the same conclusion as MinSpotKeyBits. So the
			// modular exponentiation is done by hand here.
			if !rawPKCS1v15SHA1OK(pub, s.messageID, sig) {
				t.Errorf("%s: weak-key spot did not verify on its own terms — the decode is wrong", s.messageID)
			}
		default:
			strong++
			if err != nil {
				t.Errorf("%s: genuine spot rejected: %v", s.messageID, err)
			}
		}
	}
	if strong == 0 || weak == 0 {
		t.Fatalf("fixture must cover both strengths: %d strong, %d weak", strong, weak)
	}
	t.Logf("%d spots: %d verified, %d refused as too weak", len(spots), strong, weak)
}

// A verifier that accepts a tampered spot is worse than none, because it
// launders the forgery as checked.
func TestVerifySpotRejectsTampering(t *testing.T) {
	for _, s := range loadLiveSpots(t) {
		pub, _ := ParseSpotKey(s.key)
		sig, _ := SpotSignatureBytes(s.signature)
		if pub == nil || pub.N.BitLen() < MinSpotKeyBits {
			continue
		}
		// Flip one byte of the message-id: the attack is claiming someone
		// else's signature for your own article.
		b := []byte(s.messageID)
		b[5] ^= 0x01
		if err := VerifySpot(pub, string(b), sig); !errors.Is(err, ErrSpotBadSignature) {
			t.Errorf("%s: tampered message-id gave %v, want ErrSpotBadSignature", s.messageID, err)
		}
		// And a flipped signature byte against the genuine message-id.
		bad := append([]byte(nil), sig...)
		bad[0] ^= 0x01
		if err := VerifySpot(pub, s.messageID, bad); !errors.Is(err, ErrSpotBadSignature) {
			t.Errorf("%s: tampered signature gave %v, want ErrSpotBadSignature", s.messageID, err)
		}
	}
}

// The brackets are part of the signed content, so a bare id and a bracketed one
// must produce the same result — otherwise verification depends on which header
// form a caller happened to pass.
func TestVerifySpotNormalisesBrackets(t *testing.T) {
	for _, s := range loadLiveSpots(t) {
		pub, _ := ParseSpotKey(s.key)
		sig, _ := SpotSignatureBytes(s.signature)
		if pub == nil || pub.N.BitLen() < MinSpotKeyBits {
			continue
		}
		bare := strings.TrimSuffix(strings.TrimPrefix(s.messageID, "<"), ">")
		if bare == s.messageID {
			t.Fatalf("fixture message-id %q carries no brackets", s.messageID)
		}
		if err := VerifySpot(pub, bare, sig); err != nil {
			t.Errorf("%s: unbracketed form rejected: %v", s.messageID, err)
		}
		return
	}
}

// The escaping that cost 11 of 12 verifications. '-p' and '-s' are TWO
// characters standing for one, not URL-safe single-character substitutions.
func TestDecodeSpotBase64Escaping(t *testing.T) {
	// "+/+/" encodes bytes fb ef be; written Spotnet-style it is "-p-s-p-s".
	got, err := DecodeSpotBase64("-p-s-p-s")
	if err != nil {
		t.Fatalf("escaped value did not decode: %v", err)
	}
	want, err := DecodeSpotBase64("+/+/")
	if err != nil {
		t.Fatalf("plain value did not decode: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("escaped %x != unescaped %x", got, want)
	}
	// The URL-safe reading would leave a stray 'p'/'s' and change the length,
	// which is exactly how the bug hid.
	if len(got) != 3 {
		t.Errorf("decoded %d bytes, want 3 — the escape consumed the wrong number of characters", len(got))
	}
	// Standard base64 contains no '-', so an unescaped value must round-trip
	// untouched through the same decoder.
	if b, err := DecodeSpotBase64("dGVzdA=="); err != nil || string(b) != "test" {
		t.Errorf("unescaped value = %q, %v", b, err)
	}
	if _, err := SpotSignatureBytes("!!!"); !errors.Is(err, ErrSpotBadSignature) {
		t.Error("undecodable signature was accepted")
	}
}

func TestParseSpotKey(t *testing.T) {
	for _, s := range loadLiveSpots(t) {
		pub, err := ParseSpotKey(s.key)
		if err != nil {
			t.Fatalf("live key did not parse: %v", err)
		}
		// AQAB is 65537, the universal RSA exponent. Getting this wrong means
		// the big-endian decode is wrong, which stays invisible until a
		// signature fails for the wrong reason.
		if pub.E != 65537 {
			t.Errorf("exponent = %d, want 65537", pub.E)
		}
		if pub.N == nil || pub.N.BitLen() == 0 {
			t.Error("modulus decoded to nothing")
		}
	}
}

func TestParseSpotKeyRejectsRubbish(t *testing.T) {
	for _, in := range []string{
		"",
		"not xml at all",
		`<RSAKeyValue><Modulus>!!!not base64!!!</Modulus><Exponent>AQAB</Exponent></RSAKeyValue>`,
		`<RSAKeyValue><Modulus></Modulus><Exponent>AQAB</Exponent></RSAKeyValue>`,
		// Exponent 1 makes every signature trivially forgeable.
		`<RSAKeyValue><Modulus>dGVzdA==</Modulus><Exponent>AQ==</Exponent></RSAKeyValue>`,
	} {
		if _, err := ParseSpotKey(in); !errors.Is(err, ErrSpotBadKey) {
			t.Errorf("ParseSpotKey(%.40q) = %v, want ErrSpotBadKey", in, err)
		}
	}
}

// Half the live feed signs with 384-bit keys, which factor on a laptop. Calling
// those "verified" would be the failure this package exists to avoid.
//
// The case is taken from the real feed rather than generated, because Go will
// not generate a key that weak — so a synthetic version of this test could only
// ever be skipped, and would have hidden that the live feed is full of them.
func TestVerifySpotRefusesWeakKeys(t *testing.T) {
	var found int
	for _, s := range loadLiveSpots(t) {
		pub, err := ParseSpotKey(s.key)
		if err != nil || pub.N.BitLen() >= MinSpotKeyBits {
			continue
		}
		found++
		sig, err := SpotSignatureBytes(s.signature)
		if err != nil {
			t.Fatalf("%s: %v", s.messageID, err)
		}
		// A GENUINE signature — it passes the raw arithmetic above — refused
		// anyway, because a 384-bit key proves nothing about who posted it.
		if !rawPKCS1v15SHA1OK(pub, s.messageID, sig) {
			t.Fatalf("%s: fixture signature is not actually valid", s.messageID)
		}
		if err := VerifySpot(pub, s.messageID, sig); !errors.Is(err, ErrSpotWeakKey) {
			t.Errorf("%s: %d-bit key gave %v, want ErrSpotWeakKey", s.messageID, pub.N.BitLen(), err)
		}
	}
	if found == 0 {
		t.Skip("fixture carries no weak keys")
	}
}

// rawPKCS1v15SHA1OK does the RSA verification by hand: sig^e mod n, then check
// the PKCS#1 v1.5 padding and the SHA-1 DigestInfo.
//
// Only for keys crypto/rsa refuses to touch. Short enough to be obviously
// correct, and it exists so that "the standard library will not verify this"
// cannot be mistaken for "this signature is invalid".
func rawPKCS1v15SHA1OK(pub *rsa.PublicKey, message string, sig []byte) bool {
	k := (pub.N.BitLen() + 7) / 8
	if len(sig) != k {
		return false
	}
	em := new(big.Int).Exp(new(big.Int).SetBytes(sig), big.NewInt(int64(pub.E)), pub.N).Bytes()
	// Bytes() drops the leading zero of the 0x00 0x01 ... prefix.
	digestInfo := []byte{0x30, 0x21, 0x30, 0x09, 0x06, 0x05, 0x2b, 0x0e, 0x03, 0x02, 0x1a, 0x05, 0x00, 0x04, 0x14}
	sum := sha1.Sum([]byte(message))
	want := append([]byte{0x01}, bytes.Repeat([]byte{0xff}, k-3-len(digestInfo)-len(sum))...)
	want = append(want, 0x00)
	want = append(want, digestInfo...)
	want = append(want, sum[:]...)
	return bytes.Equal(em, want)
}

func TestVerifySpotRejectsANilKey(t *testing.T) {
	if err := VerifySpot(nil, "<x@spot.net>", []byte("y")); !errors.Is(err, ErrSpotBadKey) {
		t.Error("nil key was not rejected")
	}
	if err := VerifySpot(&rsa.PublicKey{}, "<x@spot.net>", []byte("y")); !errors.Is(err, ErrSpotBadKey) {
		t.Error("key with nil modulus was not rejected")
	}
}
