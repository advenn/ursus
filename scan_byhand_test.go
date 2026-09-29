package ursus_test

// Several files read as one, and Parquet pruning, against answers worked out by
// hand: audit.md §5, I1–I7.
//
// Every case here is data corruption in an ordinary file read — the wrong value,
// a row that disappears, a null that is not one — and most of them without an
// error. Each runs single-threaded and under a recover, because one of them
// panics inside arrow-go, and a panic in a worker goroutine cannot be recovered.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// --- fixtures ----------------------------------------------------------------------

// rawCol is one leaf of one row group: its values and its levels, exactly as they
// are written.
type rawCol struct {
	vals       any // []int64, []int32, []float64 or []parquet.ByteArray
	defs, reps []int16
}

// writeRaw writes a Parquet file with the given top-level fields and one row group
// per element of groups, each holding one rawCol per leaf in schema order. It is
// the layer below ursus's writer, which cannot write nested columns or REQUIRED
// ones, and both are what these cases need.
func writeRaw(t *testing.T, dir, name string, fields schema.FieldList, groups ...[]rawCol) string {
	t.Helper()
	root, err := schema.NewGroupNode("schema", parquet.Repetitions.Required, fields, -1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root)
	for _, g := range groups {
		rg := w.AppendRowGroup()
		for _, c := range g {
			cw, err := rg.NextColumn()
			if err != nil {
				t.Fatal(err)
			}
			switch cw := cw.(type) {
			case *file.Int64ColumnChunkWriter:
				_, err = cw.WriteBatch(c.vals.([]int64), c.defs, c.reps)
			case *file.Int32ColumnChunkWriter:
				_, err = cw.WriteBatch(c.vals.([]int32), c.defs, c.reps)
			case *file.Float64ColumnChunkWriter:
				_, err = cw.WriteBatch(c.vals.([]float64), c.defs, c.reps)
			case *file.ByteArrayColumnChunkWriter:
				_, err = cw.WriteBatch(c.vals.([]parquet.ByteArray), c.defs, c.reps)
			default:
				t.Fatalf("writeRaw: no case for %T", cw)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := cw.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if err := rg.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil { // closes f too
		t.Fatal(err)
	}
	return path
}

var (
	opt = parquet.Repetitions.Optional
	req = parquet.Repetitions.Required
)

func i64Node(t *testing.T, name string, rep parquet.Repetition) schema.Node {
	t.Helper()
	n, err := schema.NewPrimitiveNodeLogical(name, rep, schema.NewIntLogicalType(64, true),
		parquet.Types.Int64, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func structNode(t *testing.T, name string, rep parquet.Repetition, fields ...schema.Node) schema.Node {
	t.Helper()
	n, err := schema.NewGroupNode(name, rep, fields, -1)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func listNode(t *testing.T, name string, rep parquet.Repetition, elem schema.Node) schema.Node {
	t.Helper()
	n, err := schema.ListOfWithName(name, elem, rep, -1)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// sinkParquet writes lf to dir/name with ursus's own writer.
func sinkParquet(t *testing.T, dir, name string, lf *ursus.LazyFrame, opts ...ursus.ParquetSinkOption) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := lf.SinkParquet(t.Context(), path, opts...); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeText(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- reading results -------------------------------------------------------------

// cellsOf renders one column's values in row order, "null" for a null.
func cellsOf(df *ursus.DataFrame, name string) ([]string, error) {
	var f dtype.Field
	found := false
	for _, x := range df.Schema().FieldSlice() {
		if x.Name == name {
			f, found = x, true
		}
	}
	if !found {
		return nil, fmt.Errorf("no column %q in %s", name, df.Schema())
	}
	render := func(n int, get func(int) (string, bool)) []string {
		out := make([]string, n)
		for i := range n {
			if s, ok := get(i); ok {
				out[i] = s
			} else {
				out[i] = "null"
			}
		}
		return out
	}
	switch f.Type.ID() {
	case dtype.TypeInt64:
		c, err := df.Column[int64](name)
		if err != nil {
			return nil, err
		}
		return render(c.Len(), func(i int) (string, bool) { v, ok := c.Get(i); return fmt.Sprint(v), ok }), nil
	case dtype.TypeUint32:
		c, err := df.Column[uint32](name)
		if err != nil {
			return nil, err
		}
		return render(c.Len(), func(i int) (string, bool) { v, ok := c.Get(i); return fmt.Sprint(v), ok }), nil
	case dtype.TypeFloat64:
		c, err := df.Column[float64](name)
		if err != nil {
			return nil, err
		}
		return render(c.Len(), func(i int) (string, bool) { v, ok := c.Get(i); return fmt.Sprint(v), ok }), nil
	case dtype.TypeString:
		c, err := df.Column[string](name)
		if err != nil {
			return nil, err
		}
		return render(c.Len(), func(i int) (string, bool) {
			v, ok := c.Get(i)
			if len(v) > 20 {
				v = fmt.Sprintf("<%d bytes>", len(v))
			}
			return v, ok
		}), nil
	case dtype.TypeBool:
		c, err := df.Column[bool](name)
		if err != nil {
			return nil, err
		}
		return render(c.Len(), func(i int) (string, bool) { v, ok := c.Get(i); return fmt.Sprint(v), ok }), nil
	}
	return nil, fmt.Errorf("cellsOf: no case for %s", f.Type)
}

// collectOnce runs lf on one thread and turns a panic into an error, so a case
// that crashes today is a failing case rather than a dead test binary.
func collectOnce(ctx context.Context, lf *ursus.LazyFrame) (df *ursus.DataFrame, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return lf.Collect(ctx, ursus.WithThreads(1))
}

// reads checks that lf produces exactly these columns, in row order.
func reads(want map[string][]string) func(context.Context, *ursus.LazyFrame) error {
	return func(ctx context.Context, lf *ursus.LazyFrame) error {
		df, err := collectOnce(ctx, lf)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(want))
		for n := range want {
			names = append(names, n)
		}
		slices.Sort(names)
		var bad []string
		for _, n := range names {
			got, err := cellsOf(df, n)
			if err != nil {
				return err
			}
			if !slices.Equal(got, want[n]) {
				bad = append(bad, fmt.Sprintf("%s = %v, want %v", n, got, want[n]))
			}
		}
		if len(bad) > 0 {
			return errors.New(strings.Join(bad, "; "))
		}
		return nil
	}
}

// refused checks that lf is refused with ErrSchema — at plan time if atPlan — and
// that the message names every one of names.
func refused(atPlan bool, names ...string) func(context.Context, *ursus.LazyFrame) error {
	return func(ctx context.Context, lf *ursus.LazyFrame) error {
		var err error
		if atPlan {
			_, err = lf.CollectSchema(ctx)
		} else {
			_, err = collectOnce(ctx, lf)
		}
		if err == nil {
			if atPlan {
				return errors.New("planned without error; want ErrSchema at plan time")
			}
			return errors.New("read without error; want ErrSchema")
		}
		if !errors.Is(err, ursus.ErrSchema) {
			return fmt.Errorf("refused, but not with ErrSchema: %v", err)
		}
		for _, n := range names {
			if !strings.Contains(err.Error(), n) {
				return fmt.Errorf("the refusal does not name %s: %v", n, err)
			}
		}
		return nil
	}
}

// --- the cases -----------------------------------------------------------------------

// knownScanDefects names each case that answers wrongly today, with what it
// answers. Emptied by the commits that fix them; a listed case that answers
// correctly fails as stale, and an unlisted one that answers wrongly fails.
var knownScanDefects = map[string]string{
	"I1 reordered":                         "a = 1, 2, 30, 40 and b = 10, 20, 3, 4: file 2 read by position",
	"I1 reordered, other types":            "column a (String) has no reader",
	"I1 renamed":                           "x and y read silently as a and b",
	"I1 missing":                           "plans, then panics inside arrow-go at read time",
	"I1 extra":                             "c silently ignored",
	"I1 String vs Int64":                   "plans, then: column a (Int64) has no fixed-width payload",
	"I1 Int32 vs Int64":                    "plans, then: column a is INT32 but mapped to Int64",
	"I1 required then optional":            "a null in a column declared non-nullable (ErrInternal in tests, silent otherwise)",
	"I1 struct optional then required":     "file 2's {a: null, b: 5} reads as a null struct",
	"I1 struct required then optional":     "file 2's null struct reads as {null, null}",
	"I2 struct before the column":          "c's statistics read from st.b: 0 rows, not 1",
	"I3 NaN under !=":                      "the NaN row is pruned away",
	"I4 big string, >":                     `the group holding "" and the big string is pruned`,
	"I4 big string, >=":                    "the same: 0 rows, not 1",
	"I4 big string, !=":                    "the same",
	"I4 big string, ==":                    "the same: 0 rows, not 1",
	"I5 struct, IsNotNull":                 "pruned on st.a's null count: 0 rows, not 2",
	"I5 list, IsNotNull":                   "pruned on the element's null count, where [] counts as a null",
	"I6 list of required elements":         "[] reads as [null]",
	"I13 required list":                    "[] reads as a null list, in a column declared non-nullable",
	"I13 required list, required elements": "the same",
	"I13 required struct field":            "ErrInternal: the type declares a: Int64!, the column is built nullable",
	"I7 reordered":                         "a = 1, 20 and b = 10, 2: file 2 read by position",
	"I7 renamed":                           "a,c read silently as a,b",
	"I7 missing":                           "refused, as a ragged row (ErrValue), not a different header",
	"I7 extra":                             "the same",
	"I7 headerless part":                   "the part's first row read as a header and lost: a = 1, 3",
	"I7 reordered, WithSchema":             "read by position",
	"I7 reordered, WithColumnNames":        "read by position",
	"I15 a BOM on one part":                `the first column is named "\ufeffa"`,
}

type scanCase struct {
	name  string
	lf    func(t *testing.T, dir string) *ursus.LazyFrame
	check func(context.Context, *ursus.LazyFrame) error
}

func scanCases() []scanCase {
	c := ursus.Col
	pq := func(t *testing.T, dir string, files ...*ursus.LazyFrame) *ursus.LazyFrame {
		var paths []string
		for i, f := range files {
			paths = append(paths, sinkParquet(t, dir, fmt.Sprintf("p%d.parquet", i+1), f))
		}
		return ursus.ScanParquetFiles(paths)
	}
	ab := func(a, b []int64) *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("a", a), ursus.Values("b", b))
	}
	csv2 := func(first, second string, opts ...ursus.CSVOption) func(*testing.T, string) *ursus.LazyFrame {
		return func(t *testing.T, dir string) *ursus.LazyFrame {
			return ursus.ScanCSVFiles([]string{writeText(t, dir, "a.csv", first),
				writeText(t, dir, "b.csv", second)}, opts...)
		}
	}
	big := strings.Repeat("z", 5000)

	return []scanCase{
		// --- I1: several Parquet files ---------------------------------------------
		{"I1 control: identical files", func(t *testing.T, dir string) *ursus.LazyFrame {
			return pq(t, dir, ab([]int64{1, 2}, []int64{10, 20}), ab([]int64{3, 4}, []int64{30, 40}))
		}, reads(map[string][]string{"a": {"1", "2", "3", "4"}, "b": {"10", "20", "30", "40"}})},
		{"I1 reordered", func(t *testing.T, dir string) *ursus.LazyFrame {
			return pq(t, dir, ab([]int64{1, 2}, []int64{10, 20}),
				ursus.Frame(ursus.Values("b", []int64{30, 40}), ursus.Values("a", []int64{3, 4})))
		}, reads(map[string][]string{"a": {"1", "2", "3", "4"}, "b": {"10", "20", "30", "40"}})},
		{"I1 reordered, other types", func(t *testing.T, dir string) *ursus.LazyFrame {
			return pq(t, dir,
				ursus.Frame(ursus.Values("a", []int64{1, 2}), ursus.Values("s", []string{"x", "y"})),
				ursus.Frame(ursus.Values("s", []string{"z", "w"}), ursus.Values("a", []int64{3, 4})))
		}, reads(map[string][]string{"a": {"1", "2", "3", "4"}, "s": {"x", "y", "z", "w"}})},
		{"I1 renamed", func(t *testing.T, dir string) *ursus.LazyFrame {
			return pq(t, dir, ab([]int64{1, 2}, []int64{10, 20}),
				ursus.Frame(ursus.Values("x", []int64{3, 4}), ursus.Values("y", []int64{30, 40})))
		}, refused(true, "p1.parquet", "p2.parquet")},
		{"I1 missing", func(t *testing.T, dir string) *ursus.LazyFrame {
			return pq(t, dir, ab([]int64{1, 2}, []int64{10, 20}), ursus.Frame(ursus.Values("a", []int64{3, 4})))
		}, refused(true, "p1.parquet", "p2.parquet")},
		{"I1 extra", func(t *testing.T, dir string) *ursus.LazyFrame {
			return pq(t, dir, ab([]int64{1, 2}, []int64{10, 20}),
				ursus.Frame(ursus.Values("a", []int64{3, 4}), ursus.Values("b", []int64{30, 40}),
					ursus.Values("c", []int64{5, 6})))
		}, refused(true, "p1.parquet", "p2.parquet")},
		{"I1 String vs Int64", func(t *testing.T, dir string) *ursus.LazyFrame {
			return pq(t, dir, ab([]int64{1, 2}, []int64{10, 20}),
				ursus.Frame(ursus.Values("a", []string{"3", "4"}), ursus.Values("b", []int64{30, 40})))
		}, refused(true, "p1.parquet", "p2.parquet")},
		{"I1 Int32 vs Int64", func(t *testing.T, dir string) *ursus.LazyFrame {
			return pq(t, dir, ab([]int64{1, 2}, []int64{10, 20}),
				ursus.Frame(ursus.Values("a", []int32{3, 4}), ursus.Values("b", []int64{30, 40})))
		}, refused(true, "p1.parquet", "p2.parquet")},
		// File 1 declares a REQUIRED; file 2 declares it OPTIONAL and holds a null.
		// The column is nullable, because one file says so.
		{"I1 required then optional", func(t *testing.T, dir string) *ursus.LazyFrame {
			p1 := writeRaw(t, dir, "p1.parquet", schema.FieldList{i64Node(t, "a", req)},
				[]rawCol{{vals: []int64{1, 2}}})
			p2 := writeRaw(t, dir, "p2.parquet", schema.FieldList{i64Node(t, "a", opt)},
				[]rawCol{{vals: []int64{3}, defs: []int16{1, 0}}})
			return ursus.ScanParquetFiles([]string{p1, p2})
		}, reads(map[string][]string{"a": {"1", "2", "3", "null"}})},
		{"I1 control: optional then required", func(t *testing.T, dir string) *ursus.LazyFrame {
			p1 := writeRaw(t, dir, "p1.parquet", schema.FieldList{i64Node(t, "a", opt)},
				[]rawCol{{vals: []int64{3}, defs: []int16{1, 0}}})
			p2 := writeRaw(t, dir, "p2.parquet", schema.FieldList{i64Node(t, "a", req)},
				[]rawCol{{vals: []int64{1, 2}}})
			return ursus.ScanParquetFiles([]string{p1, p2})
		}, reads(map[string][]string{"a": {"3", "null", "1", "2"}})},
		// A struct's OWN validity: optional in file 1, required in file 2. File 2's
		// {a: null, b: 5} is a present struct with a null field.
		{"I1 struct optional then required", func(t *testing.T, dir string) *ursus.LazyFrame {
			st := func(rep parquet.Repetition) schema.FieldList {
				return schema.FieldList{structNode(t, "st", rep, i64Node(t, "a", opt), i64Node(t, "b", opt))}
			}
			p1 := writeRaw(t, dir, "p1.parquet", st(opt), []rawCol{
				{vals: []int64{1, 2}, defs: []int16{2, 2}}, {vals: []int64{10, 20}, defs: []int16{2, 2}}})
			p2 := writeRaw(t, dir, "p2.parquet", st(req), []rawCol{
				{vals: []int64{4}, defs: []int16{0, 1}}, {vals: []int64{5, 6}, defs: []int16{1, 1}}})
			return ursus.ScanParquetFiles([]string{p1, p2}).Select(c("st").IsNull().Alias("null"),
				c("st").Struct().Field("a").Alias("a"), c("st").Struct().Field("b").Alias("b"))
		}, reads(map[string][]string{"null": {"false", "false", "false", "false"},
			"a": {"1", "2", "null", "4"}, "b": {"10", "20", "5", "6"}})},
		// The other direction: required in file 1, optional in file 2, which holds a
		// null struct.
		{"I1 struct required then optional", func(t *testing.T, dir string) *ursus.LazyFrame {
			st := func(rep parquet.Repetition) schema.FieldList {
				return schema.FieldList{structNode(t, "st", rep, i64Node(t, "a", opt), i64Node(t, "b", opt))}
			}
			p1 := writeRaw(t, dir, "p1.parquet", st(req), []rawCol{
				{vals: []int64{1, 2}, defs: []int16{1, 1}}, {vals: []int64{10, 20}, defs: []int16{1, 1}}})
			p2 := writeRaw(t, dir, "p2.parquet", st(opt), []rawCol{
				{vals: []int64{4}, defs: []int16{0, 2}}, {vals: []int64{6}, defs: []int16{0, 2}}})
			return ursus.ScanParquetFiles([]string{p1, p2}).Select(c("st").IsNull().Alias("null"),
				c("st").Struct().Field("a").Alias("a"))
		}, reads(map[string][]string{"null": {"false", "false", "true", "false"},
			"a": {"1", "2", "null", "4"}})},

		// --- pruning ----------------------------------------------------------------
		// I2: c is the second top-level field and the THIRD leaf; its stats were read
		// from leaf 1, st.b, whose range [100, 101] excludes 2.
		{"I2 struct before the column", func(t *testing.T, dir string) *ursus.LazyFrame {
			fields := schema.FieldList{
				structNode(t, "st", opt, i64Node(t, "a", opt), i64Node(t, "b", opt)),
				i64Node(t, "c", opt),
			}
			p := writeRaw(t, dir, "i2.parquet", fields,
				[]rawCol{{vals: []int64{1, 2}, defs: []int16{2, 2}}, {vals: []int64{100, 101}, defs: []int16{2, 2}},
					{vals: []int64{1, 2}, defs: []int16{1, 1}}},
				[]rawCol{{vals: []int64{3, 4}, defs: []int16{2, 2}}, {vals: []int64{102, 103}, defs: []int16{2, 2}},
					{vals: []int64{3, 4}, defs: []int16{1, 1}}})
			return ursus.ScanParquet(p).Filter(c("c").Eq(int64(2))).Select(c("c"))
		}, reads(map[string][]string{"c": {"2"}})},
		// I3: the first group holds 1 and NaN; statistics leave NaN out, so the group
		// looks like min = max = 1, and NaN != 1.
		{"I3 NaN under !=", func(t *testing.T, dir string) *ursus.LazyFrame {
			p := sinkParquet(t, dir, "i3.parquet",
				ursus.Frame(ursus.Values("f", []float64{1, math.NaN(), 2, 3})), ursus.WithRowGroupRows(2))
			return ursus.ScanParquet(p).Filter(c("f").Ne(1.0))
		}, reads(map[string][]string{"f": {"NaN", "2", "3"}})},
		// I4: the first group holds "" and a 5000-byte string, whose max the writer
		// drops; it reads back as "", and the group looks like it holds only "".
		{"I4 big string, >", i4(func(s ursus.Expr) ursus.Expr { return s.Gt(ursus.Lit("a")) }),
			reads(map[string][]string{"i": {"1", "2", "3"}})},
		{"I4 big string, >=", i4(func(s ursus.Expr) ursus.Expr { return s.Ge(ursus.Lit("y")) }),
			reads(map[string][]string{"i": {"1"}})},
		{"I4 big string, !=", i4(func(s ursus.Expr) ursus.Expr { return s.Ne(ursus.Lit("")) }),
			reads(map[string][]string{"i": {"1", "2", "3"}})},
		{"I4 big string, ==", i4(func(s ursus.Expr) ursus.Expr { return s.Eq(ursus.Lit(big)) }),
			reads(map[string][]string{"i": {"1"}})},
		// I5: a present struct whose first field is null; its IsNotNull read that
		// field's null count.
		{"I5 struct, IsNotNull", func(t *testing.T, dir string) *ursus.LazyFrame {
			fields := schema.FieldList{
				structNode(t, "st", opt, i64Node(t, "a", opt), i64Node(t, "b", opt)),
				i64Node(t, "id", req),
			}
			p := writeRaw(t, dir, "i5s.parquet", fields, []rawCol{
				{vals: []int64{}, defs: []int16{1, 1}}, {vals: []int64{1, 2}, defs: []int16{2, 2}},
				{vals: []int64{1, 2}}})
			return ursus.ScanParquet(p).Filter(c("st").IsNotNull()).Select(c("id"))
		}, reads(map[string][]string{"id": {"1", "2"}})},
		// I5: two empty lists; the element leaf counts an empty list as a null.
		{"I5 list, IsNotNull", func(t *testing.T, dir string) *ursus.LazyFrame {
			fields := schema.FieldList{
				listNode(t, "l", opt, i64Node(t, "element", opt)),
				i64Node(t, "id", req),
			}
			p := writeRaw(t, dir, "i5l.parquet", fields, []rawCol{
				{vals: []int64{}, defs: []int16{1, 1}, reps: []int16{0, 0}}, {vals: []int64{1, 2}}})
			return ursus.ScanParquet(p).Filter(c("l").IsNotNull()).Select(c("id"))
		}, reads(map[string][]string{"id": {"1", "2"}})},

		// --- list and struct decoding ------------------------------------------------
		// I6: an optional list of REQUIRED elements. [1,2], [], null, [3].
		{"I6 list of required elements", func(t *testing.T, dir string) *ursus.LazyFrame {
			p := writeRaw(t, dir, "i6.parquet",
				schema.FieldList{listNode(t, "l", opt, i64Node(t, "element", req))},
				[]rawCol{{vals: []int64{1, 2, 3}, defs: []int16{2, 2, 1, 0, 2}, reps: []int16{0, 1, 0, 0, 0}}})
			return ursus.ScanParquet(p).Select(c("l").List().Len().Alias("n"))
		}, reads(map[string][]string{"n": {"2", "0", "null", "1"}})},
		// I13: a REQUIRED list of optional elements. [1,null], [], [3].
		{"I13 required list", func(t *testing.T, dir string) *ursus.LazyFrame {
			p := writeRaw(t, dir, "i13a.parquet",
				schema.FieldList{listNode(t, "l", req, i64Node(t, "element", opt))},
				[]rawCol{{vals: []int64{1, 3}, defs: []int16{2, 1, 0, 2}, reps: []int16{0, 1, 0, 0}}})
			return ursus.ScanParquet(p).Select(c("l").List().Len().Alias("n"),
				c("l").List().Get(1).Alias("second"))
		}, reads(map[string][]string{"n": {"2", "0", "1"}, "second": {"null", "null", "null"}})},
		// I13: a required list of required elements. [1], [], [2,3].
		{"I13 required list, required elements", func(t *testing.T, dir string) *ursus.LazyFrame {
			p := writeRaw(t, dir, "i13b.parquet",
				schema.FieldList{listNode(t, "l", req, i64Node(t, "element", req))},
				[]rawCol{{vals: []int64{1, 2, 3}, defs: []int16{1, 0, 1, 1}, reps: []int16{0, 0, 0, 1}}})
			return ursus.ScanParquet(p).Select(c("l").List().Len().Alias("n"))
		}, reads(map[string][]string{"n": {"1", "0", "2"}})},
		// I13: a struct with a REQUIRED field. {1, 10}, null.
		{"I13 required struct field", func(t *testing.T, dir string) *ursus.LazyFrame {
			p := writeRaw(t, dir, "i13c.parquet",
				schema.FieldList{structNode(t, "st", opt, i64Node(t, "a", req), i64Node(t, "b", opt))},
				[]rawCol{{vals: []int64{1}, defs: []int16{1, 0}}, {vals: []int64{10}, defs: []int16{2, 0}}})
			return ursus.ScanParquet(p).Select(c("st").IsNull().Alias("null"),
				c("st").Struct().Field("a").Alias("a"))
		}, reads(map[string][]string{"null": {"false", "true"}, "a": {"1", "null"}})},

		// --- I7: several CSV files ------------------------------------------------------
		{"I7 control: identical headers", csv2("a,b\n1,10\n", "a,b\n2,20\n"),
			reads(map[string][]string{"a": {"1", "2"}, "b": {"10", "20"}})},
		{"I7 control: an empty part", csv2("a,b\n1,10\n", ""),
			reads(map[string][]string{"a": {"1"}, "b": {"10"}})},
		{"I7 reordered", csv2("a,b\n1,10\n", "b,a\n20,2\n"),
			reads(map[string][]string{"a": {"1", "2"}, "b": {"10", "20"}})},
		{"I7 renamed", csv2("a,b\n1,10\n", "a,c\n2,20\n"), refused(false, "a.csv", "b.csv")},
		{"I7 missing", csv2("a,b\n1,10\n", "a\n2\n"), refused(false, "a.csv", "b.csv")},
		{"I7 extra", csv2("a,b\n1,10\n", "a,b,c\n2,20,200\n"), refused(false, "a.csv", "b.csv")},
		// A later part with no header: its first row was read as a header and lost.
		{"I7 headerless part", csv2("a,b\n1,10\n", "2,20\n3,30\n"), refused(false, "a.csv", "b.csv")},
		{"I7 reordered, WithSchema", csv2("a,b\n1,10\n", "b,a\n20,2\n", ursus.WithSchema(mustSchema(
			dtype.Of("a", dtype.Int64), dtype.Of("b", dtype.Int64)))),
			reads(map[string][]string{"a": {"1", "2"}, "b": {"10", "20"}})},
		// WithColumnNames renames file 1 by position; file 2 is matched to file 1 by
		// the names in the files, not the new ones.
		{"I7 reordered, WithColumnNames", csv2("a,b\n1,10\n", "b,a\n20,2\n", ursus.WithColumnNames("x", "y")),
			reads(map[string][]string{"x": {"1", "2"}, "y": {"10", "20"}})},
		// I15: a byte-order mark on the first file only. Matched by name, a BOM must
		// not make the headers differ.
		{"I15 a BOM on one part", csv2("\ufeffa,b\n1,10\n", "a,b\n2,20\n"),
			reads(map[string][]string{"a": {"1", "2"}, "b": {"10", "20"}})},
	}
}

// i4 builds I4's file — "", then a 5000-byte string, in one group; "b" and "c" in
// the next — and filters it with pred over s, keeping the row index i.
func i4(pred func(ursus.Expr) ursus.Expr) func(*testing.T, string) *ursus.LazyFrame {
	return func(t *testing.T, dir string) *ursus.LazyFrame {
		p := sinkParquet(t, dir, "i4.parquet", ursus.Frame(
			ursus.Values("s", []string{"", strings.Repeat("z", 5000), "b", "c"}),
			ursus.Values("i", []int64{0, 1, 2, 3})), ursus.WithRowGroupRows(2))
		return ursus.ScanParquet(p).Filter(pred(ursus.Col("s"))).Select(ursus.Col("i"))
	}
}

func mustSchema(fields ...dtype.Field) *ursus.Schema {
	s, err := dtype.NewSchema(fields...)
	if err != nil {
		panic(err)
	}
	return s
}

func TestScansByHand(t *testing.T) {
	cases := scanCases()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.check(t.Context(), tc.lf(t, t.TempDir()))
			why, known := knownScanDefects[tc.name]
			switch {
			case known && err == nil:
				t.Errorf("answers correctly now; delete it from knownScanDefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %v", why, err)
			case err != nil:
				t.Error(err)
			}
		})
	}
	for name := range knownScanDefects {
		if !slices.ContainsFunc(cases, func(c scanCase) bool { return c.name == name }) {
			t.Errorf("knownScanDefects names %q, which is not a case", name)
		}
	}
}
