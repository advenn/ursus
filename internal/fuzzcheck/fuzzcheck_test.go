package fuzzcheck

// The harness's teeth (step 508): each outcome Run exists to catch is made to happen,
// and Run must report it; a panic in a tolerated module must be logged and pass. A
// fake TB records what Run says, so a run meant to fail does not fail this test. The
// two outcomes that end the process, a hang and a runaway allocation, run in a child
// process: this test binary again, told by an environment variable which one.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/bitutil"
	"github.com/klauspost/compress/gzip"

	"github.com/advenn/ursus/internal/uerr"
)

// fakeT is a TB that records instead of failing.
type fakeT struct {
	errs, logs []string
	failNow    bool
}

func (f *fakeT) Helper()      {}
func (f *fakeT) Name() string { return "FuzzTeeth" }
func (f *fakeT) Errorf(format string, args ...any) {
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
}
func (f *fakeT) Logf(format string, args ...any) {
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}
func (f *fakeT) FailNow() { f.failNow = true }

func (f *fakeT) failed() bool { return len(f.errs) > 0 }
func (f *fakeT) said() string { return strings.Join(f.errs, "\n") }

// run is Run under a fakeT.
func run(lim Limits, fn func() error) *fakeT {
	f := &fakeT{}
	Run(f, lim, fn)
	return f
}

// mustFail checks Run failed f, and that what it said holds every phrase.
func mustFail(t *testing.T, f *fakeT, phrases ...string) {
	t.Helper()
	if !f.failed() || !f.failNow {
		t.Fatalf("Run passed a call it must fail (logged: %q)", f.logs)
	}
	for _, p := range phrases {
		if !strings.Contains(f.said(), p) {
			t.Errorf("Run's report does not say %q:\n%s", p, f.said())
		}
	}
}

func mustPass(t *testing.T, f *fakeT) {
	t.Helper()
	if f.failed() || f.failNow {
		t.Fatalf("Run failed a call it must pass:\n%s", f.said())
	}
}

// ursusBoom panics in this module's code: a write to a nil map, raised by the runtime
// on its behalf.
//
//go:noinline
func ursusBoom() {
	var m map[string]int
	m["x"]++
}

// arrowBoom panics inside arrow-go: a bit set past the end of an empty bitmap.
func arrowBoom() { bitutil.SetBit(nil, 9) }

// klauspostBoom panics inside klauspost/compress: a read from a nil gzip reader.
func klauspostBoom() {
	var zr *gzip.Reader
	zr.Read(make([]byte, 1))
}

// attributedIO is how a reader recovers a panic in a decoder: as an error of KindIO
// carrying the panic, as Parquet's corrupt and arrowsrc's callerCode do.
func attributedIO(f func()) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = uerr.Attributed(v, uerr.KindIO, "scan_test", "the input is corrupt")
		}
	}()
	f()
	return nil
}

func TestAnAnswerOrARefusalPasses(t *testing.T) {
	mustPass(t, run(Limits{}, func() error { return nil }))
	mustPass(t, run(Limits{}, func() error {
		return uerr.New(uerr.KindValue, "scan_test", "not a number")
	}))
	mustPass(t, run(Limits{}, func() error { return context.Canceled }))
}

func TestAnInternalErrorFails(t *testing.T) {
	mustFail(t, run(Limits{}, func() error { return uerr.Internalf("a broken invariant") }),
		"internal error", "a broken invariant")
	mustFail(t, run(Limits{}, func() error {
		return fmt.Errorf("collect: %w", uerr.Internalf("wrapped"))
	}), "internal error", "wrapped")
	mustFail(t, run(Limits{}, func() error {
		return errors.Join(errors.New("first"), uerr.Internalf("joined"))
	}), "internal error", "joined")
}

func TestAPanicInUrsusCodeFails(t *testing.T) {
	t.Run("escaped", func(t *testing.T) {
		mustFail(t, run(Limits{}, func() error { ursusBoom(); return nil }),
			"a panic in ursus's code", "fuzzcheck.ursusBoom")
	})
	t.Run("recovered as ErrInternal", func(t *testing.T) {
		mustFail(t, run(Limits{}, func() error {
			return uerr.GuardErr("scan_test", func() error { ursusBoom(); return nil })
		}), "internal error", "ursusBoom")
	})
	// arrowsrc's callerCode labelled ursus's own panics KindIO until step 428; the
	// kind does not excuse it.
	t.Run("recovered as KindIO", func(t *testing.T) {
		mustFail(t, run(Limits{}, func() error { return attributedIO(ursusBoom) }),
			"a panic in ursus's code", "answered as an error of another kind", "the input is corrupt")
	})
	t.Run("in the standard library, called by ursus", func(t *testing.T) {
		n := -1
		mustFail(t, run(Limits{}, func() error {
			return attributedIO(func() { _ = strings.Repeat("x", n) })
		}), "a panic in ursus's code")
	})
}

