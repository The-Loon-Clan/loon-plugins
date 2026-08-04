package dbmaint

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lib/pq"
)

// bt_index_check reports corruption by RAISING, so an error IS the normal
// signal for a bad index. That makes it dangerously easy to also report every
// timeout as corruption -- and timeouts land on the biggest, busiest tables, so
// the false alarms would arrive weekly on exactly the indexes an operator most
// needs to trust a real alert about.
func TestClassifyVerifyError(t *testing.T) {
	// The real thing: XX002, which is what both silently-broken non-unique
	// indexes returned when the collation change was finally looked for.
	itemOrder := &pq.Error{Code: "XX002", Message: "item order invariant violated for index \"idx_release_regexes_group\""}

	for _, tc := range []struct {
		name      string
		err       error
		parentErr error
		want      verifyOutcome
	}{
		{"clean", nil, nil, verifyClean},

		// Findings.
		{"item order invariant", itemOrder, nil, verifyCorrupt},
		{"wrapped corruption", fmt.Errorf("verify idx_x: %w", itemOrder), nil, verifyCorrupt},
		{"heap tuple not indexed", errors.New("heap tuple was not found in index"), nil, verifyCorrupt},

		// Not findings. A per-index deadline means "too big to check in the
		// window", which is an argument for raising the timeout, not for
		// rebuilding the index.
		{"per-index timeout", context.DeadlineExceeded, nil, verifySkipped},
		{"wrapped timeout", fmt.Errorf("check idx_x: %w", context.DeadlineExceeded), nil, verifySkipped},
		{"per-index cancel", context.Canceled, nil, verifySkipped},

		// Shutdown. The parent context is checked FIRST because cancelling the
		// sweep also cancels the in-flight index check -- without that ordering
		// the last index of every deploy-time run gets reported as damaged.
		{"shutdown mid-check", context.Canceled, context.Canceled, verifyAborted},
		{"shutdown with a corrupt-looking error", itemOrder, context.Canceled, verifyAborted},
		{"parent deadline", context.DeadlineExceeded, context.DeadlineExceeded, verifyAborted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyVerifyError(tc.err, tc.parentErr); got != tc.want {
				t.Errorf("classifyVerifyError(%v, %v) = %v, want %v",
					tc.err, tc.parentErr, got, tc.want)
			}
		})
	}
}

// A clean sweep must not be reachable by accident. If a future refactor made
// every error classify as clean, the job would report "all indexes verified"
// forever and the corruption it exists to find would be invisible again --
// which is precisely the failure mode it replaces.
func TestClassifyVerifyErrorNeverSilentlyClean(t *testing.T) {
	for _, err := range []error{
		&pq.Error{Code: "XX002"},
		&pq.Error{Code: "XX001"}, // data_corrupted
		errors.New("some unfamiliar failure"),
		context.DeadlineExceeded,
	} {
		if got := classifyVerifyError(err, nil); got == verifyClean {
			t.Errorf("classifyVerifyError(%v, nil) reported CLEAN for a non-nil error", err)
		}
	}
}
