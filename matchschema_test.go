package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestMatchToSchema is C2 of v0.6.7-scope.md (step 299): Polars' match_to_schema.
// Every answer is Polars 1.44.1's on the same frame, but a computed fill, which
// meets the schema's type here and keeps its own in Polars (feature-gaps.md §9).
func TestMatchToSchema(t *testing.T) {
	of := ursus.Of
	lf := ursus.Frame(
		ursus.Values("b", []int32{1, 2}),
		ursus.Values("a", []string{"x", "y"}),
		ursus.Values("e", []float32{1.5, 2.5}),
	)
	s := ursus.MustSchema(of("a", ursus.String), of("b", ursus.Int64), of("c", ursus.Float64))
	upcast := ursus.MatchOptions{MissingColumns: ursus.MatchInsert, ExtraColumns: ursus.MatchIgnore,
		IntegerCast: ursus.MatchUpcast}
	one := func(name string, from, to ursus.DataType) *ursus.LazyFrame {
		return ursus.Frame(ursus.Values(name, []int64{1})).
			Select(ursus.Col(name).Cast(from)).
			MatchToSchema(ursus.MustSchema(of(name, to)), ursus.MatchOptions{
				IntegerCast: ursus.MatchUpcast, FloatCast: ursus.MatchUpcast})
	}

	for _, tc := range []struct {
		name  string
		q     *ursus.LazyFrame
		types string
		csv   string
	}{
		{"insert, ignore and upcast", lf.MatchToSchema(s, upcast),
			"a:String,b:Int64,c:Float64", "a,b,c\nx,1,\ny,2,\n"},
		{"a fill", lf.MatchToSchema(s, ursus.MatchOptions{ExtraColumns: ursus.MatchIgnore,
			IntegerCast: ursus.MatchUpcast, Columns: map[string]ursus.ColumnMatch{
				"c": {Fill: ursus.Col("e").Cast(ursus.Float64).Mul(2.0)}}}),
			"a:String,b:Int64,c:Float64", "a,b,c\nx,1,3.0\ny,2,5.0\n"},
		{"reordered", lf.MatchToSchema(ursus.MustSchema(of("a", ursus.String), of("e", ursus.Float32),
			of("b", ursus.Int32)), ursus.MatchOptions{}), "a:String,e:Float32,b:Int32", "a,e,b\nx,1.5,1\ny,2.5,2\n"},
		{"a float upcast", lf.MatchToSchema(ursus.MustSchema(of("b", ursus.Int32), of("a", ursus.String),
			of("e", ursus.Float64)), ursus.MatchOptions{FloatCast: ursus.MatchUpcast}),
			"b:Int32,a:String,e:Float64", "b,a,e\n1,x,1.5\n2,y,2.5\n"},
		{"an upcast for one column", lf.MatchToSchema(ursus.MustSchema(of("b", ursus.Int64), of("a", ursus.String),
			of("e", ursus.Float32)), ursus.MatchOptions{Columns: map[string]ursus.ColumnMatch{
			"b": {IntegerCast: ursus.MatchUpcast}}}), "b:Int64,a:String,e:Float32", "b,a,e\n1,x,1.5\n2,y,2.5\n"},
		{"insert for one column", lf.MatchToSchema(s, ursus.MatchOptions{ExtraColumns: ursus.MatchIgnore,
			IntegerCast: ursus.MatchUpcast, Columns: map[string]ursus.ColumnMatch{"c": {Missing: ursus.MatchInsert}}}),
			"a:String,b:Int64,c:Float64", "a,b,c\nx,1,\ny,2,\n"},
		{"UInt8 as Int16", one("u", ursus.Uint8, ursus.Int16), "u:Int16", "u\n1\n"},
		{"UInt32 as Int64", one("u", ursus.Uint32, ursus.Int64), "u:Int64", "u\n1\n"},
		{"Int64 as Int128", one("i", ursus.Int64, ursus.Int128), "i:Int128", "i\n1\n"},
		{"a Null column", ursus.Frame(ursus.Values("k", []int64{1, 2})).
			Select(ursus.Null(ursus.NullT).Alias("n")).
			MatchToSchema(ursus.MustSchema(of("n", ursus.Int64)), ursus.MatchOptions{}), "n:Int64", "n\n\n\n"},
		{"a List's element", ursus.Frame(ursus.Values("l", []string{"1,2", "3"})).
			Select(ursus.Col("l").Str().Split(",").List().Eval(ursus.Element().Cast(ursus.Int32))).
			MatchToSchema(ursus.MustSchema(of("l", ursus.List(ursus.Int64))),
				ursus.MatchOptions{IntegerCast: ursus.MatchUpcast}), "l:List(Int64)", ""},
	} {
		if got := matchTypes(t, tc.q); got != tc.types {
			t.Errorf("%s: types %s, want %s", tc.name, got, tc.types)
		}
		if tc.csv != "" {
			if got := csvOf(t, tc.q); got != tc.csv {
				t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.csv)
			}
		}
	}

	t.Run("refused", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			q    *ursus.LazyFrame
			want []string
		}{
			// Polars: data type mismatch for column b: incoming: Int32 != target: Int64,
			// before the missing c and the extra e.
			{"the defaults", lf.MatchToSchema(s, ursus.MatchOptions{}),
				[]string{"b is Int32, and the schema's is Int64", "IntegerCast: ursus.MatchUpcast"}},
			{"a missing column before a type", lf.MatchToSchema(ursus.MustSchema(of("c", ursus.Int64),
				of("b", ursus.Int64), of("a", ursus.String)), ursus.MatchOptions{}), []string{"b is Int32"}},
			// Polars: missing columns in `match_to_schema`: "c", every one.
			{"the missing columns", lf.MatchToSchema(ursus.MustSchema(of("a", ursus.String), of("c", ursus.Int64),
				of("d", ursus.Int8)), ursus.MatchOptions{}), []string{`no column "c", "d"`, "MissingColumns"}},
			{"a missing column raised for one", lf.MatchToSchema(ursus.MustSchema(of("a", ursus.String),
				of("c", ursus.Int64), of("d", ursus.Int8)), ursus.MatchOptions{MissingColumns: ursus.MatchInsert,
				ExtraColumns: ursus.MatchIgnore, Columns: map[string]ursus.ColumnMatch{
					"d": {Missing: ursus.MatchRaise}}}), []string{`no column "d"`}},
			// Polars: extra columns in `match_to_schema`: "e".
			{"the extra columns", lf.MatchToSchema(ursus.MustSchema(of("a", ursus.String)), ursus.MatchOptions{}),
				[]string{`the schema has no column "b", "e"`, "ExtraColumns"}},
			{"a narrower integer", lf.MatchToSchema(ursus.MustSchema(of("b", ursus.Int8), of("a", ursus.String),
				of("e", ursus.Float32)), ursus.MatchOptions{IntegerCast: ursus.MatchUpcast}),
				[]string{"Int8 cannot hold every Int32 value"}},
			{"Int8 as UInt16", one("u", ursus.Int8, ursus.Uint16), []string{"cannot hold every"}},
			{"UInt64 as Int64", one("u", ursus.Uint64, ursus.Int64), []string{"cannot hold every"}},
			{"an integer as a float", one("i", ursus.Int32, ursus.Float64), []string{"i is Int32"}},
			{"a float forbidden", lf.MatchToSchema(ursus.MustSchema(of("b", ursus.Int32), of("a", ursus.String),
				of("e", ursus.Float64)), ursus.MatchOptions{}), []string{"FloatCast: ursus.MatchUpcast"}},
			{"text as a number", lf.MatchToSchema(ursus.MustSchema(of("b", ursus.Int32), of("a", ursus.Int64),
				of("e", ursus.Float32)), ursus.MatchOptions{IntegerCast: ursus.MatchUpcast}),
				[]string{"a is String, and the schema's is Int64"}},
			{"a wider Decimal", ursus.Frame(ursus.Values("d", []int64{1})).Select(ursus.Col("d").Cast(ursus.Decimal(10, 2))).
				MatchToSchema(ursus.MustSchema(of("d", ursus.Decimal(20, 2))), ursus.MatchOptions{}),
				[]string{"Decimal(10, 2)"}},
			{"another Datetime unit", ursus.Frame(ursus.Values("t", []int64{1})).
				Select(ursus.Col("t").Cast(ursus.Datetime(ursus.Milli, ""))).
				MatchToSchema(ursus.MustSchema(of("t", ursus.Datetime(ursus.Micro, ""))), ursus.MatchOptions{}),
				[]string{"t is Datetime(ms)"}},
			// Polars keeps a fill's own type, Int32 or String under an Int64 column; here
			// it meets the column's type as the frame's own column would.
			{"a fill narrower than the column", lf.MatchToSchema(ursus.MustSchema(of("b", ursus.Int32),
				of("c", ursus.Int64)), ursus.MatchOptions{ExtraColumns: ursus.MatchIgnore,
				Columns: map[string]ursus.ColumnMatch{"c": {Fill: ursus.Col("b")}}}),
				[]string{"c is Int32, and the schema's is Int64", "IntegerCast"}},
			{"a fill of text", lf.MatchToSchema(ursus.MustSchema(of("c", ursus.Int64)),
				ursus.MatchOptions{ExtraColumns: ursus.MatchIgnore, Columns: map[string]ursus.ColumnMatch{
					"c": {Fill: ursus.Col("a")}}}), []string{"c is String"}},
			{"an action in the wrong place", lf.MatchToSchema(s, ursus.MatchOptions{MissingColumns: ursus.MatchUpcast}),
				[]string{"MissingColumns is MatchUpcast", "MatchRaise or MatchInsert"}},
			{"a column the schema lacks", lf.MatchToSchema(s, ursus.MatchOptions{Columns: map[string]ursus.ColumnMatch{
				"e": {IntegerCast: ursus.MatchUpcast}}}), []string{`unknown column "e"`}},
			{"a fill raised", lf.MatchToSchema(s, ursus.MatchOptions{Columns: map[string]ursus.ColumnMatch{
				"c": {Missing: ursus.MatchRaise, Fill: ursus.Col("e")}}}), []string{"has a Fill and Missing is MatchRaise"}},
			{"a nil schema", lf.MatchToSchema(nil, ursus.MatchOptions{}), []string{"the schema is nil"}},
		} {
			_, err := tc.q.Collect(t.Context())
			for _, w := range tc.want {
				if err == nil || !strings.Contains(err.Error(), w) {
					t.Errorf("%s: got %v, want an error with %q", tc.name, err, w)
				}
			}
		}
	})

	t.Run("structs", func(t *testing.T) {
		st := func(fs ...ursus.Field) ursus.DataType { return ursus.StructOf(fs...) }
		f := func(n string, d ursus.DataType) ursus.Field { return ursus.Field{Name: n, Type: d, Nullable: true} }
		in := st(f("x", ursus.Int32), f("y", ursus.String))
		// Two structs and a null one, as JSON reads them.
		ss := ursus.Frame(ursus.Values("j", []string{`{"x": 1, "y": "p"}`, `{"x": 2, "y": "q"}`, "null"})).
			Select(ursus.Col("j").Str().JsonDecode(in).Alias("s"))
		to := func(dt ursus.DataType, o ursus.MatchOptions) *ursus.LazyFrame {
			return ss.MatchToSchema(ursus.MustSchema(of("s", dt)), o)
		}
		json := func(q *ursus.LazyFrame) *ursus.LazyFrame {
			return q.Select(ursus.Col("s").Struct().JsonEncode())
		}
		for _, tc := range []struct {
			name  string
			q     *ursus.LazyFrame
			types string
			csv   string
		}{
			{"the same", to(in, ursus.MatchOptions{}), "s:Struct(x: Int32, y: String)", ""},
			{"reordered", to(st(f("y", ursus.String), f("x", ursus.Int32)), ursus.MatchOptions{}),
				"s:Struct(y: String, x: Int32)", "s\n\"{\"\"y\"\":\"\"p\"\",\"\"x\"\":1}\"\n\"{\"\"y\"\":\"\"q\"\",\"\"x\"\":2}\"\n\n"},
			{"a field inserted", to(st(f("x", ursus.Int32), f("y", ursus.String), f("z", ursus.Float64)),
				ursus.MatchOptions{MissingStructFields: ursus.MatchInsert}),
				"s:Struct(x: Int32, y: String, z: Float64)",
				"s\n\"{\"\"x\"\":1,\"\"y\"\":\"\"p\"\",\"\"z\"\":null}\"\n\"{\"\"x\"\":2,\"\"y\"\":\"\"q\"\",\"\"z\"\":null}\"\n\n"},
			{"a field dropped", to(st(f("x", ursus.Int32)), ursus.MatchOptions{ExtraStructFields: ursus.MatchIgnore}),
				"s:Struct(x: Int32)", "s\n\"{\"\"x\"\":1}\"\n\"{\"\"x\"\":2}\"\n\n"},
			{"a field widened", to(st(f("x", ursus.Int64), f("y", ursus.String)),
				ursus.MatchOptions{IntegerCast: ursus.MatchUpcast}), "s:Struct(x: Int64, y: String)",
				"s\n\"{\"\"x\"\":1,\"\"y\"\":\"\"p\"\"}\"\n\"{\"\"x\"\":2,\"\"y\"\":\"\"q\"\"}\"\n\n"},
		} {
			if got := matchTypes(t, tc.q); got != tc.types {
				t.Errorf("%s: types %s, want %s", tc.name, got, tc.types)
			}
			if tc.csv != "" {
				// The third row is a null struct, and stays one: no {"x": null, ...}.
				if got := csvOf(t, json(tc.q)); got != tc.csv {
					t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.csv)
				}
			}
		}
		for _, tc := range []struct {
			name string
			q    *ursus.LazyFrame
			want string
		}{
			// Polars: encountered missing struct field: z.
			{"a missing field", to(st(f("x", ursus.Int32), f("y", ursus.String), f("z", ursus.Float64)),
				ursus.MatchOptions{}), `no field "z"`},
			// Polars: encountered extra struct field: y.
			{"an extra field", to(st(f("x", ursus.Int32)), ursus.MatchOptions{}), `a field "y"`},
			// Polars: incoming: Int32 != target: Int64, of the field.
			{"a field forbidden", to(st(f("x", ursus.Int64), f("y", ursus.String)), ursus.MatchOptions{}),
				"s.x is Int32, and the schema's is Int64"},
		} {
			_, err := tc.q.Collect(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
			}
		}
	})

	t.Run("plan", func(t *testing.T) {
		explain := func(q *ursus.LazyFrame) string {
			t.Helper()
			p, err := q.Explain(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			return p
		}
		// A frame that already is the schema is returned as it was.
		same := ursus.MustSchema(of("b", ursus.Int32), of("a", ursus.String), of("e", ursus.Float32))
		if got, want := explain(lf.MatchToSchema(same, ursus.MatchOptions{})), explain(lf); got != want {
			t.Errorf("a frame of the schema was changed:\n%s", got)
		}
	})

	t.Run("eager", func(t *testing.T) {
		df, err := lf.Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		got, err := df.MatchToSchema(t.Context(), s, upcast)
		if err != nil {
			t.Fatal(err)
		}
		want, err := lf.MatchToSchema(s, upcast).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	})
}

