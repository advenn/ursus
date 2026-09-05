package kernel_test

// The radix sort's entire correctness argument is that it agrees with the
// comparator path it replaces — including tie order, which ArgSort's doc calls
// load-bearing three times over. So the test is a differential rather than a set
// of expected permutations: every radix-able dtype, every direction, every null
// placement, checked row for row against NewComparator + ArgSort.

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

// column builds a column of T with the given validity, using the physical type
// the caller names so temporal and Enum punning is covered too.
func fixedCol[T data.Fixed](name string, dt dtype.DataType, vals []T, valid []bool) *data.Column {
	vb := bitmap.NewBuilder(len(vals))
	for _, v := range valid {
		vb.Append(v)
	}
	return data.NewFixed(name, dt, vals, vb.Finish())
}

func allValid(n int) []bool {
	out := make([]bool, n)
	for i := range out {
		out[i] = true
	}
	return out
}

// someNull marks every third row null, so every column in a multi-key test has a
// different null pattern and the passes cannot accidentally agree.
func someNull(n, stride int) []bool {
	out := make([]bool, n)
	for i := range out {
		out[i] = (i+1)%stride != 0
	}
	return out
}

// TestRadixMatchesComparatorSort is the differential.
//
// Values are drawn with heavy repetition on purpose: ties are where a stable
// sort can differ from an unstable one, and a fixture of distinct values would
// pass with a completely unstable implementation.
func TestRadixMatchesComparatorSort(t *testing.T) {
	const n = 500
	rng := rand.New(rand.NewPCG(9, 17))

	// Small value ranges → many ties.
	i64 := make([]int64, n)
	i32 := make([]int32, n)
	u64 := make([]uint64, n)
	u8 := make([]uint8, n)
	f64 := make([]float64, n)
	f32 := make([]float32, n)
	for i := range n {
		i64[i] = int64(rng.IntN(11)) - 5 // negatives, to catch a missing sign flip
		i32[i] = int32(rng.IntN(7)) - 3
		u64[i] = uint64(rng.IntN(9))
		u8[i] = uint8(rng.IntN(5))
		f64[i] = float64(rng.IntN(9)) - 4
		f32[i] = float32(rng.IntN(9)) - 4
	}
	// The float total order's three special cases, which OrderKeyF64 exists for.
	f64[0], f64[1], f64[2] = math.NaN(), math.Inf(-1), math.Inf(1)
	f64[3], f64[4] = 0, math.Copysign(0, -1)

	bools := bitmap.NewBuilder(n)
	for range n {
		bools.Append(rng.IntN(2) == 0)
	}
	boolCol := data.NewBool("b", bools.Finish(), bitmap.AllSet(n))

	cols := map[string]*data.Column{
		"int64":    fixedCol("i64", dtype.Int64, i64, allValid(n)),
		"int32":    fixedCol("i32", dtype.Int32, i32, allValid(n)),
		"uint64":   fixedCol("u64", dtype.Uint64, u64, allValid(n)),
		"uint8":    fixedCol("u8", dtype.Uint8, u8, allValid(n)),
		"float64":  fixedCol("f64", dtype.Float64, f64, allValid(n)),
		"float32":  fixedCol("f32", dtype.Float32, f32, allValid(n)),
		"bool":     boolCol,
		"datetime": fixedCol("ts", dtype.Datetime(dtype.Micro, "UTC"), i64, allValid(n)),
		"date":     fixedCol("d", dtype.Date, i32, allValid(n)),

		"int64 with nulls":   fixedCol("i64n", dtype.Int64, i64, someNull(n, 3)),
		"float64 with nulls": fixedCol("f64n", dtype.Float64, f64, someNull(n, 4)),
		"uint8 with nulls":   fixedCol("u8n", dtype.Uint8, u8, someNull(n, 5)),
	}

	for name, col := range cols {
		for _, desc := range []bool{false, true} {
			for _, nullsLast := range []bool{false, true} {
				spec := kernel.SortSpec{Descending: desc, NullsLast: nullsLast}
				t.Run(name, func(t *testing.T) {
					assertAgrees(t, []*data.Column{col}, []kernel.SortSpec{spec}, n)
				})
			}
		}
	}

	// Multi-column: this is what windowSink.segments does on every window query,
	// and the LSD-over-columns order is only observable with two or more keys.
	t.Run("two keys", func(t *testing.T) {
		assertAgrees(t, []*data.Column{cols["int32"], cols["float64"]},
			[]kernel.SortSpec{{}, {Descending: true}}, n)
	})
	t.Run("two keys, both with nulls", func(t *testing.T) {
		assertAgrees(t, []*data.Column{cols["int64 with nulls"], cols["float64 with nulls"]},
			[]kernel.SortSpec{{NullsLast: true}, {Descending: true}}, n)
	})
	t.Run("three keys, mixed directions", func(t *testing.T) {
		assertAgrees(t,
			[]*data.Column{cols["uint8"], cols["bool"], cols["int64 with nulls"]},
			[]kernel.SortSpec{{Descending: true}, {}, {NullsLast: true, Descending: true}}, n)
	})
	t.Run("low-cardinality key exercises the uniform-byte skip", func(t *testing.T) {
		// u8 uses one byte, so seven of the eight rounds must be skipped and the
		// answer must still be right.
		assertAgrees(t, []*data.Column{cols["uint8"]}, []kernel.SortSpec{{}}, n)
	})
}