func TestAPanicInAToleratedModuleIsLoggedNotFailed(t *testing.T) {
	for name, boom := range map[string]func(){"arrow-go": arrowBoom, "klauspost": klauspostBoom} {
		t.Run(name+" recovered", func(t *testing.T) {
			f := run(Limits{}, func() error { return attributedIO(boom) })
			mustPass(t, f)
			if !strings.Contains(strings.Join(f.logs, "\n"), "tolerated") {
				t.Errorf("the tolerated panic is not logged: %q", f.logs)
			}
		})
		t.Run(name+" escaped", func(t *testing.T) {
			f := run(Limits{}, func() error { boom(); return nil })
			mustPass(t, f)
			if !strings.Contains(strings.Join(f.logs, "\n"), "tolerated") {
				t.Errorf("the tolerated panic is not logged: %q", f.logs)
			}
		})
		// The public API recovers every panic: one that reaches its caller is a bug,
		// whoever raised it.
		t.Run(name+" escaped the public API", func(t *testing.T) {
			mustFail(t, run(Limits{API: true}, func() error { boom(); return nil }),
				"reached the caller of ursus's public API")
		})
	}
	// An internal error is a bug however it was raised: a recovery that takes a
	// decoder's panic for ursus's mislabels a corrupt input.
	t.Run("recovered as ErrInternal", func(t *testing.T) {
		mustFail(t, run(Limits{}, func() error {
			return uerr.GuardErr("scan_test", func() error { arrowBoom(); return nil })
		}), "internal error", "took another module's panic for ursus's bug")
	})
}

// The owner of a panic is decided by the first frame of ursus's or of a tolerated
// module's, the standard library's and other modules' passed over: Thrift's, here,
// whose panics this binary cannot raise without importing it, which ursus does not.
func TestThriftsPanicsAreTolerated(t *testing.T) {
	thrift := &uerr.PanicError{Value: "index out of range", Callers: []string{
		"encoding/binary.Uvarint",
		"github.com/apache/thrift/lib/go/thrift.(*TCompactProtocol).readVarint64",
		"github.com/apache/arrow-go/v18/parquet/internal/thrift.DeserializeThrift",
		"github.com/advenn/ursus/internal/source/parquet.(*Source).Schema",
	}}
	f := run(Limits{}, func() error {
		return uerr.Wrap(thrift, uerr.KindIO, "scan_parquet", "corrupt")
	})
	mustPass(t, f)
	if !strings.Contains(strings.Join(f.logs, "\n"), "thrift.(*TCompactProtocol).readVarint64") {
		t.Errorf("the deciding frame is not Thrift's: %q", f.logs)
	}

	// Another module's frame decides nothing: here ursus called it.
	other := &uerr.PanicError{Value: "boom", Callers: []string{
		"golang.org/x/text/encoding/unicode.(*utf16Decoder).Transform",
		"github.com/advenn/ursus/internal/source/csv.(*scanner).next",
	}}
	mustFail(t, run(Limits{}, func() error {
		return uerr.Wrap(other, uerr.KindIO, "scan_csv", "corrupt")
	}), "a panic in ursus's code", "csv.(*scanner).next")
}

func TestAnAllocationPastTheBoundFails(t *testing.T) {
	var keep [][]byte
	allocate := func(mib int) func() error {
		return func() error {
			for range mib {
				keep = append(keep, make([]byte, 1<<20))
			}
			keep = nil
			return nil
		}
	}
	// Twice the bound: past it, and short of the four times at which the watchdog
	// ends the process.
	mustFail(t, run(Limits{Alloc: 8 << 20}, allocate(16)), "allocated", "over the bound of 8388608")
	mustPass(t, run(Limits{Alloc: 64 << 20}, allocate(1)))
}

func TestALeakedGoroutineFails(t *testing.T) {
	defer func(d time.Duration) { settle = d }(settle)
	settle = 100 * time.Millisecond

	release := make(chan struct{})
	gone := make(chan struct{})
	f := run(Limits{}, func() error {
		go leakyWait(release, gone)
		return nil
	})
	close(release)
	<-gone
	mustFail(t, f, "1 goroutine(s) still running", "fuzzcheck.leakyWait")

	// One that ends within settle is not a leak.
	mustPass(t, run(Limits{}, func() error {
		go time.Sleep(20 * time.Millisecond)
		return nil
	}))
}

