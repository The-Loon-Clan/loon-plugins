// storeitems.go opens the points store's catalog to other plugins.
//
// A store item type used to be a closed set inside the store plugin: a
// constant, two switch arms and two hand-written <option> lists. Everything
// the store could sell had to be granted by code the store itself carried, so
// a plugin with something worth selling — charity, a pot donation — could not
// offer it however well it fit.
//
// The shape is the multipliers one: many providers, one consumer. A plugin
// registers a StoreItemType under "store.itemtype.<kind>"; the store looks it
// up PER REQUEST rather than scanning at Provision, so neither side needs the
// other installed, neither needs a Metadata.Requires edge, and a provider may
// register in Start (games only offers charity where it can find need). A
// store with no provider for a kind HIDES those items rather than selling what
// nothing can grant.
//
// THE RULES, here rather than in the store because both sides depend on them:
//
//   - A provider is GRANT-ONLY, like every other granter (rewards.go states
//     the rule): the store debits, the provider hands over. A provider that
//     touches the ledger double-charges, because the points are already gone.
//   - A type may price itself from the member's own input (CostFrom), which is
//     what charity and a pot donation need. The bounds are the field's, so one
//     pair of numbers gates the form and the debit — never two definitions of
//     "how much may I give".
//   - A type declares its buy control as FIELDS, not markup. The store renders
//     them in its own vocabulary, which keeps one plugin answering for the
//     store page's accessibility and its 390px width instead of every provider
//     that ever contributes a control.
//   - A refusal is a sentence the member reads. PrepareStorePurchase returns
//     errors written for them, not for a log.
package pluginapi

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/the-loon-clan/loon/core"
)

// StoreItemTypePrefix is where providers register: "store.itemtype.<kind>",
// where <kind> is the value stored in the item's reward_type column.
const StoreItemTypePrefix = "store.itemtype."

// The field kinds a buy control may declare. Deliberately two: a number and a
// closed set cover every control the shipped types need, and a kind the store
// cannot render is worse than a type that has to express itself in these.
const (
	StoreFieldNumber = "number"
	StoreFieldSelect = "select"
)

// StoreOption is one choice in a select field. Value is what Grant receives.
type StoreOption struct {
	Value string
	Label string
}

// StoreField is one input on the buy control.
type StoreField struct {
	// Name is the field's own name; the store namespaces it on the wire, so
	// a field called "amount" cannot collide with the form's _csrf.
	Name  string
	Label string
	Kind  string // StoreFieldNumber | StoreFieldSelect
	Help  string
	// Min/Max bound a number field. Both zero means unbounded, which for a
	// cost field still means "must be positive" — the store never debits zero.
	Min, Max int64
	// Default is used when the member submits nothing. For a select it should
	// be one of the Options' values; for a number, a figure inside the bounds.
	Default string
	// Suffix labels a number's unit ("pts"), drawn beside the input.
	Suffix  string
	Options []StoreOption
}

// StoreItemTypeInfo is how a type describes itself to the admin's def editor
// and to the store card.
type StoreItemTypeInfo struct {
	// Kind must equal the registry suffix and the reward_type value.
	Kind  string
	Label string // the def editor's dropdown entry
	// RefLabel/RefHelp say what this type reads out of reward_ref, so the def
	// editor can label the field instead of offering one generic "Ref" box.
	RefLabel string
	RefHelp  string
	// Icon is a HOST sprite id. Absent, the store draws its generic tag —
	// a missing symbol renders an empty <use>, so guessing costs a blank box.
	Icon string
	// Note is the store card's one-line explanation of what buying this does.
	Note string
	// ButtonLabel replaces "Buy for N pts" where that sentence would be wrong
	// — a variable-cost item has no N until the member picks one.
	ButtonLabel string
	// Fields are the buy control. Empty means the store's plain Buy button.
	Fields []StoreField
	// CostFrom names the field whose value IS the points cost, for a type the
	// member prices themselves. Empty means the item's fixed points_cost.
	CostFrom string
	// Reason is the ledger type code for the debit ("spend_charity"), so a
	// purchase that is really a donation does not read as a shop sale in the
	// member's own history. Empty keeps the store's own code.
	Reason string
	// LedgerNote is the debit's description. Empty keeps the store's.
	LedgerNote string
}

// StoreRefusal is a refusal whose own sentence the BUYER reads, as opposed to
// a failure that is the operator's to fix. A provider returns one when the
// member can act on it — "nobody currently matches that band" — and a plain
// error otherwise; the store shows the sentence in the first case and its own
// generic message in the second, after reporting it.
type StoreRefusal string

func (e StoreRefusal) Error() string { return string(e) }

// StorePurchase is one settled sale handed to a provider. The points are
// ALREADY DEBITED — Cost is what the member paid, for the provider to
// distribute or credit, never to charge again.
type StorePurchase struct {
	UserID int64
	ItemID int
	// Ref is the item's reward_ref, the def's own configuration.
	Ref  string
	Days int
	Cost int
	// Fields are the buy control's submitted values, defaults already applied
	// and bounds already checked by PrepareStorePurchase.
	Fields map[string]string
}

