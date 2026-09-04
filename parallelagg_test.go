package ursus_test

// Parallel hash aggregation, end to end.
//
// internal/physical tests the merge in isolation — the remap, the key rows, the
// refusals. These are the other half: the same query at one thread and at many,
// through the real planner, over enough keys and batches that the round-robin
// dispatcher actually spreads them.

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source/memsrc"
	"github.com/advenn/ursus/ursustest"
)

// aggFrame is 4000 rows in 128-row BATCHES, and the batching is the whole point.
//
// WithBatchSize does not help here: memsrc serves the batches it was built from
// and never re-chunks, so a single-batch fixture hands everything to worker 0 and
// leaves every other sink empty — the merge then folds nothing and a missing remap
// cannot be observed. That is not hypothetical: the first version of this file was
// built with ursus.Values, and the teeth check for the remap passed with the remap
// deleted.
//
// 31 batches over 8 workers, and 97 keys scattered by a coprime multiplier so a
// key's rows land in many different batches — which is what makes each worker's
// partial table genuinely partial and its ids genuinely divergent.
func aggSource(t testing.TB) *memsrc.Source { return aggSourceN(t, 4000, 128, 97) }

func aggSourceN(t testing.TB, rows, batch, keys int) *memsrc.Source {
	t.Helper()
	schema, err := dtype.NewSchema(
		dtype.NotNull("k", dtype.Int64),
		dtype.NotNull("g", dtype.String),
		dtype.NotNull("v", dtype.Float64),
		dtype.NotNull("w", dtype.Int64),
	)
	if err != nil {
		t.Fatal(err)
	}

	var batches []*data.Batch
	for start := 0; start < rows; start += batch {
		n := min(batch, rows-start)
		k := make([]int64, n)
		g := make([]string, n)
		v := make([]float64, n)
		w := make([]int64, n)
		for i := range n {
			x := start + i
			k[i] = int64((x * 7919) % keys)
			g[i] = string(rune('a' + (x*13)%7))
			v[i] = float64((x*31)%1000) / 7
			w[i] = int64((x * 17) % 23)
		}
		b, err := data.NewBatch(schema, []*data.Column{
			data.NewFixed("k", dtype.Int64, k, bitmap.AllSet(n)),
			data.NewString("g", g, bitmap.AllSet(n)),
			data.NewFixed("v", dtype.Float64, v, bitmap.AllSet(n)),
			data.NewFixed("w", dtype.Int64, w, bitmap.AllSet(n)),
		})
		if err != nil {
			t.Fatal(err)
		}
		batches = append(batches, b)
	}
	src, err := memsrc.New(schema, batches...)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func serial() []ursus.CollectOption {
	return []ursus.CollectOption{ursus.WithThreads(1), ursus.WithVerify()}
}

func parallel(n int) []ursus.CollectOption {
	return []ursus.CollectOption{ursus.WithThreads(n), ursus.WithVerify()}
}

// TestParallelAggregationMatchesSerial is the central equivalence.
//
// Every aggregate family the h2o benchmark exercises, plus the ones with the most
// interesting merges: mean folds sum and count separately, std uses Chan's
// parallel variance update, median buffers every value and sorts in Finish, and
// n_unique folds sets.
func TestParallelAggregationMatchesSerial(t *testing.T) {
	byK := []ursus.SortKey{ursus.Asc(ursus.Col("k"))}
	byKG := []ursus.SortKey{ursus.Asc(ursus.Col("k")), ursus.Asc(ursus.Col("g"))}

	for _, c := range []struct {
		name string
		sort []ursus.SortKey
		q    func(lf *ursus.LazyFrame) *ursus.LazyFrame
	}{
		{"sum", byK, func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(ursus.Col("k")).Agg(ursus.Col("v").Sum().Alias("s"))
		}},
		{"two keys", byKG, func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(ursus.Col("k"), ursus.Col("g")).
				Agg(ursus.Col("v").Sum().Alias("s"))
		}},
		{"mean", byK, func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(ursus.Col("k")).Agg(ursus.Col("v").Mean().Alias("m"))
		}},
		{"min and max", byK, func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(ursus.Col("k")).Agg(
				ursus.Col("v").Min().Alias("lo"), ursus.Col("w").Max().Alias("hi"))
		}},
		{"count and len", byK, func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(ursus.Col("k")).Agg(
				ursus.Col("v").Count().Alias("c"), ursus.Len().Alias("n"))
		}},
		{"n_unique", byK, func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(ursus.Col("k")).Agg(ursus.Col("g").NUnique().Alias("u"))
		}},
		{"std and var", byK, func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(ursus.Col("k")).Agg(
				ursus.Col("v").Std(1).Alias("sd"), ursus.Col("v").Var(1).Alias("va"))
		}},
		{"median and quantile", byK, func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(ursus.Col("k")).Agg(
				ursus.Col("v").Median().Alias("md"),
				ursus.Col("v").Quantile(0.9, ursus.InterpLinear).Alias("q9"))
		}},
		{"compound expression over aggregates", byK, func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(ursus.Col("k")).Agg(
				ursus.Col("v").Sum().Div(ursus.Col("v").Count()).Alias("avg"))
		}},
		{"global aggregate, no keys", nil, func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			// The corner residentResult calls out: with no keys there is exactly one
			// group, so every worker's partial holds the SAME group 0 and the merge
			// is pure accumulator folding under an identity remap.
			return lf.GroupBy().Agg(ursus.Col("v").Sum().Alias("s"))
		}},
		{"filtered", byK, func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(ursus.Col("w").Gt(5)).
				GroupBy(ursus.Col("k")).Agg(ursus.Col("v").Sum().Alias("s"))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Sorting is what makes the comparison positional. Group ids are assigned
			// by first appearance, so the serial path emits groups in first-appearance
			// order as a side effect; under N workers each sink numbers only what IT
			// saw and the merge appends the groups a later worker introduced, so a
			// query whose groups span many batches comes out in a different order.
			// plan.Aggregate.MaintainOrder exists to pin exactly that, and its doc
			// already calls the order "genuinely unspecified" without it.
			q := func() *ursus.LazyFrame {
				lf := c.q(ursus.Scan(aggSource(t)))
				if len(c.sort) > 0 {
					lf = lf.Sort(c.sort...)
				}
				return lf
			}

			one, err := q().Collect(t.Context(), serial()...)
			if err != nil {
				t.Fatal(err)
			}
			many, err := q().Collect(t.Context(), parallel(8)...)
			if err != nil {
				t.Fatal(err)
			}
			// WithTolerance, because floating-point addition is not associative and a
			// parallel sum adds the same values in a different order — 1047 against
			// 1047.0000000000002. Every parallel engine does this. The values still
			// have to agree to 1e-9 relative, which no mis-remapped fold would.
			ursustest.AssertFrameEqual(t, many, one, ursustest.WithTolerance(0, 1e-9))
		})
	}
}

