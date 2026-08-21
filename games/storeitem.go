package games

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Charity, sold from the points store.
//
// The charity page is still the home of the feature; this makes it reachable
// from the shop as well, because a member spending points looks at the shop —
// and on a store-first site, a page in the Community menu is a page nobody
// finds.
//
// It is a CONTRIBUTED store item type (pluginapi/storeitems.go), which is the
// only shape that works without breaking the rule this ecosystem is built on:
// the store does not import games, games does not import the store, and
// neither declares the other in Metadata.Requires. A host with no store never
// calls any of this; a store with games uninstalled hides charity items
// rather than selling what nothing can grant.
//
// The one rule this file has to keep is grant-only: the store already debited
// the buyer by the time Grant runs, so it pays out through distribute, which
// deducts nothing. Charging again here would take the points twice.
type charityItemType struct{ p *Plugin }

// CharityItemKind is the reward_type value stored on a charity item, and the
// suffix of its registry name.
const CharityItemKind = "charity"

// bandValue renders a ratio the way it round-trips: ParseFloat of this string
// is exactly the float in charityRatios, so a submitted band matches the
// closed set by equality rather than by tolerance.
func bandValue(r float64) string { return strconv.FormatFloat(r, 'g', -1, 64) }

// bands is the offered set as store options.
func bands() []pluginapi.StoreOption {
	out := make([]pluginapi.StoreOption, 0, len(charityRatios))
	for _, r := range charityRatios {
		out = append(out, pluginapi.StoreOption{
			Value: bandValue(r),
			Label: fmt.Sprintf("members under %s ratio", bandValue(r)),
		})
	}
	return out
}

// Describe shapes the buy control from the operator's own numbers, so the
// bounds on the form are the bounds on the charity page — one definition of
// "how much may I give", not a second copy that drifts.
//
// ref pins the band: an item created with one offers no chooser (a "Give to
// members under 0.5" item is a different product from an open one), and an
// item created without lets the buyer pick.
func (c charityItemType) Describe(ctx context.Context, ref string) pluginapi.StoreItemTypeInfo {
	// Describing an item must not fail — or panic — a store page render, and
	// the defaults are the same ones Settings falls back to on a bad row.
	cfg := defaults()
	if c.p != nil && c.p.st != nil {
		if got, err := c.p.st.Settings(ctx); err == nil {
			cfg = got
		}
	}
	info := pluginapi.StoreItemTypeInfo{
		Kind:  CharityItemKind,
		Label: "Charity",
		// "users" rather than a heart: it is a host sprite that exists, and a
		// missing symbol renders an empty box.
		Icon:        "users",
		RefLabel:    "ratio band, or blank to let the buyer choose",
		RefHelp:     "One of the offered bands (0.1, 0.25, 0.5, 0.75, 1).",
		ButtonLabel: "Give",
		CostFrom:    "amount",
		// The receipt keeps its meaning: charity bought in the shop reads as
		// charity in the member's history, under the code the charity page has
		// always written.
		Reason:     "spend_charity",
		LedgerNote: "Charity to members in need",
		Note: fmt.Sprintf("Split evenly among members in need who have downloaded at least %d GB. Anonymous both ways.",
			cfg.CharityDLFloorGB),
		Fields: []pluginapi.StoreField{{
			Name: "amount", Label: "Amount", Kind: pluginapi.StoreFieldNumber,
			Min: cfg.CharityMin, Max: cfg.CharityMax,
			Default: strconv.FormatInt(cfg.CharityMin, 10), Suffix: "pts",
		}},
	}
	if _, ok := parseBand(ref); !ok {
		info.Fields = append(info.Fields, pluginapi.StoreField{
			Name: "band", Label: "Give to", Kind: pluginapi.StoreFieldSelect,
			Default: bandValue(0.5), Options: bands(),
		})
	} else {
		info.Note = fmt.Sprintf("Members under %s ratio. ", ref) + info.Note
	}
	return info
}

// parseBand reads a band off a string, reporting whether it is one of the
// offered ones. An empty or unreadable ref is "not pinned", which is the def
// that lets the buyer choose — not an error, because it is how an open
// charity item is written.
func parseBand(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	r, err := strconv.ParseFloat(s, 64)
	if err != nil || !validCharityRatio(r) {
		return 0, false
	}
	return r, true
}

// Validate refuses a def at the admin form. A blank ref is legal (the buyer
// chooses); a ref that is neither blank nor one of the bands is a typo, and
// saying so here is the difference between an admin fixing it now and a
// member meeting it at a purchase.
func (c charityItemType) Validate(_ context.Context, ref string, _ int) error {
	if ref == "" {
		return nil
	}
	if _, ok := parseBand(ref); !ok {
		return fmt.Errorf("charity needs one of the offered bands in reward ref (%s, %s, %s, %s or %s), or blank to let the buyer choose",
			bandValue(charityRatios[0]), bandValue(charityRatios[1]), bandValue(charityRatios[2]),
			bandValue(charityRatios[3]), bandValue(charityRatios[4]))
	}
	return nil
}

// Grant pays out one purchase. GRANT-ONLY: pur.Cost is money the store has
// already taken, so this distributes it and never deducts.
func (c charityItemType) Grant(ctx context.Context, pur pluginapi.StorePurchase) (string, error) {
	band, ok := parseBand(pur.Ref)
	if !ok {
		// Not pinned by the def, so the buyer picked — and the store already
		// checked their answer against the options this type declared.
		if band, ok = parseBand(pur.Field("band")); !ok {
			return "", fmt.Errorf("charity item %d: no usable ratio band", pur.ItemID)
		}
	}
	needy, err := c.p.recipients(ctx, pur.UserID, band)
	if err != nil {
		// "nobody currently matches that band" is the buyer's to act on — they
		// can pick a wider one — so it reaches them as a SENTENCE. The store
		// refunds either way.
		//
		// Rendered here, from this plugin's templates, because the store shows
		// the buyer whatever text this returns and cannot map a code it has no
		// vocabulary for. messages.go carries the reasoning.
		var bad errBadInput
		if errors.As(err, &bad) {
			return "", pluginapi.StoreRefusal(c.p.refusalText(ctx, string(bad)))
		}
		return "", err
	}
	n := c.p.distribute(ctx, pur.UserID, int64(pur.Cost), band, needy)
	return c.p.grantedText(int64(pur.Cost), n), nil
}
