package kernel_test

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"ursus/dtype"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/kernel"
)

// --- OrderKey ----------------------------------------------------------------

// TestOrderKeyTotalOrder pins ursus's documented deviation from IEEE:
//
//	-Inf < … < -0.0 == +0.0 < … < +Inf < NaN, all NaNs equal
//
// Sorting needs a total order (IEEE's unordered NaN makes some sort algorithms
// non-terminating) and hash-grouping needs an equivalence relation (under IEEE
// every NaN would form its own group, so you could not count them).
func TestOrderKeyTotalOrder(t *testing.T) {
	// Deliberately in ascending order under the intended total order.
	ordered := []float64{
		math.Inf(-1),
		-math.MaxFloat64,
		-1e100, -1.5, -1.0, -math.SmallestNonzeroFloat64,
		math.Copysign(0, -1), // -0.0 …
		0.0,                  // … must be equal to +0.0
		math.SmallestNonzeroFloat64, 1.0, 1.5, 1e100,
		math.MaxFloat64,
		math.Inf(1),
		math.NaN(), // strictly above everything
	}

	for i := 0; i+1 < len(ordered); i++ {
		a, b := kernel.OrderKeyF64(ordered[i]), kernel.OrderKeyF64(ordered[i+1])
		isZeroPair := ordered[i] == 0 && ordered[i+1] == 0
		switch {
		case isZeroPair:
			if a != b {
				t.Errorf("-0.0 and +0.0 must share an order key, got %#x and %#x", a, b)
			}
		case a >= b:
			t.Errorf("OrderKeyF64 not monotone at %v -> %v: %#x >= %#x",
				ordered[i], ordered[i+1], a, b)
		}
	}

	// Every NaN maps to the same key, whatever its payload or sign.
	nans := []float64{
		math.NaN(),
		-math.NaN(),
		math.Float64frombits(0x7FF8000000000001),
		math.Float64frombits(0xFFF8000000000042),
	}
	want := kernel.OrderKeyF64(nans[0])
	for _, n := range nans[1:] {
		if got := kernel.OrderKeyF64(n); got != want {
			t.Errorf("NaN keys differ: %#x vs %#x", got, want)
		}
	}
	if want <= kernel.OrderKeyF64(math.Inf(1)) {
		t.Error("NaN must order above +Inf")
	}
}

func TestOrderKeyF32TotalOrder(t *testing.T) {
	ordered := []float32{
		float32(math.Inf(-1)), -math.MaxFloat32, -1.5, -1.0,
		float32(math.Copysign(0, -1)), 0.0,
		1.0, 1.5, math.MaxFloat32, float32(math.Inf(1)),
	}
	for i := 0; i+1 < len(ordered); i++ {
		a, b := kernel.OrderKeyF32(ordered[i]), kernel.OrderKeyF32(ordered[i+1])
		if ordered[i] == 0 && ordered[i+1] == 0 {
			if a != b {
				t.Errorf("±0.0 keys differ: %#x vs %#x", a, b)
			}
			continue
		}
		if a >= b {
			t.Errorf("not monotone at %v -> %v", ordered[i], ordered[i+1])
		}
	}
	if kernel.OrderKeyF32(float32(math.NaN())) <= kernel.OrderKeyF32(float32(math.Inf(1))) {
		t.Error("NaN must order above +Inf")
	}
}

// --- comparator --------------------------------------------------------------

func f64Col(name string, vals []float64, valid []bool) *data.Column {
	b := bitmap.NewBuilder(len(vals))
	for i := range vals {
		b.Append(valid == nil || valid[i])
	}
	return data.NewFixed(name, dtype.Float64, vals, b.Finish())
}

// TestComparatorNullPlacement checks the full {Asc,Desc} × {NullsFirst,NullsLast}
// matrix.
//
// The property under test is that NullsLast means last in the OUTPUT regardless of
// direction. Tying null placement to direction — as SQL does — would mean Desc()
// silently relocates the nulls, and the whole point of the separate flag is that it
// does not.
func TestComparatorNullPlacement(t *testing.T) {
	vals := []float64{2, 0, 1, 0}
	valid := []bool{true, false, true, false}
	col := f64Col("x", vals, valid)

	cases := []struct {
		desc, nullsLast bool
		want            []int32
	}{
		{false, false, []int32{1, 3, 2, 0}}, // nulls, then 1, 2
		{false, true, []int32{2, 0, 1, 3}},  // 1, 2, then nulls
		{true, false, []int32{1, 3, 0, 2}},  // nulls, then 2, 1
		{true, true, []int32{0, 2, 1, 3}},   // 2, 1, then nulls
	}

	for _, c := range cases {
		cmp, err := kernel.NewComparator(
			[]*data.Column{col},
			[]kernel.SortSpec{{Descending: c.desc, NullsLast: c.nullsLast}})
		if err != nil {
			t.Fatal(err)
		}
		got := kernel.ArgSort(len(vals), cmp)
		if !slices.Equal(got, c.want) {
			t.Errorf("desc=%v nullsLast=%v: got %v, want %v",
				c.desc, c.nullsLast, got, c.want)
		}
	}
}

