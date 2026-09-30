package uerr_test

// A recovered panic, as an error: which kind, what it wraps, and where it came from.

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/uerr"
)

// panicsWith panics with v from a function of its own, so the site is known.
func panicsWith(v any) error {
	panic(v)
}

func indexOutOfRange() error {
	var xs []int
	i := 3
	return errors.New(string(rune(xs[i])))
}

func caught(fn func() error) (err error) {
	defer uerr.Catch(&err, "collect")
	return fn()
}

func TestCatchTurnsAPanicIntoAnInternalError(t *testing.T) {
	err := caught(func() error { return panicsWith("boom") })
	if !errors.Is(err, uerr.ErrInternal) {
		t.Fatalf("want an internal error, got %v", err)
	}
	var p *uerr.PanicError
	if !errors.As(err, &p) || p.Value != "boom" {
		t.Fatalf("want the panic value as the cause, got %v", err)
	}
	if !strings.Contains(p.Site, "uerr_test.panicsWith") || !strings.Contains(p.Site, "panic_test.go:") {
		t.Errorf("the site is %q, want panicsWith and its line", p.Site)
	}
	if len(p.Stack) == 0 {
		t.Error("no stack")
	}
	for _, want := range []string{"collect", "recovered a panic", "at " + p.Site, "bug in ursus", "caused by: boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not say %q:\n%s", want, err)
		}
	}
}

// TestCatchSeesARuntimeErrorThroughTheResult: a runtime panic keeps its type, so a
// caller can tell an index out of range from anything else — and the site skips
// the runtime's own frames to reach the code that indexed.
func TestCatchSeesARuntimeErrorThroughTheResult(t *testing.T) {
	err := caught(indexOutOfRange)
	var re runtime.Error
	if !errors.As(err, &re) {
		t.Fatalf("want a runtime.Error through the result, got %v", err)
	}
	var p *uerr.PanicError
	if !errors.As(err, &p) || !strings.Contains(p.Site, "uerr_test.indexOutOfRange") {
		t.Errorf("the site should be indexOutOfRange, not the runtime: %+v", p)
	}
}

func TestCatchPassesAnInternalErrorThrough(t *testing.T) {
	own := uerr.Internalf("data: column %q is ragged", "x")
	err := caught(func() error { return panicsWith(own) })
	if err != own {
		t.Errorf("an internal *Error must pass through unchanged, got %v", err)
	}
}

// TestCatchHidesAnotherKindsSentinel: MustValues panics with a KindType error. It
// is still ursus's bug, and errors.Is must not answer ErrType for it — which it
// would if PanicError unwrapped to the *Error.
func TestCatchHidesAnotherKindsSentinel(t *testing.T) {
	typed := uerr.New(uerr.KindType, "values", "column is Int64, not float64")
	err := caught(func() error { return panicsWith(typed) })
	if !errors.Is(err, uerr.ErrInternal) {
		t.Fatalf("want an internal error, got %v", err)
	}
	if errors.Is(err, uerr.ErrType) {
		t.Error("a panic carrying a KindType error answers errors.Is(ErrType)")
	}
	if !strings.Contains(err.Error(), "column is Int64, not float64") {
		t.Errorf("the message lost the panic's own: %v", err)
	}
}

func TestCatchLeavesAReturnedErrorAlone(t *testing.T) {
	want := errors.New("no rows")
	if err := caught(func() error { return want }); err != want {
		t.Errorf("got %v, want the returned error", err)
	}
	if err := caught(func() error { return nil }); err != nil {
		t.Errorf("got %v, want nil", err)
	}
}

func TestCatchCatchesPanicNil(t *testing.T) {
	err := caught(func() error { return panicsWith(nil) })
	var pn *runtime.PanicNilError
	if !errors.Is(err, uerr.ErrInternal) || !errors.As(err, &pn) {
		t.Errorf("panic(nil) must be caught too, got %v", err)
	}
}

func TestAttributedKeepsTheCallersKind(t *testing.T) {
	var err error
	func() {
		defer func() {
			err = uerr.Attributed(recover(), uerr.KindValue, "map_elements", "udf %q panicked", "f")
		}()
		panicsWith("user code")
	}()
	if !errors.Is(err, uerr.ErrValue) || errors.Is(err, uerr.ErrInternal) {
		t.Fatalf("want a value error and not an internal one, got %v", err)
	}
	if !strings.Contains(err.Error(), `udf "f" panicked`) || !strings.Contains(err.Error(), "caused by: user code") {
		t.Errorf("got %v", err)
	}

	// ursus's own assertion is ursus's bug, whoever called it.
	own := uerr.Internalf("ragged")
	func() {
		defer func() { err = uerr.Attributed(recover(), uerr.KindValue, "map_elements", "udf") }()
		panicsWith(own)
	}()
	if err != own {
		t.Errorf("an internal *Error must pass through Attributed too, got %v", err)
	}
}

func TestGuard(t *testing.T) {
	n, err := uerr.Guard("sum", func() (int, error) { return 0, panicsWith("boom") })
	if n != 0 || !errors.Is(err, uerr.ErrInternal) {
		t.Errorf("got %d, %v", n, err)
	}
	n, err = uerr.Guard("sum", func() (int, error) { return 7, nil })
	if n != 7 || err != nil {
		t.Errorf("got %d, %v", n, err)
	}
	if err := uerr.GuardErr("consume", func() error { return panicsWith("boom") }); !errors.Is(err, uerr.ErrInternal) {
		t.Errorf("got %v", err)
	}
}
