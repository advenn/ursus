package kernel_test

// Min and Max moved from one type-erased accumulator holding a *data.Column per
// group to four flat typed ones. Nothing about the ANSWERS was supposed to
// change, so these pin the behaviours that were easiest to lose in the move.

import (
	"math"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

// TestExtremumUsesTheTotalOrder is the one a `<` comparison would fail.
//
// order.go defines ursus's total order as -Inf < … < -0.0 == +0.0 < … < +Inf <
// NaN, with all NaNs equal, and Sort and GroupBy both use it. If Min/Max compared
// with plain `<` instead of through OrderKeyF64, every comparison against NaN
// would be false and `max` over a group containing one would return a number —
// disagreeing with `sort(x).last()` over the same data.
func TestExtremumUsesTheTotalOrder(t *testing.T) {
	nan := math.NaN()
	negZero := math.Copysign(0, -1)

	for _, c := range []struct {
		name                       string
		vals                       []float64
		wantMinIsNaN, wantMaxIsNaN bool
		wantMin, wantMax           float64
	}{
		{name: "NaN is above everything", vals: []float64{1, nan, -5},
			wantMin: -5, wantMaxIsNaN: true},
		{name: "all NaN", vals: []float64{nan, nan},
			wantMinIsNaN: true, wantMaxIsNaN: true},
		{name: "infinities", vals: []float64{math.Inf(1), 0, math.Inf(-1)},
			wantMin: math.Inf(-1), wantMax: math.Inf(1)},
		{name: "-0.0 equals +0.0", vals: []float64{negZero, 0},
			wantMin: 0, wantMax: 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			groups := make([]int32, len(c.vals))
			col := mkF64("v", c.vals, nil)

			minOut := run(t, expr.AggMin, dtype.Float64, col, groups, 1)
			maxOut := run(t, expr.AggMax, dtype.Float64, col, groups, 1)

			gotMin := f64At(t, minOut, 0)
			gotMax := f64At(t, maxOut, 0)

			if c.wantMinIsNaN {
				if !math.IsNaN(gotMin) {
					t.Errorf("min = %v, want NaN", gotMin)
				}
			} else if gotMin != c.wantMin {
				t.Errorf("min = %v, want %v", gotMin, c.wantMin)
			}

			if c.wantMaxIsNaN {
				if !math.IsNaN(gotMax) {
					t.Errorf("max = %v, want NaN", gotMax)
				}
			} else if gotMax != c.wantMax {
				t.Errorf("max = %v, want %v", gotMax, c.wantMax)
			}
		})
	}
}

// TestExtremumAgreesWithSort is the property the total order exists to provide.
//
// min(x) must equal the first element of sort(x), and max(x) the last, on data
// containing exactly the values that make the two definitions diverge.
func TestExtremumAgreesWithSort(t *testing.T) {
	vals := []float64{3, math.NaN(), -1, math.Inf(-1), 0, math.Copysign(0, -1), math.Inf(1)}
	col := mkF64("v", vals, nil)

	perm, err := kernel.ArgSortColumns([]*data.Column{col}, []kernel.SortSpec{{}}, len(vals))
	if err != nil {
		t.Fatal(err)
	}
	first, last := vals[perm[0]], vals[perm[len(perm)-1]]

	groups := make([]int32, len(vals))
	gotMin := f64At(t, run(t, expr.AggMin, dtype.Float64, col, groups, 1), 0)
	gotMax := f64At(t, run(t, expr.AggMax, dtype.Float64, col, groups, 1), 0)

	if !sameF64(gotMin, first) {
		t.Errorf("min = %v but sort(x)[0] = %v", gotMin, first)
	}
	if !sameF64(gotMax, last) {
		t.Errorf("max = %v but sort(x)[last] = %v", gotMax, last)
	}
}

// TestExtremumPreservesTheOutputDType: the accumulator stores the PHYSICAL type
// and publishes bind.Out.
//
// Getting this wrong makes the max of a Datetime come back as an integer tick
// count and the max of a Decimal come back off by a factor of 10^scale — both of
// which render as plausible numbers rather than as errors.
func TestExtremumPreservesTheOutputDType(t *testing.T) {
	for _, c := range []struct {
		name string
		dt   dtype.DataType
		col  *data.Column
	}{
		{"datetime", dtype.Datetime(dtype.Micro, "UTC"),
			data.NewFixed("v", dtype.Datetime(dtype.Micro, "UTC"),
				[]int64{300, 100, 200}, bitmap.AllSet(3))},
		{"date", dtype.Date,
			data.NewFixed("v", dtype.Date, []int32{30, 10, 20}, bitmap.AllSet(3))},
		{"duration", dtype.Duration(dtype.Nano),
			data.NewFixed("v", dtype.Duration(dtype.Nano),
				[]int64{30, 10, 20}, bitmap.AllSet(3))},
		{"decimal", dtype.Decimal(10, 2),
			data.NewFixed("v", dtype.Decimal(10, 2),
				[]i128.Int128{i128.FromInt64(300), i128.FromInt64(100)}, bitmap.AllSet(2))},
	} {
		t.Run(c.name, func(t *testing.T) {
			groups := make([]int32, c.col.Len())
			out := run(t, expr.AggMax, c.dt, c.col, groups, 1)
			if out.DType() != c.dt {
				t.Errorf("max of %s produced %s", c.dt, out.DType())
			}
		})
	}
}