// --- ArgTopK ≡ ArgSort[:k] ---------------------------------------------------

// TestArgTopKMatchesArgSort is the regression test for the defect that would have
// shipped.
//
// A bounded heap is not naturally stable, and two independent bugs made
// ArgTopK(n,k) disagree with ArgSort(n)[:k] — the equivalence that makes
// limit-pushdown (`Sort(...).Head(k)` → top-k) a legal rewrite rather than a silent
// change of which rows come back. Neither bug is visible without ties, and the
// optimizer's own Verify mode cannot catch it because Verify checks schemas, never
// rows.
//
// The values are drawn from a deliberately TINY alphabet so ties dominate.
func TestArgTopKMatchesArgSort(t *testing.T) {
	rng := rand.New(rand.NewPCG(20260829, 2))

	for _, n := range []int{0, 1, 2, 3, 5, 8, 13, 50, 200} {
		for _, alphabet := range []int{1, 2, 3, 5} {
			vals := make([]float64, n)
			valid := make([]bool, n)
			for i := range vals {
				vals[i] = float64(rng.IntN(alphabet))
				valid[i] = rng.IntN(5) != 0 // ~20% null
				if rng.IntN(11) == 0 {
					vals[i] = math.NaN()
				}
			}
			col := f64Col("x", vals, valid)

			for _, spec := range []kernel.SortSpec{
				{}, {Descending: true}, {NullsLast: true}, {Descending: true, NullsLast: true},
			} {
				cmp, err := kernel.NewComparator([]*data.Column{col}, []kernel.SortSpec{spec})
				if err != nil {
					t.Fatal(err)
				}
				full := kernel.ArgSort(n, cmp)

				for _, k := range []int{0, 1, 2, 3, n / 2, n - 1, n, n + 5} {
					if k < 0 {
						continue
					}
					got := kernel.ArgTopK(n, k, cmp)
					want := full[:min(max(k, 0), n)]
					if len(got) == 0 && len(want) == 0 {
						continue
					}
					if !slices.Equal(got, want) {
						t.Fatalf("n=%d k=%d alphabet=%d spec=%+v\n got  %v\n want %v\n vals %v",
							n, k, alphabet, spec, got, want, vals)
					}
				}
			}
		}
	}
}

// TestArgTopKTieCounterexamples pins the two exact inputs that exposed the bugs,
// so a regression fails with a three-row diff rather than a shrunk random case.
func TestArgTopKTieCounterexamples(t *testing.T) {
	check := func(vals []float64, k int) {
		t.Helper()
		col := f64Col("x", vals, nil)
		cmp, err := kernel.NewComparator([]*data.Column{col}, []kernel.SortSpec{{}})
		if err != nil {
			t.Fatal(err)
		}
		got := kernel.ArgTopK(len(vals), k, cmp)
		want := kernel.ArgSort(len(vals), cmp)[:k]
		if !slices.Equal(got, want) {
			t.Errorf("vals=%v k=%d: got %v, want %v", vals, k, got, want)
		}
	}
	// Eviction picked the wrong tied element: returned {2,1}, should be {2,0}.
	check([]float64{5, 5, 1, 5}, 2)
	// The final "stable" sort operated on heap order: returned {3,1,2}, should be {3,0,1}.
	check([]float64{7, 7, 7, 3}, 3)
}

// --- group keys --------------------------------------------------------------

