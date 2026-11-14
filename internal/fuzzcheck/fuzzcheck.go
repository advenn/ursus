// Package fuzzcheck is the bug hunt's one definition of an acceptable outcome for a
// fuzz target (step 508, step 443 of context_files/v0.7.2-plan.md). A target hands
// Run one call of the code under test, and Run fails the input when the call:
//
//   - hangs: a watchdog dumps every goroutine's stack and ends the process after
//     Limits.Wall, because Go's fuzzer has no timeout of its own for one input and
//     a call cannot be stopped from outside;
//   - allocates past Limits.Alloc: the change in runtime/metrics'
//     /gc/heap/allocs:bytes over the call, every goroutine's, not the live heap,
//     which the type-intern table grows during a run whatever the input; the
//     watchdog ends the process at four times the bound, before the machine's
//     memory does, since a process the kernel kills records no failing input;
//   - leaves goroutines behind: their count back at its baseline within a second;
//   - answers ErrInternal, ursus's own "this is a bug in ursus";
//   - panics in ursus's own code, whatever Kind the error it became has: a panic
//     carried in the returned error as a *uerr.PanicError, or one that escaped the
//     call, is judged by the first frame outside the runtime and the standard
//     library, so one in arrow-go, Thrift or klauspost/compress, reading the input,
//     is logged and not failed;
//   - had a request refused by its bounded Allocator (Limits.Mem, Allocator).
//
// Run never runs the call on another goroutine: a target's own t.Fatal stays legal
// inside it. Its counts of allocation and goroutines are the process's, so a target
// never runs beside a t.Parallel test.
//
// The package imports internal/uerr alone of ursus, at level 1, so the internal tests
// of any package above uerr can use it, dtype's and i128's among them; internal/data's
// boundary checks are turned on by a sub-package, internal/fuzzcheck/invariants,
// which a target that builds batches imports.
package fuzzcheck

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/advenn/ursus/internal/uerr"
)

// TB is what Run asks of a test: *testing.T, *testing.F and *testing.B have it.
type TB interface {
	Helper()
	Name() string
	Errorf(format string, args ...any)
	Logf(format string, args ...any)
	FailNow()
}

// Limits bounds one call.
type Limits struct {
	// Wall is how long the call may take before the watchdog takes it for a hang,
	// dumps every goroutine's stack and ends the process. Zero is DefaultWall.
	Wall time.Duration

	// Alloc bounds the bytes allocated while the call runs, as the change in
	// /gc/heap/allocs:bytes: every goroutine's, the call's and any other's. The
	// runtime counts a small object when its span leaves a P's cache, so the count
	// can lag by a few MiB: a bound is for an allocation far past what an input
	// needs, not an exact budget. Zero is DefaultAlloc.
	Alloc int64

	// Mem is the bounded allocator the call reads through, or nil. A request it
	// refused fails the run, even one whose panic the code under it swallowed.
	Mem *Allocator

	// API says the call uses only ursus's public API, which recovers every panic
	// into an error (TestEveryEntryPointRecovers). A panic that reaches the call's
	// caller then fails the run whoever's code raised it.
	API bool
}

// The defaults of a zero Limits field.
const (
	DefaultWall        = 10 * time.Second
	DefaultAlloc int64 = 1 << 30
)

// runawayFactor times Limits.Alloc is where the watchdog stops waiting for the call
// to end and ends the process.
const runawayFactor = 4

// settle is how long the goroutines a call started have to end once it returns.
var settle = time.Second

// tolerated are the modules whose panics, reading an input, are theirs and not
// ursus's: arrow-go's decoders, Thrift's compact protocol under them, and the
// klauspost decompressors every reader goes through.
var tolerated = []string{
	"github.com/apache/arrow-go/",
	"github.com/apache/thrift/",
	"github.com/klauspost/",
}

const self = "github.com/advenn/ursus"

