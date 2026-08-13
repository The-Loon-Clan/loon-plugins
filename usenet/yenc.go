package usenet

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
)

// yEnc article bodies: the control headers, and the payload decoder.
//
// The crawler indexes from OVERVIEW lines and has never read a body. This is the
// primitive that changes that, and five features sit on it — NFO extraction,
// sample images, RAR header and password detection, yEnc header verification,
// and part repair. See docs/BODY-FETCH.md.
//
// yEnc is an ENCODING, not encryption. Every field below is plaintext ASCII at
// the top or bottom of the body, outside whatever the payload turns out to be —
// so a password-protected archive still yields its true outer filename, size and
// CRC32. What a password hides is the archive's CONTENTS, one layer further in.

// yencHeader is what the control lines declare about a file and this part of it.
type yencHeader struct {
	// Name is the poster's own filename for the whole file, from =ybegin. It is
	// independent of the subject, which is what makes it worth fetching a body
	// for: an obfuscated subject can hide the release, while the decoder needs
	// an honest name to write the file out.
	Name string
	// Size is the size of the ENTIRE file, not of this part. From =ybegin.
	Size int64
	// Part and Total are this part's number and the true part count. Multipart
	// only; both zero for a single-part post. Total is authoritative where the
	// subject's "(1/45)" counter is whatever the poster typed.
	Part  int
	Total int
	// Begin and End are this part's byte range within the whole file, 1-based
	// and inclusive, from =ypart. Zero when single-part.
	Begin int64
	End   int64
	// EndSize is the decoded byte count of THIS part, from =yend.
	EndSize int64
	// CRC32 is the checksum of the whole file and appears only on the final
	// part; PartCRC32 covers this part alone. Both hex, lowercased, empty when
	// absent.
	//
	// CRC32 is the strongest content key available anywhere in this pipeline:
	// computed over decoded source bytes, so it is immune to renaming and to
	// re-posting, and it works on payloads we cannot otherwise identify at all.
	CRC32     string
	PartCRC32 string
}

var errNoYbegin = errors.New("yenc: no =ybegin line")

// parseYencHeader reads the control lines without decoding the payload.
//
// Cheap on purpose: the caller has the whole body in memory either way (NNTP has
// no partial read — RFC 3977 offers ARTICLE, HEAD, BODY, STAT and nothing
// byte-ranged), but the fields are what most callers actually want, and decoding
// a megabyte to read a filename would be waste.
func parseYencHeader(body []byte) (yencHeader, error) {
	var h yencHeader
	seenBegin := false
	for _, raw := range bytes.Split(body, []byte("\n")) {
		line := strings.TrimRight(string(raw), "\r")
		switch {
		case strings.HasPrefix(line, "=ybegin "):
			seenBegin = true
			h.Part = int(yencInt(line, "part="))
			h.Total = int(yencInt(line, "total="))
			h.Size = yencInt(line, "size=")
			// name= LAST and unquoted, by spec, so it runs to end of line and
			// may contain spaces. Anything that splits the line on whitespace
			// truncates every filename with a space in it.
			if i := strings.Index(line, "name="); i >= 0 {
				h.Name = strings.TrimSpace(line[i+len("name="):])
			}
		case strings.HasPrefix(line, "=ypart "):
			h.Begin = yencInt(line, "begin=")
			h.End = yencInt(line, "end=")
		case strings.HasPrefix(line, "=yend "):
			h.EndSize = yencInt(line, "size=")
			h.CRC32 = strings.ToLower(yencStr(line, "crc32="))
			h.PartCRC32 = strings.ToLower(yencStr(line, "pcrc32="))
			// =yend is the last control line; nothing after it is a header.
			if seenBegin {
				return h, nil
			}
		}
	}
	if !seenBegin {
		return h, errNoYbegin
	}
	return h, nil
}

// yencStr reads a `key=value` token, value ending at the next space.
//
// Matches on a SPACE-prefixed key (or line start) so that "pcrc32=" cannot be
// found by a search for "crc32=" — they overlap, and reading a part checksum as
// the whole-file one would silently produce a wrong content key.
func yencStr(line, key string) string {
	i := strings.Index(line, " "+key)
	if i < 0 {
		if !strings.HasPrefix(line, key) {
			return ""
		}
		i = -1
	}
	v := line[i+1+len(key):]
	if j := strings.IndexByte(v, ' '); j >= 0 {
		v = v[:j]
	}
	return v
}

func yencInt(line, key string) int64 {
	n, err := strconv.ParseInt(yencStr(line, key), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// yencDecode returns the payload bytes.
//
// The encoding is deliberately trivial: every output byte is (input - 42) mod
// 256, with '=' introducing an escape whose next byte is (input - 64 - 42) mod
// 256. Only a handful of values are ever escaped (NUL, LF, CR, '='), because the
// point is to stay transport-safe over NNTP rather than to compress.
//
// Line endings are structure, not data, and are dropped. Any line beginning
// "=y" is a control line and is skipped — decoding =ybegin as payload is the
// classic way to corrupt the first bytes of every file.
func yencDecode(body []byte) ([]byte, error) {
	out := make([]byte, 0, len(body))
	started := false
	for _, raw := range bytes.Split(body, []byte("\n")) {
		line := bytes.TrimRight(raw, "\r")
		if bytes.HasPrefix(line, []byte("=y")) {
			if bytes.HasPrefix(line, []byte("=ybegin")) {
				started = true
			}
			if bytes.HasPrefix(line, []byte("=yend")) {
				break
			}
			continue
		}
		if !started {
			// Preamble. Some posters put plain text above =ybegin; it is not
			// payload and decoding it would prepend garbage.
			continue
		}
		for i := 0; i < len(line); i++ {
			c := line[i]
			if c == '=' {
				i++
				if i >= len(line) {
					// An escape with nothing after it: the line was truncated.
					// Dropping it silently would shift every later byte.
					return nil, errors.New("yenc: truncated escape at end of line")
				}
				out = append(out, byte((int(line[i])-64-42)&0xFF))
				continue
			}
			out = append(out, byte((int(c)-42)&0xFF))
		}
	}
	if !started {
		return nil, errNoYbegin
	}
	return out, nil
}
