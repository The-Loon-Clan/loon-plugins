package usenet

// Spotnet: what the live feed actually looks like right now.
//
// WHY A PROBE AND NOT A COUNTER. There is no spot importer yet, so every
// "spots imported" figure would read zero — and a dashboard of zeros is worse
// than no dashboard, because it looks broken rather than unbuilt. A probe
// answers questions that have real answers TODAY: is free.pt carried, how far
// back does it go, how many of its articles are spots at all, and — the one
// that decides the import policy — what fraction of them carry a key worth
// verifying.
//
// It samples on demand rather than on page load. A probe HEADs one article per
// sampled spot, which is a real cost against the same connection budget the
// crawler is already short of; paying it every time an admin opens a tab would
// be a self-inflicted outage.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// SpotGroup is the Spotnet index group. A fixed property of the protocol, not
// operator config: comments live in free.usenet and the NZB and image payloads
// in alt.binaries.ftd, and a client that reads a different group is not
// reading Spotnet.
const SpotGroup = "free.pt"

// spotProbe is one sample of the live feed.
type spotProbe struct {
	At      time.Time `json:"at"`
	Server  string    `json:"server"`
	Carried bool      `json:"carried"`
	Err     string    `json:"err,omitempty"`

	// Group extent as the server reports it.
	Articles int `json:"articles"`
	Low      int `json:"low"`
	High     int `json:"high"`

	// What the sample contained. NotSpots is not a fault: free.pt carries
	// ordinary posts too and a listing pass will meet plenty of them.
	Sampled   int `json:"sampled"`
	Spots     int `json:"spots"`
	NotSpots  int `json:"not_spots"`
	Malformed int `json:"malformed"`

	// Trust breakdown of the spots that were fully fetched. These are the
	// numbers the import policy turns on.
	Fetched   int `json:"fetched"`
	Verified  int `json:"verified"`
	WeakKey   int `json:"weak_key"`
	Unsigned  int `json:"unsigned"`
	BadSig    int `json:"bad_sig"`
	SmallestK int `json:"smallest_key_bits"`
	LargestK  int `json:"largest_key_bits"`
}

// Summary renders the probe as the one line an operator actually reads.
func (p spotProbe) Summary() string {
	if p.Err != "" {
		return p.Err
	}
	if !p.Carried {
		return SpotGroup + " is not carried by this provider"
	}
	return fmt.Sprintf("%s: %d articles (%d-%d); sampled %d, %d spots, %d verified / %d weak-key / %d unsigned / %d bad",
		SpotGroup, p.Articles, p.Low, p.High, p.Sampled, p.Spots, p.Verified, p.WeakKey, p.Unsigned, p.BadSig)
}

// ForgeableShare is the fraction of fetched spots whose signature proves
// nothing. It is the headline number: it is what makes labelling the right
// policy rather than refusing.
func (p spotProbe) ForgeableShare() int {
	if p.Fetched == 0 {
		return 0
	}
	return p.WeakKey * 100 / p.Fetched
}

// probeSpots samples free.pt on the given server.
//
// sample caps the number of spots HEADed, not the number of XOVER rows read:
// the listing is one round trip regardless, and it is the per-article fetch
// that costs.
func probeSpots(ctx context.Context, srv pluginapi.Server, sample int) spotProbe {
	out := spotProbe{At: time.Now().UTC(), Server: srv.Host}
	if sample <= 0 {
		sample = 25
	}

	conn, err := dialServer(srv)
	if err != nil {
		out.Err = "connect: " + err.Error()
		return out
	}
	defer conn.Quit()

	n, low, high, err := conn.Group(SpotGroup)
	if err != nil {
		// A 411 is the interesting negative and is not an internal fault: the
		// provider simply does not carry the group, which is the single fact
		// that gates the whole feature.
		out.Err = "GROUP " + SpotGroup + ": " + err.Error()
		return out
	}
	out.Carried, out.Articles, out.Low, out.High = true, n, low, high

	// The NEWEST articles: a sample should describe what an importer would
	// meet today, not what free.pt looked like a decade ago.
	begin := high - sample + 1
	if begin < low {
		begin = low
	}
	over, _, err := conn.Overview(begin, high)
	if err != nil {
		out.Err = "XOVER: " + err.Error()
		return out
	}

	for _, ov := range over {
		if ctx.Err() != nil {
			break
		}
		out.Sampled++
		hdr, err := ParseSpotFrom(ov.From)
		switch {
		case err == ErrNotASpot:
			out.NotSpots++
			continue
		case err != nil:
			// Spot-shaped and unparseable — worth counting, because it means
			// either a format change or a bug here.
			out.Malformed++
			continue
		}
		_ = hdr
		if out.Fetched >= sample {
			continue
		}
		if !classifySpot(conn, ov.MessageId, &out) {
			out.Malformed++
		}
	}
	return out
}

// classifySpot HEADs one article and tallies what its signature proved.
func classifySpot(conn interface {
	HeadText(string) (io.Reader, error)
}, msgID string, out *spotProbe) bool {
	r, err := conn.HeadText(msgID)
	if err != nil {
		return false
	}
	h := readSpotHeaders(r)
	out.Fetched++

	key, kerr := ParseSpotKey(h["x-user-key"])
	sig, serr := SpotSignatureBytes(h["x-user-signature"])
	if kerr != nil || serr != nil {
		out.Unsigned++
		return true
	}
	if bits := key.N.BitLen(); bits > 0 {
		if out.SmallestK == 0 || bits < out.SmallestK {
			out.SmallestK = bits
		}
		if bits > out.LargestK {
			out.LargestK = bits
		}
	}
	switch label, _ := SpotTrust(VerifySpot(key, msgID, sig)); label {
	case SpotTrustVerified:
		out.Verified++
	case SpotTrustWeakKey:
		out.WeakKey++
	case SpotTrustUnsigned:
		out.Unsigned++
	default:
		// A signature that WAS checked and failed. The only outcome here that
		// means someone is forging rather than merely unprovable, so it is
		// counted separately and never folded into the others.
		out.BadSig++
	}
	return true
}

// readSpotHeaders reads a HEAD response into a lower-cased map.
//
// Repeated headers are joined in order rather than overwritten — X-Xml arrives
// as several headers whose concatenation is the document, and last-one-wins
// would silently keep only the final fragment.
func readSpotHeaders(r io.Reader) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	last := ""
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && last != "" {
			out[last] += strings.TrimLeft(line, " \t")
			continue
		}
		name, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		last = strings.ToLower(strings.TrimSpace(name))
		out[last] += strings.TrimSpace(val)
	}
	return out
}
