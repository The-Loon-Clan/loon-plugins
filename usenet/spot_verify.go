package usenet

// Spotnet: signature verification.
//
// THE ONLY THING STANDING BETWEEN THE INDEX AND ANYONE WHO CAN POST TO A
// PUBLIC NEWSGROUP. free.pt is open — the trust model is not "only trusted
// people can post", it is "every spot is signed and clients decide which keys
// to believe". A spot carries its own public key, so verification proves the
// spot was written by the holder of THAT key, and reputation is what makes a
// key worth anything.
//
// WHAT IS SIGNED, and it is not what this file used to guess. Spotweb's
// Services_Signing_Base::verifyFullSpot signs the MESSAGE-ID IN ANGLE BRACKETS:
//
//	checkRsaSignature('<' . $spot['messageid'] . '>', $spot['user-signature'], $spot['user-key'])
//
// Not the XML document, not a field concatenation — all three of the candidates
// previously listed here were wrong, which is the argument for having refused
// every spot rather than shipping the most plausible guess. Confirmed against
// 12 live spots from free.pt: 12 verify, and 12 fail when one byte of the
// message-id is flipped.
//
// WHAT THAT SIGNATURE IS WORTH, which is less than it looks. Signing the
// message-id binds the spot to a key, but covers NONE of the payload — not the
// title, the size, the category or the NZB pointer. So a valid signature says
// "the holder of this key posted this article" and says nothing whatever about
// whether the contents are honest. Spotnet covers the body separately in
// verifySpotHeader (title + header + poster, against a small set of well-known
// keys by keyid), which is a different check this file does not yet do. Do not
// read a nil return here as licence to trust the payload.

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"math/big"
	"strings"
)

var (
	// ErrSpotBadSignature means the bytes were checked and did not match.
	ErrSpotBadSignature = errors.New("spotnet: signature does not match the spot's key")
	// ErrSpotBadKey means the carried key could not be parsed at all.
	ErrSpotBadKey = errors.New("spotnet: unparseable public key")
	// ErrSpotWeakKey means the key is too small for a signature over it to mean
	// anything. Distinct from a bad signature because the spot is not forged —
	// it is merely unprovable, and a caller may knowingly accept it at lower
	// trust.
	ErrSpotWeakKey = errors.New("spotnet: key is too small to be worth verifying")
)

// MinSpotKeyBits is the smallest modulus this package will call verified.
//
// This is not theoretical. Of the 12 spots sampled from free.pt, SIX carried
// 384-bit keys — a size that factors on a laptop in minutes, so anyone can mint
// signatures for those posters at will. Verifying such a signature and
// reporting success would be the exact failure this file exists to prevent: a
// verifier that says yes without proving anything.
//
// 1024 is the protocol's own norm rather than a modern recommendation. It is
// the line between "weak but costly" and "trivially forged", which is the line
// that matters here. Raising it further would reject most of the live feed.
const MinSpotKeyBits = 1024

// SpotKey is the RSA key a spot carries in X-User-Key.
//
//	<RSAKeyValue><Modulus>…base64…</Modulus><Exponent>AQAB</Exponent></RSAKeyValue>
//
// The .NET RSAKeyValue form, because Spotweb and the original client are .NET —
// big-endian base64 for both components rather than any PEM encoding.
type SpotKey struct {
	XMLName  xml.Name `xml:"RSAKeyValue"`
	Modulus  string   `xml:"Modulus"`
	Exponent string   `xml:"Exponent"`
}

// DecodeSpotBase64 decodes Spotnet's escaped base64.
//
// It is NOT the URL-safe alphabet, which is the trap: '+' and '/' — the only
// two non-alphanumeric characters in the standard alphabet — are escaped as the
// TWO-CHARACTER sequences "-p" and "-s". Treating '-' as a single-character
// substitution (the URL-safe assumption) leaves a stray 'p' or 's' in the
// stream, shifting every subsequent byte. That produced signatures one and two
// bytes LONGER than the modulus, which is arithmetically impossible and read as
// "these spots are forged" rather than "we decoded them wrongly": 11 of 12 live
// spots failed that way before the escaping was understood.
//
// Standard base64 never emits '-', so applying this to an unescaped value is a
// no-op. That is why the same decoder serves both the key (which arrives
// unescaped, '/' and all) and the signature (which does not) — one function,
// no per-field guessing about which encoding a value uses.
func DecodeSpotBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "-p", "+")
	s = strings.ReplaceAll(s, "-s", "/")
	if pad := len(s) % 4; pad != 0 {
		s += strings.Repeat("=", 4-pad)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrSpotBadSignature
	}
	return b, nil
}

