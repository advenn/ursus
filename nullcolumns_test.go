package ursus_test

// A Null column, and a batch of no columns, hold no memory a row (step 529): a column
// of nulls is its type and its length, its validity bitmap.Zeros' all-clear form,
// which has no storage, and a scan whose result is collected or counted hands a
// batch of only Null columns, or of none, whole. That changes memory only, never an
// answer, and the differential here holds it so: small generated frames holding Null
// columns, through the frame operations qcheck draws and more, at the configurations
// its self check varies, answer exactly as they do with the bits stored and every
// batch cut at the batch size (bitmap.MaterialiseZeros, ursus.CutWholeBatches), the
// behaviour before. qcheck's own generator holds six base types and no Null, so its
// self check cannot draw a Null column; this is its operations', over one.

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/ursustest"
)

// nullFrameRecords draws a frame of rows rows, as arrow-go records, split into
// records at random where split: k, an Int64 key, a value every eight rows or so; v,
// Float64 quarters,
// whose sums are exact in any order; s, short text; b, a Boolean; each with nulls;
// and n and z, Null columns; in an order drawn too.
func nullFrameRecords(r *rand.Rand, rows int, split bool) []arrow.RecordBatch {
	mem := memory.NewGoAllocator()
	type col struct {
		f arrow.Field
		a arrow.Array
	}
	null := func() bool { return r.IntN(8) == 0 }
	kb, vb, sb, bb := array.NewInt64Builder(mem), array.NewFloat64Builder(mem),
		array.NewStringBuilder(mem), array.NewBooleanBuilder(mem)
	for range rows {
		if null() {
			kb.AppendNull()
		} else {
			kb.Append(int64(r.IntN(rows/8+3)) - 1)
		}
		if null() {
			vb.AppendNull()
		} else {
			vb.Append(float64(r.IntN(400)-200) / 4)
		}
		if null() {
			sb.AppendNull()
		} else {
			sb.Append([]string{"a", "b", "cc", ""}[r.IntN(4)])
		}
		if null() {
			bb.AppendNull()
		} else {
			bb.Append(r.IntN(2) == 0)
		}
	}
	cols := []col{
		{arrow.Field{Name: "k", Type: arrow.PrimitiveTypes.Int64, Nullable: true}, kb.NewArray()},
		{arrow.Field{Name: "v", Type: arrow.PrimitiveTypes.Float64, Nullable: true}, vb.NewArray()},
		{arrow.Field{Name: "s", Type: arrow.BinaryTypes.String, Nullable: true}, sb.NewArray()},
		{arrow.Field{Name: "b", Type: arrow.FixedWidthTypes.Boolean, Nullable: true}, bb.NewArray()},
		{arrow.Field{Name: "n", Type: arrow.Null, Nullable: true}, array.NewNull(rows)},
		{arrow.Field{Name: "z", Type: arrow.Null, Nullable: true}, array.NewNull(rows)},
	}
	r.Shuffle(len(cols), func(i, j int) { cols[i], cols[j] = cols[j], cols[i] })
	fields := make([]arrow.Field, len(cols))
	arrs := make([]arrow.Array, len(cols))
	for i, c := range cols {
		fields[i], arrs[i] = c.f, c.a
	}
	rec := array.NewRecordBatch(arrow.NewSchema(fields, nil), arrs, int64(rows))
	if !split || rows < 2 {
		return []arrow.RecordBatch{rec}
	}
	var recs []arrow.RecordBatch
	for lo := 0; lo < rows; {
		hi := min(rows, lo+1+r.IntN(rows/2+1))
		recs = append(recs, rec.NewSlice(int64(lo), int64(hi)))
		lo = hi
	}
	return recs
}

// nullQuery is a frame operation over a frame of nullFrameRecords' columns, other a
// second such frame; ordered says whether its answer's row order is promised.
type nullQuery struct {
	name    string
	ordered bool
	q       func(lf, other *ursus.LazyFrame) *ursus.LazyFrame
}

