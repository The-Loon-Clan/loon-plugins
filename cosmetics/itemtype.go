package cosmetics

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// itemType makes cosmetics purchasable.
//
// ONE kind rather than one per effect, and the def's reward_ref decides the
// shape of the buy control:
//
//	ref = ""            the item sells the CATALOGUE — the member picks
//	ref = "glow-gold"   the item sells that one effect, no chooser
//
// Both are real editorial choices an operator makes, which is why this follows
// the pattern the charity type already set (a pinned ratio band offers no band
// chooser). One shop row reading "Name effect — pick one" is the sane default;
// a pinned row is how you sell a single effect at a different price, or put a
// gold aura behind a donation tier without it appearing in the general list.
type itemType struct{ p *Plugin }

var _ pluginapi.StoreItemType = itemType{}

func (t itemType) Describe(ctx context.Context, ref string) pluginapi.StoreItemTypeInfo {
	info := pluginapi.StoreItemTypeInfo{
		Kind:     itemKind,
		Label:    "Name effect",
		RefLabel: "Effect",
		RefHelp: "The effect this item grants — aura, glow-gold, glow-ice, glow-ember, " +
			"pulse, shimmer, rainbow or sparkle. Leave blank to let the buyer choose.",
		Icon:   "star",
		Reason: "spend_cosmetic",
	}
	if e, ok := pluginapi.EffectBySlug(ref); ok {
		// Pinned. The card says which one, because "Name effect" on a row that
		// only ever grants a gold aura is a card that mis-sells itself.
		info.Note = e.Label + " — " + e.Description
		return info
	}
	info.Note = "Pick an effect for your username. You can own several and switch between them."
	opts := make([]pluginapi.StoreOption, 0, len(pluginapi.Effects))
	for _, e := range pluginapi.Effects {
		opts = append(opts, pluginapi.StoreOption{Value: e.Slug, Label: e.Label})
	}
	info.Fields = []pluginapi.StoreField{{
		Name:    "effect",
		Label:   "Effect",
		Kind:    pluginapi.StoreFieldSelect,
		Help:    "Wearable straight away, and you can switch whenever you like.",
		Default: pluginapi.Effects[0].Slug,
		Options: opts,
	}}
	return info
}

// Validate refuses a mis-configured def at the admin form.
//
// The check that matters is a ref naming an effect the catalogue does not have:
// a typo there sells something that can never render, and the buyer finds out
// by their name looking exactly the same as before.
func (t itemType) Validate(ctx context.Context, ref string, days int) error {
	if ref == "" {
		return nil // the chooser; every option comes from the catalogue
	}
	if _, ok := pluginapi.EffectBySlug(ref); !ok {
		return fmt.Errorf("no effect called %q — see the help under this field for the list", ref)
	}
	return nil
}

// Grant unlocks the effect and, if the member is wearing nothing, puts it on.
//
// AUTO-EQUIP on the first one only. Somebody who buys their first effect wants
// to see it, and making them visit a second page to turn on the thing they just
// paid for is the kind of step that gets read as "it didn't work". Somebody who
// already wears one has made a choice, and a purchase must not overwrite it —
// buying a second effect to look at is not the same as switching to it.
func (t itemType) Grant(ctx context.Context, pur pluginapi.StorePurchase) (string, error) {
	slug := pur.Ref
	if slug == "" {
		slug = pur.Field("effect")
	}
	e, ok := pluginapi.EffectBySlug(slug)
	if !ok {
		// Returning an error unwinds the sale — the store refunds the points
		// and restores the stock — which is the right outcome for a def
		// pointing at an effect that does not exist.
		return "", fmt.Errorf("cosmetics: no effect called %q", slug)
	}
	if err := t.p.st.Unlock(ctx, pur.UserID, e.Slug, SourceStore, pur.Days); err != nil {
		return "", err
	}
	worn, err := t.p.st.EquippedBy(ctx, pur.UserID, SlotName)
	if err == nil && worn == "" {
		if _, err := t.p.st.Equip(ctx, pur.UserID, SlotName, e.Slug); err != nil {
			// Not fatal: they own it, and the page they are about to be sent
			// to is exactly where you put one on. Unwinding a completed sale
			// over a failed convenience would be worse.
			return e.Label, nil
		}
		return e.Label + ", now on your name", nil
	}
	return e.Label, nil
}
