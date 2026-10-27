package ursus_test

// Step 395: the queries of an engine share one memory budget (A3 of v0.8-scope.md).
// Each query made its own before, half the machine's memory by default, so sixty-four
// at once each assumed half the machine. An engine's pressure makes a query spill
// sooner, and never makes it fail; every entry point leaves its engine as it ends.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"iter"
	"path/filepath"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// engineFrame is n rows of a key k of keys values and a value v.
func engineFrame(t *testing.T, n, keys int) *ursus.DataFrame {
	t.Helper()
	k, v := make([]int64, n), make([]int64, n)
	for i := range n {
		r := uint64(i) * 0x9E3779B97F4A7C15
		k[i], v[i] = int64(r>>40%uint64(keys)), int64(r>>16%1_000_000)
	}
	df, err := ursus.Frame(ursus.Values("k", k), ursus.Values("v", v)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return df
}

// TestEveryEntryPointLeavesItsEngine: a sort run through each way of running a query,
// to its end, broken off, failed and panicked, leaves its engine with nothing held,
// no query running and its threads given back (step 397); one paused halfway is
// counted, holding its rows and its threads.
func TestEveryEntryPointLeavesItsEngine(t *testing.T) {
	ctx := t.Context()
	e := ursus.NewEngine(ursus.WithEngineMemoryLimit(1 << 30))
	on := ursus.WithEngine(e)
	df := engineFrame(t, 200_000, 100)
	q := df.Lazy().Sort(ursus.Asc(ursus.Col("v")))
	dir := t.TempDir()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	type row struct {
		K int64 `ursus:"k"`
		V int64 `ursus:"v"`
	}
	db, _ := writeDB(t)

	cases := []struct {
		name string
		run  func() error
	}{
		{"Collect", func() error { _, err := q.Collect(ctx, on); return err }},
		{"CollectBatches, broken off", func() error {
			for _, err := range q.CollectBatches(ctx, on, ursus.WithBatchSize(1000)) {
				return err
			}
			return nil
		}},
		{"Count", func() error { _, err := q.Count(ctx, on); return err }},
		{"CollectInto", func() error { _, err := q.CollectInto[row](ctx, on); return err }},
		{"IterRows, broken off", func() error {
			for _, err := range q.IterRows[row](ctx, on) {
				return err
			}
			return nil
		}},
		{"CollectRecords", func() error {
			for rec, err := range q.CollectRecords(ctx, on) {
				if err != nil {
					return err
				}
				rec.Release()
			}
			return nil
		}},
		{"Groups", func() error {
			for _, err := range q.GroupBy(ursus.Col("k")).Groups(ctx, on) {
				if err != nil {
					return err
				}
			}
			return nil
		}},
		{"Profile", func() error { _, _, err := q.Profile(ctx, on); return err }},
		{"WriteCSV", func() error { return q.WriteCSV(ctx, io.Discard, on) }},
		{"SinkCSV", func() error { return q.SinkCSV(ctx, filepath.Join(dir, "a.csv"), on) }},
		{"WriteParquet", func() error { return q.WriteParquet(ctx, io.Discard, on) }},
		{"SinkParquet", func() error { return q.SinkParquet(ctx, filepath.Join(dir, "a.parquet"), on) }},
		{"SinkParquet, partitioned", func() error {
			return q.SinkParquet(ctx, filepath.Join(dir, "hive"), on, ursus.WithPartitionBy("k"))
		}},
		{"WriteIPC", func() error { return q.WriteIPC(ctx, io.Discard, on) }},
		{"WriteIPCStream", func() error { return q.WriteIPCStream(ctx, io.Discard, on) }},
		{"SinkIPC", func() error { return q.SinkIPC(ctx, filepath.Join(dir, "a.ipc"), on) }},
		{"WriteNDJSON", func() error { return q.WriteNDJSON(ctx, io.Discard, on) }},
		{"WriteJSON", func() error { return q.WriteJSON(ctx, io.Discard, on) }},
		{"SinkNDJSON", func() error { return q.SinkNDJSON(ctx, filepath.Join(dir, "a.jsonl"), on) }},
		{"WriteDatabase", func() error {
			_, err := q.Head(10).WriteDatabase(ctx, db, "t", ursus.WithDialect(ursus.DialectSQLite), on)
			return err
		}},
		{"cancelled", func() error {
			if _, err := q.Collect(cancelled, on); !errors.Is(err, context.Canceled) {
				return errors.Join(errors.New("not cancelled"), err)
			}
			return nil
		}},
		{"panicked", func() error {
			if _, err := q.Select(rawBoom()).Collect(ctx, on); !errors.Is(err, ursus.ErrInternal) {
				return errors.Join(errors.New("no internal error"), err)
			}
			return nil
		}},
		{"planned and not run", func() error {
			_, err := ursus.Frame(ursus.Values("a", []int64{1})).Select(ursus.Col("nope")).Collect(ctx, on)
			if err == nil {
				return errors.New("a missing column was found")
			}
			return nil
		}},
	}
	for _, c := range cases {
		if err := c.run(); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if s := e.Stats(); s.Running != 0 || s.Held != 0 || s.Parallel != 0 {
			t.Errorf("%s left its engine at %+v", c.name, s)
		}
	}

	next, stop := iter.Pull2(q.CollectBatches(ctx, on, ursus.WithBatchSize(1000)))
	if _, err, ok := next(); !ok || err != nil {
		t.Fatalf("the first batch: %v", err)
	}
	if s := e.Stats(); s.Running != 1 || s.Held < 3<<20 || s.Parallel != min(s.Threads-1, 1) {
		t.Errorf("a sort of 3 MiB paused after its first batch: %+v", s)
	}
	stop()
	if s := e.Stats(); s.Running != 0 || s.Held != 0 || s.Peak < 3<<20 || s.Parallel != 0 {
		t.Errorf("after it stopped: %+v", s)
	}

	// The default engine is shared by the queries given none, a frame's own
	// operations among them.
	before := ursus.DefaultEngine().Stats().Running
	next, stop = iter.Pull2(q.CollectBatches(ctx, ursus.WithBatchSize(1000)))
	if _, err, ok := next(); !ok || err != nil {
		t.Fatalf("the first batch: %v", err)
	}
	if got := ursus.DefaultEngine().Stats().Running; got != before+1 {
		t.Errorf("a query given no engine is not on the default one: %d running, want %d", got, before+1)
	}
	stop()
	if _, err := df.Sort(ctx, ursus.Asc(ursus.Col("k"))); err != nil {
		t.Fatal(err)
	}
	if got := ursus.DefaultEngine().Stats().Running; got != before {
		t.Errorf("%d running on the default engine after, want %d", got, before)
	}
}

// TestAnEnginesPressureSpillsAndNeverFails: a query that fits an engine's budget
// alone, run while another query holds most of it, spills where it can, holds what
// it cannot, and answers as it does alone, on one thread and on four.
//
// Each holds, alone, its peak S of a budget of 3S/2, and another query 5S/4 of it, so
// it may hold 3S/4, its even share. A query that spills holds more than its peak
// alone, the rows' ordinals and its files' buffers; half as much again leaves it
// that.
//
// A sort spills, a median of the whole frame its values, and a join by a predicate
// alone, a cross join and a filter, its build side whole. An implode of the whole
// frame, a join's build of one key, and a cumulative_eval with no partition_by and
// over one key, each of whose rows reads its whole prefix, cannot divide what they
// hold: each refused before step 395 once it held more than it might, at once, or at
// the depth where the key's or the partition's rows could go no further.
func TestAnEnginesPressureSpillsAndNeverFails(t *testing.T) {
	ctx := t.Context()
	big := engineFrame(t, 20_000, 50)
	one := engineFrame(t, 20_000, 1)
	small := engineFrame(t, 3_000, 1)
	c := ursus.Col
	probe := ursus.Frame(ursus.Values("k", []int64{0, 1}), ursus.Values("w", []int64{7, 8}))
	keepSides := ursus.DefaultOptFlags()
	keepSides.BuildSide = false // the larger side, on the right, is the one built
	cases := []struct {
		name  string
		lf    *ursus.LazyFrame
		spill int // spills on up to this many threads: a parallel aggregate may take its
		// whole input, at this size, before it folds to one sink, which spills
		order []ursustest.Option // a spilled join's rows come in an order of their own
		opts  []ursus.CollectOption
	}{
		{"a sort", big.Lazy().Sort(ursus.Asc(c("v")), ursus.Asc(c("k"))), 4, nil, nil},
		{"a median of the whole frame", big.Lazy().Select(c("v").Median()), 1, nil, nil},
		{"an implode of the whole frame", big.Lazy().Select(c("v").Implode()), 0, nil, nil},
		{"a join's build of one key", probe.Join(one.Lazy(), ursus.JoinOn(c("k")),
			ursus.JoinHow(ursus.JoinLeft)), 0, []ursustest.Option{ursustest.IgnoreRowOrder()}, nil},
		{"a join by a predicate alone", probe.JoinWhere(small.Lazy().Select(c("v").Alias("bv")),
			c("w").Lt(c("bv"))), 4, []ursustest.Option{ursustest.IgnoreRowOrder()},
			[]ursus.CollectOption{ursus.WithOptFlags(keepSides)}},
		{"a cumulative_eval with no partition_by", small.Lazy().WithColumns(
			c("v").CumulativeEval(ursus.Element().Sum()).Alias("r")), 0, nil, nil},
		{"a cumulative_eval over one key", small.Lazy().WithColumns(
			c("v").CumulativeEval(ursus.Element().Sum()).Over(c("k")).Alias("r")), 0, nil, nil},
	}
	for _, cs := range cases {
		for _, threads := range []int{1, 4} {
			opts := append([]ursus.CollectOption{ursus.WithThreads(threads), ursus.WithBatchSize(1024)}, cs.opts...)
			var alone ursus.MemoryStats
			want, err := cs.lf.Collect(ctx, append(opts, ursus.WithEngine(ursus.NewEngine(ursus.WithEngineMemoryLimit(0))),
				ursus.WithMemoryStats(&alone))...)
			if err != nil {
				t.Fatalf("%s: %v", cs.name, err)
			}
			s := alone.Peak
			e := ursus.NewEngine(ursus.WithEngineMemoryLimit(s*3/2), ursus.WithEngineSpillDir(t.TempDir()))
			opts = append(opts, ursus.WithEngine(e))

			var st ursus.MemoryStats
			got, err := cs.lf.Collect(ctx, append(opts, ursus.WithMemoryStats(&st))...)
			if err != nil {
				t.Fatalf("%s on %d threads, alone on a budget of 3/2 its peak: %v", cs.name, threads, err)
			}
			if st.Spills != 0 {
				t.Errorf("%s on %d threads spilled alone, so the pressure below proves nothing", cs.name, threads)
			}
			ursustest.AssertFrameEqual(t, got, want)

			release := ursus.Press(e, s*5/4)
			st = ursus.MemoryStats{}
			got, err = cs.lf.Collect(ctx, append(opts, ursus.WithMemoryStats(&st))...)
			release()
			if err != nil {
				t.Errorf("%s on %d threads, another query holding 5/4 of its peak: %v", cs.name, threads, err)
				continue
			}
			if threads <= cs.spill && st.Spills == 0 {
				t.Errorf("%s on %d threads did not spill where another query held most of the budget",
					cs.name, threads)
			}
			ursustest.AssertFrameEqual(t, got, want, cs.order...)
			if s := e.Stats(); s.Running != 0 || s.Held != 0 {
				t.Errorf("%s left its engine at %+v", cs.name, s)
			}
		}
	}
}

// TestTwoQueriesOnAnEngineShareIt: a sort paused halfway holds most of an engine's
// budget, and a second sort on the engine spills where it does not alone. A query
// given WithMemoryLimit is apart from its engine: it neither spills for the paused
// one nor is counted among the engine's.
func TestTwoQueriesOnAnEngineShareIt(t *testing.T) {
	ctx := t.Context()
	df := engineFrame(t, 100_000, 100)
	q := df.Lazy().Sort(ursus.Asc(ursus.Col("v")))
	var alone ursus.MemoryStats
	want, err := q.Collect(ctx, ursus.WithEngine(ursus.NewEngine(ursus.WithEngineMemoryLimit(0))),
		ursus.WithMemoryStats(&alone))
	if err != nil {
		t.Fatal(err)
	}
	e := ursus.NewEngine(ursus.WithEngineMemoryLimit(alone.Peak*3/2), ursus.WithEngineSpillDir(t.TempDir()))
	on := ursus.WithEngine(e)

	next, stop := iter.Pull2(q.CollectBatches(ctx, on, ursus.WithBatchSize(1000)))
	defer stop()
	if _, err, ok := next(); !ok || err != nil {
		t.Fatalf("the first batch: %v", err)
	}
	if s := e.Stats(); s.Running != 1 || s.Held < alone.Peak/2 || s.Spills != 0 {
		t.Fatalf("the paused sort: %+v, its peak alone %d", s, alone.Peak)
	}

	var st ursus.MemoryStats
	got, err := q.Collect(ctx, on, ursus.WithMemoryStats(&st))
	if err != nil {
		t.Fatal(err)
	}
	if st.Spills == 0 {
		t.Error("the second sort did not spill beside the first")
	}
	ursustest.AssertFrameEqual(t, got, want)
	if s := e.Stats(); s.Spills != st.Spills || s.Running != 1 {
		t.Errorf("the engine's %+v, the second sort's %d spills", s, st.Spills)
	}

	st = ursus.MemoryStats{}
	var buf bytes.Buffer
	if err := q.WriteCSV(ctx, &buf, on, ursus.WithMemoryLimit(alone.Peak*3/2),
		ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st)); err != nil {
		t.Fatal(err)
	}
	if st.Spills != 0 {
		t.Error("a query of a limit of its own spilled for the engine's other query")
	}
	stop()
	if s := e.Stats(); s.Running != 0 || s.Held != 0 {
		t.Errorf("after both: %+v", s)
	}
}