// refusedNull are the queries ursus refuses, a Null column being no key and having no
// order.
var refusedNull = map[string]bool{
	"WithColumns over n": true, "GroupBy n": true, "Min of n": true, "Sort by n": true,
	"Unique of n": true, "Join left on n": true,
}

func nullQueries() []nullQuery {
	k, v, n, z := ursus.Col("k"), ursus.Col("v"), ursus.Col("n"), ursus.Col("z")
	inner := ursus.JoinHow(ursus.JoinInner)
	return []nullQuery{
		{"the scan", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf }},
		{"Select n", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Select(n) }},
		{"Select n, z", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Select(n, z.Alias("m")) }},
		{"Select n, k", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Select(n.Alias("m"), k) }},
		{"Select nothing", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Select() }},
		{"Select null tests", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Select(n.IsNull().Alias("a"), n.IsNotNull().Alias("b"), ursus.Null(ursus.NullT).Alias("c"))
		}},
		{"Filter k", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Filter(k.Gt(0)) }},
		{"Filter n is null", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Filter(n.IsNull()) }},
		{"Filter n is not null", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Filter(n.IsNotNull()) }},
		{"Filter n is null, Select n", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(n.IsNull().And(k.Gt(0))).Select(n, z)
		}},
		{"WithColumns of n", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.WithColumns(n.FillNullWith(int64(7)).Alias("f"), n.Cast(ursus.Int64).Alias("c"),
				k.Add(n).Alias("a"), n.Eq(k).Alias("e"), n.Shift(1).Alias("h"))
		}},
		{"WithColumns When", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.WithColumns(ursus.When(k.Gt(1)).Then(v).Otherwise(n).Alias("w"),
				ursus.When(n.IsNull()).Then(n).Otherwise(z).Alias("x"))
		}},
		{"WithColumns over", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.WithColumns(n.Count().Over(k).Alias("c"), n.First().Over(k).Alias("f"))
		}},
		{"WithColumns over n", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.WithColumns(v.Sum().Over(n).Alias("s"))
		}},
		{"GroupBy k, aggregates of n", false, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(k).Agg(n.Count().Alias("c"), n.NullCount().Alias("nc"), ursus.Len().Alias("l"),
				n.First().Alias("f"), z.Last().Alias("la"), v.Sum().Alias("s"), v.Mean().Alias("me"))
		}},
		{"GroupBy k, in order", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(k).MaintainOrder().Agg(n.Last().Alias("l"), z.Count().Alias("c"))
		}},
		{"GroupBy n", false, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(n).Agg(ursus.Len().Alias("l"), k.Sum().Alias("s"))
		}},
		{"Min of n", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(k).Agg(n.Min().Alias("m"))
		}},
		{"GroupBy nothing", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Select(n.Count().Alias("c"), n.NullCount().Alias("nc"), ursus.Len().Alias("l"),
				n.First().Alias("f"), z.Last().Alias("la"))
		}},
		{"Sort, n carried", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Sort(ursus.Desc(k), ursus.Asc(v), ursus.Asc(ursus.Col("s")), ursus.Asc(ursus.Col("b")))
		}},
		{"Sort by n", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Sort(ursus.Asc(n)) }},
		{"Head", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Head(5) }},
		{"Head of n", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Select(n).Head(300) }},
		{"Slice", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Slice(3, 100) }},
		{"Slice of n", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Select(z, n).Slice(7, 1000) }},
		{"Slice from the end", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Slice(-5, 3) }},
		{"Tail", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Tail(4) }},
		{"Reverse", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Reverse() }},
		{"WithRowIndex", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.WithRowIndex("i", 0) }},
		{"Unique of k, s, n carried", false, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Select(n, k, ursus.Col("s")).Unique("k", "s")
		}},
		{"Unique of n", false, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.Unique("n") }},
		{"DropNulls of n", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame { return lf.DropNulls("n") }},
		{"Unpivot", true, func(lf, _ *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Unpivot(ursus.UnpivotOptions{On: []string{"n", "v"}, Index: []string{"k"}})
		}},
		{"Join inner on k", false, func(lf, o *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Join(o, ursus.JoinOn(k), inner)
		}},
		{"Join left on k, in order", true, func(lf, o *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Join(o, ursus.JoinOn(k), ursus.JoinHow(ursus.JoinLeft), ursus.JoinMaintainOrder(ursus.JoinOrderLeft))
		}},
		{"Join full on k", false, func(lf, o *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Join(o, ursus.JoinOn(k), ursus.JoinHow(ursus.JoinFull))
		}},
		{"Join semi on k", true, func(lf, o *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Join(o, ursus.JoinOn(k), ursus.JoinHow(ursus.JoinSemi))
		}},
		{"Join anti on k", true, func(lf, o *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Join(o, ursus.JoinOn(k), ursus.JoinHow(ursus.JoinAnti))
		}},
		{"Join left on n", false, func(lf, o *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Join(o, ursus.JoinOn(n), ursus.JoinHow(ursus.JoinLeft))
		}},
		{"Concat by name", true, func(lf, o *ursus.LazyFrame) *ursus.LazyFrame {
			return ursus.Concat([]*ursus.LazyFrame{lf, o}, ursus.WithConcatMode(ursus.ConcatDiagonal))
		}},
		{"Concat of n", true, func(lf, o *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Select(n).Concat(o.Select(n), lf.Select(z.Alias("n")))
		}},
		{"Concat diagonal", true, func(lf, o *ursus.LazyFrame) *ursus.LazyFrame {
			return ursus.Concat([]*ursus.LazyFrame{lf.Select(n, k), o.Select(v, z)},
				ursus.WithConcatMode(ursus.ConcatDiagonal))
		}},
	}
}

