package ursus_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ursus"
	"ursus/dtype"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/source/memsrc"
	"ursus/internal/uerr"
	"ursus/ursustest"
)

// memFrame builds a source of `rows` rows in batches of `batch`.
//
// `k` cycles through `distinct` values, so the sort key is MOSTLY TIES. That is
// deliberate: a merge that breaks ties towards the wrong run still produces
// sorted output, and the only thing that catches it is data where the tie order
// is observable — here through `seq`, which is the row's input position.
func memFrame(t testing.TB, rows, batch, distinct int) *memsrc.Source {
	t.Helper()
	schema, err := dtype.NewSchema(
		dtype.NotNull("k", dtype.Int64),
		dtype.NotNull("seq", dtype.Int64),
		dtype.NotNull("pad", dtype.String),
		dtype.NotNull("grp", dtype.String),
		dtype.Of("opt", dtype.Int64),
	)
	if err != nil {
		t.Fatal(err)
	}

	var batches []*data.Batch
	for start := 0; start < rows; start += batch {
		n := min(batch, rows-start)
		k := make([]int64, n)
		seq := make([]int64, n)
		pad := make([]string, n)
		grp := make([]string, n)
		opt := make([]int64, n)
		ok := bitmap.NewBuilder(n)
		for i := range n {
			v := start + i
			// A multiplier coprime with `distinct` scatters the keys across batches
			// so a run boundary lands in the middle of a tie group.
			k[i] = int64((v * 7919) % distinct)
			seq[i] = int64(v)
			pad[i] = strings.Repeat("x", 32)
			grp[i] = string(rune('a' + (v*13)%5))
			opt[i] = int64((v * 31) % 9)
			// Nulls every third row, so null PLACEMENT is exercised across a run
			// boundary rather than only within one.
			ok.Append(v%3 != 0)
		}
		b, err := data.NewBatch(schema, []*data.Column{
			data.NewFixed("k", dtype.Int64, k, bitmap.AllSet(n)),
			data.NewFixed("seq", dtype.Int64, seq, bitmap.AllSet(n)),
			data.NewString("pad", pad, bitmap.AllSet(n)),
			data.NewString("grp", grp, bitmap.AllSet(n)),
			data.NewFixed("opt", dtype.Int64, opt, ok.Finish()),
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

func int64Col(t testing.TB, df *ursus.DataFrame, name string) []int64 {
	t.Helper()
	s, err := df.Column[int64](name)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]int64, df.Height())
	for i := range out {
		v, ok := s.Get(i)
		if !ok {
			t.Fatalf("%s row %d is null", name, i)
		}
		out[i] = v
	}
	return out
}

// TestExternalSortMatchesInMemory is the point of the step: the same query, the
// same answer, whether or not it went to disk.
//
// The Spills assertion is not decoration. Without it the test passes trivially
// when the budget turns out to be large enough that nothing ever spilled — the
// same way a string test with an all-ASCII corpus passes while proving nothing
// about multi-byte handling.
func TestExternalSortMatchesInMemory(t *testing.T) {
	src := memFrame(t, 20000, 512, 40)

	var unbounded ursus.MemoryStats
	want, err := ursus.Scan(src).
		Sort(ursus.Asc(ursus.Col("k"))).
		Collect(t.Context(), ursus.WithBatchSize(512), ursus.WithVerify(),
			ursus.WithMemoryStats(&unbounded))
	if err != nil {
		t.Fatal(err)
	}
	if unbounded.Spills != 0 {
		t.Fatalf("an unbounded sort spilled %d times", unbounded.Spills)
	}

	for _, limit := range []int64{64 << 10, 128 << 10, 512 << 10} {
		var stats ursus.MemoryStats
		got, err := ursus.Scan(src).
			Sort(ursus.Asc(ursus.Col("k"))).
			Collect(t.Context(),
				ursus.WithBatchSize(512),
				ursus.WithVerify(),
				ursus.WithMemoryLimit(limit),
				ursus.WithSpillDir(t.TempDir()),
				ursus.WithMemoryStats(&stats))
		if err != nil {
			t.Fatalf("limit=%d: %v", limit, err)
		}
		if stats.Spills == 0 {
			t.Fatalf("limit=%d never spilled, so it proves nothing", limit)
		}
		if got.Height() != want.Height() {
			t.Fatalf("limit=%d: %d rows, want %d", limit, got.Height(), want.Height())
		}
		if !slices.Equal(int64Col(t, got, "k"), int64Col(t, want, "k")) {
			t.Errorf("limit=%d: keys differ from the in-memory sort", limit)
		}
		// seq is what makes the TIE ORDER observable. Equal keys must come back in
		// input order, and a merge with the wrong tie-break fails here and nowhere
		// else.
		if !slices.Equal(int64Col(t, got, "seq"), int64Col(t, want, "seq")) {
			t.Errorf("limit=%d: tie order differs — the merge is not stable", limit)
		}
	}
}

// TestExternalSortIsStable checks stability directly, against the definition
// rather than against the in-memory sort.
//
// Within one key, seq must be strictly increasing: the sort is documented stable,
// so equal rows keep their input order, and that has to survive a run boundary
// that cuts through the middle of a tie group.
func TestExternalSortIsStable(t *testing.T) {
	src := memFrame(t, 8000, 256, 8) // 8 distinct keys over 8000 rows: heavy ties

	var stats ursus.MemoryStats
	df, err := ursus.Scan(src).
		Sort(ursus.Desc(ursus.Col("k"))).
		Collect(t.Context(),
			ursus.WithBatchSize(256),
			ursus.WithMemoryLimit(48<<10),
			ursus.WithSpillDir(t.TempDir()),
			ursus.WithMemoryStats(&stats))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Spills < 2 {
		t.Fatalf("spilled %d times; the test needs several runs to mean anything",
			stats.Spills)
	}

	ks, seqs := int64Col(t, df, "k"), int64Col(t, df, "seq")
	for i := 1; i < len(ks); i++ {
		if ks[i] > ks[i-1] {
			t.Fatalf("row %d: key %d after %d, but the sort is descending",
				i, ks[i], ks[i-1])
		}
		if ks[i] == ks[i-1] && seqs[i] <= seqs[i-1] {
			t.Fatalf("row %d: key %d ties, but seq went %d then %d — ties must keep "+
				"input order", i, ks[i], seqs[i-1], seqs[i])
		}
	}
}

// TestTopKSurvivesSpilling: ArgTopK is required to equal ArgSort[:k] exactly,
// ties included, and a memory limit must not weaken that.
//
// A bounded sort never spills — past the budget it discards everything outside
// the current best k instead, which is exact — so this also pins that the limit
// short-circuits the spill path rather than merging a top-k across runs with no
// original row index left to break ties on.
func TestTopKSurvivesSpilling(t *testing.T) {
	src := memFrame(t, 20000, 512, 6)

	want, err := ursus.Scan(src).Sort(ursus.Asc(ursus.Col("k"))).Head(25).
		Collect(t.Context(), ursus.WithBatchSize(512))
	if err != nil {
		t.Fatal(err)
	}

	var stats ursus.MemoryStats
	got, err := ursus.Scan(src).Sort(ursus.Asc(ursus.Col("k"))).Head(25).
		Collect(t.Context(),
			ursus.WithBatchSize(512),
			ursus.WithMemoryLimit(32<<10),
			ursus.WithSpillDir(t.TempDir()),
			ursus.WithMemoryStats(&stats))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Spills != 0 {
		t.Errorf("a bounded sort spilled %d times; it should compact instead",
			stats.Spills)
	}
	if !slices.Equal(int64Col(t, got, "seq"), int64Col(t, want, "seq")) {
		t.Errorf("top-k under a budget picked different rows:\n got %v\nwant %v",
			int64Col(t, got, "seq"), int64Col(t, want, "seq"))
	}
	if stats.Peak > 32<<10*4 {
		t.Errorf("a bounded sort held %d bytes against a %d limit", stats.Peak, 32<<10)
	}
}

// TestSortedSinkIsBounded is the deliverable, stated as a measurement.
//
// A query that FINISHES proves only that it did not run out of memory. What has
// to be true is that it never HELD the input: sort feeding a streaming sink,
// under a budget far below the data, with the peak asserted afterwards.
//
// Collect is deliberately not used. It materialises the whole result by
// definition, so it would put the input back in memory at the last step and the
// measurement would say nothing about the sort.
func TestSortedSinkIsBounded(t *testing.T) {
	const rows = 40000
	src := memFrame(t, rows, 512, 500)
	out := filepath.Join(t.TempDir(), "sorted.parquet")

	// What the same sort holds with no limit, as the baseline the bounded run has
	// to beat by a wide margin.
	var unbounded ursus.MemoryStats
	if err := ursus.Scan(src).
		Sort(ursus.Asc(ursus.Col("k"))).
		SinkParquet(t.Context(), out,
			ursus.WithBatchSize(512),
			ursus.WithMemoryStats(&unbounded)); err != nil {
		t.Fatal(err)
	}

	const limit = 256 << 10
	var stats ursus.MemoryStats
	if err := ursus.Scan(src).
		Sort(ursus.Asc(ursus.Col("k"))).
		SinkParquet(t.Context(), out,
			ursus.WithBatchSize(512),
			ursus.WithMemoryLimit(limit),
			ursus.WithSpillDir(t.TempDir()),
			ursus.WithMemoryStats(&stats)); err != nil {
		t.Fatal(err)
	}

	if stats.Spills == 0 {
		t.Fatal("never spilled, so boundedness was not exercised")
	}
	if unbounded.Peak <= limit {
		t.Fatalf("the unbounded sort held only %d bytes, so a %d limit proves nothing "+
			"— the fixture is too small", unbounded.Peak, limit)
	}
	// The peak is NOT simply the limit, and pretending otherwise would be the kind
	// of assertion that passes while measuring the wrong thing. Two terms overshoot
	// it, both bounded and both real:
	//
	//   - one batch, because the budget is checked AFTER a batch is retained, which
	//     is the first moment its size is known;
	//   - one resident batch PER RUN during the merge, and the run count is the
	//     input size divided by the budget.
	//
	// So the claim being tested is "far below what holding the input costs", not
	// "at or under the limit" — and the unbounded baseline above is what makes that
	// a real comparison rather than a tautology.
	if stats.Peak > 3*limit {
		t.Errorf("peak = %d against a %d limit; the merge's per-run residency should "+
			"not be this large at %d runs", stats.Peak, limit, stats.Spills)
	}
	if stats.Peak > unbounded.Peak/3 {
		t.Errorf("peak = %d against an unbounded peak of %d — the limit bought little",
			stats.Peak, unbounded.Peak)
	}

	// And the file is a real sorted answer, not an empty one.
	df, err := ursus.ScanParquet(out).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != rows {
		t.Fatalf("wrote %d rows, want %d", df.Height(), rows)
	}
	ks := int64Col(t, df, "k")
	if !slices.IsSorted(ks) {
		t.Error("the written file is not sorted")
	}
}

// TestSortStreamsItsOutput: a sort emits batches, not one allocation the size of
// its input.
//
// Before step 10 Finish concatenated everything and returned a single batch, so
// even a sort that fitted in memory held a second full copy of it at the end. The
// merge is only bounded if its OUTPUT is too, and nothing else in the suite would
// notice a regression: a single giant batch is a perfectly correct answer.
func TestSortStreamsItsOutput(t *testing.T) {
	for _, limit := range []int64{0, 64 << 10} { // in memory, and spilled
		src := memFrame(t, 5000, 256, 40)
		n, rows, widest := 0, 0, 0
		for df, err := range ursus.Scan(src).Sort(ursus.Asc(ursus.Col("k"))).
			CollectBatches(t.Context(), ursus.WithBatchSize(256),
				ursus.WithMemoryLimit(limit), ursus.WithSpillDir(t.TempDir())) {
			if err != nil {
				t.Fatal(err)
			}
			n++
			rows += df.Height()
			widest = max(widest, df.Height())
		}
		if rows != 5000 {
			t.Fatalf("limit=%d: %d rows, want 5000", limit, rows)
		}
		if n < 2 {
			t.Errorf("limit=%d: the sort emitted %d batch(es) for 5000 rows at a "+
				"batch size of 256", limit, n)
		}
		if widest > 256 {
			t.Errorf("limit=%d: widest batch is %d rows, batch size is 256",
				limit, widest)
		}
	}
}

// TestSpillFilesAreCleanedUp on every exit path: success, an early break out of
// CollectBatches, and a cancelled context.
//
// The early-break case is the one that would leak. Breaking out of the loop tears
// the operator tree down, which is documented; this asserts the sort's temporary
// directory goes with it.
func TestSpillFilesAreCleanedUp(t *testing.T) {
	entries := func(dir string) int {
		t.Helper()
		es, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		return len(es)
	}

	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		src := memFrame(t, 20000, 512, 40)
		var stats ursus.MemoryStats
		if _, err := ursus.Scan(src).Sort(ursus.Asc(ursus.Col("k"))).
			Collect(t.Context(), ursus.WithBatchSize(512),
				ursus.WithMemoryLimit(64<<10), ursus.WithSpillDir(dir),
				ursus.WithMemoryStats(&stats)); err != nil {
			t.Fatal(err)
		}
		if stats.Spills == 0 {
			t.Fatal("never spilled")
		}
		if n := entries(dir); n != 0 {
			t.Errorf("%d entries left in the spill directory", n)
		}
	})

	t.Run("early break", func(t *testing.T) {
		dir := t.TempDir()
		src := memFrame(t, 20000, 512, 40)
		for range ursus.Scan(src).Sort(ursus.Asc(ursus.Col("k"))).
			CollectBatches(t.Context(), ursus.WithBatchSize(512),
				ursus.WithMemoryLimit(64<<10), ursus.WithSpillDir(dir)) {
			break // one batch, then walk away
		}
		if n := entries(dir); n != 0 {
			t.Errorf("%d entries left after an early break", n)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		dir := t.TempDir()
		src := memFrame(t, 20000, 512, 40)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		n := 0
		for range ursus.Scan(src).Sort(ursus.Asc(ursus.Col("k"))).
			CollectBatches(ctx, ursus.WithBatchSize(512),
				ursus.WithMemoryLimit(64<<10), ursus.WithSpillDir(dir)) {
			n++
			cancel()
		}
		if n == 0 {
			t.Fatal("no batches were produced, so nothing was torn down")
		}
		if got := entries(dir); got != 0 {
			t.Errorf("%d entries left after cancellation", got)
		}
	})
}

// joinShape builds a join whose left rows are mostly unmatched, so the kinds that
// emit unmatched probe rows have something to emit. A Full join over it also has
// unmatched BUILD rows, which is the other flag.
func joinShape(left, right *memsrc.Source, kind ursus.JoinKind) *ursus.LazyFrame {
	return ursus.Scan(left).Select(ursus.Col("k"), ursus.Col("seq")).
		Join(ursus.Scan(right).Select(ursus.Col("k").Alias("k2")),
			ursus.JoinHow(kind),
			ursus.JoinLeftOn(ursus.Col("k")), ursus.JoinRightOn(ursus.Col("k2")))
}

// TestJoinAccountsItsHashTable: a join's ids map is one entry plus a COPIED key
// string per distinct key, and nothing charged it until step 13.
//
// The arithmetic is what makes this a test rather than a hunch. The build side here
// is two Int64 columns over 20,000 rows — 320 KiB of column payload, all of it
// already on the ledger. The ids map over 20,000 distinct keys is roughly 20,000 x
// (48 + 9) = 1.1 MiB, and it was invisible. Measured before and after the fix: 517
// KiB reported against 1.66 MiB actually held, a factor of 3.2.
//
// So the assertion is "the peak is far more than the columns alone", with the
// threshold set between the two measurements. It survives step 13's spilling change
// because an unbounded run never spills.
func TestJoinAccountsItsHashTable(t *testing.T) {
	const n = 20_000
	left := memFrame(t, n, 512, n)
	right := memFrame(t, n, 512, n)

	var stats ursus.MemoryStats
	df, err := ursus.Scan(left).Select(ursus.Col("k")).
		Join(ursus.Scan(right).Select(ursus.Col("k").Alias("k2"), ursus.Col("seq")),
			ursus.JoinLeftOn(ursus.Col("k")), ursus.JoinRightOn(ursus.Col("k2"))).
		Collect(t.Context(), ursus.WithBatchSize(512), ursus.WithMemoryStats(&stats))
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != n {
		t.Fatalf("joined %d rows, want %d — the fixture is not one-to-one", df.Height(), n)
	}
	// 320 KiB of column payload plus a 1.1 MiB hash table. Below 1 MiB the map is
	// not being counted.
	if stats.Peak < 1<<20 {
		t.Errorf("peak = %d over %d distinct keys, which is the column payload and "+
			"little else — the ids map is not on the ledger", stats.Peak, n)
	}
}

// TestBudgetErrorNamesTheOperator: a buffering operator that cannot spill must
// FAIL with its own name in the message rather than being killed by the operating
// system with no explanation.
//
// FOUR operators, plus group_by and join each under a condition. Sort and group_by are not
// unconditional entries any more — both spill, which is the point of steps 10 and
// 12 — but a group_by whose per-group state is O(rows) still cannot be helped by
// partitioning, because partitioning divides the KEY SPACE and a holistic
// accumulator's problem is inside one key. That is the case below, and its sibling
// proves the distinction is real rather than a limit that is simply too small.
func TestBudgetErrorNamesTheOperator(t *testing.T) {
	src := memFrame(t, 4000, 128, 50)
	other := memFrame(t, 4000, 128, 50)

	cases := []struct {
		name  string
		build func() *ursus.LazyFrame
		want  string
	}{
		// Fifty keys over four thousand rows: every key is resident, nothing routes,
		// and a quantile keeps every value of every group. This case USED TO PASS for
		// a different reason — before step 12 the group table itself was what
		// overflowed — which is worse than failing, because the message was right for
		// the wrong cause.
		{"group_by_holistic", func() *ursus.LazyFrame {
			return ursus.Scan(src).GroupBy(ursus.Col("k")).
				Agg(ursus.Col("seq").Quantile(0.5, ursus.InterpLinear))
		}, "group_by"},
		{"unique", func() *ursus.LazyFrame {
			return ursus.Scan(src).Unique("seq")
		}, "unique"},
		// A CROSS join, not a keyed one. A keyed join partitions since step 13 and
		// succeeds here — TestCrossJoinUnderALimitRefuses asserts that pair — but a
		// cross join has exactly one key by construction, and radix partitioning
		// divides the key space, so there is nothing to divide.
		{"cross_join", func() *ursus.LazyFrame {
			return ursus.Scan(src).Join(ursus.Scan(other), ursus.JoinHow(ursus.JoinCross))
		}, "join"},
		{"over", func() *ursus.LazyFrame {
			return ursus.Scan(src).WithColumns(
				ursus.Col("seq").Sum().Over(ursus.Col("k")).Alias("t"))
		}, "over"},
		{"reverse", func() *ursus.LazyFrame {
			return ursus.Scan(src).Reverse()
		}, "reverse"},
		{"hstack", func() *ursus.LazyFrame {
			return ursus.Scan(src).HStack(
				ursus.Scan(other).Select(ursus.Col("k").Alias("k2")))
		}, "hstack"},
		{"tail", func() *ursus.LazyFrame {
			return ursus.Scan(src).Tail(3000)
		}, "tail"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Generous enough that the query plans and runs, far too small to hold
			// 4000 rows of 32-byte strings.
			_, err := c.build().Collect(t.Context(),
				ursus.WithBatchSize(128), ursus.WithMemoryLimit(4<<10))
			if err == nil {
				t.Fatal("a 4KiB limit was enough")
			}
			if !errors.Is(err, uerr.ErrResource) {
				t.Fatalf("want a resource error, got %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the error must name %q: %v", c.want, err)
			}
		})
	}

	// The sibling for the join, for the same reason as the group_by one below: the
	// entry above must not be readable as "a join cannot spill", which stopped being
	// true in step 13.
	//
	// 32 KiB rather than 4. This fixture has fifty keys over four thousand rows —
	// eighty rows each, and at 4 KiB one key's rows genuinely do not fit, so the join
	// refuses there with the RIGHT diagnosis ("a single join key has more build rows
	// than the limit"). That is the third face of the same rule: partitioning divides
	// the key space, and one key is where it runs out. The number here is chosen to
	// be above one key and far below fifty.
	t.Run("keyed_join_spills", func(t *testing.T) {
		var stats ursus.MemoryStats
		if _, err := ursus.Scan(src).Join(ursus.Scan(other), ursus.JoinOn(ursus.Col("k"))).
			Count(t.Context(), ursus.WithBatchSize(128), ursus.WithMemoryLimit(32<<10),
				ursus.WithSpillDir(t.TempDir()),
				ursus.WithMemoryStats(&stats)); err != nil {
			t.Errorf("a keyed join must partition rather than refuse: %v", err)
		}
		if stats.Spills == 0 {
			t.Error("it succeeded without spilling, so it proves nothing")
		}
	})

	// And the case the entry above cannot show: one key too large for any amount of
	// partitioning gets its OWN message, not the cross join's.
	t.Run("one_key_too_large", func(t *testing.T) {
		_, err := ursus.Scan(src).Join(ursus.Scan(other), ursus.JoinOn(ursus.Col("k"))).
			Count(t.Context(), ursus.WithBatchSize(128), ursus.WithMemoryLimit(4<<10),
				ursus.WithSpillDir(t.TempDir()))
		if err == nil {
			t.Fatal("eighty rows per key against a 4KiB limit was accepted")
		}
		if !strings.Contains(err.Error(), "a single join key") {
			t.Errorf("the message should name the cause:\n%v", err)
		}
	})

	// The sibling that makes the group_by case mean something: the SAME keys, the
	// SAME limit, a BOUNDED aggregate — and it succeeds. Without this, the entry
	// above is indistinguishable from "group_by cannot spill", which stopped being
	// true in step 12.
	t.Run("group_by_bounded_succeeds", func(t *testing.T) {
		if _, err := ursus.Scan(src).GroupBy(ursus.Col("k")).
			Agg(ursus.Col("seq").Sum().Alias("s")).
			Collect(t.Context(), ursus.WithBatchSize(128),
				ursus.WithMemoryLimit(4<<10), ursus.WithSpillDir(t.TempDir())); err != nil {
			t.Errorf("a bounded aggregate over the same keys and limit must fit: %v", err)
		}
	})
}

