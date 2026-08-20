package cosmetics

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// titleItemType sells the RIGHT to have a custom title.
//
// Its own kind rather than a reward_ref on the effect type, because the two
// grant genuinely different things and an admin choosing from a dropdown should
// be able to see that: one puts a look on you immediately, the other opens a
// form whose output a moderator has to read.
//
// BUYING PUBLISHES NOTHING. That is the entire security argument for selling
// this at all — the shop hands out the right to propose words, staff decide
// whether they appear, and a member who buys it and writes something unusable
// has spent their points on a rejection.
type titleItemType struct{ p *Plugin }

var _ pluginapi.StoreItemType = titleItemType{}

func (t titleItemType) Describe(ctx context.Context, ref string) pluginapi.StoreItemTypeInfo {
	return pluginapi.StoreItemTypeInfo{
		Kind:     titleKind,
		Label:    "Custom title",
		RefLabel: "Unused",
		RefHelp: "This type takes no reference — it grants the right to propose a title. " +
			"Set a day count to make the right expire.",
		Icon: "tag",
		Note: "Write your own words under your name. Staff read every one before it appears.",
		// Said on the card, because a member who buys this and sees nothing
		// change has been sold something that looks broken. The sentence is the
		// difference between "it didn't work" and "it is waiting".
		Reason:     "spend_cosmetic",
		LedgerNote: "Bought the right to a custom title",
	}
}

// Validate refuses a ref, because this type has none and a def carrying one is
// an admin expecting behaviour that does not exist.
func (t titleItemType) Validate(ctx context.Context, ref string, days int) error {
	if ref != "" {
		return fmt.Errorf("this type takes no reference — it grants the right to propose a title, " +
			"and the words come from the member")
	}
	return nil
}

func (t titleItemType) Grant(ctx context.Context, pur pluginapi.StorePurchase) (string, error) {
	if err := t.p.st.Unlock(ctx, pur.UserID, titleUnlock, SourceStore, pur.Days); err != nil {
		return "", err
	}
	return "the right to a custom title — write one on your appearance page", nil
}
