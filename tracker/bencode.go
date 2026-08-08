// Package tracker implements the minimum BitTorrent tracker surface needed
// for a private tracker: a bencode scanner that can extract the `info` dict
// without re-encoding (so SHA-1 info hashes stay stable), a sanitizer that
// strips every tracker-identifying marker from a scraped .torrent, and the
// announce/scrape wire format.
//
// Decoding philosophy: never round-trip the info dict. Once `info` has been
// re-emitted from parsed Go values, its byte layout (dict key order, int
// encoding, string escaping, trailing whitespace) can diverge from the
// original and the SHA-1 changes — which means every existing swarm member
// would reject the new .torrent. Instead we keep a byte span into the
// original buffer and splice it back into the rebuilt outer dict.
package tracker

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// Span is a half-open byte range [Start, End) within a buffer.
type Span struct {
	Start, End int
}

// Len returns the number of bytes covered by the span.
func (s Span) Len() int { return s.End - s.Start }

// Bytes returns the sub-slice the span covers.
func (s Span) Bytes(b []byte) []byte { return b[s.Start:s.End] }

type bscan struct {
	b []byte
	p int
}

func (d *bscan) peek() (byte, error) {
	if d.p >= len(d.b) {
		return 0, errors.New("bencode: unexpected EOF")
	}
	return d.b[d.p], nil
}

// skipValue advances past exactly one bencoded value and returns its span.
func (d *bscan) skipValue() (Span, error) {
	start := d.p
	c, err := d.peek()
	if err != nil {
		return Span{}, err
	}
	switch {
	case c == 'i':
		d.p++
		for d.p < len(d.b) && d.b[d.p] != 'e' {
			d.p++
		}
		if d.p >= len(d.b) {
			return Span{}, errors.New("bencode: unterminated int")
		}
		d.p++
	case c >= '0' && c <= '9':
		colon := -1
		for i := d.p; i < len(d.b); i++ {
			if d.b[i] == ':' {
				colon = i
				break
			}
			if d.b[i] < '0' || d.b[i] > '9' {
				return Span{}, fmt.Errorf("bencode: bad length digit at %d", i)
			}
		}
		if colon < 0 {
			return Span{}, errors.New("bencode: no colon in string")
		}
		n, err := strconv.Atoi(string(d.b[d.p:colon]))
		if err != nil || n < 0 {
			return Span{}, fmt.Errorf("bencode: bad length: %v", err)
		}
		d.p = colon + 1 + n
		if d.p > len(d.b) {
			return Span{}, errors.New("bencode: string truncated")
		}
	case c == 'l':
		d.p++
		for {
			c2, err := d.peek()
			if err != nil {
				return Span{}, err
			}
			if c2 == 'e' {
				d.p++
				break
			}
			if _, err := d.skipValue(); err != nil {
				return Span{}, err
			}
		}
	case c == 'd':
		d.p++
		for {
			c2, err := d.peek()
			if err != nil {
				return Span{}, err
			}
			if c2 == 'e' {
				d.p++
				break
			}
			if _, err := d.skipValue(); err != nil { // key
				return Span{}, err
			}
			if _, err := d.skipValue(); err != nil { // value
				return Span{}, err
			}
		}
	default:
		return Span{}, fmt.Errorf("bencode: unknown type %q at %d", c, d.p)
	}
	return Span{Start: start, End: d.p}, nil
}

// readStringInto reads a bencoded byte string starting at d.p, advances the
// cursor past it, and returns the raw bytes plus the span.
func (d *bscan) readStringInto() ([]byte, Span, error) {
	start := d.p
	c, err := d.peek()
	if err != nil {
		return nil, Span{}, err
	}
	if c < '0' || c > '9' {
		return nil, Span{}, fmt.Errorf("bencode: expected string at %d", d.p)
	}
	colon := -1
	for i := d.p; i < len(d.b); i++ {
		if d.b[i] == ':' {
			colon = i
			break
		}
	}
	if colon < 0 {
		return nil, Span{}, errors.New("bencode: no colon in string")
	}
	n, _ := strconv.Atoi(string(d.b[d.p:colon]))
	if n < 0 || colon+1+n > len(d.b) {
		return nil, Span{}, errors.New("bencode: string truncated")
	}
	val := d.b[colon+1 : colon+1+n]
	d.p = colon + 1 + n
	return val, Span{Start: start, End: d.p}, nil
}