// TestSortingAnAllNullColumnStillRefuses pins behaviour the fast path must NOT
// change.
//
// An all-null column is payload-free — data.NewNull carries no buffer at all —
// and NewComparator has always refused it with "has no fixed-width payload". A
// radix sort could trivially handle it (every row is null, so no key is ever
// read), and making it do so would be a silent capability change smuggled in
// under a performance step: the same query would stop erroring depending on which
// sort path its key types happened to select. So radixable declines it and the
// error is unchanged.
//
// Whether refusing is the RIGHT answer is a real question, and not this step's.
func TestSortingAnAllNullColumnStillRefuses(t *testing.T) {
	const n = 16
	col := data.NewNull("x", dtype.Float64, n)

	_, err := kernel.ArgSortColumns([]*data.Column{col}, []kernel.SortSpec{{}}, n)
	if err == nil {
		t.Fatal("sorting an all-null column must keep refusing, not silently start working")
	}
	if _, cmpErr := kernel.NewComparator([]*data.Column{col}, []kernel.SortSpec{{}}); cmpErr == nil {
		t.Fatal("the comparator path is supposed to be the one that refuses")
	}
}

// TestRadixFallsBackForUnorderableKeys: String and Int128 have no fixed-width
// order-preserving encoding, so they take the comparator path. The answer must be
// the same one they gave before the fast path existed.
func TestRadixFallsBackForUnorderableKeys(t *testing.T) {
	const n = 200
	rng := rand.New(rand.NewPCG(4, 5))

	words := []string{"pear", "apple", "fig", "apple", "date", ""}
	s := make([]string, n)
	for i := range n {
		s[i] = words[rng.IntN(len(words))]
	}
	strCol := data.NewString("s", s, bitmap.AllSet(n))

	nums := make([]int64, n)
	for i := range n {
		nums[i] = int64(rng.IntN(7))
	}
	numCol := fixedCol("n", dtype.Int64, nums, allValid(n))

	t.Run("string alone", func(t *testing.T) {
		assertAgrees(t, []*data.Column{strCol}, []kernel.SortSpec{{Descending: true}}, n)
	})
	// A MIXED set falls back wholesale: an LSD pass has to be a stable sort of the
	// whole array by one column, and a comparator pass interleaved with radix
	// passes would not compose.
	t.Run("mixed with a numeric key", func(t *testing.T) {
		assertAgrees(t, []*data.Column{numCol, strCol},
			[]kernel.SortSpec{{}, {NullsLast: true}}, n)
	})
}