// TestMatchToSchemaUnderAMemoryLimit holds MatchToSchema to its pieces: a Select of
// per-row expressions streams, so it answers under a limit far below its input as
// without one.
func TestMatchToSchemaUnderAMemoryLimit(t *testing.T) {
	const n = 20_000
	a, b := make([]int32, n), make([]string, n)
	for i := range n {
		a[i], b[i] = int32(i), []string{"p", "q", "r"}[i%3]
	}
	q := ursus.Frame(ursus.Values("a", a), ursus.Values("b", b), ursus.Values("x", a)).
		MatchToSchema(ursus.MustSchema(ursus.Of("b", ursus.String), ursus.Of("a", ursus.Int64),
			ursus.Of("c", ursus.Float64)), ursus.MatchOptions{MissingColumns: ursus.MatchInsert,
			ExtraColumns: ursus.MatchIgnore, IntegerCast: ursus.MatchUpcast})
	want, err := q.Collect(t.Context(), ursus.WithBatchSize(64))
	if err != nil {
		t.Fatal(err)
	}
	got, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(16<<10),
		ursus.WithSpillDir(t.TempDir()))
	if err != nil {
		t.Fatalf("under 16 KiB: %v", err)
	}
	ursustest.AssertFrameEqual(t, got, want)
}

func matchTypes(t *testing.T, q *ursus.LazyFrame) string {
	t.Helper()
	s, err := q.CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range s.All() {
		out = append(out, f.Name+":"+f.Type.String())
	}
	return strings.Join(out, ",")
}