// ScanTopDict returns key → raw-value-span for a top-level bencoded dict.
// Spans are absolute within b so callers can splice fields back untouched.
func ScanTopDict(b []byte) (map[string]Span, error) {
	d := &bscan{b: b}
	c, err := d.peek()
	if err != nil {
		return nil, err
	}
	if c != 'd' {
		return nil, errors.New("bencode: top-level must be a dict")
	}
	d.p++
	m := make(map[string]Span)
	for {
		c, err := d.peek()
		if err != nil {
			return nil, err
		}
		if c == 'e' {
			return m, nil
		}
		key, _, err := d.readStringInto()
		if err != nil {
			return nil, err
		}
		v, err := d.skipValue()
		if err != nil {
			return nil, err
		}
		m[string(key)] = v
	}
}

// ScanDict parses the dict that span covers (span must point at `d...e`) and
// returns key → value-span in absolute coordinates within b.
func ScanDict(b []byte, span Span) (map[string]Span, error) {
	if span.Len() < 2 || b[span.Start] != 'd' {
		return nil, errors.New("bencode: span is not a dict")
	}
	sub := b[span.Start:span.End]
	m, err := ScanTopDict(sub)
	if err != nil {
		return nil, err
	}
	shifted := make(map[string]Span, len(m))
	for k, v := range m {
		shifted[k] = Span{Start: v.Start + span.Start, End: v.End + span.Start}
	}
	return shifted, nil
}

// DecodeString decodes the bencoded string at span and returns its payload.
func DecodeString(b []byte, span Span) ([]byte, error) {
	d := &bscan{b: b, p: span.Start}
	v, _, err := d.readStringInto()
	if err != nil {
		return nil, err
	}
	return v, nil
}

// DecodeInt decodes the bencoded int at span.
func DecodeInt(b []byte, span Span) (int64, error) {
	if span.Len() < 3 || b[span.Start] != 'i' || b[span.End-1] != 'e' {
		return 0, errors.New("bencode: span is not an int")
	}
	return strconv.ParseInt(string(b[span.Start+1:span.End-1]), 10, 64)
}

// DecodeList decodes the bencoded list at span, returning each element's
// span in absolute coordinates. Empty lists return a nil slice.
func DecodeList(b []byte, span Span) ([]Span, error) {
	if span.Len() < 2 || b[span.Start] != 'l' || b[span.End-1] != 'e' {
		return nil, errors.New("bencode: span is not a list")
	}
	d := &bscan{b: b, p: span.Start + 1}
	var out []Span
	for d.p < span.End-1 {
		v, err := d.skipValue()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// --- Encoder (outer wrapper only — never touch the info span) ----------------

type benc struct{ out []byte }

func (e *benc) str(s string) {
	e.out = strconv.AppendInt(e.out, int64(len(s)), 10)
	e.out = append(e.out, ':')
	e.out = append(e.out, s...)
}

func (e *benc) bytes(b []byte) {
	e.out = strconv.AppendInt(e.out, int64(len(b)), 10)
	e.out = append(e.out, ':')
	e.out = append(e.out, b...)
}

func (e *benc) intVal(n int64) {
	e.out = append(e.out, 'i')
	e.out = strconv.AppendInt(e.out, n, 10)
	e.out = append(e.out, 'e')
}

// BuildOuterDict emits a bencoded dict with the given fields, sorted by key
// (BEP-3 requires keys sorted bytewise). Values already bencoded (raw) are
// spliced in verbatim; string/int fields are encoded here.
type OuterField struct {
	Key string
	// Exactly one of these is non-nil/zero:
	Raw []byte // already bencoded
	Str string
	Int *int64
}

// BuildOuterDict produces d<sorted key/value pairs>e.
func BuildOuterDict(fields []OuterField) []byte {
	sorted := make([]OuterField, len(fields))
	copy(sorted, fields)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	e := benc{}
	e.out = append(e.out, 'd')
	for _, f := range sorted {
		e.str(f.Key)
		switch {
		case f.Raw != nil:
			e.out = append(e.out, f.Raw...)
		case f.Int != nil:
			e.intVal(*f.Int)
		default:
			e.str(f.Str)
		}
	}
	e.out = append(e.out, 'e')
	return e.out
}

// InfoHash returns the SHA-1 of the raw `info` dict bytes — stable as long
// as the info span is spliced rather than re-encoded.
func InfoHash(b []byte) ([20]byte, error) {
	m, err := ScanTopDict(b)
	if err != nil {
		return [20]byte{}, err
	}
	info, ok := m["info"]
	if !ok {
		return [20]byte{}, errors.New("bencode: missing info dict")
	}
	return sha1.Sum(b[info.Start:info.End]), nil
}
