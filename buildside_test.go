package ursus_test

// An inner join hashes its smaller input (step 104).
//
// The physical join builds from the right input. The build_side rule rewrites
// Join(L, R) to Project(Join(R, L)) when R is estimated more than twice L's size,
// and the Project restores every output column. These pin that the answer — the
// rows, the names, the types, the nullability — is the unswapped one in every key
// shape the layout treats differently, that the rule fires where it should and
// nowhere else, and that JoinMaintainOrder keeps the left order.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/ursustest"
)

// noBuildSide is the default optimizer with the build_side rule off.
func noBuildSide() plan.Flags {
	f := plan.DefaultFlags()
	f.BuildSide = false
	return f
}

// sortedByAll sorts df by every column, so two answers that differ only in row
// order compare equal.
func sortedByAll(t *testing.T, df *ursus.DataFrame) *ursus.DataFrame {
	t.Helper()
	var keys []ursus.SortKey
	for _, f := range df.Schema().All() {
		keys = append(keys, ursus.Asc(ursus.Col(f.Name)))
	}
	out, err := df.Lazy().Sort(keys...).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// swapped reports whether lf's optimized plan exchanged a join's inputs. A swapped
// join carries the rule's internal suffix, which Explain renders on its JOIN line
// whether or not any column needed it.
func swapped(t *testing.T, lf *ursus.LazyFrame) bool {
	t.Helper()
	p, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(p, `suffix "__build_side_swap"`)
}

func TestBuildSideKeepsTheAnswer(t *testing.T) {
	c := ursus.Col
	// small has 6 rows and big 60, so big is the larger input by ten times.
	small := func() *ursus.LazyFrame {
		return ursus.Frame(
			ursus.ValuesNullable("k", []int64{1, 2, 3, 4, 5, 0}, []bool{true, true, true, true, true, false}),
			ursus.Values("k32", []int32{1, 2, 3, 4, 5, 6}),
			ursus.Values("v", []string{"a", "b", "c", "d", "e", "f"}),
			ursus.Values("w", []int64{1, 2, 3, 4, 5, 6}),
		)
	}
	big := func() *ursus.LazyFrame {
		k := make([]int64, 60)
		kv := make([]bool, 60)
		k64 := make([]int64, 60)
		v := make([]string, 60)
		x := make([]float64, 60)
		for i := range 60 {
			k[i], kv[i] = int64(i%8), i%13 != 0
			k64[i] = int64(i % 7)
			v[i] = fmt.Sprintf("big%02d", i)
			x[i] = float64(i) / 4
		}
		return ursus.Frame(
			ursus.ValuesNullable("k", k, kv),
			ursus.Values("k64", k64),
			ursus.Values("v", v), // collides with small's v
			ursus.Values("x", x),
		)
	}

	cases := []struct {
		name   string
		opts   []ursus.JoinOption
		noSwap bool
	}{
		{"one key, the same name, merged", []ursus.JoinOption{ursus.JoinOn(c("k"))}, false},
		{"one key, different names", []ursus.JoinOption{ursus.JoinLeftOn(c("w")), ursus.JoinRightOn(c("k64"))}, false},
		{"different names, merged by request", []ursus.JoinOption{ursus.JoinLeftOn(c("w")), ursus.JoinRightOn(c("k64")), ursus.JoinCoalesce(true)}, false},
		{"an Int32 key against an Int64 one", []ursus.JoinOption{ursus.JoinLeftOn(c("k32")), ursus.JoinRightOn(c("k64"))}, false},
		{"null keys equal", []ursus.JoinOption{ursus.JoinOn(c("k")), ursus.JoinNullsEqual(true)}, false},
		{"two keys", []ursus.JoinOption{ursus.JoinLeftOn(c("k"), c("k32")), ursus.JoinRightOn(c("k"), c("k64"))}, false},
		{"a computed key", []ursus.JoinOption{ursus.JoinLeftOn(c("k32").Add(int32(1))), ursus.JoinRightOn(c("k64"))}, false},
		{"merged and kept apart, with a custom suffix", []ursus.JoinOption{ursus.JoinOn(c("k")), ursus.JoinCoalesce(false), ursus.JoinSuffix("_r")}, false},
		// The merged key is nullable from the left and not from the right, so the
		// swapped join's would not be: its schema differs, and the join stays.
		{"a merged key nullable on one side only: not swapped", []ursus.JoinOption{ursus.JoinLeftOn(c("k")), ursus.JoinRightOn(c("k64")), ursus.JoinCoalesce(true)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := func() *ursus.LazyFrame { return small().Join(big(), tc.opts...) }
			if got := swapped(t, q()); got == tc.noSwap {
				t.Fatalf("swapped = %v, want %v", got, !tc.noSwap)
			}
			want, err := q().Collect(t.Context(), ursus.WithOptFlags(noBuildSide()))
			if err != nil {
				t.Fatal(err)
			}
			got, err := q().Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			if !got.Schema().Equal(want.Schema()) {
				t.Fatalf("schema %s, want %s", got.Schema(), want.Schema())
			}
			if want.Height() == 0 {
				t.Fatal("the join matched nothing; this case tests nothing")
			}
			ursustest.AssertFrameEqual(t, sortedByAll(t, got), sortedByAll(t, want))
		})
	}
}

func TestBuildSideFiresOnlyWhereItShould(t *testing.T) {
	c := ursus.Col
	frame := func(n int) *ursus.LazyFrame {
		k := make([]int64, n)
		for i := range k {
			k[i] = int64(i % 5)
		}
		return ursus.Frame(ursus.Values("k", k), ursus.Values(fmt.Sprintf("v%d", n), k))
	}
	cases := []struct {
		name string
		lf   *ursus.LazyFrame
		want bool
	}{
		{"inner, the right ten times the left", frame(10).Join(frame(100), ursus.JoinOn(c("k"))), true},
		{"inner, the right twice the left: not enough", frame(10).Join(frame(20), ursus.JoinOn(c("k"))), false},
		{"inner, the right smaller", frame(100).Join(frame(10), ursus.JoinOn(c("k"))), false},
		{"a left join keeps its order", frame(10).Join(frame(100), ursus.JoinOn(c("k")), ursus.JoinHow(ursus.JoinLeft)), false},
		// A semi join's output is the left columns alone, so a swapped inner join
		// projected back would pass the schema check — and multiply every left row
		// by its matches. Only the kind check stops it, once the keys are named
		// differently: a merged key would fail the mapping first, and hide that.
		{"a semi join is not an inner join", frame(10).Join(frame(100), ursus.JoinLeftOn(c("k")), ursus.JoinRightOn(c("v100")), ursus.JoinHow(ursus.JoinSemi)), false},
		{"an anti join is not either", frame(10).Join(frame(100), ursus.JoinLeftOn(c("k")), ursus.JoinRightOn(c("v100")), ursus.JoinHow(ursus.JoinAnti)), false},
		{"JoinMaintainOrder", frame(10).Join(frame(100), ursus.JoinOn(c("k")), ursus.JoinMaintainOrder(true)), false},
		{"a validated join", frame(10).Join(frame(100), ursus.JoinOn(c("k")), ursus.JoinValidate(ursus.ValidateManyToMany)), false},
		{"through a filter, still an upper bound", frame(10).Join(frame(100).Filter(c("k").Gt(int64(3))), ursus.JoinOn(c("k"))), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := swapped(t, tc.lf); got != tc.want {
				t.Errorf("swapped = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestJoinMaintainOrderKeepsTheLeftOrder(t *testing.T) {
	c := ursus.Col
	left := ursus.Frame(ursus.Values("k", []int64{3, 1, 2, 3, 1}), ursus.Values("i", []int64{0, 1, 2, 3, 4}))
	k := make([]int64, 50)
	j := make([]int64, 50)
	for i := range 50 {
		k[i], j[i] = int64(4-i%4), int64(i)
	}
	right := ursus.Frame(ursus.Values("k", k), ursus.Values("j", j))

	want, err := left.Join(right, ursus.JoinOn(c("k"))).Collect(t.Context(), ursus.WithOptFlags(noBuildSide()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := left.Join(right, ursus.JoinOn(c("k")), ursus.JoinMaintainOrder(true)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)
}

// marked reports whether lf's optimized plan marks a join SwapAtRuntime (step 162).
func marked(t *testing.T, lf *ursus.LazyFrame) bool {
	t.Helper()
	p, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(p, "swap_at_runtime")
}

// TestBuildSideMarksWhatItCouldSwap: since step 162 the rule marks every join it
// could exchange, exchanged or not, so the physical join may exchange it by its
// inputs' actual sizes; and no join it could not.
func TestBuildSideMarksWhatItCouldSwap(t *testing.T) {
	c := ursus.Col
	frame := func(n int) *ursus.LazyFrame {
		k := make([]int64, n)
		for i := range k {
			k[i] = int64(i % 5)
		}
		return ursus.Frame(ursus.Values("k", k), ursus.Values(fmt.Sprintf("v%d", n), k))
	}
	floats := ursus.Frame(ursus.Values("k", []float64{1, 2}), ursus.Values("a", []int64{1, 2}))
	cases := []struct {
		name string
		lf   *ursus.LazyFrame
		want bool
	}{
		{"inner, exchanged", frame(10).Join(frame(100), ursus.JoinOn(c("k"))), true},
		{"inner, kept", frame(10).Join(frame(20), ursus.JoinOn(c("k"))), true},
		{"a left join", frame(10).Join(frame(100), ursus.JoinOn(c("k")), ursus.JoinHow(ursus.JoinLeft)), false},
		{"a semi join", frame(10).Join(frame(100), ursus.JoinLeftOn(c("k")), ursus.JoinRightOn(c("v100")), ursus.JoinHow(ursus.JoinSemi)), false},
		{"JoinMaintainOrder", frame(10).Join(frame(100), ursus.JoinOn(c("k")), ursus.JoinMaintainOrder(true)), false},
		{"a validated join", frame(10).Join(frame(100), ursus.JoinOn(c("k")), ursus.JoinValidate(ursus.ValidateManyToMany)), false},
		{"a sorted left input", frame(10).Sort(ursus.Asc(c("v10"))).Join(frame(20), ursus.JoinOn(c("k"))), false},
		{"a float key", floats.Join(floats, ursus.JoinOn(c("k"))), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := marked(t, tc.lf); got != tc.want {
				t.Errorf("marked = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestARuntimeSwapKeepsTheAnswer: q12's shape. The right input is estimated at four
// times the left, all of it, so the rule builds the left; its filter leaves a
// hundredth, so the physical join builds that instead. The answer is the join's as
// written, by threads and by whether the rule ran.
func TestARuntimeSwapKeepsTheAnswer(t *testing.T) {
	c := ursus.Col
	left := func() *ursus.LazyFrame {
		k := make([]int64, 10_000)
		v := make([]int64, 10_000)
		for i := range k {
			k[i], v[i] = int64(i), int64(i*3)
		}
		return ursus.Frame(ursus.Values("k", k), ursus.Values("v", v))
	}
	right := func() *ursus.LazyFrame {
		k := make([]int64, 40_000)
		w := make([]int64, 40_000)
		for i := range k {
			k[i], w[i] = int64(i%12_000), int64(i)
		}
		return ursus.Frame(ursus.Values("k", k), ursus.Values("w", w)).Filter(c("w").Lt(int64(400)))
	}
	q := func() *ursus.LazyFrame { return left().Join(right(), ursus.JoinOn(c("k"))) }
	if !swapped(t, q()) || !marked(t, q()) {
		t.Fatal("the fixture is not q12's shape: exchanged by the rule, and marked")
	}
	want, err := q().Collect(t.Context(), ursus.WithThreads(1), ursus.WithOptFlags(noBuildSide()))
	if err != nil {
		t.Fatal(err)
	}
	if want.Height() != 400 {
		t.Fatalf("the fixture joins %d rows, want 400", want.Height())
	}
	for _, threads := range []int{1, 8} {
		got, err := q().Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(1024))
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, sortedByAll(t, got), sortedByAll(t, want))
	}
}