// Run calls fn once under lim and fails t, with Errorf and then FailNow, on any
// outcome the package doc lists. An error fn returns is otherwise its answer: a
// refusal of the input is fine.
func Run(t TB, lim Limits, fn func() error) {
	t.Helper()
	if lim.Wall <= 0 {
		lim.Wall = DefaultWall
	}
	if lim.Alloc <= 0 {
		lim.Alloc = DefaultAlloc
	}
	refusedBefore := lim.Mem.refusals()

	base := runtime.NumGoroutine()
	before := heapAllocs()
	wd := watch(t.Name(), lim, before)
	// Deferred, so a t.FailNow inside fn, which ends this goroutine, stops it too.
	defer wd.stop()

	err, esc := call(fn)
	wd.stop()
	allocated := heapAllocs() - before

	var fail []string
	logf := func(format string, args ...any) { t.Logf("fuzzcheck: "+format, args...) }

	if lim.Mem != nil {
		for _, r := range lim.Mem.Refusals()[refusedBefore:] {
			fail = append(fail, r.report())
		}
	}
	if esc != nil {
		fail = append(fail, esc.judge(lim.API, logf)...)
	}
	if err != nil {
		fail = append(fail, judge(err, logf)...)
	}
	if allocated > lim.Alloc {
		msg := fmt.Sprintf("allocated %d bytes (%.1f MiB) in one call, over the bound of %d (%.1f MiB)",
			allocated, mib(allocated), lim.Alloc, mib(lim.Alloc))
		if s := wd.overStacks(); s != nil {
			msg += "\nthe goroutines when the bound was first passed:\n" + string(s)
		}
		fail = append(fail, msg)
	}
	if n := goroutinesAbove(base); n > 0 {
		fail = append(fail, fmt.Sprintf("%d goroutine(s) still running %v after the call returned; "+
			"all goroutines:\n%s", n, settle, allStacks()))
	}

	if len(fail) > 0 {
		// A refusal the allocator recorded and the error carried is reported once.
		slices.Sort(fail)
		fail = slices.Compact(fail)
		t.Errorf("fuzzcheck: %s", strings.Join(fail, "\n\nfuzzcheck: "))
		t.FailNow()
	}
}

// call runs fn, and recovers a panic that escapes it with where it was raised.
func call(fn func() error) (err error, esc *escaped) {
	defer func() {
		if v := recover(); v != nil {
			esc = &escaped{value: v, callers: panicCallers(), stack: debug.Stack()}
		}
	}()
	return fn(), nil
}

// escaped is a panic that reached Run.
type escaped struct {
	value   any
	callers []string
	stack   []byte
}

func (e *escaped) judge(api bool, logf func(string, ...any)) []string {
	if r, ok := e.value.(*Refusal); ok {
		return []string{r.report()}
	}
	if err, ok := e.value.(error); ok {
		if f := judge(err, logf); len(f) > 0 {
			return f
		}
	}
	who, fn := ownerOf(e.callers)
	switch {
	case api:
		return []string{fmt.Sprintf("a panic reached the caller of ursus's public API, raised in %s: %v\n%s",
			fn, e.value, e.stack)}
	case who == ownedByOthers:
		logf("tolerated: a panic in %s: %v", fn, e.value)
		return nil
	case who == ownedByUrsus:
		return []string{fmt.Sprintf("a panic in ursus's code, %s: %v\n%s", fn, e.value, e.stack)}
	}
	return []string{fmt.Sprintf("a panic in code neither ursus's nor a tolerated module's, %s: %v\n%s",
		fn, e.value, e.stack)}
}

// judge is what is wrong with err, an error the call returned or a panic value that
// is one.
func judge(err error, logf func(string, ...any)) []string {
	var panics []*uerr.PanicError
	var refusal *Refusal
	walk(err, func(e error) {
		switch e := e.(type) {
		case *uerr.PanicError:
			panics = append(panics, e)
		case *Refusal:
			if refusal == nil {
				refusal = e
			}
		}
	})
	if refusal != nil {
		return []string{refusal.report()}
	}
	if errors.Is(err, uerr.ErrInternal) {
		msg := "an internal error, ursus's own \"this is a bug in ursus\":\n" + err.Error()
		for _, p := range panics {
			if who, fn := ownerOf(p.Callers); who != ownedByUrsus {
				msg += fmt.Sprintf("\n(the panic it recovered was raised in %s: the recovery "+
					"took another module's panic for ursus's bug)", fn)
			}
			msg += "\n" + string(p.Stack)
		}
		return []string{msg}
	}
	var fail []string
	for _, p := range panics {
		switch who, fn := ownerOf(p.Callers); who {
		case ownedByOthers:
			logf("tolerated: a panic in %s, answered as: %v", fn, firstLine(err))
		case ownedByUrsus:
			fail = append(fail, fmt.Sprintf("a panic in ursus's code, %s, answered as an error "+
				"of another kind:\n%s\n%s", fn, err, p.Stack))
		default:
			fail = append(fail, fmt.Sprintf("a panic in code neither ursus's nor a tolerated "+
				"module's, %s, answered as:\n%s\n%s", fn, err, p.Stack))
		}
	}
	return fail
}

// walk calls f on err and on every error it wraps, through Unwrap() error and
// Unwrap() []error both.
func walk(err error, f func(error)) {
	for depth := 0; err != nil && depth < 100; depth++ {
		f(err)
		switch u := err.(type) {
		case interface{ Unwrap() []error }:
			for _, e := range u.Unwrap() {
				walk(e, f)
			}
			return
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		default:
			return
		}
	}
}

type owner int

const (
	ownedByNoOne  owner = iota // no frame of ursus's or of a tolerated module's
	ownedByUrsus               // this module, its tests among it
	ownedByOthers              // a tolerated module: arrow-go, Thrift, klauspost
)

