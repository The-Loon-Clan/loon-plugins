package donations

import (
	"embed"
	"fmt"
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"
)

// The two donation pages, owned by the plugin that owns donations.
//
// The HOST used to carry both (help_donate.html at 963 lines, admin_donate
// at 559), so the plugin could not change its own pages without editing the
// site. Embedded, so a missing template is a build error here rather than a
// 500 at runtime in the host. error.html deliberately did NOT move — see
// renderError.

//go:embed templates/*.html
var pageFS embed.FS

// pageTmpl is parsed in Provision — the FuncMap binds deps.RelativeTime.
// Nil on the legacy contract, where the host renders its own copies of
// these templates by name.
var pageTmpl *template.Template

func parseTemplates() error {
	t, err := template.New("donations").Funcs(template.FuncMap{
		// The one seam-bound function: the site's time wording.
		"relativeTime": func(v any) string { return deps.RelativeTime(v) },
		// What this deployment calls itself. A function rather than a view-model
		// field because both pages want it and neither VM is shared.
		"site":    siteName,
		"siteCap": siteNameCap,
		// Exact copies of the host FuncMap entries these pages rendered
		// with — parity is the lift's contract. All pure; none can drift
		// toward a silently wrong answer.
		"percent": func(a, b int) int {
			if b <= 0 {
				return 0
			}
			p := a * 100 / b
			if p < 0 {
				return 0
			}
			if p > 100 {
				return 100
			}
			return p
		},
		// float64 → int coercion for percent's arguments (donation amounts
		// come out of float64 columns). Truncates toward zero; unknown
		// shapes render as 0 rather than panicking.
		"int": func(v interface{}) int {
			switch n := v.(type) {
			case int:
				return n
			case int64:
				return int(n)
			case float64:
				return int(n)
			case float32:
				return int(n)
			}
			return 0
		},
		"inc":  func(i int) int { return i + 1 },
		"list": func(items ...any) []any { return items },
		"deref": func(p *int) int {
			if p == nil {
				return 0
			}
			return *p
		},
	}).ParseFS(pageFS, "templates/*.html")
	if err != nil {
		return fmt.Errorf("donations: parse templates: %w", err)
	}
	pageTmpl = t
	return nil
}

// The view models are STRUCTS, deliberately, not the gin.H the legacy
// branch still carries. A map answers a missing key with the empty value
// and no error — which is exactly how the public page shipped a reference
// to a field the Donation model lost in a rename, and truncated at the
// Recent Donors carousel whenever a donation existed. Against a struct that
// is a hard render error, and the render tests catch it.

// donateChromeVM carries the viewer-derived keys the fragments read,
// populated centrally in render so no call site can forget them.
type donateChromeVM struct {
	CSRFToken string
	// IsAnon mirrors the BaseData key the public page's "log in first" tip
	// gates on.
	IsAnon bool
}

func (v *donateChromeVM) setChrome(csrf string, anon bool) {
	v.CSRFToken = csrf
	v.IsAnon = anon
}

type donateVM interface {
	setChrome(csrf string, anon bool)
}

// donatePageVM mirrors the DonatePage handler's data, key for key.
type donatePageVM struct {
	donateChromeVM
	Groups []*DonationGoalGroup
	// PointsConfig and PointsPreview are carried for parity with the legacy
	// data map; the current markup does not read them.
	PointsConfig    DonationPointsConfig
	PointsPreview   []DonationPointsRow
	BTCAddress      string
	ETHAddress      string
	XMRAddress      string
	RecentDonations []*Donation
	AddressesHidden bool
	TotalMonthlyUSD float64
	TotalYearlyUSD  float64
	TotalAnnualUSD  float64
	TipJarGoals     []TipJarGoal
	Packages        []*DonationPackageView
	FundedPackages  []*DonationPackageView
	// The donor ladder, from donate_tiers or the plain default. See
	// tiers.go for why it is not five cards in the markup any more.
	Tiers []DonorTier
}

// adminDonateVM mirrors the AdminDonatePage handler's data, key for key.
type adminDonateVM struct {
	donateChromeVM
	IsAdmin       bool
	Costs         []*SiteCost
	Edit          *SiteCost
	Config        DonationPointsConfig
	LockingGroups string
	Preview       []DonationPointsRow
	DonateEnabled bool
	Donations     []*Donation
	Usernames     map[int]string
	Wallet        map[string]string
	TipJar        map[string]string
	Packages      []*DonationPackage
	EditPkg       *DonationPackage
	Saved         string
	ErrCode       string
	BTCPayTest    string
	BTCPayMsg     string
}

// render draws one page: fragment from the plugin's set, chrome from the host.
//
// It used to take a second argument beside vm — the exact gin.H the pre-lift
// handler passed, for hosts still rendering these templates by NAME. Both call
// sites therefore built two descriptions of the same page and threw one away.
// That contract is gone, and so is the duplicate.
func (h *Handlers) render(c *gin.Context, status int, title, name string, vm donateVM) {
	_, signedIn := h.auth.CurrentUser(c)
	vm.setChrome(h.deps.CSRFToken(c), !signedIn)

	var sb strings.Builder
	if err := pageTmpl.ExecuteTemplate(&sb, name, vm); err != nil {
		// html/template streams: a partly-rendered page must not go out as
		// though it were whole.
		c.String(500, "this page failed to render")
		return
	}
	h.deps.RenderPage(c, status, title, template.HTML(sb.String()))
}

// renderError shows the host's error page.
//
// error.html stays HOST-owned on purpose: it is the site-wide 404/403/429
// surface with a dozen host render sites, and moving a copy here would fork
// the page every visitor sees when anything breaks. What donations needs is
// only to reach it — offers and lists cross the same way — with a title
// parameter added because the BTCPay-unconfigured 503 carries custom copy.
func (h *Handlers) renderError(c *gin.Context, code int, title, msg string) {
	h.deps.RenderError(c, code, title, msg)
}
