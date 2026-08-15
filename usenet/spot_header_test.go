package usenet

import (
	"errors"
	"strings"
	"testing"
)

// The real spot from the 2026-08-15 spike against free.pt, kept verbatim.
// A synthesised example would only prove the parser agrees with my reading of
// the documentation, which is the thing the spike existed to distrust.
const realSpotFrom = `Paaldanser <KEY@27a02b00c08d13z00.3365188124.20.1786812549.1.NL.SIG>`

func TestParseSpotFromTheLiveSample(t *testing.T) {
	h, err := ParseSpotFrom(realSpotFrom)
	if err != nil {
		t.Fatalf("the live sample did not parse: %v", err)
	}
	if h.Poster != "Paaldanser" {
		t.Errorf("Poster = %q", h.Poster)
	}
	if h.PublicKey != "KEY" {
		t.Errorf("PublicKey = %q — the address local part is the key", h.PublicKey)
	}
	if h.Category != 2 || h.KeyID != 7 {
		t.Errorf("category/key = %d/%d, want 2/7 — the first two characters are packed, not one field",
			h.Category, h.KeyID)
	}
	// 3.36 GB for a 5CD album, which is what made the size field trustworthy
	// during the spike: it checks out against the titles.
	if h.SizeBytes != 3365188124 {
		t.Errorf("SizeBytes = %d", h.SizeBytes)
	}
	if h.PostedAt != 1786812549 {
		t.Errorf("PostedAt = %d", h.PostedAt)
	}
	if h.Locale != "NL" {
		t.Errorf("Locale = %q", h.Locale)
	}
	if h.Signature != "SIG" {
		t.Errorf("Signature = %q", h.Signature)
	}
	want := []string{"a02", "b00", "c08", "d13", "z00"}
	if strings.Join(h.SubCats, ",") != strings.Join(want, ",") {
		t.Errorf("SubCats = %v, want %v", h.SubCats, want)
	}
	// The XML for this spot says <Category>02<Sub>02a02</Sub>… — same content,
	// different form. The XML's form is what Spotweb's table is keyed on.
	wantFull := []string{"02a02", "02b00", "02c08", "02d13", "02z00"}
	if strings.Join(h.FullSubCats(), ",") != strings.Join(wantFull, ",") {
		t.Errorf("FullSubCats = %v, want %v", h.FullSubCats(), wantFull)
	}
}

// The two unidentified fields are CARRIED, not dropped. A parser that quietly
// discards what it does not understand is how a format change becomes
// invisible — the spot would keep parsing and keep meaning something else.
func TestParseSpotFromKeepsTheUnknownFields(t *testing.T) {
	h, err := ParseSpotFrom(realSpotFrom)
	if err != nil {
		t.Fatal(err)
	}
	if h.Unknown1 != "20" {
		t.Errorf("Unknown1 = %q, want the value after the size", h.Unknown1)
	}
	if h.Unknown2 != "1" {
		t.Errorf("Unknown2 = %q, want the value after the timestamp", h.Unknown2)
	}
}

// free.pt carries ordinary posts too, so "not a spot" must be an ordinary,
// cheap outcome and NOT confused with "a spot that failed to parse". A listing
// pass meets plenty of the former and should log none of them.
func TestParseSpotFromDistinguishesNotASpotFromMalformed(t *testing.T) {
	notSpots := []string{
		"Some Person <user@example.com>",
		"nobody@example.org",
		"",
		"No angle brackets here",
		"Broken <no-at-sign>",
	}
	for _, in := range notSpots {
		if _, err := ParseSpotFrom(in); !errors.Is(err, ErrNotASpot) {
			t.Errorf("ParseSpotFrom(%q) = %v, want ErrNotASpot", in, err)
		}
	}

	malformed := []string{
		// Spot-shaped but the wrong field count.
		`P <K@27a02.3365188124.20.1786812549.1.NL>`,
		// Non-numeric where numbers are required.
		`P <K@2Xa02.3365188124.20.1786812549.1.NL.SIG>`,
		`P <K@27a02.notasize.20.1786812549.1.NL.SIG>`,
		`P <K@27a02.3365188124.20.notatime.1.NL.SIG>`,
		// Missing the key: unverifiable, so it must never become a release.
		`P <@27a02.3365188124.20.1786812549.1.NL.SIG>`,
	}
	for _, in := range malformed {
		_, err := ParseSpotFrom(in)
		if !errors.Is(err, ErrSpotMalformed) {
			t.Errorf("ParseSpotFrom(%q) = %v, want ErrSpotMalformed", in, err)
		}
	}
}

func TestSplitSubCats(t *testing.T) {
	cases := map[string][]string{
		"":             nil,
		"a02":          {"a02"},
		"a02b00":       {"a02", "b00"},
		"a02b00c08d13": {"a02", "b00", "c08", "d13"},
		// A trailing short run is KEPT. Dropping it would hide a format change
		// behind a parser that still looked like it worked.
		"a02b0": {"a02", "b0"},
	}
	for in, want := range cases {
		got := splitSubCats(in)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("splitSubCats(%q) = %v, want %v", in, got, want)
		}
	}
}

// A spot with no subcategories is legal and must not produce a phantom entry.
func TestParseSpotFromWithNoSubCats(t *testing.T) {
	h, err := ParseSpotFrom(`P <KEY@27.100.20.1786812549.1.NL.SIG>`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(h.SubCats) != 0 || len(h.FullSubCats()) != 0 {
		t.Errorf("SubCats = %v, want empty", h.SubCats)
	}
	if h.Category != 2 || h.KeyID != 7 || h.SizeBytes != 100 {
		t.Errorf("fields = cat %d key %d size %d", h.Category, h.KeyID, h.SizeBytes)
	}
}