// SpotSignatureBytes decodes the X-User-Signature header.
func SpotSignatureBytes(sig string) ([]byte, error) {
	b, err := DecodeSpotBase64(sig)
	if err != nil || len(b) == 0 {
		return nil, ErrSpotBadSignature
	}
	return b, nil
}

// ParseSpotKey turns the X-User-Key header into an rsa.PublicKey.
func ParseSpotKey(header string) (*rsa.PublicKey, error) {
	var k SpotKey
	if err := xml.Unmarshal([]byte(header), &k); err != nil {
		return nil, ErrSpotBadKey
	}
	mod, err := DecodeSpotBase64(k.Modulus)
	if err != nil || len(mod) == 0 {
		return nil, ErrSpotBadKey
	}
	exp, err := DecodeSpotBase64(k.Exponent)
	if err != nil || len(exp) == 0 {
		return nil, ErrSpotBadKey
	}
	e := new(big.Int).SetBytes(exp)
	if !e.IsInt64() || e.Int64() < 3 {
		return nil, ErrSpotBadKey
	}
	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(mod),
		E: int(e.Int64()),
	}, nil
}

// VerifySpot checks a spot's signature against the key it carries.
//
// messageID may arrive with or without its angle brackets; what gets signed is
// always the bracketed form, because that is what Spotweb signs and a spot
// verified against the wrong shape would silently fail for every poster.
//
// A nil return means the signature is genuine AND the key is large enough for
// that to be evidence. ErrSpotWeakKey means the maths was not attempted because
// the answer would not have meant anything.
func VerifySpot(pub *rsa.PublicKey, messageID string, signature []byte) error {
	if pub == nil || pub.N == nil {
		return ErrSpotBadKey
	}
	if pub.N.BitLen() < MinSpotKeyBits {
		return ErrSpotWeakKey
	}
	sum := sha1.Sum([]byte(bracketMessageID(messageID)))
	// PKCS#1 v1.5 over SHA-1: openssl_verify's default algorithm, which is what
	// Spotweb calls with no algorithm argument.
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA1, sum[:], signature); err != nil {
		return ErrSpotBadSignature
	}
	return nil
}

// Trust labels for a spot, stored on the release as nzbs.origin_trust.
const (
	// SpotTrustVerified: signature checked against a key worth checking.
	SpotTrustVerified = "verified"
	// SpotTrustWeakKey: the signature is arithmetically valid and proves
	// nothing, because the key is small enough to forge cheaply.
	SpotTrustWeakKey = "weak-key"
	// SpotTrustUnsigned: no key or no signature to check at all.
	SpotTrustUnsigned = "unsigned"
)

// SpotTrust turns a VerifySpot result into the label stored with the release.
//
// This exists so the import path has ONE place that decides what a
// verification outcome means, rather than each call site inventing its own
// mapping — the difference between "unprovable" and "forged" is the whole
// value of the check, and it is exactly the distinction an ad-hoc `if err !=
// nil` at the call site would flatten.
//
// A false second return means DO NOT IMPORT. Note that a weak key is
// importable: refusing it would drop half the live feed, and the honest
// treatment is to carry it with a label saying the signature proved nothing.
func SpotTrust(err error) (string, bool) {
	switch {
	case err == nil:
		return SpotTrustVerified, true
	case errors.Is(err, ErrSpotWeakKey):
		return SpotTrustWeakKey, true
	case errors.Is(err, ErrSpotBadKey):
		// An unparseable key is indistinguishable from an absent one for our
		// purposes: nothing can be checked, so nothing is claimed.
		return SpotTrustUnsigned, true
	default:
		// ErrSpotBadSignature and anything unforeseen. A signature that was
		// checked and FAILED is the one case that must never reach the index:
		// it is a positive claim of authorship that did not hold up.
		return "", false
	}
}

// bracketMessageID normalises a message-id to the <id> form that is signed.
func bracketMessageID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "<")
	id = strings.TrimSuffix(id, ">")
	return "<" + id + ">"
}