// nullConfig is one configuration the self check varies.
type nullConfig struct {
	threads, batch int
	noOpt          bool
}

func (c nullConfig) String() string {
	return fmt.Sprintf("threads %d, batches of %d, optimizer off %v", c.threads, c.batch, c.noOpt)
}

func (c nullConfig) opts() []ursus.CollectOption {
	o := []ursus.CollectOption{ursus.WithThreads(c.threads), ursus.WithBatchSize(c.batch)}
	if c.noOpt {
		o = append(o, ursus.WithOptFlags(ursus.NoOptFlags()))
	}
	return o
}

// nullAnswer is a query's answer under the current representation: its frame, its
// Count, and its CollectBatches stacked.
type nullAnswer struct {
	df, batches *ursus.DataFrame
	count       int64
	err         error
}

func answerNulls(ctx context.Context, lf *ursus.LazyFrame, cfg nullConfig) nullAnswer {
	var a nullAnswer
	if a.df, a.err = lf.Collect(ctx, cfg.opts()...); a.err != nil {
		return a
	}
	if a.count, a.err = lf.Count(ctx, cfg.opts()...); a.err != nil {
		return a
	}
	var parts []*ursus.DataFrame
	for b, err := range lf.CollectBatches(ctx, cfg.opts()...) {
		if err != nil {
			a.err = err
			return a
		}
		parts = append(parts, b)
	}
	if len(parts) == 0 {
		a.batches, a.err = a.df.Head(ctx, 0)
		return a
	}
	a.batches, a.err = ursus.ConcatFrames(ctx, parts)
	return a
}

