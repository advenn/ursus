package ursus_test

// A hoisted Expr must mean the same thing in every query that uses it.
//
// These pin two properties that share a cause: an ursus.Expr is a plain VALUE, so a
// user may build one once and apply it to several frames, and anything the engine
// caches or mutates per-expression has to survive that.
//
// # The one that was a real defect
//
// TestHoistedIsInDoesNotCacheTheWrongEncoding. physical's callCache was keyed on the
// *expr.Call pointer alone while is_in's probe set is encoded against the RECEIVER's
// type, so the second frame was probed with the first frame's encoding and matched
// nothing. Zero rows, no error.
//
// # The one that was not, and why it is pinned anyway
//
// uerr.Annotate MUTATES: errors.As then e.WithOp(op).WithNode(node), writing through
// the pointer. expr.FirstErr hands out the parked error's pointer, and plan.Resolve
// annotates what it gets -- so a construction-time error looked like it would be
// branded by the first query to touch it, permanently, since WithNode is
// innermost-wins-by-emptiness.
//
// It is not, and the reason is a guard nobody had written down: lazy.go's nodes()
// checks e.Err() and returns BEFORE building any plan node, so a parked error never
// reaches Resolve. Every expression-taking builder goes through it. The tests below
// pin the property from the user's side, so that removing that guard makes the
// contamination visible rather than silent.

import (
	"strings"
	"sync"
	"testing"

	"github.com/advenn/ursus"
)

// TestParkedErrorIsNotBrandedByTheFirstQuery. Round(-1) parks a KindValue error
// with no Node; if it reached Resolve, the Select would stamp Node = "Project" onto
// the parked object and the Filter below would report a node it was never part of.
func TestParkedErrorIsNotBrandedByTheFirstQuery(t *testing.T) {
	bad := ursus.Col("v").Round(-1)
	if bad.Err() == nil {
		t.Fatal("Round(-1) should park an error; this test's premise is gone")
	}

	f := func() *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("v", []float64{1.5, 2.5}))
	}

	// First query: a Select, whose resolve arm annotates with node "Project".
	_, first := f().Select(bad).Collect(t.Context())
	if first == nil {
		t.Fatal("expected the parked error to surface")
	}

	// Second query: a Filter. Its own attribution is all it may report.
	_, second := f().Filter(bad.Gt(0.0)).Collect(t.Context())
	if second == nil {
		t.Fatal("expected the parked error to surface again")
	}

	if strings.Contains(second.Error(), "Project") {
		t.Errorf("the filter reports a node from the earlier SELECT — the parked "+
			"error was branded in place and the branding is sticky:\n%v", second)
	}

	// And the reverse direction: the first error must not have acquired the
	// second query's attribution either.
	if strings.Contains(first.Error(), "Filter") {
		t.Errorf("the select's error acquired the later filter's node:\n%v", first)
	}
}

// TestParkedErrorIsStableAcrossRepeatedCollects. The same query twice must report
// the same thing; an annotation that accumulates or drifts shows up here.
func TestParkedErrorIsStableAcrossRepeatedCollects(t *testing.T) {
	bad := ursus.Col("v").Round(-1)
	lf := func() *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("v", []float64{1.5})).Select(bad)
	}

	_, a := lf().Collect(t.Context())
	_, b := lf().Collect(t.Context())
	if a == nil || b == nil {
		t.Fatal("expected an error from both")
	}
	if a.Error() != b.Error() {
		t.Errorf("collecting the same query twice reported differently:\n1: %v\n2: %v", a, b)
	}
}

// TestParkedErrorIsSafeUnderConcurrentCollect. An Expr is a value, so a user may
// hoist one to a package var and use it from several goroutines. Annotating in
// place made that an unsynchronised write to one struct.
//
// This is the -race half; without the fix it is a data race rather than a wrong
// answer, so it is the run under `make race` that matters.
func TestParkedErrorIsSafeUnderConcurrentCollect(t *testing.T) {
	bad := ursus.Col("v").Round(-1)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f := ursus.Frame(ursus.Values("v", []float64{1.5}))
			var err error
			if i%2 == 0 {
				_, err = f.Select(bad).Collect(t.Context())
			} else {
				_, err = f.Filter(bad.Gt(0.0)).Collect(t.Context())
			}
			if err == nil {
				t.Error("expected the parked error")
			}
		}(i)
	}
	wg.Wait()
}

// TestHoistedIsInDoesNotCacheTheWrongEncoding is the live defect.
//
// physical.callCache memoises a compiled Call so a regex is not recompiled per
// batch, and it was keyed on the *expr.Call pointer alone. But is_in's probe set is
// encoded against the RECEIVER's type through GroupKeyEncoder, so the same hoisted
// expression applied to a differently-typed column was probed with the first
// frame's encoding.
//
// It returned ZERO rows instead of two, with no error -- nothing is wrong with the
// encoding, it is simply an encoding of another type. The cache key now includes the
// receiver type.
//
// The old comment claimed "that type is fixed for the plan's lifetime, so the cached
// entry stays valid", which is true of one plan and false of a reused expression.
func TestHoistedIsInDoesNotCacheTheWrongEncoding(t *testing.T) {
	// ONE expression value, applied to two frames. This is ordinary use: an Expr is
	// a value, and hoisting a predicate is what a user does with one.
	probe := ursus.Col("id").IsIn(int64(2), int64(3))

	for _, c := range []struct {
		name string
		lf   func() *ursus.LazyFrame
	}{
		{"int64 first", func() *ursus.LazyFrame {
			return ursus.Frame(ursus.Values("id", []int64{1, 2, 3, 4}))
		}},
		{"int32 second", func() *ursus.LazyFrame {
			return ursus.Frame(ursus.Values("id", []int32{1, 2, 3, 4}))
		}},
		{"int16 third", func() *ursus.LazyFrame {
			return ursus.Frame(ursus.Values("id", []int16{1, 2, 3, 4}))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			df, err := c.lf().Filter(probe).Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			if df.Height() != 2 {
				t.Errorf("got %d rows, want 2 — the probe set was encoded against "+
					"an earlier frame's receiver type\n%s", df.Height(), df)
			}
		})
	}
}

// TestHoistedListContainsDoesNotCacheTheWrongEncoding is the same cache, the other
// field: list.contains encodes its needle against the ELEMENT type.
func TestHoistedListContainsDoesNotCacheTheWrongEncoding(t *testing.T) {
	probe := ursus.Col("t").List().Contains("b")

	mk := func(sep string) *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("s", []string{"a" + sep + "b", "c" + sep + "d"})).
			WithColumns(ursus.Col("s").Str().Split(sep).Alias("t"))
	}
	for _, sep := range []string{",", "|"} {
		df, err := mk(sep).Filter(probe).Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatalf("sep %q: %v", sep, err)
		}
		if df.Height() != 1 {
			t.Errorf("sep %q: got %d rows, want 1", sep, df.Height())
		}
	}
}