//go:noinline
func leakyWait(release <-chan struct{}, gone chan<- struct{}) {
	<-release
	close(gone)
}

func TestTheBoundedAllocatorRefusesPastItsLimit(t *testing.T) {
	if got := RequestLimit(1000); got != RequestBase+RequestPerByte*1000 {
		t.Fatalf("RequestLimit(1000) = %d", got)
	}

	a := &Allocator{Limit: 1 << 20}
	mustPass(t, run(Limits{Mem: a}, func() error {
		b := a.Allocate(1 << 20)
		a.Free(a.Reallocate(1<<19, b))
		return nil
	}))

	// Escaped: the Refusal is the panic's value.
	mustFail(t, run(Limits{Mem: a}, func() error { a.Allocate(1<<20 + 1); return nil }),
		"refused a request for 1048577 bytes", "over its limit of 1048576", "TestTheBoundedAllocatorRefusesPastItsLimit")
	// Recovered into an error, as ursus's recovery does.
	mustFail(t, run(Limits{}, func() error {
		return uerr.GuardErr("scan_test", func() error { a.Reallocate(2<<20, nil); return nil })
	}), "refused a request for 2097152 bytes")
	// Swallowed: the record still fails the run.
	mustFail(t, run(Limits{Mem: a}, func() error {
		func() {
			defer func() { _ = recover() }()
			a.Allocate(-1)
		}()
		return nil
	}), "refused a request for -1 bytes")
	if n := len(a.Refusals()); n != 3 {
		t.Errorf("the allocator recorded %d refusals of three", n)
	}
}

func TestAPoisonedAllocatorFillsWhatItHandsOutAndTakesBack(t *testing.T) {
	a := &Allocator{Poison: true}
	b := a.Allocate(100)
	if len(b) != 100 || bytes.Count(b, []byte{PoisonByte}) != len(b) {
		t.Fatalf("a fresh buffer of %d bytes is not all poison: %x", len(b), b)
	}
	for i := range b {
		b[i] = 0
	}
	b = a.Reallocate(50, b)
	b = a.Reallocate(100, b) // within its capacity: the 50 bytes it gains are poison
	if b[49] != 0 || b[50] != PoisonByte {
		t.Errorf("a buffer regrown within its capacity: %x", b)
	}
	a.Free(b)
	if b[0] != PoisonByte {
		t.Errorf("a freed buffer is not poisoned: %x", b[:4])
	}
}

// teethEnv names the outcome a child process runs.
const teethEnv = "FUZZCHECK_TEETH"

// TestTeethChild is the child half of the tests of the outcomes that end the process.
func TestTeethChild(t *testing.T) {
	switch os.Getenv(teethEnv) {
	case "":
		t.Skip("the child half of TestAHangEndsTheProcess and TestARunawayAllocationEndsTheProcess")
	case "hang":
		Run(t, Limits{Wall: 200 * time.Millisecond}, func() error {
			blockForever()
			return nil
		})
	case "runaway":
		var keep [][]byte
		Run(t, Limits{Alloc: 4 << 20}, func() error {
			for range 256 {
				keep = append(keep, make([]byte, 1<<20))
				time.Sleep(time.Millisecond)
			}
			return nil
		})
		runtime.KeepAlive(keep)
	}
	t.Error("the child's run returned")
}

//go:noinline
func blockForever() { <-make(chan struct{}) }

// child runs TestTeethChild for outcome, and returns its output, failing unless the
// process ended by a panic.
func child(t *testing.T, outcome string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTeethChild$", "-test.count=1", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), teethEnv+"="+outcome)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !bytes.Contains(out, []byte("\npanic: fuzzcheck: ")) {
		t.Fatalf("the child did not end by the watchdog's panic (%v):\n%s", err, out)
	}
	return string(out)
}

func TestAHangEndsTheProcess(t *testing.T) {
	out := child(t, "hang")
	for _, p := range []string{"TestTeethChild: hang: no answer after 200ms", "all goroutines:", "fuzzcheck.blockForever"} {
		if !strings.Contains(out, p) {
			t.Errorf("the hung child's output does not say %q:\n%s", p, out)
		}
	}
}

func TestARunawayAllocationEndsTheProcess(t *testing.T) {
	out := child(t, "runaway")
	for _, p := range []string{"TestTeethChild: runaway allocation", "4 times the bound of 4194304", "all goroutines:"} {
		if !strings.Contains(out, p) {
			t.Errorf("the child's output does not say %q:\n%s", p, out)
		}
	}
}