// TestParallelAggregationIsDeterministic guards the property the equivalence test
// above can only sample.
//
// hashAggSink.Merge visits the other sink's keys in ITS id order rather than by
// ranging its map. Ranging would still produce correct totals — every key ends up
// with the right value — but the group ordering would differ run to run, turning a
// deterministic output into a random one. One comparison cannot see that; repeated
// runs can.
func TestParallelAggregationIsDeterministic(t *testing.T) {
	q := func() *ursus.LazyFrame {
		return ursus.Scan(aggSource(t)).GroupBy(ursus.Col("k")).Agg(ursus.Col("v").Sum().Alias("s"))
	}
	first, err := q().Collect(t.Context(), parallel(8)...)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		got, err := q().Collect(t.Context(), parallel(8)...)
		if err != nil {
			t.Fatal(err)
		}
		if got.String() != first.String() {
			t.Fatalf("run %d produced a different group order:\n%s\nfirst:\n%s",
				i, got.String(), first.String())
		}
	}
}

// TestOrderDependentAggregatesStaySerial pins the order gate.
//
// These four cannot be merged from interleaved portions: First and Last name a
// position outright, and ArgMin/ArgMax report an index that their Merge shifts by
// a row count it assumes came earlier. The assertion is on the ANSWER at several
// thread counts, which is what a user would notice, and the gate is what keeps it
// stable.
func TestOrderDependentAggregatesStaySerial(t *testing.T) {
	for _, c := range []struct {
		name string
		agg  ursus.Expr
	}{
		{"first", ursus.Col("v").First().Alias("a")},
		{"last", ursus.Col("v").Last().Alias("a")},
		{"arg_min", ursus.Col("v").ArgMin().Alias("a")},
		{"arg_max", ursus.Col("v").ArgMax().Alias("a")},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := func() *ursus.LazyFrame {
				return ursus.Scan(aggSource(t)).GroupBy(ursus.Col("k")).Agg(c.agg)
			}
			one, err := q().Collect(t.Context(), serial()...)
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range []int{2, 4, 8} {
				many, err := q().Collect(t.Context(), parallel(n)...)
				if err != nil {
					t.Fatal(err)
				}
				ursustest.AssertFrameEqual(t, many, one)
			}
		})
	}
}

// TestMaintainOrderStaysSerial: firstSeen holds per-sink input ordinals, and two
// sinks that saw interleaved portions numbered their rows independently. The
// aggregate falls back rather than inventing a reconciliation.
func TestMaintainOrderStaysSerial(t *testing.T) {
	q := func() *ursus.LazyFrame {
		return ursus.Scan(aggSource(t)).GroupBy(ursus.Col("k")).MaintainOrder().
			Agg(ursus.Col("v").Sum().Alias("s"))
	}
	one, err := q().Collect(t.Context(), serial()...)
	if err != nil {
		t.Fatal(err)
	}
	many, err := q().Collect(t.Context(), parallel(8)...)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, many, one)
}

