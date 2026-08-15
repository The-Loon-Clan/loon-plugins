package usenet

import (
	"errors"
	"strings"
	"testing"
)

// The document from the live spike, with the description shortened. Kept as
// the real shape — mixed-content <Category>, attributes on <Image>, a bare
// message-id in <NZB><Segment> — because every one of those is somewhere a
// hand-written fixture would have quietly simplified.
const spotDoc = `<Spotnet><Posting>
  <Key>7</Key>
  <Created>1786812549</Created>
  <Poster>Paaldanser</Poster>
  <Title>Back To The US Of A (5CD) (2007)</Title>
  <Description>a description[br]with a newline marker</Description>
  <Image Width='600' Height='513'><Segment>9n5hBCFmFwggpiAagRPYa@spot.net</Segment></Image>
  <Size>3365188124</Size>
  <Category>02<Sub>02a02</Sub><Sub>02b00</Sub><Sub>02c08</Sub><Sub>02d13</Sub><Sub>02z00</Sub></Category>
  <NZB><Segment>f8xvTXWFhW8hJiAagl8V8@spot.net</Segment></NZB>
  <PREVSPOTS></PREVSPOTS>
</Posting></Spotnet>`

// split cuts a string into n-byte pieces the way the wire does: at an
// arbitrary byte offset, with no regard for tag or attribute boundaries.
func split(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}

func TestParseSpotXMLJoinsRepeatedHeaders(t *testing.T) {
	pieces := split(spotDoc, 90) // the wire uses ~900; smaller here means more seams
	if len(pieces) < 4 {
		t.Fatalf("test fixture only split into %d pieces", len(pieces))
	}
	doc, err := ParseSpotXML(pieces)
	if err != nil {
		t.Fatalf("joined document did not parse: %v", err)
	}
	if doc.Posting.Key != 7 {
		t.Errorf("Key = %d", doc.Posting.Key)
	}
	if doc.Posting.Title != "Back To The US Of A (5CD) (2007)" {
		t.Errorf("Title = %q", doc.Posting.Title)
	}
	if doc.Posting.Size != 3365188124 {
		t.Errorf("Size = %d", doc.Posting.Size)
	}
	if doc.Posting.Created != 1786812549 {
		t.Errorf("Created = %d", doc.Posting.Created)
	}
	// Attributes on a child element survive a seam falling inside them.
	if doc.Posting.Image.Width != 600 || doc.Posting.Image.Height != 513 {
		t.Errorf("image = %dx%d", doc.Posting.Image.Width, doc.Posting.Image.Height)
	}
	// The whole point of importing: a finished NZB, one fetch away.
	if got := doc.NZBSegment(); got != "f8xvTXWFhW8hJiAagl8V8@spot.net" {
		t.Errorf("NZBSegment = %q", got)
	}
	if got := doc.CategoryValue(); got != "02" {
		t.Errorf("CategoryValue = %q, want 02 — mixed content picks up the indentation", got)
	}
	if len(doc.Posting.Category.Subs) != 5 || doc.Posting.Category.Subs[0] != "02a02" {
		t.Errorf("Subs = %v", doc.Posting.Category.Subs)
	}
}

// THE ONE THAT MATTERS. Taking only the first header yields a document that
// parses far enough to look right — and silently loses the NZB segment, which
// is the only part that makes a spot importable. This test exists to make that
// failure loud, because on the wire it is not.
func TestParseSpotXMLRefusesATruncatedDocument(t *testing.T) {
	pieces := split(spotDoc, 90)

	_, err := ParseSpotXML(pieces[:1])
	if !errors.Is(err, ErrSpotXMLTruncated) {
		t.Fatalf("the first piece alone gave %v — a parser that accepts it indexes half a spot", err)
	}
	// Dropping a piece from the MIDDLE is the subtler version: the document
	// still opens and closes, and the hole is inside.
	holed := append(append([]string{}, pieces[:2]...), pieces[3:]...)
	if _, err := ParseSpotXML(holed); !errors.Is(err, ErrSpotXMLTruncated) {
		t.Errorf("a document missing a middle piece parsed anyway: %v", err)
	}
	// Reordered pieces must not parse either.
	rev := append([]string{}, pieces...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	if _, err := ParseSpotXML(rev); !errors.Is(err, ErrSpotXMLTruncated) {
		t.Errorf("reversed pieces parsed: %v", err)
	}
}

func TestParseSpotXMLNoHeader(t *testing.T) {
	if _, err := ParseSpotXML(nil); !errors.Is(err, ErrNoSpotXML) {
		t.Errorf("nil = %v, want ErrNoSpotXML", err)
	}
	if _, err := ParseSpotXML([]string{}); !errors.Is(err, ErrNoSpotXML) {
		t.Errorf("empty = %v, want ErrNoSpotXML", err)
	}
}

// The join must not trim: a seam can fall inside a description, and eating the
// whitespace there would corrupt content that parsed perfectly well.
func TestJoinSpotXMLPreservesSeams(t *testing.T) {
	got := JoinSpotXML([]string{"<a>one ", " two</a>"})
	if got != "<a>one  two</a>" {
		t.Errorf("JoinSpotXML = %q — the seam whitespace was altered", got)
	}
	if JoinSpotXML(nil) != "" {
		t.Error("nil join produced content")
	}
}

// A spot pointing at no NZB is legal (some spots are announcements only) and
// must not panic or invent a segment — the importer skips it instead.
func TestSpotXMLWithoutNZB(t *testing.T) {
	doc, err := ParseSpotXML([]string{
		`<Spotnet><Posting><Key>1</Key><Title>T</Title><NZB></NZB></Posting></Spotnet>`,
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := doc.NZBSegment(); got != "" {
		t.Errorf("NZBSegment = %q, want empty", got)
	}
	var nilDoc *SpotXML
	if nilDoc.NZBSegment() != "" || nilDoc.CategoryValue() != "" {
		t.Error("nil receiver did not degrade quietly")
	}
	if strings.TrimSpace(doc.Posting.Title) != "T" {
		t.Errorf("Title = %q", doc.Posting.Title)
	}
}
