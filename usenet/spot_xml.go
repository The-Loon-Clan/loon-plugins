package usenet

// Spotnet: the full copy of a spot, from HEAD.
//
// XOVER gives a listing; the complete document lives in headers XOVER does not
// return. The document is chopped into ~900-byte pieces, each given its OWN
// header line with the same name, and the spot is their concatenation in order.
//
// THIS IS THE THING TO GET RIGHT. `X-Xml` is REPEATED, not folded. A parser
// that takes the first one, or treats repeats as alternatives, gets a
// truncated document that still parses far enough to look like it worked —
// title and key present, description cut mid-sentence, NZB segment missing
// entirely. It does not error; it just quietly indexes half a spot.

import (
	"encoding/xml"
	"errors"
	"strings"
)

// SpotXML is the document a spot carries. Field names follow the wire.
type SpotXML struct {
	XMLName xml.Name `xml:"Spotnet"`
	Posting struct {
		Key         int    `xml:"Key"`
		Created     int64  `xml:"Created"`
		Poster      string `xml:"Poster"`
		Title       string `xml:"Title"`
		Description string `xml:"Description"`
		Size        int64  `xml:"Size"`

		Image struct {
			Width   int    `xml:"Width,attr"`
			Height  int    `xml:"Height,attr"`
			Segment string `xml:"Segment"`
		} `xml:"Image"`

		Category struct {
			// Value is the leading text node ("02"); Subs are the nested
			// <Sub> elements ("02a02", …). Mixed content, so the category
			// itself is chardata rather than an element of its own.
			Value string   `xml:",chardata"`
			Subs  []string `xml:"Sub"`
		} `xml:"Category"`

		// NZB.Segment is a bare message-id in alt.binaries.ftd. It is the
		// whole point of the spot: fetch it and a finished NZB comes back,
		// which is why importing skips the crawler's expensive half entirely.
		NZB struct {
			Segment []string `xml:"Segment"`
		} `xml:"NZB"`
	} `xml:"Posting"`
}

var (
	// ErrNoSpotXML means the article carried no X-Xml header at all.
	ErrNoSpotXML = errors.New("spotnet: article has no X-Xml header")
	// ErrSpotXMLTruncated means the joined document did not parse. Given the
	// pieces arrive as separate headers, the overwhelmingly likely cause is a
	// missing or reordered piece rather than a malformed spot.
	ErrSpotXMLTruncated = errors.New("spotnet: X-Xml document did not parse (missing a piece?)")
)

// JoinSpotXML concatenates the repeated X-Xml header values in the order they
// were received.
//
// No separator, no trimming of the pieces: the split is at an arbitrary byte
// offset, so a boundary can fall in the middle of a tag name or an attribute
// value, and trimming whitespace at the seam would corrupt a document that
// happened to split on a space inside a description.
func JoinSpotXML(values []string) string {
	if len(values) == 0 {
		return ""
	}
	var b strings.Builder
	for _, v := range values {
		b.WriteString(v)
	}
	return b.String()
}

// ParseSpotXML joins the pieces and parses the result.
//
// Takes the SLICE rather than a joined string on purpose, so a caller cannot
// accidentally pass the first header and get a plausible-looking answer. The
// count is reported in the error precisely because "we only had one piece" is
// the failure that otherwise looks like success.
func ParseSpotXML(values []string) (*SpotXML, error) {
	if len(values) == 0 {
		return nil, ErrNoSpotXML
	}
	var doc SpotXML
	if err := xml.Unmarshal([]byte(JoinSpotXML(values)), &doc); err != nil {
		return nil, ErrSpotXMLTruncated
	}
	return &doc, nil
}

// NZBSegment is the message-id of the article holding this spot's NZB, or ""
// when the spot points at none.
func (s *SpotXML) NZBSegment() string {
	if s == nil || len(s.Posting.NZB.Segment) == 0 {
		return ""
	}
	return strings.TrimSpace(s.Posting.NZB.Segment[0])
}

// CategoryValue is the numeric category as written in the document ("02"),
// with the surrounding whitespace of the mixed content removed.
//
// chardata on a mixed-content element collects the text around the children
// too, so the raw value carries the indentation between <Sub> elements.
func (s *SpotXML) CategoryValue() string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(strings.Fields(s.Posting.Category.Value + " ")[0])
}
