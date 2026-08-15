package usenet

// Spotnet: the cheap copy of a spot, read straight off XOVER.
//
// Spotnet is a decentralised index that lives ON usenet rather than beside it.
// A human "spots" an upload and posts a signed description to a public group;
// every client builds its own copy by reading that group. Three groups are
// involved and they are fixed properties of the protocol, not operator config:
//
//	free.pt            the spots (the index itself)
//	free.usenet        comments
//	alt.binaries.ftd   the NZB and image each spot points at
//
// WHY THIS LIVES IN THE USENET PLUGIN rather than its own. Spotnet is not a
// different protocol, it is a different CONVENTION on the same transport. It
// shares the NNTP pool, the newsgroup watermarks, the nzbs sink and the junk
// machinery — four of five layers — and differs only in how a release is
// discovered. Splitting it out would have meant inventing an NNTP capability
// in the core registry purely to share a connection pool between two plugins
// that both do nothing but read usenet, and the pool is already the binding
// constraint: 120 configured connections against a crawler that throws i/o
// timeouts by the thousand. One owner of that budget is the correct answer.
//
// Everything in this file was read off the wire during a live spike against
// free.pt on 2026-08-15 (docs/SPOTNET.md in loon-demo-site). The published
// descriptions of the protocol are accurate in outline and useless for writing
// a parser, so the format below is described from observation and the two
// fields nobody has identified are carried as unknowns rather than guessed at.

import (
	"errors"
	"strconv"
	"strings"
)

// SpotHeader is what XOVER alone yields: enough to list a spot without a
// second round trip. Spotnet clients build their whole listing from this,
// roughly one round trip per thousand spots, which is why they feel fast.
type SpotHeader struct {
	Poster    string // display name, before the angle bracket
	PublicKey string // travels WITH the spot — there is no key directory
	Signature string // the last dotted field

	Category int      // 1 video, 2 audio, 3 game (observed; canonical table is Spotweb's)
	KeyID    int      // matches <Key> in the XML document
	SubCats  []string // "a02", "b00", … letter-prefixed, three characters each

	SizeBytes int64 // checked against titles during the spike and consistent
	PostedAt  int64 // unix seconds
	Locale    string

	// Unknown1 and Unknown2 are the two fields nobody has identified: the
	// value after the size and the value after the timestamp. Carried rather
	// than dropped, because a parser that silently discards fields it does not
	// understand is how a format change becomes invisible.
	Unknown1 string
	Unknown2 string
}

var (
	// ErrNotASpot is returned for a From header that is not in spot form. It
	// is not a failure worth logging per article: free.pt carries ordinary
	// posts too, and a listing pass will meet plenty of them.
	ErrNotASpot = errors.New("spotnet: From header is not a spot")
	// ErrSpotMalformed means it LOOKED like a spot and was not parseable —
	// which is worth noticing, because it means either a format change or a
	// bug here.
	ErrSpotMalformed = errors.New("spotnet: malformed spot header")
)

// ParseSpotFrom reads the From header of a spot.
//
//	Paaldanser <KEY@27a02b00c08d13z00.3365188124.20.1786812549.1.NL.SIG>
//	            │    ││  └ subcats ┘  └ size ──┘ └┘ └ posted ─┘ │ └ locale
//	            │    │└ key id                   ?              ?
//	            │    └ category
//	            └ public key                             signature ┘
//
// The address local part is the public key; everything after the @ is a dotted
// tuple whose FIRST element packs three values with no separator: one digit of
// category, one digit of key id, then subcategories in three-character groups.
func ParseSpotFrom(from string) (*SpotHeader, error) {
	from = strings.TrimSpace(from)
	open := strings.LastIndex(from, "<")
	close := strings.LastIndex(from, ">")
	if open < 0 || close < open {
		return nil, ErrNotASpot
	}
	addr := from[open+1 : close]
	at := strings.Index(addr, "@")
	if at < 0 {
		return nil, ErrNotASpot
	}
	// An EMPTY local part is allowed through deliberately: it carries the spot
	// tail, so it is spot-shaped and unusable rather than simply not a spot.
	// The key check at the end reports it as malformed, which is the outcome
	// worth noticing — free.pt is full of genuine non-spots and none of them
	// should be logged, but a spot with no key means a format change or a bug.

	h := &SpotHeader{
		Poster:    strings.TrimSpace(from[:open]),
		PublicKey: addr[:at],
	}

	parts := strings.Split(addr[at+1:], ".")
	// packed, size, unk1, posted, unk2, locale, signature
	if len(parts) != 7 {
		// A plain address (user@example.com) lands here and is simply not a
		// spot; anything else with a dotted tail is malformed.
		if len(parts) < 3 {
			return nil, ErrNotASpot
		}
		return nil, ErrSpotMalformed
	}

	packed := parts[0]
	if len(packed) < 2 {
		return nil, ErrSpotMalformed
	}
	cat, err := strconv.Atoi(packed[0:1])
	if err != nil {
		return nil, ErrSpotMalformed
	}
	keyID, err := strconv.Atoi(packed[1:2])
	if err != nil {
		return nil, ErrSpotMalformed
	}
	h.Category, h.KeyID = cat, keyID
	h.SubCats = splitSubCats(packed[2:])

	if h.SizeBytes, err = strconv.ParseInt(parts[1], 10, 64); err != nil {
		return nil, ErrSpotMalformed
	}
	h.Unknown1 = parts[2]
	if h.PostedAt, err = strconv.ParseInt(parts[3], 10, 64); err != nil {
		return nil, ErrSpotMalformed
	}
	h.Unknown2 = parts[4]
	h.Locale = parts[5]
	h.Signature = parts[6]

	if h.PublicKey == "" || h.Signature == "" {
		// Both are required for verification, and a spot that cannot be
		// verified must never become a release. Refusing here keeps that
		// decision in one place.
		return nil, ErrSpotMalformed
	}
	return h, nil
}

// splitSubCats cuts the packed subcategory run into its three-character
// groups: a letter and two digits, e.g. "a02b00c08" -> [a02 b00 c08].
//
// A trailing run that is not a multiple of three is kept as its own element
// rather than dropped. Losing it silently would hide a format change, and the
// caller can tell a short group from a valid one.
func splitSubCats(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for len(s) >= 3 {
		out = append(out, s[:3])
		s = s[3:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// FullSubCats renders the subcategories the way the XML document does, with
// the category prefixed onto each: category 2 + "a02" -> "02a02".
//
// The header and the XML disagree in FORM but not in content, and the XML's
// form is the one Spotweb's category table is keyed on.
func (h *SpotHeader) FullSubCats() []string {
	if h == nil || len(h.SubCats) == 0 {
		return nil
	}
	prefix := "0" + strconv.Itoa(h.Category)
	out := make([]string, 0, len(h.SubCats))
	for _, s := range h.SubCats {
		out = append(out, prefix+s)
	}
	return out
}
