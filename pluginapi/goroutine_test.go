package pluginapi

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// A panic in fanned-out work must reach the error log instead of the process's
// exit status. If this test ever fails by CRASHING the run rather than
// reporting, that is the bug it exists to prevent.
func TestGoReportsAPanicInsteadOfKillingTheProcess(t *testing.T) {
	var (
		mu     sync.Mutex
		gotOp  string
		gotErr error
		done   = make(chan struct{})
	)
	errs := core.NewErrorReporter(core.ErrorAdapter{
		ReportFn: func(_ context.Context, op string, err error) {
			mu.Lock()
			gotOp, gotErr = op, err
			mu.Unlock()
			close(done)
		},
	})

	Go(errs, "test/boom", func() { panic("kaboom") })
	<-done

	mu.Lock()
	defer mu.Unlock()
	if gotOp != "test/boom" {
		t.Errorf("op = %q, want the label passed in", gotOp)
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "kaboom") {
		t.Fatalf("err = %v, want the panic value", gotErr)
	}
	// The stack is the whole value of the report — without it the message
	// names no line in any file.
	if !strings.Contains(gotErr.Error(), "goroutine") {
		t.Errorf("report carries no stack: %v", gotErr)
	}
}

// The ordinary path stays ordinary: fn runs, nothing is reported.
func TestGoRunsTheWorkAndReportsNothingWhenItSucceeds(t *testing.T) {
	var reported int
	var mu sync.Mutex
	errs := core.NewErrorReporter(core.ErrorAdapter{
		ReportFn: func(context.Context, string, error) {
			mu.Lock()
			reported++
			mu.Unlock()
		},
	})

	done := make(chan struct{})
	Go(errs, "test/ok", func() { close(done) })
	<-done

	mu.Lock()
	defer mu.Unlock()
	if reported != 0 {
		t.Errorf("reported %d errors on a clean run", reported)
	}
}

// A nil reporter is a real state (a plugin under test, or wired before the
// host's reporter exists) and must not itself panic — the recover would then
// be the thing that killed the process.
func TestGoSurvivesANilReporter(t *testing.T) {
	done := make(chan struct{})
	Go(nil, "test/nil", func() { defer close(done); panic("still contained") })
	<-done
}