// ownerOf is whose panic it is, by the frames from the panic outward, the runtime's
// left out, as uerr.PanicError.Callers lists them. The standard library's frames
// are passed over: a strconv or a slices that panicked was called wrongly by its
// caller. So are another module's, a decoder arrow-go called among them, until a
// frame of ursus's or of a tolerated module's decides. It returns the deciding
// frame's function, or the first frame's when none decides.
func ownerOf(callers []string) (owner, string) {
	for _, fn := range callers {
		pkg := pkgOf(fn)
		if pkg == self || strings.HasPrefix(pkg, self+"/") || strings.HasPrefix(pkg, self+"_test") {
			return ownedByUrsus, fn
		}
		for _, p := range tolerated {
			if strings.HasPrefix(pkg+"/", p) {
				return ownedByOthers, fn
			}
		}
	}
	if len(callers) > 0 {
		return ownedByNoOne, callers[0]
	}
	return ownedByNoOne, "an unknown frame"
}

// pkgOf is the package path of a function as runtime.Frame names it:
// "github.com/advenn/ursus/internal/x.(*T).F" is "github.com/advenn/ursus/internal/x".
func pkgOf(fn string) string {
	slash := strings.LastIndexByte(fn, '/')
	if dot := strings.IndexByte(fn[slash+1:], '.'); dot >= 0 {
		return fn[:slash+1+dot]
	}
	return fn
}

// panicCallers is, called inside a deferred recover, the functions from the panic
// site outward, the runtime's left out: the frames below runtime.gopanic, as
// uerr.PanicError.Callers are taken.
func panicCallers() []string {
	pcs := make([]uintptr, 128)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(1, pcs)])
	var out []string
	below := false
	for {
		f, more := frames.Next()
		switch {
		case f.Function == "runtime.gopanic":
			below = true
		case below && !strings.HasPrefix(f.Function, "runtime."):
			out = append(out, f.Function)
		}
		if !more {
			return out
		}
	}
}

// goroutinesAbove waits up to settle for the process's goroutines to fall to base,
// and returns how many above it remain.
func goroutinesAbove(base int) int {
	deadline := time.Now().Add(settle)
	for {
		n := runtime.NumGoroutine() - base
		if n <= 0 || time.Now().After(deadline) {
			return max(n, 0)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// heapAllocs is the process's cumulative heap allocation, in bytes.
func heapAllocs() int64 {
	s := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return int64(s[0].Value.Uint64())
}

// allStacks is every goroutine's stack.
func allStacks() []byte {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) || len(buf) >= 64<<20 {
			return buf[:n]
		}
		buf = make([]byte, 2*len(buf))
	}
}

func mib(n int64) float64 { return float64(n) / (1 << 20) }

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}

// pollEvery is how often the watchdog reads the allocation counter.
const pollEvery = 10 * time.Millisecond

// watchdog watches one call from its own goroutine.
type watchdog struct {
	done chan struct{}
	gone chan struct{}
	once sync.Once

	mu   sync.Mutex
	over []byte // every goroutine's stack when the call first passed Limits.Alloc
}

// die ends the process for a call that cannot be stopped from outside: it prints
// msg and every goroutine's stack, then panics on the watchdog's goroutine, which no
// one recovers. Under go test -fuzz the fuzzer records the input as a crasher, where
// a process the kernel killed for its memory records none. The package's tests run
// it in a child process.
func die(msg string) {
	fmt.Fprintf(os.Stderr, "\n%s\n\nall goroutines:\n%s\n", msg, allStacks())
	panic(msg)
}

func watch(name string, lim Limits, before int64) *watchdog {
	w := &watchdog{done: make(chan struct{}), gone: make(chan struct{})}
	go func() {
		defer close(w.gone)
		wall := time.NewTimer(lim.Wall)
		defer wall.Stop()
		tick := time.NewTicker(pollEvery)
		defer tick.Stop()
		for {
			select {
			case <-w.done:
				return
			case <-wall.C:
				die(fmt.Sprintf("fuzzcheck: %s: hang: no answer after %v", name, lim.Wall))
				return
			case <-tick.C:
				n := heapAllocs() - before
				if n <= lim.Alloc {
					continue
				}
				w.mu.Lock()
				first := w.over == nil
				if first {
					w.over = allStacks()
				}
				w.mu.Unlock()
				if n > runawayFactor*lim.Alloc {
					die(fmt.Sprintf("fuzzcheck: %s: runaway allocation: %d bytes (%.1f MiB) and "+
						"counting, %d times the bound of %d", name, n, mib(n), runawayFactor, lim.Alloc))
					return
				}
			}
		}
	}()
	return w
}

// stop ends the watchdog and waits for its goroutine to return.
func (w *watchdog) stop() {
	w.once.Do(func() { close(w.done) })
	<-w.gone
}

func (w *watchdog) overStacks() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.over
}
