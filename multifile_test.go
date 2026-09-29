package ursus_test

// Several files against one: the same twelve rows, whole in one file and split
// into three parts whose columns are in three different orders, must read the same
// way — as Parquet and as CSV, with and without a schema, on one thread and on four
// with small batches so staged batches span files.
//
// The parts' columns have disjoint ranges, so a part read by position gets values
// of the wrong column, and a filter pruned on the first file's layout skips the
// wrong row groups. Both are silent, and both are I1 or I7 (audit.md §5).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/ursustest"
)

// twelveRows is a in 1-12, b in 101-112, s in "s01"-"s12".
func twelveRows(lo, hi int) *ursus.LazyFrame {
	var a, b []int64
	var s []string
	for i := lo; i < hi; i++ {
		a = append(a, int64(i+1))
		b = append(b, int64(101+i))
		s = append(s, fmt.Sprintf("s%02d", i+1))
	}
	return ursus.Frame(ursus.Values("a", a), ursus.Values("b", b), ursus.Values("s", s))
}

// partSets are the column orders of the three parts. "swapped" exchanges only a
// and b, which share a type, so a part read by position is silently WRONG rather
// than refused; "permuted" also moves s, so a type differs at some position.
var partSets = []struct {
	name   string
	orders [][]string
}{
	{"swapped", [][]string{{"a", "b", "s"}, {"b", "a", "s"}, {"a", "b", "s"}}},
	{"permuted", [][]string{{"a", "b", "s"}, {"b", "s", "a"}, {"s", "a", "b"}}},
}

func cols(names []string) []ursus.Expr {
	out := make([]ursus.Expr, len(names))
	for i, n := range names {
		out[i] = ursus.Col(n)
	}
	return out
}

func multiFileQueries() []struct {
	name string
	q    func(*ursus.LazyFrame) *ursus.LazyFrame
} {
	c := ursus.Col
	qs := []struct {
		name string
		q    func(*ursus.LazyFrame) *ursus.LazyFrame
	}{
		{"bare", func(l *ursus.LazyFrame) *ursus.LazyFrame { return l }},
		{"select s, a", func(l *ursus.LazyFrame) *ursus.LazyFrame { return l.Select(c("s"), c("a")) }},
		{"head 5", func(l *ursus.LazyFrame) *ursus.LazyFrame { return l.Head(5) }},
	}
	for _, v := range []int64{2, 6, 11} {
		qs = append(qs,
			struct {
				name string
				q    func(*ursus.LazyFrame) *ursus.LazyFrame
			}{fmt.Sprintf("a == %d", v), func(l *ursus.LazyFrame) *ursus.LazyFrame { return l.Filter(c("a").Eq(v)) }},
			struct {
				name string
				q    func(*ursus.LazyFrame) *ursus.LazyFrame
			}{fmt.Sprintf("b > %d", 100+v), func(l *ursus.LazyFrame) *ursus.LazyFrame { return l.Filter(c("b").Gt(100 + v)) }},
		)
	}
	return qs
}

// knownMultiFileMismatches counts the queries several files answer differently
// from one, per defect. Checked both ways.
var knownMultiFileMismatches = map[string]knownMismatch{
	"I7": {60, "every CSV part is read by position against the first header"},
}

