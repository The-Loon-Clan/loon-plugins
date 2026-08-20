// contributions.go is the standard shape for a CLOSED SET a plugin can open:
// a setting whose values the host thought it owned, and which somebody else
// turns out to have a fourth of.
//
// The site's registration modes are the worked example. The host shipped three
// — open, invite-only, closed — as a validated string in a settings row, and a
// three-way switch is exactly the sort of thing nobody expects to grow. Then
// the applications plugin arrived with a fourth ("apply and wait"), and it had
// nowhere to put it: not a value the host's validator accepted, not a radio
// button on the host's form, not a case in its registration handler. The
// options were to edit the host for every idea anybody has about joining, or to
// make the set extensible once.
//
// EXTENSIBLE ONCE is this file. The mechanism was already there — the registry
// takes any key, so a PREFIX is a namespace and a scan over it is a set — but
// every domain wrote the scan itself, and five copies of a loop drift: two of
// the five sorted their results and three did not, so a dropdown built from one
// reshuffled between page loads while another was stable, and one checked that
// the registered key matched the value's own idea of its key while the rest
// took whatever they were handed.
//
// See CHECKLIST.md section 1 for the question this is the answer to: before
// shipping a setting with a closed set of values, ask whether another plugin
// could ever want to add one.
package pluginapi

import (
	"sort"
	"strings"

	"github.com/the-loon-clan/loon/core"
)

// Contribution is one value registered under a prefix, with the key it was
// registered under.
//
// The key travels WITH the value because most consumers need both: a stored
// setting names a contribution by key, a form submits one by key, and the value
// itself usually carries its own copy — which is precisely why they can
// disagree, and why the check below exists.
type Contribution[T any] struct {
	// Key is the part after the prefix: "apply" from "auth.regmode.apply".
	Key   string
	Value T
}

// Contributions returns every value registered under prefix that is a T,
// ordered by key.
//
// ORDERED, always, and it is not a nicety. These become radio buttons, dropdown
// entries and admin tables; map iteration in Go is deliberately randomised, so
// an unsorted scan produces a control whose options move between page loads.
// That reads as a bug in the page and is the reason two of the five hand-rolled
// versions of this loop were quietly wrong.
//
// Anything registered under the prefix that is NOT a T is skipped rather than
// reported. The registry is shared and a prefix is a namespace, not a lock: a
// domain that wants to know about a mismatch should check its own registrations
// at boot, which is what /admin/contracts is for.
func Contributions[T any](c *core.Core, prefix string) []Contribution[T] {
	if c == nil || prefix == "" {
		return nil
	}
	var out []Contribution[T]
	for _, name := range c.ExtensionNames() {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		v, ok := c.Lookup(name)
		if !ok {
			continue
		}
		t, ok := v.(T)
		if !ok {
			continue
		}
		out = append(out, Contribution[T]{Key: strings.TrimPrefix(name, prefix), Value: t})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// ContributedValues is Contributions when the caller does not need the keys.
func ContributedValues[T any](c *core.Core, prefix string) []T {
	cs := Contributions[T](c, prefix)
	out := make([]T, 0, len(cs))
	for _, x := range cs {
		out = append(out, x.Value)
	}
	return out
}

// Contributed resolves ONE contribution by key.
//
// A direct lookup rather than a scan, because a caller with a key already knows
// what it wants — a purchase names its item kind, a stored setting names its
// registration mode. Absent and wrong-type are the same answer: not available.
func Contributed[T any](c *core.Core, prefix, key string) (T, bool) {
	var zero T
	if c == nil || key == "" {
		return zero, false
	}
	v, ok := c.Lookup(prefix + key)
	if !ok {
		return zero, false
	}
	t, ok := v.(T)
	return t, ok
}

// ConsistentKeys drops any contribution whose value disagrees with the key it
// was registered under.
//
// The failure it catches is worth naming, because it is silent and it is the
// kind of thing that only bites in production. A registered key is what a
// settings row STORES and what a form SUBMITS; the value's own key is what the
// code branches on. When they differ, a site saves "apply", looks up "apply",
// finds the mode, renders it — and then the mode reports itself as something
// else to whatever asked, so one half of the site believes the setting took and
// the other half does not. It surfaces to an operator as "the site quietly
// reverted my choice", which is nearly impossible to chase from that end.
//
// Not folded into Contributions, because it only applies where a value carries
// its own key, which is not most of them.
func ConsistentKeys[T any](cs []Contribution[T], keyOf func(T) string) []Contribution[T] {
	out := cs[:0]
	for _, x := range cs {
		if k := keyOf(x.Value); k != "" && k == x.Key {
			out = append(out, x)
		}
	}
	return out
}