// TestNullColumnsAnswerAsBefore: each of nullQueries over frames of nullFrameRecords,
// scanned from arrow-go's records in one or several, and from a DataFrame in memory,
// at threads {1, 4, 8}, batches of {1, 2, 3, 7, 64, 1024, 65536} rows and the
// optimizer on and off, answers with Null columns held as their length, and batches
// of them handed whole, exactly as with their bits stored and every batch cut: the
// frame Collect answers, its Count, and its CollectBatches stacked; and the three
// agree with each other.
func TestNullColumnsAnswerAsBefore(t *testing.T) {
	ctx := context.Background()
	configs := []nullConfig{
		{1, 8192, false}, {4, 7, false}, {8, 64, true}, {1, 1, false}, {4, 1024, true},
		{8, 2, false}, {1, 65536, true}, {4, 3, false},
	}
	queries := nullQueries()
	nonEmpty := map[string]bool{}
	cases := 0
	for seed := range uint64(6) {
		r := rand.New(rand.NewPCG(529, 100+seed))
		rows := []int{0, 1, 3, 17, 300, 1000}[seed]
		left := nullFrameRecords(r, rows, seed%2 == 1)
		right := nullFrameRecords(r, []int{5, 0, 40, 300, 9, 64}[seed], seed%3 == 0)
		mem, err := ursus.ScanArrowRecords(left...).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, src := range []string{"records", "memory"} {
			scan := func() (*ursus.LazyFrame, *ursus.LazyFrame) {
				if src == "memory" {
					return mem.Lazy(), ursus.ScanArrowRecords(right...)
				}
				return ursus.ScanArrowRecords(left...), ursus.ScanArrowRecords(right...)
			}
			for qi, q := range queries {
				cfg := configs[(int(seed)*len(queries)+qi)%len(configs)]
				if rows > 17 && cfg.batch < 64 {
					cfg.batch *= 9 // a batch of a row or two is for the small frames
				}
				for _, c := range []nullConfig{configs[0], cfg} {
					ctx := fmt.Sprintf("seed %d, %d rows from %s, %s, %v", seed, rows, src, q.name, c)
					lf, other := scan()
					after := answerNulls(context.Background(), q.q(lf, other), c)
					bitmap.MaterialiseZeros.Store(true)
					restore := ursus.CutWholeBatches()
					lf, other = scan()
					before := answerNulls(context.Background(), q.q(lf, other), c)
					restore()
					bitmap.MaterialiseZeros.Store(false)
					cases++
					sameNullAnswer(t, ctx, q.ordered, after, before)
					// Unique refuses a Null key once a batch reaches it, so not of no rows.
					if refusedNull[q.name] && rows > 0 && after.err == nil || !refusedNull[q.name] && after.err != nil {
						t.Fatalf("%s: refused %v, want %v", ctx, after.err, refusedNull[q.name])
					}
					if after.df != nil && after.df.Height() > 0 {
						nonEmpty[q.name] = true
					}
				}
			}
		}
	}
	// Anti-vacuity: every query answers rows somewhere, but the two that keep the
	// rows where a Null column holds a value, which no row does.
	for _, q := range queries {
		if !refusedNull[q.name] && !nonEmpty[q.name] && !strings.Contains(q.name, "is not null") && q.name != "DropNulls of n" {
			t.Errorf("%s answered no rows over every frame", q.name)
		}
	}
	t.Logf("%d answers compared", cases)
}

// sameNullAnswer fails t unless got, an answer with step 529, is want, the answer
// before: the same refusal, or the same frame, Count and stacked batches, in order
// where the query promises one; and got's three agree.
func sameNullAnswer(t *testing.T, ctx string, ordered bool, got, want nullAnswer) {
	t.Helper()
	if (got.err == nil) != (want.err == nil) || got.err != nil && got.err.Error() != want.err.Error() {
		t.Fatalf("%s: %v, and before %v", ctx, got.err, want.err)
	}
	if got.err != nil {
		return
	}
	var opts []ursustest.Option
	if !ordered {
		opts = append(opts, ursustest.IgnoreRowOrder())
	}
	if got.count != want.count || got.count != int64(got.df.Height()) {
		t.Fatalf("%s: Count %d and %d rows, and before Count %d", ctx, got.count, got.df.Height(), want.count)
	}
	t.Run(ctx, func(t *testing.T) {
		ursustest.AssertFrameEqual(t, got.df, want.df, opts...)
		ursustest.AssertFrameEqual(t, got.batches, want.batches, opts...)
		ursustest.AssertFrameEqual(t, got.batches, got.df, opts...)
	})
}

