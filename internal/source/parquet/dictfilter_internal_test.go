package parquet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/parquet"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowout"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/source"
)

// fakeEval stands in for the engine's evaluator over the conjunct shapes these
// tests push: a String column against a string literal by == or !=, and IsNull and
// IsNotNull. A null compared is null, as the engine answers.
func fakeEval(_ context.Context, n expr.Node, b *data.Batch) (*data.Column, error) {
	var col string
	var test func(v string, ok bool) (bool, bool) // (result, result is valid)
	switch t := n.(type) {
	case *expr.Binary:
		c, lit := t.L.(*expr.Col), t.R.(*expr.Lit)
		col = c.Name
		want := lit.Value.(string)
		eq := t.Op == expr.OpEq
		test = func(v string, ok bool) (bool, bool) { return (v == want) == eq, ok }
	case *expr.Unary:
		col = t.Child.(*expr.Col).Name
		isNull := t.Op == expr.OpIsNull
		test = func(_ string, ok bool) (bool, bool) { return !ok == isNull, true }
	default:
		return nil, fmt.Errorf("fakeEval: %s", n)
	}
	c, _ := b.ByName(col)
	ss, err := data.TypedColumn[string](c)
	if err != nil {
		return nil, err
	}
	vals, valid := bitmap.NewBuilder(c.Len()), bitmap.NewBuilder(c.Len())
	for i := range c.Len() {
		v, ok := ss.Get(i)
		r, rv := test(v, ok)
		vals.Append(r && rv)
		valid.Append(rv)
	}
	return data.NewBool("r", vals.Finish(), valid.Finish()), nil
}

