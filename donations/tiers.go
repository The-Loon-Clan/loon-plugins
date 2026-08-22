package donations

import (
	"context"
	"encoding/json"
	"strings"
)

// DonorTier is one rung of the donor ladder as the donate page renders it.
//
// Icon is a short string — an emoji, or nothing. The plugin does not care what
// it is and will not validate it: a site that wants no icons leaves it empty
// and the column collapses.
type DonorTier struct {
	Icon  string `json:"icon"`
	Name  string `json:"name"`
	Perks string `json:"perks"`
	Price string `json:"price"`
}

// defaultDonorTiers is the ladder a host gets when it has configured none.
//
// It used to be five hardcoded cards in help_donate.html named Rain, Storm,
// Monsoon, Typhoon and Legendary Supporter — a weather ladder, which is the
// motif of the site this plugin was lifted out of (ame, rain) and means
// nothing anywhere else. The template's own comment already knew: "Tiers are
// currently static. To make them admin-editable wire them to `donate_tiers`
// settings (TODO)."
//
// forum's RepBadge seam states the rule this follows: "what the earned tiers
// are called... is the operator's decision, and a plugin that hardcoded one
// site's ladder would ship those words to every adopter."
//
// So the default is deliberately plain. It is a starting point an operator
// replaces, not a theme they inherit.
var defaultDonorTiers = []DonorTier{
	{Icon: "", Name: "Supporter", Perks: "Badge · Bonus Points", Price: "$5+"},
	{Icon: "", Name: "Patron", Perks: "Badge · Border · Bonus Points", Price: "$25+"},
	{Icon: "", Name: "Benefactor", Perks: "Badge · Border · Effects · Bonus Points", Price: "$50+"},
	{Icon: "", Name: "Champion", Perks: "All Perks · Exclusive Theme · More", Price: "$100+"},
	{Icon: "", Name: "Founder", Perks: "All Perks · Custom Flair · Hall of Fame", Price: "$250+"},
}

// donorTiers reads the operator's ladder, or returns the default.
//
// The setting is a JSON array of DonorTier. Malformed JSON falls back to the
// default rather than rendering an empty section: a donate page with no tiers
// looks like a site that offers nothing for donating, which is a worse answer
// to a typo than showing the defaults.
//
// An EMPTY array is honoured, though — a site that deliberately has no tiers
// says so with [], and that is different from having never configured any.
func donorTiers(ctx context.Context, s Settings) []DonorTier {
	if s == nil {
		return defaultDonorTiers
	}
	raw, err := s.GetSetting(ctx, "donate_tiers")
	if err != nil || strings.TrimSpace(raw) == "" {
		return defaultDonorTiers
	}
	var out []DonorTier
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return defaultDonorTiers
	}
	return out
}
