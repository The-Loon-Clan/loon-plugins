package usenet

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"testing"
)

// The real key from the spike, truncated to its shape. Enough to prove the
// .NET RSAKeyValue decoding path, which is the half of verification that is
// actually known.
const spotKeyHeader = `<RSAKeyValue><Modulus>tZ/DNmKoDIXH4P0v9zbalNL2U/ZKYFmkLWMqr1y6dgdkYVoKROkTp18gmu6cMWsl</Modulus><Exponent>AQAB</Exponent></RSAKeyValue>`

func TestParseSpotKey(t *testing.T) {
	pub, err := ParseSpotKey(spotKeyHeader)
	if err != nil {
		t.Fatalf("the live key did not parse: %v", err)
	}
	// AQAB is 65537, the universal RSA exponent. Getting this wrong means the
	// big-endian decode is wrong, which would be invisible until a signature
	// check failed for the wrong reason.
	if pub.E != 65537 {
		t.Errorf("exponent = %d, want 65537 (AQAB big-endian)", pub.E)
	}
	if pub.N == nil || pub.N.BitLen() == 0 {
		t.Error("modulus decoded to nothing")
	}
}

func TestParseSpotKeyRejectsRubbish(t *testing.T) {
	for _, in := range []string{
		"",
		"not xml at all",
		`<RSAKeyValue><Modulus>!!!not base64!!!</Modulus><Exponent>AQAB</Exponent></RSAKeyValue>`,
		`<RSAKeyValue><Modulus></Modulus><Exponent>AQAB</Exponent></RSAKeyValue>`,
		// Exponent 1 is not a usable RSA exponent and would make every
		// signature trivially forgeable.
		`<RSAKeyValue><Modulus>dGVzdA==</Modulus><Exponent>AQ==</Exponent></RSAKeyValue>`,
	} {
		if _, err := ParseSpotKey(in); !errors.Is(err, ErrSpotBadKey) {
			t.Errorf("ParseSpotKey(%.40q) = %v, want ErrSpotBadKey", in, err)
		}
	}
}

// THE IMPORTANT ONE. Verification refuses everything until the
// canonicalisation is known, and it must refuse a spot that is otherwise
// PERFECTLY VALID — a genuine key, a genuine signature over the document.
//
// If this test ever starts failing because a real signature verified, that is
// not a fix: it means the gate was opened without pinning which bytes Spotweb
// signs, and a verifier that accepts everything is indistinguishable from one
// that works.
func TestVerifySpotRefusesUntilCanonicalisationIsKnown(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	doc := []byte(spotDoc)
	sum := sha1.Sum(doc)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA1, sum[:])
	if err != nil {
		t.Fatal(err)
	}

	err = VerifySpot(&key.PublicKey, doc, sig)
	if !errors.Is(err, ErrSpotUnverified) {
		t.Fatalf("VerifySpot accepted a spot: %v\n\n"+
			"The gate is open but the canonicalisation is not pinned. Until the "+
			"byte sequence Spotweb signs is confirmed against known-good spots, "+
			"every spot must be refused — the index sits on a public group and "+
			"anyone can post to it.", err)
	}
	// And the refusal is DISTINGUISHABLE from a forgery, so an operator
	// reading logs can tell "cannot check yet" from "this spot is fake".
	if errors.Is(err, ErrSpotBadSignature) {
		t.Error("an unimplemented verifier reported a signature mismatch")
	}
}

func TestVerifySpotRejectsANilKey(t *testing.T) {
	if err := VerifySpot(nil, []byte("x"), []byte("y")); !errors.Is(err, ErrSpotBadKey) {
		t.Errorf("nil key = %v, want ErrSpotBadKey", err)
	}
}

// Signatures use the URL-safe alphabet. Decoding with the standard one fails
// on any signature containing - or _, which is roughly half of them, and the
// failure would read as "forged" rather than "decoded wrongly".
func TestSpotSignatureBytesHandlesBothAlphabets(t *testing.T) {
	raw := []byte{0xfb, 0xef, 0xbe, 0x00, 0x11, 0x22}
	urlSafe := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(raw)
	std := base64.StdEncoding.WithPadding(base64.NoPadding).EncodeToString(raw)
	if urlSafe == std {
		t.Fatal("fixture does not exercise the alphabet difference")
	}
	for name, in := range map[string]string{"url-safe": urlSafe, "standard": std} {
		got, err := SpotSignatureBytes(in)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(got) != string(raw) {
			t.Errorf("%s decoded to %x, want %x", name, got, raw)
		}
	}
	if _, err := SpotSignatureBytes("!!!"); !errors.Is(err, ErrSpotBadSignature) {
		t.Error("undecodable signature was accepted")
	}
}