// rowsOf reads path under spec, each row rendered, and with column s's value and
// s_req's alongside.
func rowsOf(t *testing.T, path string, spec source.ScanSpec) (rows []string, s, sReq []*string) {
	t.Helper()
	bs, err := openSource(t, path).Open(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer bs.Close()
	for {
		b, err := bs.Next(t.Context())
		if errors.Is(err, io.EOF) {
			return rows, s, sReq
		}
		if err != nil {
			t.Fatal(err)
		}
		rec, err := arrowout.Record(b)
		if err != nil {
			t.Fatal(err)
		}
		si, ri := rec.Schema().FieldIndices("s")[0], rec.Schema().FieldIndices("s_req")[0]
		for i := range int(rec.NumRows()) {
			var parts []string
			for _, c := range rec.Columns() {
				parts = append(parts, c.ValueStr(i))
			}
			rows = append(rows, strings.Join(parts, "|"))
			s = append(s, value(rec.Column(si).(*array.String), i))
			sReq = append(sReq, value(rec.Column(ri).(*array.String), i))
		}
		rec.Release()
	}
}

func value(a *array.String, i int) *string {
	if a.IsNull(i) {
		return nil
	}
	v := a.Value(i)
	return &v
}

// subsequence reports whether a's rows appear in b in order.
func subsequence(a, b []string) bool {
	j := 0
	for _, r := range b {
		if j < len(a) && a[j] == r {
			j++
		}
	}
	return j == len(a)
}

// TestDictionaryConjunctsDropOnlyFailingRows: string conjuncts handed to the reader
// with an evaluator (step 193).
//
// Read with them, every row that passes is there, in order, among a subsequence of
// the file's rows. Where every page of the columns is dictionary-encoded, the
// reader judges every row, nulls included, and only the passing rows are there.
// Under PLAIN pages it judges only the nulls, and the Filter above, absent here,
// would finish. On one thread and on four, in batches of 1,000.
func TestDictionaryConjunctsDropOnlyFailingRows(t *testing.T) {
	str := func(v string) *expr.Lit { return &expr.Lit{Value: v, DT: dtype.String} }
	cases := []struct {
		name  string
		preds []expr.Node
		keep  func(s, sReq *string) bool
	}{
		{"s == AIR", []expr.Node{&expr.Binary{Op: expr.OpEq, L: &expr.Col{Name: "s"}, R: str("AIR")}},
			func(s, _ *string) bool { return s != nil && *s == "AIR" }},
		{"s != TRUCK", []expr.Node{&expr.Binary{Op: expr.OpNe, L: &expr.Col{Name: "s"}, R: str("TRUCK")}},
			func(s, _ *string) bool { return s != nil && *s != "TRUCK" }},
		{"s is null", []expr.Node{&expr.Unary{Op: expr.OpIsNull, Child: &expr.Col{Name: "s"}}},
			func(s, _ *string) bool { return s == nil }},
		{"s is not null and s_req == MAIL", []expr.Node{
			&expr.Unary{Op: expr.OpIsNotNull, Child: &expr.Col{Name: "s"}},
			&expr.Binary{Op: expr.OpEq, L: &expr.Col{Name: "s_req"}, R: str("MAIL")}},
			func(s, r *string) bool { return s != nil && r != nil && *r == "MAIL" }},
		// As the planner hands a Filter over: one And over two columns.
		{"one And: s == TRUCK & s_req != x", []expr.Node{&expr.Binary{Op: expr.OpAnd,
			L: &expr.Binary{Op: expr.OpEq, L: &expr.Col{Name: "s"}, R: str("TRUCK")},
			R: &expr.Binary{Op: expr.OpNe, L: &expr.Col{Name: "s_req"}, R: str("x")}}},
			func(s, r *string) bool { return s != nil && *s == "TRUCK" && r != nil && *r != "x" }},
	}
	layouts := []struct {
		name  string
		exact bool
		props []parquet.WriterProperty
	}{
		{"v1 dictionary", true, []parquet.WriterProperty{parquet.WithDictionaryDefault(true)}},
		{"v2 dictionary, small pages", true, []parquet.WriterProperty{parquet.WithDictionaryDefault(true),
			parquet.WithDataPageVersion(parquet.DataPageV2), parquet.WithDataPageSize(512)}},
		{"plain", false, []parquet.WriterProperty{parquet.WithDictionaryDefault(false)}},
	}
	dir := t.TempDir()
	for _, l := range layouts {
		for _, nulls := range []string{"none", "scattered", "all"} {
			path := filepath.Join(dir, strings.ReplaceAll(l.name+"-"+nulls, " ", "_")+".parquet")
			writePagesFixture(t, path, 5000, nulls, l.props...)
			all, s, sReq := rowsOf(t, path, source.ScanSpec{Threads: 1, BatchSize: 1000})
			for _, c := range cases {
				var want []string
				for i, r := range all {
					if c.keep(s[i], sReq[i]) {
						want = append(want, r)
					}
				}
				for _, threads := range []int{1, 4} {
					name := fmt.Sprintf("%s, nulls %s, %s, %d threads", l.name, nulls, c.name, threads)
					got, _, _ := rowsOf(t, path, source.ScanSpec{Threads: threads, BatchSize: 1000,
						Predicate: c.preds, Eval: fakeEval})
					if l.exact {
						if strings.Join(got, "\n") != strings.Join(want, "\n") {
							t.Fatalf("%s: %d rows read, %d pass", name, len(got), len(want))
						}
						continue
					}
					// A present value on a PLAIN page is not judged: what is read is
					// every passing row, in order, among a subsequence of the file's.
					if !subsequence(want, got) || !subsequence(got, all) {
						t.Fatalf("%s: %d rows read, %d pass, of %d", name, len(got), len(want), len(all))
					}
				}
			}
		}
	}
}

// TestDictableIsOneStringColumnRowByRow: what the reader is handed.
func TestDictableIsOneStringColumnRowByRow(t *testing.T) {
	s := dtype.MustSchema(dtype.Of("s", dtype.String), dtype.Of("b", dtype.Binary),
		dtype.Of("t", dtype.String), dtype.Of("i", dtype.Int64))
	lit := &expr.Lit{Value: "x", DT: dtype.String}
	for _, c := range []struct {
		e    expr.Node
		want bool
	}{
		{&expr.Binary{Op: expr.OpEq, L: &expr.Col{Name: "s"}, R: lit}, true},
		{&expr.Unary{Op: expr.OpIsNull, Child: &expr.Col{Name: "b"}}, true},
		{&expr.Call{Fn: expr.FnStrContains, Args: []expr.Node{&expr.Col{Name: "s"}, lit}}, true},
		{&expr.Binary{Op: expr.OpEq, L: &expr.Col{Name: "s"}, R: &expr.Col{Name: "t"}}, false},
		{&expr.Binary{Op: expr.OpGt, L: &expr.Col{Name: "i"}, R: &expr.Lit{Value: int64(1), DT: dtype.Int64}}, false},
		{lit, false},
		{&expr.Binary{Op: expr.OpEq, L: &expr.Col{Name: "missing"}, R: lit}, false},
		{&expr.Binary{Op: expr.OpEq, L: &expr.Agg{Op: expr.AggMin, Child: &expr.Col{Name: "s"}}, R: lit}, false},
	} {
		if _, got := dictable(c.e, s); got != c.want {
			t.Errorf("dictable(%s) = %v, want %v", c.e, got, c.want)
		}
	}
}

// TestWidenReadsWhatTheFilterDrops: a read that kept an eighth reads eight batches'
// rows next; one that kept all reads a batch; one that kept none reads the most.
func TestWidenReadsWhatTheFilterDrops(t *testing.T) {
	for _, c := range []struct{ rows, kept, want int }{
		{1000, 1000, 1000}, {1000, 125, 8000}, {1000, 500, 2000}, {1000, 0, 8000}, {1000, 10, 8000},
	} {
		if got := widen(1000, c.rows, c.kept); got != c.want {
			t.Errorf("widen(1000, %d, %d) = %d, want %d", c.rows, c.kept, got, c.want)
		}
	}
}

// TestColumnsBesideATextConjunctAreReadAtItsKeptRows: where a text's conjunct drops
// rows in the reader, the batch's other columns are read at the rows it keeps (step
// 377), not read whole and taken. A conjunct that keeps one row in 500 of eight row
// groups is read with two Int64 columns beside its own and without: what the two
// add is a fraction of their bytes, where reading them whole added all of them. And
// the rows are the ones it keeps, their values theirs.
func TestColumnsBesideATextConjunctAreReadAtItsKeptRows(t *testing.T) {
	const groups, rows = 8, 50_000
	path := filepath.Join(t.TempDir(), "t.parquet")
	want := writePooledFile(t, path, groups, rows)
	pred := []expr.Node{&expr.Binary{Op: expr.OpEq, L: &expr.Col{Name: "s"},
		R: &expr.Lit{Value: "value-7", DT: dtype.String}}}
	var keepRows []int64
	for r, s := range want.s {
		if s == "value-7" {
			keepRows = append(keepRows, int64(r))
		}
	}
	// read reads the file with the conjunct, the columns cols, and returns what it
	// allocated from the third row group on: the reader widens its reads as the
	// filter drops rows, and its scratch grows with them once. Each batch's rows are
	// checked against the file's, where a and c are read.
	read := func(cols []string) int64 {
		bs, err := openSource(t, path).Open(t.Context(), source.ScanSpec{Projection: cols,
			Predicate: pred, Eval: fakeEval, Threads: 1, BatchSize: 8192})
		if err != nil {
			t.Fatal(err)
		}
		defer bs.Close()
		var before uint64
		seen := 0
		for {
			b, err := bs.Next(t.Context())
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(cols) == 1 {
				// The conjunct's column alone: the third row group begins after the
				// kept rows of the first two.
				seen += b.Rows()
				if before == 0 && seen > len(keepRows)/4 {
					runtime.GC()
					before = allocatedBytes()
				}
				continue
			}
			a, _ := data.TypedColumn[int64](b.Column(0))
			c, _ := data.TypedColumn[int64](b.Column(1))
			for i := range b.Rows() {
				av, _ := a.Get(i)
				cv, _ := c.Get(i)
				if av != keepRows[seen] || cv != want.c[av] {
					t.Fatalf("kept row %d reads %d and %d, want %d and %d", seen, av, cv, keepRows[seen], want.c[keepRows[seen]])
				}
				seen++
				if before == 0 && seen > len(keepRows)/4 {
					runtime.GC()
					before = allocatedBytes()
				}
			}
		}
		if seen != len(keepRows) {
			t.Fatalf("read %d rows, want %d", seen, len(keepRows))
		}
		return int64(allocatedBytes() - before)
	}
	// The least of three reads: the count is the process's, and what else allocates
	// while one reads, the runtime's or a goroutine's, only adds to it. One read on
	// CI's runners counted 1.22 MB beside where the laptop counts 0.72 (step 391).
	least := func(cols []string) int64 {
		return min(read(cols), read(cols), read(cols))
	}
	alone := least([]string{"s"})
	beside := least([]string{"a", "c", "s"})
	ints := int64(groups*rows*16) * 3 / 4
	t.Logf("the conjunct's column alone %.2f MB; with the two Int64 columns %.2f MB, which whole are %.2f MB",
		float64(alone)/(1<<20), float64(beside)/(1<<20), float64(ints)/(1<<20))
	if beside-alone > ints/4 && !raceDetector {
		t.Errorf("the two columns beside added %d bytes; read whole they are %d", beside-alone, ints)
	}
}
