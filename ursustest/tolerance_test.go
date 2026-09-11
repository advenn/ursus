package ursustest_test

// ursustest had no test files at all, which matters more here than almost anywhere:
// these helpers decide whether every OTHER test in the repository is capable of
// failing. A silent gap in AssertFrameEqual weakens 54 call sites at once.

import (
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// recorder captures whether an assertion failed, so a helper can be tested on its
// negative case without failing the run.
type recorder struct {
	testing.TB
	failed bool
	msgs   []string
}

func (r *recorder) Errorf(format string, args ...any) { r.failed = true }
func (r *recorder) Error(args ...any)                 { r.failed = true }
func (r *recorder) Fatalf(format string, args ...any) { r.failed = true; panic(sentinel{}) }
func (r *recorder) Fatal(args ...any)                 { r.failed = true; panic(sentinel{}) }
func (r *recorder) Helper()                           {}

type sentinel struct{}

// didFail runs fn against a recorder, treating a Fatal as a failure rather than
// aborting the surrounding test.
func didFail(fn func(testing.TB)) bool {
	r := &recorder{}
	func() {
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(sentinel); !ok {
					panic(p)
				}
			}
		}()
		fn(r)
	}()
	return r.failed
}

func frameOf(t *testing.T, name string, vals any) *ursus.DataFrame {
	t.Helper()
	var lf *ursus.LazyFrame
	switch v := vals.(type) {
	case []float32:
		lf = ursus.Frame(ursus.Values(name, v))
	case []float64:
		lf = ursus.Frame(ursus.Values(name, v))
	default:
		t.Fatalf("unsupported %T", vals)
	}
	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return df
}

// TestToleranceComparesFloat32 is the regression test for a hole that had no caller.
//
// renderRows drops EVERY float column when a tolerance is configured — isFloat
// covers both widths — and assertFloatsClose used to read only float64, skipping a
// Float32 column because TypedColumn[float64] refuses it by design. So a Float32
// column was compared by neither path, and two frames with wildly different Float32
// values compared equal.
//
// Nothing in the repository passed a Float32 column with WithTolerance, which is why
// it survived: the gap was real and unreachable at the same time.
func TestToleranceComparesFloat32(t *testing.T) {
	for _, c := range []struct {
		name     string
		got      []float32
		want     []float32
		mustFail bool
	}{
		{"far apart", []float32{1, 2, 3}, []float32{1, 2, 99}, true},
		{"within tolerance", []float32{1, 2, 3}, []float32{1, 2, 3.0000001}, false},
		{"identical", []float32{1, 2, 3}, []float32{1, 2, 3}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := frameOf(t, "v", c.got)
			want := frameOf(t, "v", c.want)
			failed := didFail(func(tb testing.TB) {
				ursustest.AssertFrameEqual(tb, got, want, ursustest.WithTolerance(0, 1e-6))
			})
			if failed != c.mustFail {
				t.Errorf("failed = %v, want %v — a Float32 column under WithTolerance "+
					"must be compared, not skipped", failed, c.mustFail)
			}
		})
	}
}

// TestToleranceStillComparesFloat64 pins the path that already worked, so the
// Float32 fix cannot have been bought by breaking it.
func TestToleranceStillComparesFloat64(t *testing.T) {
	got := frameOf(t, "v", []float64{1, 2, 3})
	want := frameOf(t, "v", []float64{1, 2, 99})
	if !didFail(func(tb testing.TB) {
		ursustest.AssertFrameEqual(tb, got, want, ursustest.WithTolerance(0, 1e-9))
	}) {
		t.Error("a Float64 difference beyond tolerance must still fail")
	}
}
