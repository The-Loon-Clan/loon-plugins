package hitrun

import "sync"

// The host seams.
//
// A package-level Notifier set before core.Boot, matching every other plugin
// here that needs the host to do something it cannot do itself.
//
// The important one is LimitReached. This plugin detects hit-and-runs; it does
// not punish them, because the punishment is the host's to define — revoke the
// tracker entitlement, drop a rank, send an email, all three. Putting that
// choice here would bake one site's policy into a framework, and putting it in
// the tracker would mean editing a plugin that has no idea this one exists.

var (
	depsMu   sync.RWMutex
	hostDeps Notifier
)

// SetDeps installs the host's notification seams. Called from main() before
// core.Boot.
//
// Every field is optional and an unset one is simply not called: a host that
// wires nothing still gets the accounting and the member page, just silently.
// That is worse for the member than a notice, and better than a boot failure
// for a site that has not decided yet.
func SetDeps(n Notifier) {
	depsMu.Lock()
	defer depsMu.Unlock()
	hostDeps = n
}

// notifier reads the installed seams.
//
// Under a lock because SetDeps is a package global and the sweep runs on a job
// goroutine — a host that re-wires at runtime (tests do) would otherwise race
// the thing reading it.
func notifier() Notifier {
	depsMu.RLock()
	defer depsMu.RUnlock()
	return hostDeps
}