// TestNullBatchesAreCutForOtherOperators: a scan hands a batch of only Null columns
// whole only where Collect or Count is reached through operators that keep a Null
// column a length; under any other operator its batches are cut at the batch size,
// which operators rely on (source.ScanSpec.BatchSize). A row index is such an
// operator, a udf over it sees each batch it is handed, and none is longer than the
// batch size, over records of 50,000 Null rows, scanned from arrow-go's records, an
// IPC stream and a DataFrame in memory, with a row-blind operator below the row
// index or none; and a projection of an expression is another, a udf in it seeing
// the scan's batches directly.
func TestNullBatchesAreCutForOtherOperators(t *testing.T) {
	ctx := context.Background()
	schema := arrow.NewSchema([]arrow.Field{{Name: "n", Type: arrow.Null, Nullable: true},
		{Name: "z", Type: arrow.Null, Nullable: true}}, nil)
	var recs []arrow.RecordBatch
	for range 3 {
		recs = append(recs, array.NewRecordBatch(schema,
			[]arrow.Array{array.NewNull(50_000), array.NewNull(50_000)}, 50_000))
	}
	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(schema))
	for _, rec := range recs {
		if err := w.Write(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	mem, err := ursus.ScanArrowRecords(recs...).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]func() *ursus.LazyFrame{
		"records": func() *ursus.LazyFrame { return ursus.ScanArrowRecords(recs...) },
		"stream":  func() *ursus.LazyFrame { return ursus.ScanIPCStreamBytes(buf.Bytes(), "s") },
		"memory":  func() *ursus.LazyFrame { return mem.Lazy() },
	}
	n := ursus.Col("n")
	below := map[string]func(*ursus.LazyFrame) *ursus.LazyFrame{
		"nothing":     func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf },
		"a Select":    func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Select(n.Alias("m")) },
		"a Head":      func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Head(120_000) },
		"a Slice":     func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Slice(10, 120_000) },
		"a Concat":    func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Concat(lf) },
		"a Filter":    func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Filter(n.IsNull()) },
		"WithColumns": func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.WithColumns(n.IsNull().Alias("b")) },
	}
	const size = 1024
	for sname, src := range sources {
		longest := 0
		var mu sync.Mutex
		seen := n.IsNull().MapBatches("seen", ursus.Bool, func(v []bool, ok []bool) ([]bool, []bool, error) {
			mu.Lock()
			longest = max(longest, len(v))
			mu.Unlock()
			return v, ok, nil
		})
		if _, err := src().Select(seen).Collect(ctx, ursus.WithBatchSize(size)); err != nil {
			t.Fatalf("%s, a udf in a Select: %v", sname, err)
		}
		if longest > size || longest == 0 {
			t.Fatalf("%s: a udf in a Select saw batches of up to %d rows, at a batch size of %d", sname, longest, size)
		}
		for bname, op := range below {
			var mu sync.Mutex
			longest, rows := 0, 0
			seen := ursus.Col("i").MapBatches("seen", ursus.Uint32,
				func(v []uint32, ok []bool) ([]uint32, []bool, error) {
					mu.Lock()
					longest, rows = max(longest, len(v)), rows+len(v)
					mu.Unlock()
					return v, ok, nil
				})
			for _, threads := range []int{1, 4} {
				longest, rows = 0, 0
				df, err := op(src()).WithRowIndex("i", 0).Select(seen).
					Collect(ctx, ursus.WithBatchSize(size), ursus.WithThreads(threads))
				if err != nil {
					t.Fatalf("%s, a row index over %s: %v", sname, bname, err)
				}
				if longest > size || rows != df.Height() || rows == 0 {
					t.Fatalf("%s, a row index over %s, %d threads: the udf saw %d rows, in batches of up to %d, "+
						"of a frame of %d, at a batch size of %d", sname, bname, threads, rows, longest, df.Height(), size)
				}
			}
		}
	}
}