// TestMemoryLimitIsNotASemanticKnob: the same query must give the same answer at
// every limit, exactly as it must at every batch size.
//
// This is the invariant a spilling operator is most likely to break, and it is
// checked over several shapes rather than only over sort, because the accounting
// itself is new in every one of them.
func TestMemoryLimitIsNotASemanticKnob(t *testing.T) {
	src := memFrame(t, 6000, 256, 20)
	// A second fixture for the group-by shapes: twenty keys all stay resident, so a
	// group-by over `src` could never route a row and the shape would prove nothing.
	hi := memFrame(t, 6000, 256, 3000)

	type shape struct {
		build func() *ursus.LazyFrame
		// col is the column compared across limits. Sorts are compared on `seq`, the
		// row's input position, which pins the tie order; an aggregation has no such
		// column and is compared on its key.
		col string
		// bounded marks a shape that legitimately never reaches disk, replacing an
		// exemption that used to be spelled `name == "sort_then_head"` inside the
		// assertion. A per-shape field is the difference between "this one is
		// different" and a rule with one name hard-coded into it.
		bounded bool
		// multiset compares the whole frame as a SET of rows rather than positionally. A spilling
		// join reorders and has no MaintainOrder to opt out with — restoring probe
		// order would mean buffering an output that can exceed both inputs — so a
		// join shape here can only assert what the contract actually promises, which
		// is that the CONTENT does not move. TestJoinOrderIsUnspecifiedUnderALimit
		// asserts the other half: that the order really does.
		multiset bool
	}

	queries := map[string]shape{
		"sort": {build: func() *ursus.LazyFrame {
			return ursus.Scan(src).Sort(ursus.Asc(ursus.Col("k")), ursus.Asc(ursus.Col("seq")))
		}, col: "seq"},
		"sort_desc": {build: func() *ursus.LazyFrame {
			return ursus.Scan(src).Sort(ursus.Desc(ursus.Col("k")))
		}, col: "seq"},
		"sort_then_head": {build: func() *ursus.LazyFrame {
			return ursus.Scan(src).Sort(ursus.Asc(ursus.Col("k"))).Head(100)
		}, col: "seq", bounded: true},
		"sort_computed_key": {build: func() *ursus.LazyFrame {
			return ursus.Scan(src).Sort(ursus.Asc(ursus.Col("k").Mul(int64(-1))))
		}, col: "seq"},
		"sort_string": {build: func() *ursus.LazyFrame {
			return ursus.Scan(src).Sort(ursus.Asc(ursus.Col("grp")))
		}, col: "seq"},
		"sort_nulls_first": {build: func() *ursus.LazyFrame {
			return ursus.Scan(src).Sort(ursus.Asc(ursus.Col("opt")).NullsFirst())
		}, col: "seq"},
		"sort_nulls_last_desc": {build: func() *ursus.LazyFrame {
			return ursus.Scan(src).Sort(ursus.Desc(ursus.Col("opt")).NullsLast())
		}, col: "seq"},
		"sort_two_keys_mixed": {build: func() *ursus.LazyFrame {
			return ursus.Scan(src).Sort(
				ursus.Desc(ursus.Col("grp")), ursus.Asc(ursus.Col("opt")))
		}, col: "seq"},

		// Group-by shapes, and every one of them declares MaintainOrder — because
		// this test compares POSITIONALLY and an unordered spilling group-by's row
		// order genuinely does depend on the limit. That is the one thing step 12
		// takes away from this test's name, and WithMemoryLimit's doc now says so:
		// content is invariant at every limit, order is invariant only under
		// MaintainOrder. Adding these shapes WITHOUT the flag would have made the
		// test flaky rather than made the library wrong.
		"group_by_ordered": {build: func() *ursus.LazyFrame {
			return ursus.Scan(hi).GroupBy(ursus.Col("k")).MaintainOrder().
				Agg(ursus.Len().Alias("n"))
		}, col: "k"},
		"group_by_ordered_multi_agg": {build: func() *ursus.LazyFrame {
			return ursus.Scan(hi).GroupBy(ursus.Col("k")).MaintainOrder().
				Agg(ursus.Col("seq").Min().Alias("lo"), ursus.Col("seq").Max().Alias("hi"),
					ursus.Col("opt").Mean().Alias("m"), ursus.Len().Alias("n"))
		}, col: "k"},
		"group_by_ordered_then_head": {build: func() *ursus.LazyFrame {
			return ursus.Scan(hi).GroupBy(ursus.Col("k")).MaintainOrder().
				Agg(ursus.Len().Alias("n")).Head(50)
		}, col: "k"},
		// Twenty keys: nothing to partition, so this one is bounded for the same
		// reason sort_then_head is — and it keeps a low-cardinality group-by in the
		// invariance check rather than only the shapes that spill.
		"group_by_ordered_few_keys": {build: func() *ursus.LazyFrame {
			return ursus.Scan(src).GroupBy(ursus.Col("k")).MaintainOrder().
				Agg(ursus.Col("seq").Sum().Alias("s"))
		}, col: "k", bounded: true},

		// Join shapes, all compared as multisets. Every kind whose flags differ is
		// here, because "the limit did not change the answer" has to hold for the
		// unmatched-row paths too, and those are exactly what the flags select.
		"join_inner": {build: func() *ursus.LazyFrame {
			return joinShape(src, hi, ursus.JoinInner)
		}, col: "seq", multiset: true},
		"join_left": {build: func() *ursus.LazyFrame {
			return joinShape(src, hi, ursus.JoinLeft)
		}, col: "seq", multiset: true},
		"join_full": {build: func() *ursus.LazyFrame {
			return joinShape(src, hi, ursus.JoinFull)
		}, col: "seq", multiset: true},
		"join_anti": {build: func() *ursus.LazyFrame {
			return joinShape(src, hi, ursus.JoinAnti)
		}, col: "seq", multiset: true},
	}

	for name, sh := range queries {
		t.Run(name, func(t *testing.T) {
			want, err := sh.build().Collect(t.Context(), ursus.WithBatchSize(256))
			if err != nil {
				t.Fatal(err)
			}
			var spilled int64
			for _, limit := range []int64{16 << 10, 64 << 10, 1 << 20, 0} {
				var stats ursus.MemoryStats
				got, err := sh.build().Collect(t.Context(),
					ursus.WithBatchSize(256),
					ursus.WithMemoryLimit(limit),
					ursus.WithSpillDir(t.TempDir()),
					ursus.WithMemoryStats(&stats))
				if err != nil {
					t.Fatalf("limit=%d: %v", limit, err)
				}
				spilled += stats.Spills
				if sh.multiset {
					// The WHOLE frame as a set of rows, which is stronger than one
					// column and is the only thing a join shape can assert: a Full
					// join's left columns are null on an unmatched build row, so no
					// single column is even readable as a non-null sequence.
					ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
					continue
				}
				if !slices.Equal(int64Col(t, got, sh.col), int64Col(t, want, sh.col)) {
					t.Errorf("limit=%d changed the answer", limit)
				}
			}
			// A bounded shape compacts, or has too little to partition, instead of
			// spilling. Every other shape must actually have gone to disk, or "the
			// limit changed nothing" is true only because the limit did nothing.
			if sh.bounded != (spilled == 0) {
				t.Errorf("spilled %d times; bounded = %v", spilled, sh.bounded)
			}
		})
	}
}
