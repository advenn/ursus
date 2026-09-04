package kernel_test

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"ursus/dtype"
	"ursus/i128"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/kernel"
)

// mergeCol builds one key column of dt over n rows, with nulls every 7th row.
//
// Values repeat heavily on purpose: ties are where both the cross comparator and
// the merge break, and they are invisible on distinct data. ArgTopK needed two
// separate tie fixes for exactly this reason.
func mergeCol(rng *rand.Rand, dt dtype.DataType, name string, n int) *data.Column {
	valid := bitmap.NewBuilder(n)
	for i := range n {
		valid.Append(i%7 != 3)
	}
	v := valid.Finish()

	switch dt.ID() {
	case dtype.TypeBool:
		bits := bitmap.NewBuilder(n)
		for range n {
			bits.Append(rng.IntN(2) == 0)
		}
		return data.NewBool(name, bits.Finish(), v)
	case dtype.TypeString:
		vals := make([]string, n)
		for i := range vals {
			vals[i] = string(rune('a' + rng.IntN(4)))
		}
		return data.NewString(name, vals, v)
	case dtype.TypeInt32:
		vals := make([]int32, n)
		for i := range vals {
			vals[i] = int32(rng.IntN(5))
		}
		return data.NewFixed(name, dt, vals, v)
	case dtype.TypeInt64:
		vals := make([]int64, n)
		for i := range vals {
			vals[i] = int64(rng.IntN(5))
		}
		return data.NewFixed(name, dt, vals, v)
	case dtype.TypeUint64:
		vals := make([]uint64, n)
		for i := range vals {
			vals[i] = uint64(rng.IntN(5))
		}
		return data.NewFixed(name, dt, vals, v)
	case dtype.TypeFloat64:
		// NaN and -0.0 belong in the corpus: they are the two values where the
		// total order deliberately differs from IEEE, and the cross comparator has
		// to make the same choice the within-run one does.
		pool := []float64{0, math.Copysign(0, -1), 1, math.NaN(), math.Inf(1), -1}
		vals := make([]float64, n)
		for i := range vals {
			vals[i] = pool[rng.IntN(len(pool))]
		}
		return data.NewFixed(name, dt, vals, v)
	case dtype.TypeInt128:
		vals := make([]i128.Int128, n)
		for i := range vals {
			vals[i] = i128.FromInt64(int64(rng.IntN(5) - 2))
		}
		return data.NewFixed(name, dt, vals, v)
	default:
		panic("mergeCol has no case for " + dt.String())
	}
}

var mergeTypes = []dtype.DataType{
	dtype.Bool, dtype.String, dtype.Int32, dtype.Int64,
	dtype.Uint64, dtype.Float64, dtype.Int128,
}

func sign(x int) int {
	switch {
	case x < 0:
		return -1
	case x > 0:
		return 1
	default:
		return 0
	}
}

// TestCrossComparatorMatchesWithinRun is the differential test that licenses a
// second comparator existing at all.
//
// Comparing row ia of run A against row ib of run B through the cross comparator
// must give the same answer as NewComparator over the two runs CONCATENATED —
// across every dtype, both directions, and both null placements. Null placement
// applied before direction is the rule most likely to drift between two copies,
// and it is invisible unless the corpus carries nulls in both runs.
func TestCrossComparatorMatchesWithinRun(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 17))

	for _, dt := range mergeTypes {
		for _, desc := range []bool{false, true} {
			for _, nullsLast := range []bool{false, true} {
				spec := kernel.SortSpec{Descending: desc, NullsLast: nullsLast}
				const nA, nB = 23, 19

				a := mergeCol(rng, dt, "k", nA)
				b := mergeCol(rng, dt, "k", nB)

				cross, err := kernel.NewCrossComparator(
					[][]*data.Column{{a}, {b}}, []kernel.SortSpec{spec})
				if err != nil {
					t.Fatalf("%s: %v", dt, err)
				}

				joined := concatOne(t, dt, a, b)
				within, err := kernel.NewComparator(
					[]*data.Column{joined}, []kernel.SortSpec{spec})
				if err != nil {
					t.Fatalf("%s: %v", dt, err)
				}

				// Every pair, in every run combination — including A against A,
				// which must agree with the plain comparator too.
				runs := [][2]int{{0, 0}, {0, 1}, {1, 0}, {1, 1}}
				lens := [2]int{nA, nB}
				base := [2]int{0, nA}
				for _, rr := range runs {
					for i := range lens[rr[0]] {
						for j := range lens[rr[1]] {
							got := sign(cross(rr[0], i, rr[1], j))
							want := sign(within(base[rr[0]]+i, base[rr[1]]+j))
							if got != want {
								t.Fatalf("%s desc=%v nullsLast=%v: cross(%d,%d,%d,%d) = %d, "+
									"within-run says %d", dt, desc, nullsLast,
									rr[0], i, rr[1], j, got, want)
							}
						}
					}
				}
			}
		}
	}
}

