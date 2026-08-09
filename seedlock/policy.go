// Package seedlock keeps one member's torrent on one host at a time.
//
// The cheat it exists for: a member runs the same torrent on several machines
// or seedboxes, each announcing its own uploaded figure, and the tracker
// credits all of them. Nothing about a single announce looks wrong — the
// numbers only stop adding up when you notice one person seeding from four
// places at once.
//
// So a torrent is CLAIMED by the first host that announces it, and other hosts
// are refused until the claim lapses. The claim is held for a lock window past
// the last announce, released immediately on "stopped", and can be cleared by
// the member when they genuinely move machines.
package seedlock

import (
	"fmt"
	"time"
)

// Policy is the site's rules.
type Policy struct {
	// Enabled switches the whole thing off. Default FALSE, like every other
	// rule here that can refuse a member something.
	Enabled bool `json:"enabled"`

	// LockMinutes is how long a claim survives past the claiming host's last
	// announce.
	//
	// Thirty by default: about one announce interval plus slack. Too short and
	// a client restart or a brief network drop hands the torrent to another
	// host mid-session; too long and a member who genuinely moved machines is
	// locked out of their own torrent with no idea why — which is what the
	// clear action is for.
	LockMinutes int `json:"lock_minutes"`

	// IdentifyBy decides what counts as "the same host".
	//
	// "ip" is the default and the honest one: the cheat being prevented is one
	// person seeding from several machines, and machines are what have
	// addresses. "peer" uses the client's peer_id, which is per torrent-session
	// rather than per machine — a client restart mints a new one, so it locks
	// far more aggressively and is offered only for a site that wants that.
	IdentifyBy string `json:"identify_by"`
}

func DefaultPolicy() Policy {
	return Policy{Enabled: false, LockMinutes: 30, IdentifyBy: "ip"}
}

func (p Policy) normalise() Policy {
	if p.LockMinutes <= 0 {
		p.LockMinutes = 30
	}
	if p.IdentifyBy != "peer" {
		p.IdentifyBy = "ip"
	}
	return p
}

// LockWindow is how long a claim outlives its last announce.
func (p Policy) LockWindow() time.Duration {
	return time.Duration(p.normalise().LockMinutes) * time.Minute
}

// Host is what identifies the machine an announce came from.
func (p Policy) Host(ip, peerID string) string {
	if p.normalise().IdentifyBy == "peer" {
		return peerID
	}
	return ip
}

// Claim is one member's hold on one torrent.
type Claim struct {
	Host     string
	LastSeen time.Time
}

// Verdict is what to do with an announce.
type Verdict int

const (
	// Allow — this host holds the claim, or nobody does.
	Allow Verdict = iota
	// Refuse — somebody else's host holds it and the claim is still live.
	Refuse
	// Release — the member is stopping; the claim should be dropped.
	Release
)

// Decide judges one announce against the existing claim.
//
// held is the current claim, or the zero value if the torrent is unclaimed.
// The order of the checks is the design, and each one before the refusal is a
// reason NOT to lock somebody out:
//
//  1. the rule is off;
//  2. the member is stopping — that releases rather than refuses, or a member
//     who stops on host A could never start on host B;
//  3. nobody holds it;
//  4. this IS the holder, which is the overwhelmingly common case and has to
//     be cheap;
//  5. the previous holder has gone quiet for longer than the window.
func Decide(p Policy, held Claim, host string, event string, now time.Time) (Verdict, string) {
	p = p.normalise()
	if !p.Enabled {
		return Allow, ""
	}
	if event == "stopped" {
		return Release, ""
	}
	if held.Host == "" {
		return Allow, ""
	}
	if held.Host == host {
		return Allow, ""
	}
	if now.Sub(held.LastSeen) >= p.LockWindow() {
		// The old holder is gone. This announce takes the claim.
		return Allow, ""
	}
	// The reason is read by a person looking at their torrent client, so it
	// says what to do rather than what went wrong.
	return Refuse, fmt.Sprintf(
		"this torrent is already active from another host (%s). Stop it there, or wait %s, or clear the lock on the site.",
		masked(held.Host), remaining(p, held, now))
}

// masked hides most of an address in the client-facing message.
//
// A member needs to recognise their own other machine, not read it back in
// full: the failure string is shown by a torrent client, which is not a private
// place, and "192.168.x.x" is enough to tell your seedbox from your laptop.
func masked(host string) string {
	if len(host) <= 7 {
		return host
	}
	// Keep the leading half, which is the part that identifies a network.
	keep := len(host) / 2
	return host[:keep] + "…"
}

func remaining(p Policy, held Claim, now time.Time) string {
	left := p.LockWindow() - now.Sub(held.LastSeen)
	if left < time.Minute {
		return "under a minute"
	}
	return fmt.Sprintf("%d minutes", int(left.Minutes())+1)
}
