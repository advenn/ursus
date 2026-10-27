package uerr_test

import (
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/advenn/ursus/internal/uerr"
)

// relayed runs fns on goroutines through a Relay, as a kernel splits its work, and
// raises their first panic in the caller, under the caller's Catch.
func relayed(fns ...func()) (err error) {
	defer uerr.Catch(&err, "sort")
	var (
		wg    sync.WaitGroup
		relay uerr.Relay
	)
	for _, fn := range fns {
		relay.Go(&wg, fn)
	}
	wg.Wait()
	relay.Raise()
	return nil
}

func panicsInAPiece() { panicsWith("piece boom") }

// TestARelayRaisesItsGoroutinesPanic (step 393): a panic on a goroutine started
// through a Relay reaches the caller's recovery as an internal error, the caller's
// operation on it, with the site and stack of the goroutine that panicked, not of the
// Raise; a runtime error keeps its type; a Raise keeps its kind; of several panics
// one is raised; and without one nothing is.
func TestARelayRaisesItsGoroutinesPanic(t *testing.T) {
	err := relayed(func() {}, panicsInAPiece, func() {})
	if !errors.Is(err, uerr.ErrInternal) {
		t.Fatalf("want an internal error, got %v", err)
	}
	var p *uerr.PanicError
	if !errors.As(err, &p) || p.Value != "piece boom" {
		t.Fatalf("want the piece's panic value as the cause, got %v", err)
	}
	if !strings.Contains(p.Site, "uerr_test.panicsWith") {
		t.Errorf("the site is %q, want the goroutine's panicsWith", p.Site)
	}
	if !strings.Contains(string(p.Stack), "panicsInAPiece") {
		t.Errorf("the stack is not the goroutine's:\n%s", p.Stack)
	}
	if !strings.Contains(err.Error(), "sort: recovered a panic") {
		t.Errorf("the message does not name the caller's operation:\n%s", err)
	}

	err = relayed(func() { _ = indexOutOfRange() })
	var re runtime.Error
	if !errors.As(err, &re) {
		t.Fatalf("want a runtime.Error through the result, got %v", err)
	}

	err = relayed(func() { uerr.Raise(uerr.New(uerr.KindResource, "concat", "too many characters")) })
	if !errors.Is(err, uerr.ErrResource) || errors.Is(err, uerr.ErrInternal) {
		t.Fatalf("a Raise relayed: want its own kind, got %v", err)
	}

	err = relayed(panicsInAPiece, panicsInAPiece, panicsInAPiece, panicsInAPiece)
	if !errors.As(err, &p) || p.Value != "piece boom" {
		t.Fatalf("several panics: want one of them, got %v", err)
	}

	if err := relayed(func() {}, func() {}); err != nil {
		t.Fatalf("no panic: %v", err)
	}
}