// concatOne stacks two single-column runs into one column, so the within-run
// comparator can be asked about a pair that spans them.
func concatOne(t *testing.T, dt dtype.DataType, a, b *data.Column) *data.Column {
	t.Helper()
	sch, err := dtype.NewSchema(dtype.Of("k", dt))
	if err != nil {
		t.Fatal(err)
	}
	ba, err := data.NewBatch(sch, []*data.Column{a})
	if err != nil {
		t.Fatal(err)
	}
	bb, err := data.NewBatch(sch, []*data.Column{b})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := kernel.Concat(sch, []*data.Batch{ba, bb})
	if err != nil {
		t.Fatal(err)
	}
	return joined.Column(0)
}

// TestMergerMatchesArgSort is the merge core's whole contract, checked with no
// disk and no budget involved.
//
// Split an input into contiguous runs, sort each run stably, merge them: the
// result must be EXACTLY ArgSort over the whole input, ties included. That is a
// stronger claim than "sorted" — a merge that broke ties towards the later run
// still produces sorted output, and would silently reorder equal rows.
func TestMergerMatchesArgSort(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 5))

	for _, dt := range mergeTypes {
		for _, desc := range []bool{false, true} {
			for _, nullsLast := range []bool{false, true} {
				for _, nRuns := range []int{1, 2, 5} {
					spec := []kernel.SortSpec{{Descending: desc, NullsLast: nullsLast}}
					const n = 120
					whole := mergeCol(rng, dt, "k", n)

					cmp, err := kernel.NewComparator([]*data.Column{whole}, spec)
					if err != nil {
						t.Fatal(err)
					}
					want := kernel.ArgSort(n, cmp)

					// Contiguous runs, each sorted stably on its own — which is what
					// spilling produces, because a run is a sorted prefix of the input.
					bounds := make([]int, nRuns+1)
					for i := 1; i < nRuns; i++ {
						bounds[i] = i * n / nRuns
					}
					bounds[nRuns] = n

					runCols := make([][]*data.Column, nRuns)
					runRows := make([][]int32, nRuns) // local order -> global row
					for r := range nRuns {
						lo, hi := bounds[r], bounds[r+1]
						sub := make([]int32, hi-lo)
						for i := range sub {
							sub[i] = int32(lo + i)
						}
						part, err := kernel.Take(whole, sub)
						if err != nil {
							t.Fatal(err)
						}
						pc, err := kernel.NewComparator([]*data.Column{part}, spec)
						if err != nil {
							t.Fatal(err)
						}
						local := kernel.ArgSort(part.Len(), pc)
						sorted, err := kernel.Take(part, local)
						if err != nil {
							t.Fatal(err)
						}
						runCols[r] = []*data.Column{sorted}
						runRows[r] = make([]int32, len(local))
						for i, l := range local {
							runRows[r][i] = int32(lo) + l
						}
					}

					rs, err := kernel.NewRunSet(nRuns, []dtype.DataType{dt}, spec)
					if err != nil {
						t.Fatal(err)
					}
					for r := range nRuns {
						if err := rs.Set(r, runCols[r]); err != nil {
							t.Fatal(err)
						}
					}
					m := kernel.NewMerger(rs)
					pos := make([]int, nRuns)
					for r := range nRuns {
						if len(runRows[r]) > 0 {
							m.Push(r, 0)
						}
					}

					got := make([]int32, 0, n)
					for {
						c, ok := m.Pop()
						if !ok {
							break
						}
						got = append(got, runRows[c.Run][c.Row])
						pos[c.Run] = c.Row + 1
						if pos[c.Run] < len(runRows[c.Run]) {
							m.Push(c.Run, pos[c.Run])
						}
					}

					if !slices.Equal(got, want) {
						t.Fatalf("%s desc=%v nullsLast=%v runs=%d:\n got %v\nwant %v",
							dt, desc, nullsLast, nRuns, got, want)
					}
				}
			}
		}
	}
}