// TestRadixSkippedRoundsKeepKeysAligned is the hazard the key-carrying rewrite
// introduced, and the only one.
//
// radixByKey gathers the keys into permutation order ONCE and then swaps its key
// buffer alongside its permutation buffer on every round that runs. A round that
// is SKIPPED — because that byte takes one value everywhere, which is the
// optimisation that makes a narrow key cheap — must not swap either. Swapping one
// and not the other leaves the keys one permutation behind, and every later round
// then reads another row's key.
//
// A uniform byte in the MIDDLE is what exposes it: rounds run on both sides of it,
// so the desynchronisation has somewhere to show up. Byte 1 here is always zero
// while bytes 0 and 2 vary.
func TestRadixSkippedRoundsKeepKeysAligned(t *testing.T) {
	const n = 400
	rng := rand.New(rand.NewPCG(3, 9))
	vals := make([]uint64, n)
	for i := range vals {
		vals[i] = uint64(rng.IntN(200)) | uint64(rng.IntN(200))<<16
	}
	col := fixedCol("u", dtype.Uint64, vals, allValid(n))

	// Descending too: inverting every bit moves which byte is the uniform one, so
	// the two directions skip different rounds.
	for _, desc := range []bool{false, true} {
		assertAgrees(t, []*data.Column{col},
			[]kernel.SortSpec{{Descending: desc}}, n)
	}
}

// TestRadixEveryRoundSkipped: a constant key sorts nothing, so the gather is the
// only thing that runs and the permutation must come back exactly as it went in.
// That is the stability claim in its purest form.
func TestRadixEveryRoundSkipped(t *testing.T) {
	const n = 100
	col := fixedCol("z", dtype.Int64, make([]int64, n), allValid(n))

	got, err := kernel.ArgSortColumns([]*data.Column{col}, []kernel.SortSpec{{}}, n)
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		if got[i] != int32(i) {
			t.Fatalf("a constant key must leave the permutation identical, "+
				"but got[%d] = %d", i, got[i])
		}
	}
}

// TestRadixTinyInputs covers the sizes that return before the gather loop, and the
// smallest one that reaches it.
func TestRadixTinyInputs(t *testing.T) {
	for _, n := range []int{1, 2, 3} {
		vals := make([]int64, n)
		for i := range vals {
			vals[i] = int64(n - i) // reversed, so n >= 2 actually has work to do
		}
		col := fixedCol("v", dtype.Int64, vals, allValid(n))
		assertAgrees(t, []*data.Column{col}, []kernel.SortSpec{{}}, n)
	}
}

// TestRadixNullPartitionSubset: sortPass hands radixByKey r.nonNull, which is a
// SUBSET of the permutation. The gather must index that subset BY POSITION —
// ka[i], not ka[row] — and a mostly-null column whose surviving values need many
// rounds is what tells the two apart.
func TestRadixNullPartitionSubset(t *testing.T) {
	const n = 500
	rng := rand.New(rand.NewPCG(5, 13))
	vals := make([]int64, n)
	valid := make([]bool, n)
	for i := range n {
		vals[i] = rng.Int64N(1 << 40) // wide enough to need six rounds
		valid[i] = i%7 == 0           // ~14% present, so the subset is far from all
	}
	col := fixedCol("sparse", dtype.Int64, vals, valid)

	for _, nullsLast := range []bool{false, true} {
		assertAgrees(t, []*data.Column{col},
			[]kernel.SortSpec{{NullsLast: nullsLast}}, n)
	}
	// And with a second key, so the subset pass is not the first thing to run.
	dense := fixedCol("d", dtype.Int32, make([]int32, n), allValid(n))
	assertAgrees(t, []*data.Column{dense, col},
		[]kernel.SortSpec{{}, {NullsLast: true}}, n)
}

// assertAgrees is the whole point: ArgSortColumns and the comparator path must
// produce IDENTICAL permutations, not merely equivalent orderings. Comparing the
// permutations rather than the sorted values is what makes tie order observable.
func assertAgrees(t *testing.T, cols []*data.Column, specs []kernel.SortSpec, n int) {
	t.Helper()

	got, err := kernel.ArgSortColumns(cols, specs, n)
	if err != nil {
		t.Fatalf("ArgSortColumns: %v", err)
	}

	cmp, err := kernel.NewComparator(cols, specs)
	if err != nil {
		t.Fatalf("NewComparator: %v", err)
	}
	want := kernel.ArgSort(n, cmp)

	if !slices.Equal(got, want) {
		// Report the first divergence with its neighbourhood; a 500-element diff
		// is unreadable and the first one is always the informative one.
		for i := range want {
			if got[i] != want[i] {
				lo := max(0, i-3)
				hi := min(len(want), i+4)
				t.Fatalf("permutations diverge at %d\n got:  %v\n want: %v",
					i, got[lo:hi], want[lo:hi])
			}
		}
		t.Fatalf("permutations differ in length: %d vs %d", len(got), len(want))
	}
}
