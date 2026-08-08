package tracker

import (
	"errors"
	"fmt"
	"strings"
)

// trackerMarkers is the set of top-level keys stripped from scraped .torrents.
// The `info` dict is always preserved byte-for-byte (its hash is the swarm
// identity); everything else is discarded except that we rewrite `announce`
// to point at our own tracker with the user's passkey.
//
// `private` is NOT in here — we keep/override it at rebuild time to 1.
var trackerMarkers = map[string]struct{}{
	"announce":      {},
	"announce-list": {},
	"nodes":         {},
	"url-list":      {},
	"httpseeds":     {},
	"comment":       {},
	"comment.utf-8": {},
	"created by":    {},
	"creation date": {},
	"created":       {},
	"encoding":      {},
	"publisher":     {},
	"publisher-url": {},
	"source":        {},
	"website":       {},
}

// Sanitized is the result of parsing a scraped .torrent into the shape the
// tracker stores: a byte-stable copy of the info dict, its info hash, and
// the fields we surface in the UI.
type Sanitized struct {
	InfoHash    [20]byte
	InfoHashHex string
	InfoBytes   []byte
	Summary     InfoSummary
}

// InfoSummary is the display-only metadata pulled out of the info dict.
type InfoSummary struct {
	Name        string
	PieceLength int64
	TotalSize   int64
	Private     bool
	Files       []TorrentFile
}

// TorrentFile is a single entry in a multi-file torrent.
type TorrentFile struct {
	Path   string
	Length int64
}

// Sanitize parses a raw .torrent and returns the byte-stable info dict plus
// metadata. It does not emit a new .torrent — call BuildForUser at download
// time so each download carries the passkey-specific announce URL.
func Sanitize(raw []byte) (*Sanitized, error) {
	top, err := ScanTopDict(raw)
	if err != nil {
		return nil, fmt.Errorf("scan top dict: %w", err)
	}
	infoSpan, ok := top["info"]
	if !ok {
		return nil, errors.New("torrent: missing info dict")
	}
	infoBytes := make([]byte, infoSpan.Len())
	copy(infoBytes, raw[infoSpan.Start:infoSpan.End])

	sum, err := summarizeInfoBytes(infoBytes)
	if err != nil {
		return nil, fmt.Errorf("summarize info: %w", err)
	}
	hash, err := InfoHash(raw)
	if err != nil {
		return nil, err
	}
	hex := hashToHex(hash)
	return &Sanitized{
		InfoHash:    hash,
		InfoHashHex: hex,
		InfoBytes:   infoBytes,
		Summary:     sum,
	}, nil
}

// BuildForUser rebuilds a .torrent using the stored info bytes and a single
// announce URL (passkey already baked in by the caller). private=1 is set so
// well-behaved clients disable DHT/PEX/LSD.
func BuildForUser(infoBytes []byte, announceURL string) []byte {
	priv := int64(1)
	fields := []OuterField{
		{Key: "announce", Str: announceURL},
		{Key: "info", Raw: infoBytes},
		{Key: "private", Int: &priv},
	}
	return BuildOuterDict(fields)
}

// SanitizeAndRebuild is a convenience: sanitize in one pass and emit a new
// .torrent with the given announce URL. Used by tests and one-shot flows.
func SanitizeAndRebuild(raw []byte, announceURL string) ([]byte, error) {
	s, err := Sanitize(raw)
	if err != nil {
		return nil, err
	}
	return BuildForUser(s.InfoBytes, announceURL), nil
}

// SummarizeInfo extracts display-only metadata from a .torrent without
// copying the info bytes out.
func SummarizeInfo(raw []byte) (InfoSummary, error) {
	top, err := ScanTopDict(raw)
	if err != nil {
		return InfoSummary{}, err
	}
	infoSpan, ok := top["info"]
	if !ok {
		return InfoSummary{}, errors.New("torrent: missing info dict")
	}
	return summarizeInfoBytes(raw[infoSpan.Start:infoSpan.End])
}

func summarizeInfoBytes(info []byte) (InfoSummary, error) {
	fields, err := ScanTopDict(info)
	if err != nil {
		return InfoSummary{}, err
	}
	var sum InfoSummary
	if s, ok := fields["name"]; ok {
		b, err := DecodeString(info, s)
		if err != nil {
			return InfoSummary{}, err
		}
		sum.Name = string(b)
	}
	if s, ok := fields["piece length"]; ok {
		n, err := DecodeInt(info, s)
		if err != nil {
			return InfoSummary{}, err
		}
		sum.PieceLength = n
	}
	if s, ok := fields["private"]; ok {
		n, _ := DecodeInt(info, s)
		sum.Private = n == 1
	}
	if s, ok := fields["length"]; ok {
		// single-file mode
		n, err := DecodeInt(info, s)
		if err != nil {
			return InfoSummary{}, err
		}
		sum.TotalSize = n
		sum.Files = []TorrentFile{{Path: sum.Name, Length: n}}
		return sum, nil
	}
	if s, ok := fields["files"]; ok {
		elems, err := DecodeList(info, s)
		if err != nil {
			return InfoSummary{}, err
		}
		sum.Files = make([]TorrentFile, 0, len(elems))
		for _, el := range elems {
			fd, err := ScanDict(info, el)
			if err != nil {
				return InfoSummary{}, err
			}
			var f TorrentFile
			if ls, ok := fd["length"]; ok {
				n, _ := DecodeInt(info, ls)
				f.Length = n
			}
			if ps, ok := fd["path"]; ok {
				parts, err := DecodeList(info, ps)
				if err != nil {
					return InfoSummary{}, err
				}
				segs := make([]string, 0, len(parts))
				for _, p := range parts {
					b, err := DecodeString(info, p)
					if err != nil {
						return InfoSummary{}, err
					}
					segs = append(segs, string(b))
				}
				f.Path = strings.Join(segs, "/")
			}
			sum.TotalSize += f.Length
			sum.Files = append(sum.Files, f)
		}
	}
	return sum, nil
}

func hashToHex(h [20]byte) string {
	const hexchars = "0123456789abcdef"
	out := make([]byte, 40)
	for i, b := range h {
		out[i*2] = hexchars[b>>4]
		out[i*2+1] = hexchars[b&0xf]
	}
	return string(out)
}

var _ = trackerMarkers // reserved for a future explicit-strip path; currently we drop everything except `info` and `announce` which is safer by default.