// TestMergerBreaksTiesByRun pins the stability rule directly, without relying on
// the differential test noticing it.
//
// Every key is identical, so nothing but the tie-break decides the order. It must
// be run 0 before run 1 before run 2, and within a run the rows in order — the
// exact shape "runs are created in input order" buys.
func TestMergerBreaksTiesByRun(t *testing.T) {
	const nRuns, perRun = 3, 4
	spec := []kernel.SortSpec{{}}

	runs := make([][]*data.Column, nRuns)
	for r := range runs {
		vals := make([]int64, perRun)
		runs[r] = []*data.Column{
			data.NewFixed("k", dtype.Int64, vals, bitmap.View{}),
		}
	}
	rs, err := kernel.NewRunSet(nRuns, []dtype.DataType{dtype.Int64}, spec)
	if err != nil {
		t.Fatal(err)
	}
	for r := range nRuns {
		if err := rs.Set(r, runs[r]); err != nil {
			t.Fatal(err)
		}
	}

	m := kernel.NewMerger(rs)
	for r := range nRuns {
		m.Push(r, 0)
	}
	var got []string
	pos := make([]int, nRuns)
	for {
		c, ok := m.Pop()
		if !ok {
			break
		}
		got = append(got, string(rune('A'+c.Run))+string(rune('0'+c.Row)))
		pos[c.Run] = c.Row + 1
		if pos[c.Run] < perRun {
			m.Push(c.Run, pos[c.Run])
		}
	}

	want := []string{
		"A0", "A1", "A2", "A3",
		"B0", "B1", "B2", "B3",
		"C0", "C1", "C2", "C3",
	}
	if !slices.Equal(got, want) {
		t.Errorf("all-equal keys merged as %v, want %v", got, want)
	}
}

// TestRunSetReplacesRunColumns: a run's resident batch is REPLACED when it is
// exhausted, and comparisons after the swap must read the new values. This is the
// property that makes the merge bounded, and a comparator that had closed over
// its columns would silently keep comparing the old batch.
func TestRunSetReplacesRunColumns(t *testing.T) {
	spec := []kernel.SortSpec{{}}
	rs, err := kernel.NewRunSet(2, []dtype.DataType{dtype.Int64}, spec)
	if err != nil {
		t.Fatal(err)
	}
	col := func(v int64) []*data.Column {
		return []*data.Column{
			data.NewFixed("k", dtype.Int64, []int64{v}, bitmap.View{}),
		}
	}
	if err := rs.Set(0, col(1)); err != nil {
		t.Fatal(err)
	}
	if err := rs.Set(1, col(2)); err != nil {
		t.Fatal(err)
	}
	if got := sign(rs.Compare(0, 0, 1, 0)); got != -1 {
		t.Fatalf("1 vs 2 = %d, want -1", got)
	}
	if err := rs.Set(0, col(9)); err != nil {
		t.Fatal(err)
	}
	if got := sign(rs.Compare(0, 0, 1, 0)); got != 1 {
		t.Errorf("after replacing run 0 with 9: 9 vs 2 = %d, want 1", got)
	}
}
