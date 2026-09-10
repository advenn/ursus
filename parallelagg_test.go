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
//
// # It was blind in two independent ways, and fixing one was not enough
//
// FIRST, the assertion. It was `got.String() != first.String()`, and
// DataFrame.String renders `min(Height(), 10)` rows — so it compared the first ten
// groups, twelve times. This repository has been bitten by that cap twice before;
// join_test.go and parallel_test.go each carry a comment about it, and each fix
// stopped at the line that had failed.
//
// SECOND, and worse, the FIXTURE could not produce the condition the property
// depends on. aggSource has 97 keys in 4000 rows; eight sinks take about 512 rows
// each, which is ample to see all 97. Every key is therefore already present when
// Merge runs, GetOrInsert never inserts, and the visit order it is so careful about
// cannot affect anything. Shuffling that loop changed no output at all.
//
// It takes MORE KEYS THAN ONE SINK CAN SEE for the merge to introduce a group —
// 2000 keys over 4000 rows, so each sink holds at most ~512 of them. Then shuffling
// the visit order does reorder the output, and it first differs at ROW 352: past the
// ten String() renders, which is how the two defects hid behind each other.
//
// Row order is checked by default, which is the entire point here.
func TestParallelAggregationIsDeterministic(t *testing.T) {
	src := aggSourceN(t, 4000, 128, 2000)
	q := func() *ursus.LazyFrame {
		return ursus.Scan(src).GroupBy(ursus.Col("k")).Agg(ursus.Col("v").Sum().Alias("s"))
	}
	first, err := q().Collect(t.Context(), parallel(8)...)
	if err != nil {
		t.Fatal(err)
	}
	// Anti-vacuity, and not decorative: with too few keys every sink sees every
	// key, the merge inserts nothing, and this test silently stops testing. The
	// quantity that matters is keys against one SINK's share of the rows.
	if perSink := 4000 / 8; first.Height() < perSink {
		t.Fatalf("the fixture has %d groups, fewer than one sink's %d rows — so "+
			"every sink sees every key, Merge never inserts one, and the ordering "+
			"this test is named for cannot vary", first.Height(), perSink)
	}

	for range 12 {
		got, err := q().Collect(t.Context(), parallel(8)...)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, first, ursustest.CheckNullability())
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

// TestNUniqueMergeRemapsGroups reaches a code path nothing else does.
//
// TestParallelAggregationMatchesSerial covers n_unique at every thread count and
// never calls nuniqueAcc.Merge once: its frame is small enough that the work lands
// in a single sink, so there is nothing to fold. Instrumenting Merge to print on
// entry produced no output for the whole of that test — while the same method is 28%
// of PDS-H q21's heap profile.
//
// This fixture does reach it, seven times, with a NON-IDENTITY remap: 200,000 rows
// over 5,000 groups at a batch size of 512 gives the scheduler enough batches to
// spread across sinks, and two sinks that saw different keys first number their
// groups differently. Without the remap the folded counts land in the wrong groups.
//
// It only became reachable in step 37. Before that memsrc handed back one batch
// however large, so an in-memory frame produced one sink and the merge never ran.
//
// The values overlap across groups on purpose — `g` cycles independently of `k` —
// because a fixture where each group holds its own private values would agree
// whether or not the group survives the fold.
func TestNUniqueMergeRemapsGroups(t *testing.T) {
	const (
		rows   = 200_000
		groups = 5_000
		vals   = 37
	)
	k := make([]string, rows)
	g := make([]int64, rows)
	for i := range rows {
		k[i] = "k" + itoa64(int64(i%groups))
		g[i] = int64(i % vals)
	}
	frame := func() *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("k", k), ursus.Values("g", g)).
			GroupBy(ursus.Col("k")).
			Agg(ursus.Col("g").NUnique().Alias("u")).
			Sort(ursus.Asc(ursus.Col("k")))
	}

	want, err := frame().Collect(t.Context(),
		append(serial(), ursus.WithBatchSize(512))...)
	if err != nil {
		t.Fatal(err)
	}
	if want.Height() != groups {
		t.Fatalf("got %d groups, want %d", want.Height(), groups)
	}

	for _, n := range []int{2, 4, 8} {
		got, err := frame().Collect(t.Context(),
			append(parallel(n), ursus.WithBatchSize(512))...)
		if err != nil {
			t.Fatalf("threads=%d: %v", n, err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}
}

// TestParallelBooleanAggregatesMatchSerial reaches extremumBool.Merge, which had
// ZERO coverage — measured with cross-package attribution, so not an artifact —
// while being reachable on every parallel group-by by default.
//
// It is not order-dependent, so aggWorkers does not gate it, and threads default to
// runtime.NumCPU(). The reason nothing reached it is the FIXTURE: aggSourceN has
// k(Int64), g(String), v(Float64), w(Int64) and no Bool column at all, and
// TestParallelAggregationMatchesSerial scopes itself to "every aggregate family the
// h2o benchmark exercises" — h2o has no boolean aggregate.
//
// That is exactly the configuration reverseSink.Merge sat in for thirty-four steps:
// reachable in production, non-trivial, never executed. Here the answer is the happy
// one — the merge is correct — but it was unverified rather than verified, and those
// look identical from the outside.
//
// Deriving the Bool columns with WithColumns rather than widening the shared fixture
// keeps every other test in this file on the data it was written for.
func TestParallelBooleanAggregatesMatchSerial(t *testing.T) {
	src := aggSource(t)

	// Two derived predicates, chosen so every asserted column VARIES across the 97
	// groups. The first draft of this test used `v > 0.5` for everything, and four
	// of its five subtests were vacuous: Any was true for every group, so an OR
	// fold and an AND fold agreed and the case proved nothing. The anti-vacuity
	// assertion below found that immediately, which is why it is here.
	//
	//	mostly = v > 0.5   true for ~99.6% of rows, so a group is all-true unless
	//	                   it contains one of the rare small values — which makes
	//	                   AllTrue and Min vary.
	//	rare   = v < 1.0   true for ~0.7% of rows, so most groups contain none —
	//	                   which makes Any and Max vary.
	//
	// Asserting Any on `mostly`, or AllTrue on `rare`, yields a constant column and
	// is precisely the mistake the first draft made.
	for _, c := range []struct {
		name string
		agg  func() []ursus.Expr
	}{
		{"any over a rare predicate", func() []ursus.Expr {
			return []ursus.Expr{ursus.Col("rare").Any().Alias("a")}
		}},
		{"all_true over a common predicate", func() []ursus.Expr {
			return []ursus.Expr{ursus.Col("mostly").AllTrue().Alias("t")}
		}},
		{"min and max over Bool", func() []ursus.Expr {
			// Min saturates at false like AllTrue, Max at true like Any, so each
			// needs the predicate whose fold it mirrors.
			return []ursus.Expr{
				ursus.Col("mostly").Min().Alias("lo"),
				ursus.Col("rare").Max().Alias("hi")}
		}},
		{"any and all together", func() []ursus.Expr {
			return []ursus.Expr{
				ursus.Col("rare").Any().Alias("a"),
				ursus.Col("mostly").AllTrue().Alias("t")}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := func() *ursus.LazyFrame {
				return ursus.Scan(src).
					WithColumns(
						ursus.Col("v").Gt(0.5).Alias("mostly"),
						ursus.Col("v").Lt(1.0).Alias("rare")).
					GroupBy(ursus.Col("k")).Agg(c.agg()...).
					Sort(ursus.Asc(ursus.Col("k")))
			}

			want, err := q().Collect(t.Context(), serial()...)
			if err != nil {
				t.Fatal(err)
			}
			// 97 groups, so this must not go through String(), which renders ten.
			if want.Height() != 97 {
				t.Fatalf("fixture has %d groups, want 97", want.Height())
			}
			assertBothValuesPresent(t, want)

			for _, threads := range []int{2, 4, 8} {
				got, err := q().Collect(t.Context(), parallel(threads)...)
				if err != nil {
					t.Fatalf("threads=%d: %v", threads, err)
				}
				ursustest.AssertFrameEqual(t, got, want, ursustest.CheckNullability())
			}
		})
	}
}

// assertBothValuesPresent fails when a Bool column is constant over every group.
//
// A boolean aggregate that comes out constant cannot distinguish an OR fold from an
// AND one: both agree when every partial holds the same value. Without this the
// subtest passes whatever Merge does — which is the failure mode this whole step
// exists to remove, and which it caught in this test's own first draft.
//
// Same shape as TestPushdownSoundness's vacuity counter and weakfit_test's "too few
// for the agreement to mean anything".
func assertBothValuesPresent(t *testing.T, df *ursus.DataFrame) {
	t.Helper()
	for _, f := range df.Schema().All() {
		if f.Type != ursus.Bool {
			continue
		}
		var sawTrue, sawFalse bool
		for i := range df.Height() {
			v, ok, err := df.At[bool](i, f.Name)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				continue
			}
			sawTrue = sawTrue || v
			sawFalse = sawFalse || !v
		}
		if !sawTrue || !sawFalse {
			t.Errorf("column %q is constant across all %d groups (sawTrue=%v "+
				"sawFalse=%v) — an OR fold and an AND fold agree on constant "+
				"input, so this case cannot test the merge",
				f.Name, df.Height(), sawTrue, sawFalse)
		}
	}
}