// TestAnEngineCountsAScansPages: a Parquet scan's pages, which its query does not
// hold, count on its engine while it reads them (step 400): a scan of 200,000 rows,
// filtered to none by a predicate no statistic answers, holds nothing, and its engine
// peaks at a page of its column; once it ends, the engine holds none.
func TestAnEngineCountsAScansPages(t *testing.T) {
	df := engineFrame(t, 200_000, 1_000)
	var pq bytes.Buffer
	if err := df.Lazy().WriteParquet(t.Context(), &pq); err != nil {
		t.Fatal(err)
	}
	c := ursus.Col
	for _, threads := range []int{1, 4} {
		e := ursus.NewEngine(ursus.WithEngineThreads(threads))
		got, err := ursus.ScanParquetBytes(pq.Bytes(), "t.parquet").Filter(c("k").Add(c("v")).Lt(int64(0))).
			Collect(t.Context(), ursus.WithEngine(e))
		if err != nil {
			t.Fatal(err)
		}
		// A row group's column of 200,000 Int64s, or its share of it, decompressed: 1.6
		// MB at most, and a page of it at the least.
		s := e.Stats()
		t.Logf("%d threads: the engine's peak %d bytes", threads, s.Peak)
		if got.Height() != 0 || s.Peak < 64<<10 || s.Held != 0 {
			t.Errorf("%d threads: %d rows, the engine's peak %d bytes, %d held after", threads, got.Height(), s.Peak, s.Held)
		}
	}
}