// TestExtremumOverEveryPayloadShape: one accumulator per payload shape now, so
// each needs to be reached at least once.
func TestExtremumOverEveryPayloadShape(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		col := data.NewString("v", []string{"pear", "apple", "fig"}, bitmap.AllSet(3))
		groups := []int32{0, 0, 0}
		if got := strAt(t, run(t, expr.AggMin, dtype.String, col, groups, 1), 0); got != "apple" {
			t.Errorf("min = %q, want apple", got)
		}
		if got := strAt(t, run(t, expr.AggMax, dtype.String, col, groups, 1), 0); got != "pear" {
			t.Errorf("max = %q, want pear", got)
		}
	})

	t.Run("bool", func(t *testing.T) {
		bits := bitmap.NewBuilder(3)
		bits.Append(false)
		bits.Append(true)
		bits.Append(false)
		col := data.NewBool("v", bits.Finish(), bitmap.AllSet(3))
		groups := []int32{0, 0, 0}
		if got := boolAt(t, run(t, expr.AggMin, dtype.Bool, col, groups, 1), 0); got {
			t.Error("min over [false true false] = true, want false")
		}
		if got := boolAt(t, run(t, expr.AggMax, dtype.Bool, col, groups, 1), 0); !got {
			t.Error("max over [false true false] = false, want true")
		}
	})

	t.Run("uint64", func(t *testing.T) {
		col := data.NewFixed("v", dtype.Uint64, []uint64{7, 2, 9}, bitmap.AllSet(3))
		groups := []int32{0, 0, 0}
		out := run(t, expr.AggMin, dtype.Uint64, col, groups, 1)
		s, err := data.TypedColumn[uint64](out)
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := s.Get(0); v != 2 {
			t.Errorf("min = %d, want 2", v)
		}
	})

	t.Run("int8 negatives", func(t *testing.T) {
		// A sign-handling slip shows up here and nowhere else: widened wrongly,
		// -128 becomes the largest value rather than the smallest.
		col := data.NewFixed("v", dtype.Int8, []int8{-128, 0, 127}, bitmap.AllSet(3))
		groups := []int32{0, 0, 0}
		out := run(t, expr.AggMin, dtype.Int8, col, groups, 1)
		s, err := data.TypedColumn[int8](out)
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := s.Get(0); v != -128 {
			t.Errorf("min = %d, want -128", v)
		}
	})
}

// TestExtremumDoesNotPinTheBatchBuffer is the memory hazard the string
// accumulator has to avoid.
//
// data.StringAccessor.Get returns a string ALIASING the batch's character buffer
// — its own doc says so. Storing that alias as the running best would keep every
// batch that ever won alive for the whole aggregation. The accumulator clones on
// store, and this checks the stored bytes survive the source being overwritten.
func TestExtremumDoesNotPinTheBatchBuffer(t *testing.T) {
	col := data.NewString("v", []string{"zzz", "aaa"}, bitmap.AllSet(2))

	bind, err := expr.ResolveAggBinding(expr.AggMin, dtype.String)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := kernel.NewAccumulator(expr.AggMin, dtype.String, bind, expr.AggParams{})
	if err != nil {
		t.Fatal(err)
	}
	acc.Reserve(1)
	if err := acc.AddBatch([]int32{0, 0}, col); err != nil {
		t.Fatal(err)
	}

	// Scribble over the COLUMN'S OWN character buffer, which is what a reused
	// decode buffer does in the reader. RawChars is documented as "must not be
	// mutated"; mutating it is the entire point here, because it is the only way
	// to tell a stored copy from a stored alias from outside the package.
	//
	// An earlier version of this test built a fresh column from a []byte it then
	// overwrote — and passed with strings.Clone deleted, because NewStringParts
	// copies its input. That version proved nothing.
	raw := col.RawChars()
	for i := range raw {
		raw[i] = 'X'
	}

	out, err := acc.Finish("out", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := strAt(t, out, 0); got != "aaa" {
		t.Errorf("min = %q, want \"aaa\" — the accumulator aliased the batch buffer "+
			"instead of copying", got)
	}
}

// --- helpers ------------------------------------------------------------------

func f64At(t *testing.T, c *data.Column, i int) float64 {
	t.Helper()
	s, err := data.TypedColumn[float64](c)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := s.Get(i)
	if !ok {
		t.Fatalf("row %d is null", i)
	}
	return v
}

func strAt(t *testing.T, c *data.Column, i int) string {
	t.Helper()
	s, err := data.TypedColumn[string](c)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := s.Get(i)
	if !ok {
		t.Fatalf("row %d is null", i)
	}
	return v
}

func boolAt(t *testing.T, c *data.Column, i int) bool {
	t.Helper()
	s, err := data.TypedColumn[bool](c)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := s.Get(i)
	if !ok {
		t.Fatalf("row %d is null", i)
	}
	return v
}

// sameF64 treats NaN as equal to NaN, which is what the total order says.
func sameF64(a, b float64) bool {
	if math.IsNaN(a) && math.IsNaN(b) {
		return true
	}
	return a == b
}
