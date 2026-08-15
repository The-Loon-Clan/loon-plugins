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
// WHY THIS REFUSES EVERYTHING TODAY. The canonicalisation is unknown: the key
// and the signature are both in hand and the maths is crypto/rsa, but WHICH
// BYTES are signed, in what order, under which digest, has to come from
// Spotweb's verifier. Guessing produces a verifier that rejects valid spots
// (harmless, visible) or accepts invalid ones (silent, and the whole index is
// then writable by anyone).
//
// A verifier that accepts everything is indistinguishable from one that works.
// So this one accepts NOTHING until the canonicalisation is pinned by a test
// against known-good spots, and the importer treats a verification failure as
// "do not import" rather than "import unverified". That means the feature is
// inert rather than dangerous while the question is open, which is the correct
// direction to be wrong in.

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"math/big"
)

var (
	// ErrSpotUnverified is what every spot gets until the canonicalisation is
	// known. It is deliberately distinct from a genuine signature mismatch so
	// an operator reading logs can tell "we cannot check this yet" from "this
	// spot is forged".
	ErrSpotUnverified = errors.New("spotnet: signature canonicalisation is not implemented — refusing to trust any spot")
	// ErrSpotBadSignature means the bytes were checked and did not match.
	ErrSpotBadSignature = errors.New("spotnet: signature does not match the spot's key")
	// ErrSpotBadKey means the carried key could not be parsed at all.
	ErrSpotBadKey = errors.New("spotnet: unparseable public key")
)

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

// ParseSpotKey turns the X-User-Key header into an rsa.PublicKey.
//
// This part IS known and is worth having now: it is pure decoding with no
// protocol ambiguity, and having it lets the importer record WHICH key signed
// a spot even while the signature itself cannot be checked — which is what a
// key reputation list will need first.
func ParseSpotKey(header string) (*rsa.PublicKey, error) {
	var k SpotKey
	if err := xml.Unmarshal([]byte(header), &k); err != nil {
		return nil, ErrSpotBadKey
	}
	mod, err := base64.StdEncoding.DecodeString(k.Modulus)
	if err != nil || len(mod) == 0 {
		return nil, ErrSpotBadKey
	}
	exp, err := base64.StdEncoding.DecodeString(k.Exponent)
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
// Always returns ErrSpotUnverified for now. The signature check below is
// written and correct FOR A GIVEN canonicalisation — what is missing is the
// canonicalisation itself, so the call is gated rather than the code absent:
// when the byte sequence is known, `canonicalSpotBytes` is the only function
// that changes and the gate comes off in the same commit as its test.
func VerifySpot(pub *rsa.PublicKey, doc []byte, signature []byte) error {
	if pub == nil {
		return ErrSpotBadKey
	}
	if !spotVerificationImplemented {
		return ErrSpotUnverified
	}
	sum := sha1.Sum(canonicalSpotBytes(doc))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA1, sum[:], signature); err != nil {
		return ErrSpotBadSignature
	}
	return nil
}

// spotVerificationImplemented is the gate. Flipping it is a deliberate act
// that must arrive WITH a test proving a known-good spot verifies and a
// tampered one does not — not as a convenience during development.
const spotVerificationImplemented = false

// canonicalSpotBytes is the open question: which bytes Spotweb signs.
//
// Candidates observed so far, none confirmed:
//   - the joined X-Xml document exactly as received
//   - the same with the XML declaration or whitespace normalised
//   - a field concatenation from the header rather than the document
//
// Left as identity so the shape of the call is settled and only this body
// changes when the answer is known.
func canonicalSpotBytes(doc []byte) []byte { return doc }

// SpotSignatureBytes decodes the header's signature field.
//
// Spotnet uses a URL-safe base64 variant with '-' and '_' — the standard
// alphabet fails on roughly half of real signatures, which reads as "this spot
// is forged" rather than "we decoded it wrongly".
func SpotSignatureBytes(sig string) ([]byte, error) {
	if b, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(sig); err == nil {
		return b, nil
	}
	b, err := base64.StdEncoding.WithPadding(base64.NoPadding).DecodeString(sig)
	if err != nil {
		return nil, ErrSpotBadSignature
	}
	return b, nil
}