// Field returns one submitted value, empty for a field that was not declared.
func (p StorePurchase) Field(name string) string { return p.Fields[name] }

// StoreItemType is one purchasable kind contributed by a plugin.
type StoreItemType interface {
	// Describe names the type for the def editor and the store card. ref is
	// the item's reward_ref, so a def can shape its own widget (charity with a
	// pinned ratio band offers no band chooser); it is empty when the editor
	// is describing the type in general. Called per request: one cheap read at
	// most, and never a write.
	Describe(ctx context.Context, ref string) StoreItemTypeInfo

	// Validate refuses a mis-configured def at the ADMIN FORM, where the
	// person who can fix it is looking, rather than at a member's purchase.
	Validate(ctx context.Context, ref string, days int) error

	// Grant settles one purchase and returns the label the store shows the
	// buyer. GRANT-ONLY: see the package comment. An error unwinds the sale —
	// the store refunds the points and restores the stock — so a provider that
	// cannot deliver must say so rather than half-succeed.
	Grant(ctx context.Context, pur StorePurchase) (label string, err error)
}

// LookupStoreItemType resolves one kind. A direct lookup rather than a scan:
// the buy path knows the kind it needs.
func LookupStoreItemType(c *core.Core, kind string) (StoreItemType, bool) {
	if c == nil || kind == "" {
		return nil, false
	}
	v, ok := c.Lookup(StoreItemTypePrefix + kind)
	if !ok {
		return nil, false
	}
	t, ok := v.(StoreItemType)
	return t, ok
}

// StoreItemTypes returns every contributed type, ordered by kind so the def
// editor's dropdown does not reshuffle between page loads.
func StoreItemTypes(c *core.Core) []StoreItemType {
	if c == nil {
		return nil
	}
	var names []string
	for _, name := range c.ExtensionNames() {
		if strings.HasPrefix(name, StoreItemTypePrefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]StoreItemType, 0, len(names))
	for _, name := range names {
		v, ok := c.Lookup(name)
		if !ok {
			continue
		}
		if t, ok := v.(StoreItemType); ok {
			out = append(out, t)
		}
	}
	return out
}

// PrepareStorePurchase checks the buy control's submitted values against what
// the type declared and returns what the purchase costs, plus the values with
// defaults applied — which is what Grant must receive, or a field the member
// left alone arrives empty rather than at its default.
//
// Runs BEFORE the stock claim and the debit, so a refusal costs nothing.
//
// A value the member chose badly comes back as a StoreRefusal they read; a def
// the PROVIDER declared badly comes back as a plain error, because that one is
// the operator's to fix and a member cannot do anything with it.
func PrepareStorePurchase(info StoreItemTypeInfo, fixedCost int, fields map[string]string) (int, map[string]string, error) {
	resolved := make(map[string]string, len(info.Fields))
	for _, f := range info.Fields {
		v := strings.TrimSpace(fields[f.Name])
		if v == "" {
			v = f.Default
		}
		label := f.Label
		if label == "" {
			label = f.Name
		}
		switch f.Kind {
		case StoreFieldNumber:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return 0, nil, StoreRefusal(fmt.Sprintf("%s needs a number", label))
			}
			// Bounds only where the type set them. A Max of zero is "no
			// ceiling", not "nothing allowed" — the alternative silently
			// refuses every purchase of a type that only set a floor.
			if f.Min > 0 && n < f.Min {
				return 0, nil, StoreRefusal(fmt.Sprintf("%s is at least %d", label, f.Min))
			}
			if f.Max > 0 && n > f.Max {
				return 0, nil, StoreRefusal(fmt.Sprintf("%s is at most %d", label, f.Max))
			}
			resolved[f.Name] = strconv.FormatInt(n, 10)
		case StoreFieldSelect:
			ok := false
			for _, o := range f.Options {
				if o.Value == v {
					ok = true
					break
				}
			}
			if !ok {
				return 0, nil, StoreRefusal(fmt.Sprintf("%s: pick one of the offered options", label))
			}
			resolved[f.Name] = v
		default:
			// A kind the store cannot render is a wiring bug in the provider,
			// and passing the raw value through would send an unchecked string
			// to a granter. Refuse, and name the field so it is findable.
			return 0, nil, fmt.Errorf("this item is misconfigured (field %q has unknown kind %q)", f.Name, f.Kind)
		}
	}

	cost := fixedCost
	if info.CostFrom != "" {
		v, ok := resolved[info.CostFrom]
		if !ok {
			return 0, nil, fmt.Errorf("this item is misconfigured (priced from undeclared field %q)", info.CostFrom)
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, nil, StoreRefusal("pick an amount")
		}
		cost = int(n)
	}
	if cost <= 0 {
		return 0, nil, StoreRefusal("pick an amount")
	}
	return cost, resolved, nil
}
