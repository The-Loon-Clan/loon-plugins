package dbmaint

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lib/pq"
)

// The distinction this makes is the difference between a job that retries a
// transient hiccup next month and a job that retries an impossibility forever.
//
// It got the second one for a long time: a unique-violation on one index was
// treated as "a concurrent ALTER, skip past it", so the same REINDEX failed
// every month, left an invalid _ccnew index each time, and said so only to an
// in-memory ring buffer that every deploy cleared. Eighty-two of them
// accumulated before anyone looked.
func TestIsPermanentReindexFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},

		// Permanent: the table holds rows the unique index cannot represent,
		// so the build fails at the same row every time.
		{"unique violation", &pq.Error{Code: "23505"}, true},
		{"exclusion violation", &pq.Error{Code: "23P01"}, true},
		{"wrapped unique violation",
			fmt.Errorf("reindex users_username_key: %w", &pq.Error{Code: "23505"}), true},

		// Transient: worth trying again on the next pass.
		{"lock timeout", &pq.Error{Code: "55P03"}, false},
		{"deadlock", &pq.Error{Code: "40P01"}, false},
		{"admin shutdown", &pq.Error{Code: "57P01"}, false},
		{"connection lost", errors.New("driver: bad connection"), false},

		// The text fallback exists for errors whose *pq.Error did not survive
		// wrapping. Narrow on purpose: a false positive here turns a transient
		// failure into a loud one, which is the harmless direction to be wrong.
		{"text fallback", errors.New("could not create unique index \"x\""), true},
		{"unrelated text", errors.New("could not create index"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPermanentReindexFailure(tc.err); got != tc.want {
				t.Errorf("isPermanentReindexFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
