package ursus_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source/memsrc"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// joinFrame builds a source with a NULLABLE key, which memFrame does not have.
//
// nullsLate puts every null key in the second half of the input. That is the lesson
// step 12 learned the hard way: residency is decided at a key's first appearance, so
// a special key scattered from row 0 is resident for the query's whole life and
// NEVER REACHES THE PARTITIONER. TestSpilledGroupKeysUseGroupingEquality passed for
// months while hashing raw bits because of exactly that.
func joinFrame(t testing.TB, rows, batch, distinct int, nullEvery int, nullsLate bool) *memsrc.Source {
	t.Helper()
	schema := dtype.MustSchema(
		dtype.Of("k", dtype.Int64),
		dtype.NotNull("v", dtype.Int64),
		dtype.NotNull("pad", dtype.String),
	)
	var batches []*data.Batch
	for start := 0; start < rows; start += batch {
		n := min(batch, rows-start)
		ks := make([]int64, n)
		vs := make([]int64, n)
		pad := make([]string, n)
		ok := bitmap.NewBuilder(n)
		for i := range n {
			v := start + i
			// Coprime with distinct, so keys scatter across batches rather than
			// arriving in blocks — a partitioner that only ever saw one bucket per
			// batch would not be exercised.
			ks[i] = int64((v * 7919) % distinct)
			vs[i] = int64(v)
			pad[i] = strings.Repeat("x", 16)
			null := nullEvery > 0 && v%nullEvery == 0
			if nullsLate && v < rows/2 {
				null = false
			}
			ok.Append(!null)
		}
		b, err := data.NewBatch(schema, []*data.Column{
			data.NewFixed("k", dtype.Int64, ks, ok.Finish()),
			data.NewFixed("v", dtype.Int64, vs, bitmap.AllSet(n)),
			data.NewString("pad", pad, bitmap.AllSet(n)),
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

var spillingKinds = []ursus.JoinKind{
	ursus.JoinInner, ursus.JoinLeft, ursus.JoinRight,
	ursus.JoinFull, ursus.JoinSemi, ursus.JoinAnti,
}

// joinQuery builds `left ⋈ right` on k, with the right side's key renamed so the
// two frames' columns do not collide and the assertion reads cleanly.
func joinQuery(l, r *memsrc.Source, kind ursus.JoinKind) *ursus.LazyFrame {
	return ursus.Scan(l).Select(ursus.Col("k"), ursus.Col("v")).
		Join(ursus.Scan(r).Select(ursus.Col("k").Alias("k2"), ursus.Col("v").Alias("v2")),
			ursus.JoinHow(kind),
			ursus.JoinLeftOn(ursus.Col("k")), ursus.JoinRightOn(ursus.Col("k2")))
}

// TestSpillingJoinMatchesInMemory is the point of the step: the same answer whether
// or not the build side went to disk.
//
// All six spilling kinds, because the flags that differ between them — which side's
// unmatched rows are emitted, whether right columns exist at all — are exactly what
// partitioning has to preserve, and a wrong one shows in only one kind.
//
// Compared with AssertFrameEqual and IgnoreRowOrder, not df.String(): String stops
// at ten rows and the answer here is tens of thousands.
func TestSpillingJoinMatchesInMemory(t *testing.T) {
	// The right side is the BUILD side. Its key space is WIDER than the left's, so
	// the left has matched rows and the right has unmatched ones — and, crucially,
	// its ids map is large: Semi and Anti retain no build rows at all, so the map is
	// the only thing they hold and a fixture with few keys never makes them spill.
	// The left is narrower, so Left/Full/Anti have unmatched rows to emit too. A
	// fixture where everything matches passes every wrong implementation.
	left := joinFrame(t, 8000, 256, 6000, 37, true)
	right := joinFrame(t, 4000, 256, 4000, 53, true)

	for _, kind := range spillingKinds {
		t.Run(kind.String(), func(t *testing.T) {
			var unbounded ursus.MemoryStats
			want, err := joinQuery(left, right, kind).Collect(t.Context(),
				ursus.WithBatchSize(256), ursus.WithVerify(),
				ursus.WithMemoryStats(&unbounded))
			if err != nil {
				t.Fatal(err)
			}
			if unbounded.Spills != 0 {
				t.Fatalf("an unbounded join spilled %d times", unbounded.Spills)
			}
			if want.Height() < 1000 {
				t.Fatalf("%d output rows is too few for a spill to mean anything",
					want.Height())
			}

			for _, limit := range []int64{8 << 10, 32 << 10, 96 << 10} {
				var stats ursus.MemoryStats
				got, err := joinQuery(left, right, kind).Collect(t.Context(),
					ursus.WithBatchSize(256), ursus.WithVerify(),
					ursus.WithMemoryLimit(limit), ursus.WithSpillDir(t.TempDir()),
					ursus.WithMemoryStats(&stats))
				if err != nil {
					t.Fatalf("limit=%d: %v", limit, err)
				}
				// Two, not one: a single bucket cannot catch a bug that only shows
				// when two sub-joins have to agree.
				if stats.Spills < 2 {
					t.Fatalf("limit=%d spilled %d times, so it proves nothing",
						limit, stats.Spills)
				}
				ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
			}
		})
	}
}

// TestSpilledJoinEmitsEveryProbeRow covers the sharpest silent-row-loss shape in the
// design: a probe key that matches NOTHING once the build side has partitioned.
//
// A replay that walked build files and joined each against the probe rows beside it
// would drop those rows entirely — invisible for Inner and Semi, and a missing
// output row for Left, Full and Anti. ursus removes the case by construction: a
// probe key is routed only when its bucket HAS a build file, so a key that can match
// nothing is answered inline, in probe order. This asserts the outcome rather than
// the mechanism, so it survives a redesign.
func TestSpilledJoinEmitsEveryProbeRow(t *testing.T) {
	// Disjoint key spaces: the right side has keys 0..99, the left 5000..5899, so
	// NOTHING matches and every left row must be emitted by Left, Full and Anti.
	// 4000 distinct build keys, so even Semi and Anti — whose only state is the ids
	// map — go over an 8 KiB budget and actually partition.
	right := joinFrame(t, 4000, 256, 4000, 0, false)
	left := shiftKeys(t, joinFrame(t, 6000, 256, 900, 0, false), 50_000)

	for _, c := range []struct {
		kind ursus.JoinKind
		want int
	}{
		{ursus.JoinLeft, 6000}, {ursus.JoinFull, 10000}, {ursus.JoinAnti, 6000},
		{ursus.JoinInner, 0}, {ursus.JoinSemi, 0},
	} {
		t.Run(c.kind.String(), func(t *testing.T) {
			var stats ursus.MemoryStats
			got, err := joinQuery(left, right, c.kind).Collect(t.Context(),
				ursus.WithBatchSize(256), ursus.WithVerify(),
				ursus.WithMemoryLimit(8<<10), ursus.WithSpillDir(t.TempDir()),
				ursus.WithMemoryStats(&stats))
			if err != nil {
				t.Fatal(err)
			}
			if stats.Spills == 0 {
				t.Fatal("never spilled, so the case was not exercised")
			}
			if got.Height() != c.want {
				t.Errorf("%d rows, want %d — probe rows whose key matches nothing "+
					"were dropped by the replay", got.Height(), c.want)
			}
		})
	}
}

// shiftKeys adds n to every key, so two fixtures can be made disjoint.
func shiftKeys(t testing.TB, src *memsrc.Source, n int64) *memsrc.Source {
	t.Helper()
	df, err := ursus.Scan(src).
		Select(ursus.Col("k").Add(n).Alias("k"), ursus.Col("v"), ursus.Col("pad")).
		Collect(context.Background(), ursus.WithBatchSize(256))
	if err != nil {
		t.Fatal(err)
	}
	out, err := memsrc.New(df.Schema(), df.Batch())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// nullKeyRows counts output rows whose right-side key is null.
func nullKeyRows(t testing.TB, df *ursus.DataFrame, col string) int {
	t.Helper()
	n := 0
	for row := range df.Height() {
		if _, ok, err := df.At[int64](row, col); err != nil {
			t.Fatal(err)
		} else if !ok {
			n++
		}
	}
	return n
}

// TestSpilledNullKeysStillFlush is the silent-row-loss mode step 12 named and could
// not fix from where it stood.
//
// A null-keyed build row has NO HASH — Consume skips the encoder for it entirely —
// so it cannot be routed with the others, and the dense per-build-row array that
// saves it today does not survive partitioning. Right and Full must still emit it.
//
// The nulls are in the SECOND HALF of the build input on purpose. A null-keyed row
// that arrives before the split is resident and is flushed by the ordinary path, so
// a fixture that scatters them from row 0 tests nothing about the null bucket.
func TestSpilledNullKeysStillFlush(t *testing.T) {
	left := joinFrame(t, 4000, 256, 6000, 0, false)
	right := joinFrame(t, 4000, 256, 4000, 7, true) // ~285 null keys, all late

	for _, kind := range []ursus.JoinKind{ursus.JoinRight, ursus.JoinFull} {
		t.Run(kind.String(), func(t *testing.T) {
			want, err := joinQuery(left, right, kind).Collect(t.Context(),
				ursus.WithBatchSize(256))
			if err != nil {
				t.Fatal(err)
			}
			wantNulls := nullKeyRows(t, want, "k2")
			if wantNulls < 100 {
				t.Fatalf("the fixture has %d null-keyed build rows; too few to notice "+
					"losing them", wantNulls)
			}

			var stats ursus.MemoryStats
			got, err := joinQuery(left, right, kind).Collect(t.Context(),
				ursus.WithBatchSize(256), ursus.WithMemoryLimit(8<<10),
				ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&stats))
			if err != nil {
				t.Fatal(err)
			}
			if stats.Spills < 2 {
				t.Fatalf("spilled %d times", stats.Spills)
			}
			if n := nullKeyRows(t, got, "k2"); n != wantNulls {
				t.Errorf("%d null-keyed build rows survived the spill, want %d — they "+
					"have no hash, so without a bucket of their own they are dropped",
					n, wantNulls)
			}
			ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
		})
	}
}

// TestSpilledSemiAntiPartitionTheLeftFrame: semi and anti are complements, and they
// stay complements when the build side spills.
//
// This is the invariant the mask bug broke. Semi and Anti emitted through a filter
// mask over a CONTIGUOUS slice of the probe batch; routing punches holes in that
// run, the window slides, and the filter lands on the wrong rows — with the row
// COUNT still right, which is why only a value comparison finds it. Asserting
// semi + anti == n and disjointness catches the shifted window directly.
func TestSpilledSemiAntiPartitionTheLeftFrame(t *testing.T) {
	const rows = 8000
	left := joinFrame(t, rows, 256, 6000, 37, true)
	right := joinFrame(t, 4000, 256, 4000, 53, true)

	collect := func(kind ursus.JoinKind, limit int64) *ursus.DataFrame {
		t.Helper()
		opts := []ursus.CollectOption{ursus.WithBatchSize(256), ursus.WithVerify()}
		if limit > 0 {
			opts = append(opts, ursus.WithMemoryLimit(limit), ursus.WithSpillDir(t.TempDir()))
		}
		df, err := joinQuery(left, right, kind).Collect(t.Context(), opts...)
		if err != nil {
			t.Fatal(err)
		}
		return df
	}

	for _, limit := range []int64{0, 8 << 10, 32 << 10} {
		semi, anti := collect(ursus.JoinSemi, limit), collect(ursus.JoinAnti, limit)
		if got := semi.Height() + anti.Height(); got != rows {
			t.Errorf("limit=%d: semi %d + anti %d = %d, want %d",
				limit, semi.Height(), anti.Height(), got, rows)
		}
		// Disjoint, checked by value rather than by count: a shifted window keeps the
		// count and changes which rows come out.
		in := map[int64]bool{}
		for _, v := range int64Col(t, semi, "v") {
			in[v] = true
		}
		for _, v := range int64Col(t, anti, "v") {
			if in[v] {
				t.Fatalf("limit=%d: row v=%d is in BOTH semi and anti", limit, v)
			}
		}
	}
}

// TestJoinSpillIsDeterministic: two runs of one query at one limit must agree
// exactly, order included. A hash seeded per process would break this and nothing
// else, which is why partitionOf is seedless.
func TestJoinSpillIsDeterministic(t *testing.T) {
	left := joinFrame(t, 6000, 256, 5000, 0, false)
	right := joinFrame(t, 4000, 256, 4000, 11, true)

	run := func() *ursus.DataFrame {
		t.Helper()
		df, err := joinQuery(left, right, ursus.JoinFull).Collect(t.Context(),
			ursus.WithBatchSize(256), ursus.WithMemoryLimit(8<<10),
			ursus.WithSpillDir(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		return df
	}
	ursustest.AssertFrameEqual(t, run(), run())
}

// TestCrossJoinUnderALimitRefuses is the pair that tells the whole story: the split
// bounds CARDINALITY, and a cross join has exactly one key to divide.
func TestCrossJoinUnderALimitRefuses(t *testing.T) {
	left := joinFrame(t, 4000, 128, 900, 0, false)
	right := joinFrame(t, 4000, 128, 900, 0, false)

	_, err := ursus.Scan(left).Join(ursus.Scan(right), ursus.JoinHow(ursus.JoinCross)).
		Count(t.Context(), ursus.WithBatchSize(128), ursus.WithMemoryLimit(4<<10),
			ursus.WithSpillDir(t.TempDir()))
	if err == nil {
		t.Fatal("a cross join under a 4KiB limit must refuse")
	}
	if !errors.Is(err, uerr.ErrResource) {
		t.Fatalf("want a resource error, got %v", err)
	}
	for _, want := range []string{"join", "cross join", "NO join key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should say %q:\n%v", want, err)
		}
	}

	// The same data with a KEY succeeds at the same limit, by partitioning. Without
	// this the case above is indistinguishable from "a join cannot spill".
	var stats ursus.MemoryStats
	if _, err := joinQuery(left, right, ursus.JoinInner).Count(t.Context(),
		ursus.WithBatchSize(128), ursus.WithMemoryLimit(4<<10),
		ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&stats)); err != nil {
		t.Fatalf("a keyed join at the same limit must spill rather than refuse: %v", err)
	}
	if stats.Spills == 0 {
		t.Error("the keyed join succeeded without spilling, so it proves nothing")
	}
}

// TestSpillingJoinIsBounded is the deliverable, as a measurement.
//
// Through a SINK rather than Collect: a join's output can be LARGER than either
// input, and Collect would hold all of it, which is exactly what the measurement is
// trying to exclude.
//
// Parquet, not CSV. Step 13 wrote "not SinkParquet either — it refuses Int128 and
// every temporal type" here, which was true of Parquet and false of this test: the
// fixture is four Int64 columns, all writable then and now. A justification that does
// not apply to the code it sits above is worse than none.
func TestSpillingJoinIsBounded(t *testing.T) {
	// HIGH FAN-OUT: 500 distinct keys over 60,000 build rows, a hundred and twenty
	// each. That
	// shape is the whole reason residency is decided by hash bucket rather than by
	// arrival, and a fixture with one row per key does not test it — under arrival
	// order the first batch admits nearly every key, those keys then collect a
	// hundred and twenty rows apiece, and the resident set grows without bound while
	// the rule works exactly as written. See extjoin.go's header.
	left := joinFrame(t, 500, 512, 500, 0, false)
	right := joinFrame(t, 60_000, 512, 500, 0, false)
	out := filepath.Join(t.TempDir(), "join.parquet")

	var unbounded ursus.MemoryStats
	if err := joinQuery(left, right, ursus.JoinInner).
		SinkParquet(t.Context(), out, ursus.WithBatchSize(512),
			ursus.WithMemoryStats(&unbounded)); err != nil {
		t.Fatal(err)
	}

	const limit = 256 << 10
	var stats ursus.MemoryStats
	if err := joinQuery(left, right, ursus.JoinInner).
		SinkParquet(t.Context(), out, ursus.WithBatchSize(512),
			ursus.WithMemoryLimit(limit), ursus.WithSpillDir(t.TempDir()),
			ursus.WithMemoryStats(&stats)); err != nil {
		t.Fatal(err)
	}
	if stats.Spills == 0 {
		t.Fatal("never spilled, so boundedness was not exercised")
	}
	// Asserted FIRST, so the comparison below is not a tautology: if the unbounded
	// join already fitted, a limit above it would prove nothing.
	if unbounded.Peak <= limit {
		t.Fatalf("the unbounded join held only %d bytes, so a %d limit proves "+
			"nothing — the fixture is too small", unbounded.Peak, limit)
	}
	// The peak is NOT the limit, and claiming otherwise would measure the wrong
	// thing. Three terms overshoot it, all bounded: one batch, because the split is
	// decided after a batch is retained; the resident bucket at the moment of the
	// split; and seventeen build write buffers plus sixteen probe ones.
	if stats.Peak > unbounded.Peak/2 {
		t.Errorf("peak = %d against an unbounded peak of %d — the limit bought little",
			stats.Peak, unbounded.Peak)
	}
}

// TestJoinOrderIsUnspecifiedUnderALimit pins that a spilling join genuinely
// reorders, which is the negative assertion that stops every IgnoreRowOrder
// comparison above from proving nothing.
//
// Asserting a DIFFERENCE is legitimate because the contract says so: WithMemoryLimit
// documents that content is invariant and order is not, and a join has no
// MaintainOrder to opt out with — restoring probe order would mean buffering an
// output that can exceed both inputs.
func TestJoinOrderIsUnspecifiedUnderALimit(t *testing.T) {
	left := joinFrame(t, 8000, 256, 6000, 0, false)
	right := joinFrame(t, 4000, 256, 4000, 0, false)

	want, err := joinQuery(left, right, ursus.JoinInner).Collect(t.Context(),
		ursus.WithBatchSize(256))
	if err != nil {
		t.Fatal(err)
	}
	got, err := joinQuery(left, right, ursus.JoinInner).Collect(t.Context(),
		ursus.WithBatchSize(256), ursus.WithMemoryLimit(8<<10),
		ursus.WithSpillDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
	if slices.Equal(int64Col(t, got, "v"), int64Col(t, want, "v")) {
		t.Fatal("the spilled and unspilled orders matched, so every IgnoreRowOrder " +
			"assertion in this file is proving less than it looks")
	}
}

// TestSpilledJoinValidateStillFires: partitioning must not weaken a cardinality
// check into a silent pass.
//
// It cannot, and the proof is two lines — a key is wholly resident or wholly routed,
// and equal keys hash to the same bucket, so a duplicate is always seen by exactly
// one sink. This runs it.
func TestSpilledJoinValidateStillFires(t *testing.T) {
	// Every key appears twice on the build side, so m:1 must reject it however the
	// rows are partitioned.
	dup := joinFrame(t, 8000, 256, 4000, 0, false)
	left := joinFrame(t, 4000, 256, 4000, 0, false)

	for _, limit := range []int64{0, 4 << 10, 16 << 10} {
		opts := []ursus.CollectOption{ursus.WithBatchSize(256)}
		if limit > 0 {
			opts = append(opts, ursus.WithMemoryLimit(limit), ursus.WithSpillDir(t.TempDir()))
		}
		_, err := ursus.Scan(left).Select(ursus.Col("k"), ursus.Col("v")).
			Join(ursus.Scan(dup).Select(ursus.Col("k").Alias("k2")),
				ursus.JoinLeftOn(ursus.Col("k")), ursus.JoinRightOn(ursus.Col("k2")),
				ursus.JoinValidate(ursus.ValidateManyToOne)).
			Count(t.Context(), opts...)
		if err == nil {
			t.Errorf("limit=%d: a duplicated right key must be rejected", limit)
			continue
		}
		if !strings.Contains(err.Error(), "m:1") {
			t.Errorf("limit=%d: the error should name the validation:\n%v", limit, err)
		}
	}
}

// TestJoinSpillFilesAreCleanedUp on every exit path. A join has TWO file sets and a
// live sub-join to unwind, where the sort had one set and a merge.
func TestJoinSpillFilesAreCleanedUp(t *testing.T) {
	entries := func(dir string) int {
		t.Helper()
		es, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		return len(es)
	}
	left := joinFrame(t, 8000, 256, 6000, 0, false)
	right := joinFrame(t, 4000, 256, 4000, 0, false)
	q := func(dir string) *ursus.LazyFrame { return joinQuery(left, right, ursus.JoinFull) }

	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		var stats ursus.MemoryStats
		if _, err := q(dir).Collect(t.Context(), ursus.WithBatchSize(256),
			ursus.WithMemoryLimit(8<<10), ursus.WithSpillDir(dir),
			ursus.WithMemoryStats(&stats)); err != nil {
			t.Fatal(err)
		}
		if stats.Spills == 0 {
			t.Fatal("never spilled")
		}
		if n := entries(dir); n != 0 {
			t.Errorf("%d entries left after success", n)
		}
	})

	// Two break points. Breaking on batch 0 leaves both file sets written and
	// unvisited with no reader open; breaking later lands inside a replay, with a
	// spill reader and a live sub-join to unwind. The sort has only the second shape.
	for _, after := range []int{1, 8} {
		t.Run("early break after "+itoa64(int64(after)), func(t *testing.T) {
			dir := t.TempDir()
			seen := 0
			for range q(dir).CollectBatches(t.Context(), ursus.WithBatchSize(256),
				ursus.WithMemoryLimit(8<<10), ursus.WithSpillDir(dir)) {
				if seen++; seen >= after {
					break
				}
			}
			if seen < after {
				t.Fatalf("the query produced %d batches, so it never reached the break "+
					"point this case is named for", seen)
			}
			if n := entries(dir); n != 0 {
				t.Errorf("%d entries left after an early break", n)
			}
		})
	}

	t.Run("cancelled", func(t *testing.T) {
		dir := t.TempDir()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		for range q(dir).CollectBatches(ctx, ursus.WithBatchSize(256),
			ursus.WithMemoryLimit(8<<10), ursus.WithSpillDir(dir)) {
			cancel()
		}
		if n := entries(dir); n != 0 {
			t.Errorf("%d entries left after cancellation", n)
		}
	})

	t.Run("error", func(t *testing.T) {
		dir := t.TempDir()
		// Validate fails partway through, after both file sets have been written.
		_, _ = ursus.Scan(left).Select(ursus.Col("k"), ursus.Col("v")).
			Join(ursus.Scan(right).Select(ursus.Col("k").Alias("k2")),
				ursus.JoinLeftOn(ursus.Col("k")), ursus.JoinRightOn(ursus.Col("k2")),
				ursus.JoinValidate(ursus.ValidateOneToOne)).
			Collect(t.Context(), ursus.WithBatchSize(256),
				ursus.WithMemoryLimit(8<<10), ursus.WithSpillDir(dir))
		if n := entries(dir); n != 0 {
			t.Errorf("%d entries left after an error", n)
		}
	})
}
