package uerr

import (
	"errors"
	"testing"
)

// TestARaiseIsItself pins Raise's contract: whichever recovery sees it, a raised
// *Error comes back as itself, of its own kind — while an *Error of any kind but
// internal that is panicked the ordinary way is still taken for a bug in ursus.
func TestARaiseIsItself(t *testing.T) {
	e := New(KindResource, "string", "too many characters")
	recovered := func() (v any) {
		defer func() { v = recover() }()
		Raise(e)
		return nil
	}()

	if got := FromPanic(recovered, "op"); got != e {
		t.Errorf("FromPanic gave %v, want the raised error", got)
	}
	if got := Attributed(recovered, KindIO, "op", "a decoder panicked"); got != e {
		t.Errorf("Attributed gave %v, want the raised error", got)
	}
	if err, ok := recovered.(error); !ok || err.Error() != e.Error() {
		t.Errorf("an unrecovered raise prints %v, want %q", recovered, e.Error())
	}

	if got := FromPanic(e, "op"); !errors.Is(got, ErrInternal) {
		t.Errorf("a resource *Error panicked without Raise came back %v, want an internal error", got)
	}
}
