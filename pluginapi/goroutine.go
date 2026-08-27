// goroutine.go — fire-and-forget work that cannot take the process down.
//
// WHY IT EXISTS. Go terminates the whole program on an unrecovered panic in
// ANY goroutine, and gin's Recovery middleware covers only the goroutine
// serving the request. So a plugin that answers a request and then fans work
// out — send the notification, refresh the metadata, scrape the archive — has
// put a process-killer on a member-triggered path. The request already
// returned 200; there is nothing left to fail, and yet the site goes down.
//
// It bites hardest where a plugin calls a HOST SEAM. The plugin cannot see
// that code, cannot test it, and cannot know what it will do with a nil map or
// a short slice — but it is the plugin's goroutine that dies of it, and the
// host's whole process that goes with it. loon's job loop has had exactly this
// protection since it was written (schedule.runTickProtected, for exactly this
// reason: "a panic in one tick doesn't kill the ServiceLoop goroutine"); the
// asynchronous half of the request path never got the equivalent.
//
// Found 2026-08-27 by auditing after the loon-demo-site session hit the same
// shape on its own host, where three jobs were protected on their timer path
// and fatal on the operator's press-the-button path. Their lesson came with a
// method note worth repeating: they first counted goroutines with
// `grep "go func("`, got five, and were about to report that. An AST pass
// found fourteen — `go someCall(...)` and a `go` inside a closure argument are
// both goroutines and neither matches that pattern, and ALL THREE real bugs
// were in the nine the grep missed. This package was audited with an AST walk
// for that reason.
package pluginapi

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"

	"github.com/the-loon-clan/loon/core"
)

// Go runs fn in a goroutine whose panic is recovered and reported rather than
// fatal.
//
// Use it for work nothing waits on. It is deliberately NOT a drop-in for a
// goroutine joined by a WaitGroup: there, recovering changes what the request
// renders — the caller proceeds with whatever the panicking branch failed to
// fill in — and whether partial data beats no data is a decision per call
// site, not one this helper may make for you.
//
// op is the stable label the error is filed under ("dm/notify"), matching the
// host's error-log convention: no ids, no user input, so the log's dedup-merge
// still works.
//
// errs may be nil — a plugin under test, or one wired before the host's
// reporter exists — in which case the panic goes to the standard logger. It is
// never swallowed: a panic that vanishes is worse than one that crashes,
// because the second at least gets investigated.
func Go(errs core.ErrorReporter, op string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				err := fmt.Errorf("panic: %v", r)
				if errs != nil {
					// The stack is the whole value of the report; without it
					// "panic: runtime error: invalid memory address" names no
					// line in any file.
					errs.Report(context.Background(), op,
						fmt.Errorf("%w\n%s", err, debug.Stack()))
					return
				}
				log.Printf("pluginapi.Go/%s: %v\n%s", op, err, debug.Stack())
			}
		}()
		fn()
	}()
}