// TestMemoryLimitDisablesParallelAggregation is the spill gate.
//
// A hashAggSink refuses to Merge once it has frozen or spilled, and N workers
// splitting one budget make spilling likelier rather than less likely — so a limit
// would turn a slow query into a failed one. Under a limit the aggregate keeps
// exactly the serial path steps 10 and 12 built.
func TestMemoryLimitDisablesParallelAggregation(t *testing.T) {
	q := func() *ursus.LazyFrame {
		return ursus.Scan(aggSource(t)).GroupBy(ursus.Col("k"), ursus.Col("g")).
			Agg(ursus.Col("v").Sum().Alias("s"))
	}
	want, err := q().Collect(t.Context(), serial()...)
	if err != nil {
		t.Fatal(err)
	}
	// 16KB is tight enough that the sinks actually SPILL, which is what makes this
	// a real test rather than a formality: with the gate removed the query fails
	// outright with "cannot merge hashAggSinks that have spilled", not merely with
	// a different float rounding. Measured — at 64KB the workers stay resident and
	// the merge succeeds, so a looser limit would prove much less.
	got, err := q().Collect(t.Context(), ursus.WithThreads(8),
		ursus.WithMemoryLimit(16<<10), ursus.WithSpillDir(t.TempDir()))
	if err != nil {
		t.Fatalf("a memory-limited parallel aggregation must fall back, not fail: %v", err)
	}
	ursustest.AssertFrameEqual(t, got, want)
}

// TestParallelAggregationIsNotASemanticKnob: thread count is not a semantic knob,
// checked across every shape at once rather than per aggregate.
func TestParallelAggregationIsNotASemanticKnob(t *testing.T) {
	q := func() *ursus.LazyFrame {
		return ursus.Scan(aggSource(t)).
			Filter(ursus.Col("w").Lt(20)).
			GroupBy(ursus.Col("g")).
			Agg(
				ursus.Col("v").Sum().Alias("s"),
				ursus.Col("v").Mean().Alias("m"),
				ursus.Col("k").Max().Alias("hi"),
				ursus.Len().Alias("n"),
			).
			Sort(ursus.Asc(ursus.Col("g")))
	}
	want, err := q().Collect(t.Context(), serial()...)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{2, 3, 5, 8, 16} {
		got, err := q().Collect(t.Context(), parallel(n)...)
		if err != nil {
			t.Fatalf("threads=%d: %v", n, err)
		}
		// Tolerance for the same reason as the equivalence test: a parallel float
		// sum reassociates. The integer columns (hi, n) are still compared exactly.
		ursustest.AssertFrameEqual(t, got, want, ursustest.WithTolerance(0, 1e-9))
	}
}

// TestParallelAggregationExplainIsUnchanged: the driver is a physical-plan choice,
// so it must not leak into the logical plan a user is shown.
func TestParallelAggregationExplainIsUnchanged(t *testing.T) {
	lf := ursus.Scan(aggSource(t)).GroupBy(ursus.Col("k")).
		Agg(ursus.Col("v").Sum().Alias("s"))
	txt, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(txt, "AGGREGATE") {
		t.Errorf("plan should still describe an AGGREGATE:\n%s", txt)
	}
}

// TestOptimizerFlagsDoNotAffectParallelAggregation: the two features landed one
// step apart and both touch the aggregate, so they are checked together once.
func TestOptimizerFlagsDoNotAffectParallelAggregation(t *testing.T) {
	q := func() *ursus.LazyFrame {
		return ursus.Scan(aggSource(t)).GroupBy(ursus.Col("k")).Agg(ursus.Col("v").Sum().Alias("s"))
	}
	want, err := q().Collect(t.Context(), serial()...)
	if err != nil {
		t.Fatal(err)
	}
	got, err := q().Collect(t.Context(), ursus.WithThreads(8),
		ursus.WithOptFlags(plan.NoFlags()))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want, ursustest.WithTolerance(0, 1e-9))
}

// BenchmarkParallelAggregation is the step's own before/after.
//
// Shaped like h2o gb1 — one key column, one sum — at two cardinalities, because
// that is what decides whether the merge is cheap or the dominant cost: the fold
// is O(groups) per worker, so a low-cardinality group-by parallelises almost
// perfectly and a high-cardinality one pays for N partial tables.
func BenchmarkParallelAggregation(b *testing.B) {
	for _, keys := range []int{100, 10_000} {
		src := aggSourceN(b, 2_000_000, 8192, keys)
		for _, threads := range []int{1, 2, 4, 8} {
			b.Run(fmtBench(keys, threads), func(b *testing.B) {
				for b.Loop() {
					_, err := ursus.Scan(src).
						GroupBy(ursus.Col("k")).
						Agg(ursus.Col("v").Sum().Alias("s")).
						Collect(b.Context(), ursus.WithThreads(threads))
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func fmtBench(keys, threads int) string {
	return "keys=" + itoaBench(keys) + "/threads=" + itoaBench(threads)
}

func itoaBench(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