func TestSeveralFilesReadAsOne(t *testing.T) {
	dir := t.TempDir()
	whole := twelveRows(0, 12)

	pqWhole := sinkParquet(t, dir, "whole.parquet", whole, ursus.WithRowGroupRows(2))
	csvWhole := filepath.Join(dir, "whole.csv")
	if err := whole.SinkCSV(t.Context(), csvWhole); err != nil {
		t.Fatal(err)
	}
	sch := mustSchema(dtype.Of("a", dtype.Int64), dtype.Of("b", dtype.Int64), dtype.Of("s", dtype.String))
	type source struct {
		name, class  string
		parts, whole func() *ursus.LazyFrame
	}
	var sources []source
	for _, set := range partSets {
		var pqParts, csvParts []string
		for i, order := range set.orders {
			part := twelveRows(4*i, 4*i+4).Select(cols(order)...)
			pqParts = append(pqParts, sinkParquet(t, dir, fmt.Sprintf("%s%d.parquet", set.name, i), part,
				ursus.WithRowGroupRows(2)))
			p := filepath.Join(dir, fmt.Sprintf("%s%d.csv", set.name, i))
			if err := part.SinkCSV(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			csvParts = append(csvParts, p)
		}
		sources = append(sources,
			source{"parquet, " + set.name, "I1",
				func() *ursus.LazyFrame { return ursus.ScanParquetFiles(pqParts) },
				func() *ursus.LazyFrame { return ursus.ScanParquet(pqWhole) }},
			source{"csv, " + set.name, "I7",
				func() *ursus.LazyFrame { return ursus.ScanCSVFiles(csvParts) },
				func() *ursus.LazyFrame { return ursus.ScanCSV(csvWhole) }},
			source{"csv with a schema, " + set.name, "I7",
				func() *ursus.LazyFrame { return ursus.ScanCSVFiles(csvParts, ursus.WithSchema(sch)) },
				func() *ursus.LazyFrame { return ursus.ScanCSV(csvWhole, ursus.WithSchema(sch)) }},
		)
	}
	runs := []struct {
		name string
		opts []ursus.CollectOption
	}{
		{"1 thread", []ursus.CollectOption{ursus.WithThreads(1)}},
		{"4 threads, batch 5", []ursus.CollectOption{ursus.WithThreads(4), ursus.WithBatchSize(5)}},
	}

	var ms []mismatch
	queries := 0
	for _, src := range sources {
		for _, q := range multiFileQueries() {
			for _, run := range runs {
				queries++
				name := src.name + " | " + q.name + " | " + run.name
				want, err := q.q(src.whole()).Collect(t.Context(), run.opts...)
				if err != nil {
					t.Fatalf("%s: the whole file: %v", name, err)
				}
				got, err := collectRecovered(t.Context(), q.q(src.parts()), run.opts...)
				var diff string
				switch {
				case err != nil:
					diff = "fails: " + firstLine(err.Error())
				case want.Height() == 0:
					t.Fatalf("%s returns no rows, so it compares nothing", name)
				default:
					diff = framesDiffer(t, got, want, ursustest.CheckNullability())
				}
				if diff != "" {
					ms = append(ms, mismatch{name: name, detail: diff, class: src.class})
				}
			}
		}
	}
	t.Logf("%d queries: %d mismatches", queries, len(ms))
	for _, p := range judge(ms, knownMultiFileMismatches) {
		t.Error(p)
	}
}

// collectRecovered is collectOnce with options: a panic is an error, not a dead
// test binary.
func collectRecovered(ctx context.Context, lf *ursus.LazyFrame, opts ...ursus.CollectOption) (df *ursus.DataFrame, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return lf.Collect(ctx, opts...)
}

// knownMultiFileDefects names each refusal case that is not refused today.
var knownMultiFileDefects = map[string]string{
	"csv duplicate header": "read without error",
}

// TestSeveralFilesThatDifferAreRefused: files whose columns differ in anything
// but order are refused with ErrSchema, naming both. The by-hand test covers
// missing, extra, renamed, and Int32 or String for Int64; these are the rest.
func TestSeveralFilesThatDifferAreRefused(t *testing.T) {
	st := func(t *testing.T, a, b string) schema.FieldList {
		return schema.FieldList{structNode(t, "st", opt, i64Node(t, a, opt), i64Node(t, b, opt))}
	}
	stRow := []rawCol{{vals: []int64{1}, defs: []int16{2}}, {vals: []int64{2}, defs: []int16{2}}}
	datetime := func(u ursus.TimeUnit, tz string) *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("t", []int64{1})).
			Select(ursus.Col("t").Cast(ursus.Datetime(u, tz)))
	}
	cases := []struct {
		name  string
		lf    func(t *testing.T, dir string) *ursus.LazyFrame
		check func(context.Context, *ursus.LazyFrame) error
	}{
		{"parquet Datetime unit", func(t *testing.T, dir string) *ursus.LazyFrame {
			return ursus.ScanParquetFiles([]string{
				sinkParquet(t, dir, "p1.parquet", datetime(ursus.Micro, "UTC")),
				sinkParquet(t, dir, "p2.parquet", datetime(ursus.Milli, "UTC"))})
		}, refused(true, "p1.parquet", "p2.parquet")},
		{"parquet Datetime zone", func(t *testing.T, dir string) *ursus.LazyFrame {
			return ursus.ScanParquetFiles([]string{
				sinkParquet(t, dir, "p1.parquet", datetime(ursus.Micro, "UTC")),
				sinkParquet(t, dir, "p2.parquet", datetime(ursus.Micro, ""))})
		}, refused(true, "p1.parquet", "p2.parquet")},
		{"parquet struct field renamed", func(t *testing.T, dir string) *ursus.LazyFrame {
			return ursus.ScanParquetFiles([]string{
				writeRaw(t, dir, "p1.parquet", st(t, "a", "b"), stRow),
				writeRaw(t, dir, "p2.parquet", st(t, "a", "c"), stRow)})
		}, refused(true, "p1.parquet", "p2.parquet")},
		// A struct is one type; its fields in another order are another type.
		{"parquet struct fields reordered", func(t *testing.T, dir string) *ursus.LazyFrame {
			return ursus.ScanParquetFiles([]string{
				writeRaw(t, dir, "p1.parquet", st(t, "a", "b"), stRow),
				writeRaw(t, dir, "p2.parquet", st(t, "b", "a"), stRow)})
		}, refused(true, "p1.parquet", "p2.parquet")},
		{"parquet list element type", func(t *testing.T, dir string) *ursus.LazyFrame {
			i32, err := schema.NewPrimitiveNodeLogical("element", opt, schema.NewIntLogicalType(32, true),
				parquet.Types.Int32, -1, -1)
			if err != nil {
				t.Fatal(err)
			}
			return ursus.ScanParquetFiles([]string{
				writeRaw(t, dir, "p1.parquet", schema.FieldList{listNode(t, "l", opt, i64Node(t, "element", opt))},
					[]rawCol{{vals: []int64{1}, defs: []int16{3}, reps: []int16{0}}}),
				writeRaw(t, dir, "p2.parquet", schema.FieldList{listNode(t, "l", opt, i32)},
					[]rawCol{{vals: []int32{1}, defs: []int16{3}, reps: []int16{0}}})})
		}, refused(true, "p1.parquet", "p2.parquet")},
		{"csv duplicate header", func(t *testing.T, dir string) *ursus.LazyFrame {
			return ursus.ScanCSVFiles([]string{writeText(t, dir, "a.csv", "a,b\n1,10\n"),
				writeText(t, dir, "b.csv", "a,a\n2,20\n")})
		}, refused(false, "a.csv", "b.csv")},
		// Planned, then rewritten with its columns in another order before it is
		// read. The layout is the one in the footer being read, not the one seen
		// at plan time, so it is still read by name.
		{"parquet rewritten after planning", func(t *testing.T, dir string) *ursus.LazyFrame {
			ab := func(a, b []int64) *ursus.LazyFrame {
				return ursus.Frame(ursus.Values("a", a), ursus.Values("b", b))
			}
			p1 := sinkParquet(t, dir, "p1.parquet", ab([]int64{1}, []int64{10}))
			p2 := sinkParquet(t, dir, "p2.parquet", ab([]int64{2}, []int64{20}))
			lf := ursus.ScanParquetFiles([]string{p1, p2})
			if _, err := lf.CollectSchema(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(p2); err != nil {
				t.Fatal(err)
			}
			sinkParquet(t, dir, "p2.parquet",
				ursus.Frame(ursus.Values("b", []int64{20}), ursus.Values("a", []int64{2})))
			return lf
		}, reads(map[string][]string{"a": {"1", "2"}, "b": {"10", "20"}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.check(t.Context(), tc.lf(t, t.TempDir()))
			why, known := knownMultiFileDefects[tc.name]
			switch {
			case known && err == nil:
				t.Errorf("answers correctly now; delete it from knownMultiFileDefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %v", why, err)
			case err != nil:
				t.Error(err)
			}
		})
	}
	for name := range knownMultiFileDefects {
		if !slices.ContainsFunc(cases, func(c struct {
			name  string
			lf    func(t *testing.T, dir string) *ursus.LazyFrame
			check func(context.Context, *ursus.LazyFrame) error
		}) bool {
			return c.name == name
		}) {
			t.Errorf("knownMultiFileDefects names %q, which is not a case", name)
		}
	}
}
