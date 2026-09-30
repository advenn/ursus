package uerr

import (
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// PanicError is a recovered panic: the value, where it was raised, and the stack.
//
// # A panic is an error, never a dead process
//
// ursus runs a query's expressions on worker goroutines, and a panic in a worker
// goroutine can be recovered by no one else — not the caller, not a test, not the
// program that asked the question. Until step 72 one panic in one row killed the
// host process. Every place ursus runs code now recovers around the CALL, and turns
// what it recovers into an ordinary error through FromPanic or Attributed.
type PanicError struct {
	Value any    // what was passed to panic
	Site  string // the function and line that panicked, or "" if not found
	Stack []byte // the goroutine's stack at the recover

	// Callers are the functions from the panic site outward, the runtime's own left
	// out. It is how a recover decides whose code panicked: a Parquet read that
	// panicked inside arrow-go was reading a corrupt file, and one that panicked in
	// ursus is ursus's bug.
	Callers []string
}

func (p *PanicError) Error() string { return fmt.Sprint(p.Value) }

// Unwrap exposes a panic value that is itself an error, so errors.As finds a
// runtime.Error — a nil dereference, an index out of range — through the result.
//
// Never an ursus *Error. Its Kind would then answer errors.Is for a kind that is
// not the result's: data.MustValues panics with a KindType error, and a caller
// asking errors.Is(err, ErrType) of the internal error it became would get true.
func (p *PanicError) Unwrap() error {
	err, ok := p.Value.(error)
	if !ok {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return nil
	}
	return err
}

// FromPanic turns a value recovered from ursus's own code into an internal error.
//
// An internal *Error passes through unchanged: it is ursus's own assertion
// (Internalf, raised where a signature has no error to return), and it already says
// what it is. Anything else — a runtime error, a string, an *Error of another kind
// — is a bug in ursus too, and becomes KindInternal with the value as its cause.
func FromPanic(v any, op string) *Error {
	if e, ok := v.(*Error); ok && e.Kind == KindInternal {
		return e
	}
	p := recovered(v)
	e := &Error{Kind: KindInternal, Op: op, Msg: "recovered a panic", Err: p}
	if p.Site != "" {
		e.Hint("at %s", p.Site)
	}
	return e.Hint("this is a bug in ursus; please report it")
}

// Attributed turns a value recovered from code ursus calls but did not write — a
// user's udf, or a decoder reading a file — into an error of that code's kind.
//
// An internal *Error still passes through: raised by ursus, it is ursus's bug
// wherever it surfaced.
func Attributed(v any, kind Kind, op, format string, args ...any) *Error {
	if e, ok := v.(*Error); ok && e.Kind == KindInternal {
		return e
	}
	p := recovered(v)
	e := &Error{Kind: kind, Op: op, Msg: fmt.Sprintf(format, args...), Err: p}
	if p.Site != "" {
		e.Hint("at %s", p.Site)
	}
	return e
}

// Catch recovers a panic into *errp, as FromPanic describes. It must be deferred
// DIRECTLY — `defer uerr.Catch(&err, op)` — because recover only stops a panic
// when called by the deferred function itself.
//
// Without a panic it leaves *errp alone, so it costs nothing on the path that
// returns normally.
func Catch(errp *error, op string) {
	if v := recover(); v != nil {
		*errp = FromPanic(v, op)
	}
}

// Guard calls fn, and returns a panic in it as an error.
func Guard[T any](op string, fn func() (T, error)) (v T, err error) {
	defer Catch(&err, op)
	return fn()
}

// GuardErr is Guard for a function that returns only an error.
func GuardErr(op string, fn func() error) (err error) {
	defer Catch(&err, op)
	return fn()
}

// recovered records v with where it was raised. Called from inside a deferred
// recover, the panicking frames are still on the stack: the site is the first
// frame below runtime.gopanic that is not the runtime's own — for an index out of
// range that skips runtime.goPanicIndex, for a nil dereference runtime.sigpanic.
func recovered(v any) *PanicError {
	p := &PanicError{Value: v, Stack: debug.Stack()}
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(1, pcs)])
	below := false
	for {
		f, more := frames.Next()
		switch {
		case f.Function == "runtime.gopanic":
			below = true
		case below && !strings.HasPrefix(f.Function, "runtime."):
			if p.Site == "" {
				p.Site = fmt.Sprintf("%s (%s:%d)", f.Function, trimPath(f.File), f.Line)
			}
			p.Callers = append(p.Callers, f.Function)
		}
		if !more {
			return p
		}
	}
}

// trimPath keeps a file's last two path elements, which name it without the
// machine it was built on.
func trimPath(file string) string {
	i := strings.LastIndexByte(file, '/')
	if i < 0 {
		return file
	}
	if j := strings.LastIndexByte(file[:i], '/'); j >= 0 {
		return file[j+1:]
	}
	return file
}
