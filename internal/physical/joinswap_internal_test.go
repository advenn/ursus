package physical

// Step 162: a join the planner may exchange, whose probe side turns out the smaller,
// exchanges its inputs at runtime. It must answer as the join as written: the same
// rows, as a set, when it exchanges, and the same rows in the same order when it
// reads ahead and does not.

import (
	"errors"
	"io"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
)

// swapJoinOf is an inner join of left rows of (k, x) against right rows of (k, x):
// the keys and the other column collide by name, so the exchanged join's Project
// has a merged key and a suffixed column to put back.
func swapJoinOf(t *testing.T, left, right int) *plan.Join {
	t.Helper()
	return &plan.Join{
		Left:          sideScan(t, rand.New(rand.NewPCG(162, 1)), "k", "x", left, 2_000, 40),
		Right:         sideScan(t, rand.New(rand.NewPCG(162, 2)), "k", "x", right, 2_000, 40),
		LeftOn:        []expr.Node{&expr.Col{Name: "k"}},
		RightOn:       []expr.Node{&expr.Col{Name: "k"}},
		Kind:          plan.JoinInner,
		SwapAtRuntime: true,
	}
}

func TestASmallProbeSideIsBuiltInstead(t *testing.T) {
	serial := Options{Threads: 1, BatchSize: 512, Budget: execopt.NewBudget(0, "")}
	parallel := Options{Threads: 4, BatchSize: 512, Budget: execopt.NewBudget(0, "")}
	for _, c := range []struct {
		name        string
		left, right int
		swaps       bool
	}{
		{"a probe side under half the build side's rows", 1_500, 8_000, true},
		{"one just over half", 4_200, 8_000, false},
		{"a larger probe side", 12_000, 3_000, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			want, _, _, err := runJoin(t, swapJoinOf(t, c.left, c.right), serial)
			if err != nil {
				t.Fatal(err)
			}
			before := swappedJoins.Load()
			got, _, _, err := runJoin(t, swapJoinOf(t, c.left, c.right), parallel)
			if err != nil {
				t.Fatal(err)
			}
			if swapped := swappedJoins.Load() != before; swapped != c.swaps {
				t.Fatalf("exchanged %v, want %v", swapped, c.swaps)
			}
			if len(want) == 0 {
				t.Fatal("the fixture joins nothing")
			}
			if !c.swaps {
				// Read ahead and put back: the same rows in the same order.
				if !slices.Equal(got, want) {
					t.Fatalf("%d rows, the join as written gives %d, or in another order", len(got), len(want))
				}
				return
			}
			slices.Sort(want)
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("%d rows, the join as written gives %d, or they differ", len(got), len(want))
			}
		})
	}
}

// TestAnUnmarkedJoinIsNotExchanged: the mark is the planner's proof that the join's
// order is free; without it the join runs as written, however small its probe side.
func TestAnUnmarkedJoinIsNotExchanged(t *testing.T) {
	j := swapJoinOf(t, 1_000, 8_000)
	j.SwapAtRuntime = false
	before := swappedJoins.Load()
	if _, _, _, err := runJoin(t, j, Options{Threads: 4, BatchSize: 512, Budget: execopt.NewBudget(0, "")}); err != nil {
		t.Fatal(err)
	}
	if swappedJoins.Load() != before {
		t.Fatal("a join the planner did not mark was exchanged")
	}
}

// TestReadingAheadStopsAtHalfTheBudget: past half a default budget the probe side is
// read no further, as a deferred build stops waiting there too, and the join runs as
// written. The build is drained by hand, and the budget then taken past half by
// another account, since a build that took it there itself would have caught up.
func TestReadingAheadStopsAtHalfTheBudget(t *testing.T) {
	ctx := t.Context()
	want, _, _, err := runJoin(t, swapJoinOf(t, 1_500, 8_000),
		Options{Threads: 1, BatchSize: 512, Budget: execopt.NewBudget(0, "")})
	if err != nil {
		t.Fatal(err)
	}
	b := execopt.NewBudget(64<<20, t.TempDir())
	b.MarkDefault()
	op, err := planJoin(ctx, swapJoinOf(t, 1_500, 8_000), Options{Threads: 4, BatchSize: 512, Budget: b})
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	br := op.(*joinBreaker)
	for {
		in, err := br.build.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := br.builder.Consume(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	other := b.Account("other")
	other.RetainBytes(b.Limit() / 2)
	defer other.Release()

	before := swappedJoins.Load()
	out, err := br.swapIfSmaller(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil || swappedJoins.Load() != before {
		t.Fatal("exchanged past half the budget")
	}
	probe, err := br.builder.Probe(ctx, br.probe)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	got, err := drainRows(ctx, probe)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%d rows, the join as written gives %d, or in another order", len(got), len(want))
	}
}