// TestGroupKeyBiconditional is the property that matters for correctness of
// GROUP BY: encodings are equal IF AND ONLY IF the rows are equal under ursus's
// grouping equality.
//
// The forward direction catches collisions (two different keys landing in one
// group). The reverse direction catches UNDER-grouping — two equal keys landing in
// different groups — which nobody thinks to look for and which is exactly what
// happens if NaN or -0.0 reach the encoder without canonicalisation.
func TestGroupKeyBiconditional(t *testing.T) {
	strs := []string{"", "a", "b", "ab", "a\x00b", "\x00"}
	nums := []float64{0, math.Copysign(0, -1), 1, -1, math.NaN(), math.Inf(1)}

	type row struct {
		s      string
		sValid bool
		n      float64
		nValid bool
	}
	var rows []row
	for _, s := range strs {
		for _, sv := range []bool{true, false} {
			for _, n := range nums {
				for _, nv := range []bool{true, false} {
					rows = append(rows, row{s, sv, n, nv})
				}
			}
		}
	}

	sVals := make([]string, len(rows))
	sValid := make([]bool, len(rows))
	nVals := make([]float64, len(rows))
	nValid := make([]bool, len(rows))
	for i, r := range rows {
		sVals[i], sValid[i], nVals[i], nValid[i] = r.s, r.sValid, r.n, r.nValid
	}

	sb := bitmap.NewBuilder(len(rows))
	for _, v := range sValid {
		sb.Append(v)
	}
	nb := bitmap.NewBuilder(len(rows))
	for _, v := range nValid {
		nb.Append(v)
	}

	cols := []*data.Column{
		data.NewString("s", sVals, sb.Finish()),
		data.NewFixed("n", dtype.Float64, nVals, nb.Finish()),
	}
	enc, err := kernel.NewGroupKeyEncoder("test", cols)
	if err != nil {
		t.Fatal(err)
	}

	keys := make([]string, len(rows))
	for i := range rows {
		keys[i] = string(enc.Encode(i))
	}

	// groupEqual is ursus's grouping equality, stated independently of the encoder.
	groupEqual := func(a, b row) bool {
		if a.sValid != b.sValid || a.nValid != b.nValid {
			return false
		}
		if a.sValid && a.s != b.s {
			return false
		}
		if a.nValid {
			// NaN groups with NaN; -0.0 groups with +0.0. Both differ from IEEE ==.
			aNaN, bNaN := a.n != a.n, b.n != b.n
			if aNaN != bNaN {
				return false
			}
			if !aNaN && a.n != b.n {
				return false
			}
		}
		return true
	}

	for i := range rows {
		for j := range rows {
			wantEq := groupEqual(rows[i], rows[j])
			gotEq := keys[i] == keys[j]
			if gotEq != wantEq {
				verb := "collide"
				if wantEq {
					verb = "differ"
				}
				t.Fatalf("rows %+v and %+v must not %s\n key[i]=%q\n key[j]=%q",
					rows[i], rows[j], verb, keys[i], keys[j])
			}
		}
	}
}

// TestGroupKeyOrderPreserving checks the property the encoder documents and which
// a future sort-merge join or spill file would depend on: byte ordering of the
// encoding matches value ordering.
//
// This is what the unsigned sign-bit flip broke — grouping still worked, so nothing
// else would have noticed until the encoder was reused.
func TestGroupKeyOrderPreserving(t *testing.T) {
	t.Run("unsigned", func(t *testing.T) {
		vals := []uint8{0, 1, 100, 127, 128, 200, 255}
		col := data.NewFixed("u", dtype.Uint8, vals, bitmap.AllSet(len(vals)))
		assertKeysAscend(t, col, len(vals))
	})
	t.Run("signed", func(t *testing.T) {
		vals := []int8{-128, -100, -1, 0, 1, 100, 127}
		col := data.NewFixed("i", dtype.Int8, vals, bitmap.AllSet(len(vals)))
		assertKeysAscend(t, col, len(vals))
	})
	t.Run("signed 64-bit", func(t *testing.T) {
		vals := []int64{math.MinInt64, -1 << 40, -1, 0, 1, 1 << 40, math.MaxInt64}
		col := data.NewFixed("i", dtype.Int64, vals, bitmap.AllSet(len(vals)))
		assertKeysAscend(t, col, len(vals))
	})
	t.Run("float", func(t *testing.T) {
		vals := []float64{math.Inf(-1), -1e10, -1, 0, 1, 1e10, math.Inf(1)}
		col := data.NewFixed("f", dtype.Float64, vals, bitmap.AllSet(len(vals)))
		assertKeysAscend(t, col, len(vals))
	})
}

func assertKeysAscend(t *testing.T, col *data.Column, n int) {
	t.Helper()
	enc, err := kernel.NewGroupKeyEncoder("test", []*data.Column{col})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, n)
	for i := range n {
		keys[i] = string(enc.Encode(i))
	}
	for i := 0; i+1 < n; i++ {
		if keys[i] >= keys[i+1] {
			t.Errorf("row %d encodes >= row %d: %x vs %x", i, i+1, keys[i], keys[i+1])
		}
	}
}
