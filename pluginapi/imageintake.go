// imageintake.go declares how a plugin gets a MEMBER-SUPPLIED image onto the
// site: it hands over a URL and gets back a local path.
//
// WHY A SEAM RATHER THAN A PLUGIN FETCHING IT ITSELF. Fetching a URL somebody
// typed is the single most dangerous thing a web application does casually. It
// is a request made by the SERVER, from inside the network, to an address the
// attacker chose — which is how an image field becomes a way to read a cloud
// metadata endpoint, port-scan a private subnet, or make a site with a VPN
// egress policy leak its real address by asking it to fetch something.
//
// None of that is a plugin's business to get right, and every plugin getting it
// right separately is every plugin getting it wrong eventually. The host owns
// the HTTP client, the egress policy, the address rules, the size cap, the
// content sniffing and the file store; a plugin owns knowing that it wants a
// picture.
//
// See anidb.go for the package-level contract discipline this follows.
package pluginapi

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// ImageIntakeName is the Core extension-registry key under which a host
// publishes its ImageIntake.
const ImageIntakeName = "media.intake"

// StoredImage is where a fetched picture ended up.
type StoredImage struct {
	// URL is the path this site serves it from — always local. A caller
	// renders THIS and never the address it was given, which is the whole
	// point: a page that renders a remote URL sends every one of its readers
	// to a third party on load, handing that host a log of who reads what, and
	// leaves it free to swap the image afterwards.
	URL string
	// MIME is what the bytes actually were, sniffed rather than declared.
	MIME  string
	Bytes int64
}

// ImageIntake fetches a member-supplied image and stores it locally.
type ImageIntake interface {
	// FetchImage retrieves remote, checks it, and stores it under dir.
	//
	// The error is safe to show the member who supplied the URL: it says what
	// was wrong with their link, never anything about this site's network. A
	// refusal that leaked whether an internal address answered would be the
	// scan this exists to prevent, only politer.
	FetchImage(ctx context.Context, dir, remote string) (StoredImage, error)
}

// Images resolves the registered implementation. Absent means the host does not
// offer intake, and a plugin that wanted it should say so rather than fetch the
// URL itself.
func Images(c *core.Core) (ImageIntake, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Lookup(ImageIntakeName)
	if !ok {
		return nil, false
	}
	i, ok := v.(ImageIntake)
	return i, ok
}
