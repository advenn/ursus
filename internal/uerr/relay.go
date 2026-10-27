package uerr

import "sync"

// Relay carries a panic out of the goroutines a function starts, to be raised again
// in that function once they are done, where its caller's recovery is (step 393, A1
// of v0.8-scope.md). A panic on a goroutine is recovered by no one else: unrecovered,
// it ends the process, and a service with it. A sort's pieces, a key table's parts
// and a frame's columns were started so, with nothing to catch them.
//
// TestEveryGoroutineIsCovered holds every goroutine to recovering in its own body;
// one started through a Relay recovers by construction.
type Relay struct {
	once sync.Once
	v    any
}

// Go runs fn on a goroutine of wg's, and keeps the first panic among them for Raise.
func (r *Relay) Go(wg *sync.WaitGroup, fn func()) {
	wg.Go(func() {
		defer func() {
			if v := recover(); v != nil {
				r.once.Do(func() { r.v = relayable(v) })
			}
		}()
		fn()
	})
}

// Raise panics in the caller with the first panic a goroutine of the Relay's raised,
// once their WaitGroup's Wait has returned; without one it does nothing.
func (r *Relay) Raise() {
	if r.v != nil {
		panic(r.v)
	}
}

// relayed is a panic recovered on another goroutine and raised again: FromPanic and
// Attributed read its value, site and stack, the first panic's, where they would take
// the Raise's own.
type relayed struct{ p *PanicError }

func (r relayed) Error() string { return r.p.Error() }

// relayable is v as Raise raises it: a Raise in flight, ursus's own internal error or
// a panic already relayed as it is, as the recovery it reaches reads them so; anything
// else recorded where it was raised, called as recovered is, below the panic.
func relayable(v any) any {
	switch e := v.(type) {
	case raised, relayed:
		return v
	case *Error:
		if e.Kind == KindInternal {
			return v
		}
	}
	return relayed{recovered(v)}
}

// panicOf is v's record: a relayed panic's own, else v's, taken where it was raised.
func panicOf(v any) *PanicError {
	if r, ok := v.(relayed); ok {
		return r.p
	}
	return recovered(v)
}
