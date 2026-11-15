package duck

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/build"
	"github.com/advenn/ursus/qcheck/compare"
	"github.com/advenn/ursus/qcheck/model"
	"github.com/advenn/ursus/qcheck/sqlgen"
)

// --- a small language for the rules' queries ------------------------------------

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func c(name string, t model.Type) ast.Expr { return &ast.Col{Name: name, T: t} }

// lit is a literal: v an int64 for an integer, an int32 for a Date.
func lit(t model.Type, v any) ast.Expr {
	if i, ok := v.(int); ok {
		v = int64(i)
	}
	return &ast.Lit{T: t, V: v}
}

func null(t model.Type) ast.Expr                { return &ast.Lit{T: t, Null: true} }
func bin(op ast.BinOp, l, r ast.Expr) ast.Expr  { return must(ast.NewBin(op, l, r)) }
func un(op ast.UnOp, x ast.Expr) ast.Expr       { return must(ast.NewUn(op, x)) }
func when(cond, then, els ast.Expr) ast.Expr    { return must(ast.NewWhen(cond, then, els)) }
func fill(x, with ast.Expr) ast.Expr            { return must(ast.NewFillNull(x, with)) }
func cast(x ast.Expr, to model.Type) ast.Expr   { return must(ast.NewCast(x, to, false)) }
func lossy(x ast.Expr, to model.Type) ast.Expr  { return must(ast.NewCast(x, to, true)) }
func agg(op ast.AggOp, x ast.Expr) ast.Expr     { return must(ast.NewAgg(op, x)) }
func scan(t string) ast.Frame                   { return &ast.Scan{Table: t} }
func filter(in ast.Frame, p ast.Expr) ast.Frame { return &ast.Filter{In: in, Pred: p} }
func named(es []ast.Expr, prefix string) []ast.Named {
	out := make([]ast.Named, len(es))
	for i, e := range es {
		out[i] = ast.Named{Name: fmt.Sprintf("%s%d", prefix, i), E: e}
		if col, ok := e.(*ast.Col); ok {
			out[i].Name = col.Name
		}
	}
	return out
}
func sel(in ast.Frame, es ...ast.Expr) ast.Frame { return &ast.Select{In: in, Cols: named(es, "x")} }
func group(in ast.Frame, keys []ast.Expr, maintain bool, aggs ...ast.Expr) ast.Frame {
	return &ast.GroupBy{In: in, Keys: named(keys, "k"), Aggs: named(aggs, "a"), MaintainOrder: maintain}
}
func sortBy(in ast.Frame, keys ...ast.SortKey) ast.Frame { return &ast.Sort{In: in, Keys: keys} }

// pairs is a table of every pair of a and b's values, as columns a and b.
func pairs(name string, ta model.Type, a []any, tb model.Type, b []any) *model.Table {
	var av, bv []any
	for _, x := range a {
		for _, y := range b {
			av, bv = append(av, x), append(bv, y)
		}
	}
	return table(name, col{"a", ta, av}, col{"b", tb, bv})
}

// The inputs the rules' queries read: each type's ends and oddities.
var (
	ints64 = []any{int64(math.MinInt64), int64(math.MinInt64 + 1), int64(-1000), int64(-7), int64(-2), int64(-1),
		int64(0), nil, int64(1), int64(2), int64(7), int64(1000), int64(1<<53 + 1), int64(math.MaxInt64 - 1), int64(math.MaxInt64)}
	ints32 = []any{int64(math.MinInt32), int64(-129), int64(-128), int64(-7), int64(-1), int64(0), nil, int64(1),
		int64(7), int64(127), int64(128), int64(32768), int64(math.MaxInt32)}
	floats = []any{math.NaN(), math.Inf(-1), -math.MaxFloat64, -1e300, -7.5, -2.0, -1.0, -0.5, math.Copysign(0, -1),
		nil, 0.0, 0.5, 1.0, 2.0, 7.5, 1e300, math.MaxFloat64, math.Inf(1), 5e-324, 0.1, 127.0, 128.0, -128.0,
		-129.0, 9.223372036854775807e18, -9.223372036854775808e18, float64(1 << 53), 2147483647.0}
	strs = []any{"", "a", "+5", "-0", "007", " 12", "12 ", "1_000", "1e3", "0x10", "1.0", "9223372036854775808",
		"-9223372036854775808", "-129", "127", "32767", nil, "inf", "-Infinity", "nan", "NaN", "+nan", "5.", ".5",
		"1e400", "1e-400", "-0.0", "true", "True", "TRUE", "t", "1", "0", "f", "no", "2024-02-29", "2023-02-29",
		"0000-01-01", "9999-12-31", "2024-1-01", "1970-01-01", " 2024-01-01", "日本語", "a\x00b", "İnf"}
	days = []any{int64(math.MinInt32), int64(-math.MaxInt32), int64(-719529), int64(-719162), int64(-1), int64(0),
		int64(1), int64(18262), int64(2932896), int64(2932897), int64(math.MaxInt32), nil}
	bools = []any{true, false, nil}
)

// floatSeq is the floats some facts read, as table values.
func floatSeq(n int) []any {
	var out []any
	for _, f := range someFloats(n) {
		out = append(out, f)
	}
	return out
}

// ursusConfigs are the configurations each rule's query runs in ursus under: the
// reference, and one of many threads, small batches, records cut and the optimizer
// off.
var ursusConfigs = []model.Config{model.Reference(),
	{Threads: 4, Batch: 3, OptOff: true, Verify: true, Records: 3}}

// agree runs q over tables in ursus under ursusConfigs and in DuckDB, and holds
// each ursus answer to DuckDB's by compare.Compare: the rule's expected answer is
// ursus's, its SQL's job to compute it.
func agree(t *testing.T, d engine, tables []*model.Table, q *ast.Query) {
	t.Helper()
	tm := map[string]*model.Table{}
	for _, tab := range tables {
		tm[tab.Name] = tab
	}
	if err := ast.Check(q, tm); err != nil {
		t.Fatalf("an ill-formed query: %v\n%s", err, q.Go())
	}
	bq, bounds := ast.Bounded(q)
	s, err := sqlgen.Translate(bq, tm)
	if err != nil {
		t.Fatalf("%v\n%s", err, q.Go())
	}
	info := must(ast.Schema(bq.Root, tm))
	if err := d.Load(tables); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	schema, recs, err := d.Answer(ctx, bq, s)
	if err != nil {
		t.Fatalf("DuckDB: %v\n%s\n%s", err, q.Go(), s.Text)
	}
	var want *compare.Answer
	if q.Count {
		a := must(compare.FromArrow(schema, recs, []ast.Column{{Name: "n", Type: model.Int64}}))
		want = &compare.Answer{IsCount: true}
		fmt.Sscan(a.Cells[0][0][1:], &want.Count)
	} else {
		want = must(compare.FromArrow(schema, recs, info.Cols))
	}
	for _, r := range recs {
		r.Release()
	}
	for _, cfg := range ursusConfigs {
		lf := build.Frame(bq.Root, build.RecordsOf(tables, cfg.Records))
		var got *compare.Answer
		if q.Count {
			n, err := lf.Count(ctx, build.Options(cfg)...)
			if err != nil {
				t.Fatalf("ursus %v refused: %v\n%s", cfg, err, q.Go())
			}
			got = &compare.Answer{IsCount: true, Count: n}
		} else {
			df, err := lf.Collect(ctx, build.Options(cfg)...)
			if err != nil {
				t.Fatalf("ursus %v refused: %v\n%s", cfg, err, q.Go())
			}
			got = must(compare.FromUrsus(df, info.Cols))
		}
		plan := must(compare.NewPlan(bq, bounds, tm, cfg.Threads))
		if v := compare.Compare(want, got, plan); v.Diff != "" {
			t.Errorf("ursus %v against DuckDB: %s\n%s\nSQL:\n%s\nDuckDB answered %s\nursus answered %s",
				cfg, v.Diff, q.Go(), s.Text, want, got)
		}
	}
}

// ruleCases are each rule's queries.
var ruleCases = map[string]func(t *testing.T, d engine){
	sqlgen.RCastStrict: func(t *testing.T, d engine) {
		i64, i32, f, b, dt := table("i64", col{"x", model.Int64, ints64}), table("i32", col{"x", model.Int32, ints32}),
			table("f", col{"x", model.Float64, floats}), table("b", col{"x", model.Bool, bools}), table("d", col{"x", model.Date, days})
		agree(t, d, []*model.Table{i64}, &ast.Query{Root: sel(scan("i64"), cast(c("x", model.Int64), model.Float64),
			cast(c("x", model.Int64), model.String))})
		agree(t, d, []*model.Table{i32}, &ast.Query{Root: sel(scan("i32"), cast(c("x", model.Int32), model.Int64),
			cast(c("x", model.Int32), model.Float64), cast(c("x", model.Int32), model.String), cast(c("x", model.Int32), model.Date))})
		agree(t, d, []*model.Table{b}, &ast.Query{Root: sel(scan("b"), cast(c("x", model.Bool), model.Int8),
			cast(c("x", model.Bool), model.Int64), cast(c("x", model.Bool), model.Float64), cast(c("x", model.Bool), model.String))})
		agree(t, d, []*model.Table{f}, &ast.Query{Root: sel(scan("f"), cast(c("x", model.Float64), model.String))})
		agree(t, d, []*model.Table{dt}, &ast.Query{Root: sel(scan("d"), cast(c("x", model.Date), model.String),
			cast(c("x", model.Date), model.Int32), cast(c("x", model.Date), model.Int64))})
	},
	sqlgen.RPromote: func(t *testing.T, d engine) {
		tab := table("t", col{"b", model.Bool, []any{true, false, nil, true}}, col{"i", model.Int32, []any{int64(1), nil, int64(-7), int64(math.MaxInt32)}},
			col{"l", model.Int64, []any{nil, int64(math.MinInt64), int64(3), int64(4)}}, col{"f", model.Float64, []any{0.5, math.NaN(), nil, -0.0}})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"),
			when(c("b", model.Bool), c("i", model.Int32), c("l", model.Int64)),
			when(c("b", model.Bool), c("i", model.Int32), c("f", model.Float64)),
			fill(c("i", model.Int32), c("l", model.Int64)), fill(c("l", model.Int64), c("f", model.Float64)),
			bin(ast.Add, c("i", model.Int32), c("f", model.Float64)))})
	},
	sqlgen.RI128Arith: func(t *testing.T, d engine) {
		tab := table("t", col{"k", model.Int64, []any{int64(1), int64(1), int64(2), int64(2), int64(3)}},
			col{"x", model.Int64, []any{int64(math.MaxInt64), int64(math.MaxInt64), int64(-5), nil, nil}})
		g := group(scan("t"), []ast.Expr{c("k", model.Int64)}, true, agg(ast.Sum, c("x", model.Int64)), agg(ast.Count, c("x", model.Int64)))
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sortBy(filter(g, bin(ast.Gt, c("a0", model.Int128), lit(model.Int64, -10))),
			ast.SortKey{Col: "a0", Desc: true}, ast.SortKey{Col: "a1"})})
	},
	sqlgen.RMeanBool: func(t *testing.T, d engine) {
		tab := table("t", col{"i", model.Int32, ints32}, col{"l", model.Int64, ints64[:len(ints32)]})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: group(scan("t"), nil, false, agg(ast.Mean, c("i", model.Int32)),
			agg(ast.Mean, c("l", model.Int64)))})
	},
	sqlgen.RJoinSuffix: func(t *testing.T, d engine) {
		l := table("l", col{"k", model.Int64, []any{int64(1), int64(2), nil, int64(3)}}, col{"v", model.String, []any{"a", "b", "c", "d"}})
		r := table("r", col{"k", model.Int64, []any{int64(2), int64(3), int64(3), nil}}, col{"v", model.String, []any{"x", "y", "z", "w"}})
		for _, how := range []ast.JoinHow{ast.Inner, ast.Left} {
			agree(t, d, []*model.Table{l, r}, &ast.Query{Root: &ast.Join{L: scan("l"), R: scan("r"), How: how, LeftOn: "k", RightOn: "k"}})
			agree(t, d, []*model.Table{l, r}, &ast.Query{Root: &ast.Join{L: scan("l"), R: scan("r"), How: how, LeftOn: "v", RightOn: "v"}})
		}
	},
	sqlgen.RJoinKeys: func(t *testing.T, d engine) {
		l := table("l", col{"i", model.Int32, []any{int64(1), int64(-1), nil, int64(math.MaxInt32)}},
			col{"f", model.Float64, []any{math.NaN(), 0.0, math.Copysign(0, -1), 1.5}}, col{"s", model.String, []any{"a", "", nil, "a\x00b"}})
		r := table("r", col{"j", model.Int64, []any{int64(1), int64(math.MaxInt32), int64(1) << 40, nil}},
			col{"g", model.Float64, []any{math.NaN(), math.Copysign(0, -1), nil, 1.5}}, col{"t", model.String, []any{"a", "a", "", "a\x00b"}})
		for _, on := range [][2]string{{"i", "j"}, {"f", "g"}, {"s", "t"}} {
			for _, how := range []ast.JoinHow{ast.Inner, ast.Left, ast.Semi, ast.Anti} {
				agree(t, d, []*model.Table{l, r}, &ast.Query{Root: &ast.Join{L: scan("l"), R: scan("r"), How: how, LeftOn: on[0], RightOn: on[1]}})
			}
		}
	},
	sqlgen.RSelectLit: func(t *testing.T, d engine) {
		for _, n := range []int{0, 3} {
			tab := table("t", col{"x", model.Int64, ints64[:n]})
			agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), lit(model.Int64, 1), lit(model.String, "x"))})
		}
	},
	sqlgen.TNulls: func(t *testing.T, d engine) {
		tab := table("t", col{"x", model.Int64, ints64}, col{"s", model.String, strs[:len(ints64)]})
		for _, desc := range []bool{false, true} {
			for _, last := range []bool{false, true} {
				agree(t, d, []*model.Table{tab}, &ast.Query{Root: sortBy(scan("t"), ast.SortKey{Col: "x", Desc: desc, NullsLast: last})})
				agree(t, d, []*model.Table{tab}, &ast.Query{Root: sortBy(scan("t"), ast.SortKey{Col: "s", Desc: desc, NullsLast: last})})
			}
		}
	},
	sqlgen.TSortStable: func(t *testing.T, d engine) {
		tab := table("t", col{"k", model.Int32, []any{int64(2), int64(1), int64(2), nil, int64(1), int64(2), nil}},
			col{"v", model.Int64, []any{int64(0), int64(1), int64(2), int64(3), int64(4), int64(5), int64(6)}})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sortBy(scan("t"), ast.SortKey{Col: "k", Desc: true})})
	},
	sqlgen.TKeyZero: func(t *testing.T, d engine) {
		f := table("f", col{"x", model.Float64, append(append([]any{}, floats...), math.NaN(), 0.0, math.Copysign(0, -1))})
		agree(t, d, []*model.Table{f}, &ast.Query{Root: sortBy(scan("f"), ast.SortKey{Col: "x"})})
		agree(t, d, []*model.Table{f}, &ast.Query{Root: sortBy(scan("f"), ast.SortKey{Col: "x", Desc: true, NullsLast: true})})
		agree(t, d, []*model.Table{f}, &ast.Query{Root: group(scan("f"), nil, false, agg(ast.NUnique, c("x", model.Float64)))})
		r := table("r", col{"y", model.Float64, []any{math.NaN(), 0.0, math.Copysign(0, -1), 7.5, nil}})
		for _, how := range []ast.JoinHow{ast.Inner, ast.Semi, ast.Anti} {
			agree(t, d, []*model.Table{f, r}, &ast.Query{Root: &ast.Join{L: scan("f"), R: scan("r"), How: how, LeftOn: "x", RightOn: "y"}})
		}
	},
	sqlgen.TIEEECmp: func(t *testing.T, d engine) {
		fs := []any{math.NaN(), math.Inf(-1), -1.0, math.Copysign(0, -1), 0.0, 1.0, math.Inf(1), nil}
		tab := pairs("t", model.Float64, fs, model.Float64, fs)
		a, b := c("a", model.Float64), c("b", model.Float64)
		var es []ast.Expr
		for _, op := range []ast.BinOp{ast.Eq, ast.Ne, ast.Lt, ast.Le, ast.Gt, ast.Ge} {
			es = append(es, bin(op, a, b), bin(op, a, lit(model.Float64, math.NaN())), bin(op, a, lit(model.Float64, 0.0)))
		}
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), es...)})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: filter(scan("t"), bin(ast.Ne, a, b))})
	},
	sqlgen.TCmpPromote: func(t *testing.T, d engine) {
		tab := pairs("t", model.Int64, []any{int64(1<<53 + 1), int64(1 << 53), int64(math.MaxInt64), int64(-3), nil},
			model.Float64, []any{float64(1 << 53), 9.223372036854775807e18, -3.0, math.NaN(), nil})
		a, b := c("a", model.Int64), c("b", model.Float64)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), bin(ast.Eq, a, b), bin(ast.Lt, a, b),
			bin(ast.Ge, lossy(a, model.Int32), a), bin(ast.Eq, a, lit(model.Int8, -3)))})
	},
	sqlgen.TIntWrap: func(t *testing.T, d engine) {
		tab := pairs("t", model.Int64, ints64, model.Int64, ints64)
		a, b := c("a", model.Int64), c("b", model.Int64)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), bin(ast.Add, a, b), bin(ast.Sub, a, b),
			bin(ast.Mul, a, b), un(ast.Neg, a), un(ast.Abs, a))})
		a8, b8 := lossy(a, model.Int8), lossy(b, model.Int8)
		a16, b32 := lossy(a, model.Int16), lossy(b, model.Int32)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), bin(ast.Add, a8, b8), bin(ast.Mul, a8, b8),
			un(ast.Neg, a8), un(ast.Abs, a8), bin(ast.Mul, a16, a16), bin(ast.Sub, b32, a16), un(ast.Neg, b32))})
	},
	sqlgen.TFloor: func(t *testing.T, d engine) {
		ints := []any{int64(math.MinInt64), int64(-7), int64(-2), int64(-1), int64(0), nil, int64(1), int64(2), int64(7), int64(math.MaxInt64)}
		tab := pairs("t", model.Int64, ints, model.Int64, ints)
		a, b := c("a", model.Int64), c("b", model.Int64)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), bin(ast.FloorDiv, a, b), bin(ast.Mod, a, b),
			bin(ast.FloorDiv, lossy(a, model.Int8), lit(model.Int8, -1)), bin(ast.Mod, a, lit(model.Int8, -7)))})
		fs := []any{-7.5, -2.0, -0.5, math.Copysign(0, -1), 0.0, 0.5, 2.0, 7.5, math.Inf(1), math.NaN(), nil}
		ft := pairs("f", model.Float64, fs, model.Float64, fs)
		agree(t, d, []*model.Table{ft}, &ast.Query{Root: sel(scan("f"), bin(ast.FloorDiv, c("a", model.Float64), c("b", model.Float64)))})
	},
	sqlgen.TFloatMod: func(t *testing.T, d engine) {
		fs := []any{-7.5, -4.0, -2.0, math.Copysign(0, -1), 0.0, 2.0, 4.0, 7.5, math.Inf(1), math.Inf(-1), math.NaN(), 1e300, nil}
		tab := pairs("t", model.Float64, fs, model.Float64, fs)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), bin(ast.Mod, c("a", model.Float64), c("b", model.Float64)),
			bin(ast.Mod, c("a", model.Float64), lit(model.Int64, -2)))})
	},
	sqlgen.TDivFloat: func(t *testing.T, d engine) {
		tab := pairs("t", model.Int64, []any{int64(7), int64(-7), int64(0), int64(math.MaxInt64), nil}, model.Float64,
			[]any{2.0, 0.0, math.Copysign(0, -1), math.NaN(), nil})
		a, b := c("a", model.Int64), c("b", model.Float64)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), bin(ast.Div, a, b), bin(ast.Div, a, a),
			bin(ast.Div, a, lit(model.Int32, 0)), bin(ast.Div, b, b))})
	},
	sqlgen.TLitType: func(t *testing.T, d engine) {
		tab := table("t", col{"x", model.Int32, []any{int64(1), nil, int64(-3)}})
		x := c("x", model.Int32)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"),
			lit(model.Int8, -128), lit(model.Int16, 32767), lit(model.Int32, math.MinInt32), lit(model.Int64, math.MinInt64),
			lit(model.Float64, math.NaN()), lit(model.Float64, math.Copysign(0, -1)), lit(model.Float64, math.Inf(-1)),
			lit(model.Float64, 5e-324), lit(model.Float64, 0.1), lit(model.String, "a\x00b"), lit(model.String, "日本語"),
			lit(model.String, "it's"), lit(model.Bool, false), lit(model.Date, int32(math.MaxInt32)),
			lit(model.Date, int32(-719162)), null(model.Int16), null(model.Date), null(model.String),
			bin(ast.Add, x, lit(model.Int8, 100)), bin(ast.Mul, x, lit(model.Float64, 2.5)))})
	},
	sqlgen.TPromote: func(t *testing.T, d engine) {
		tab := pairs("t", model.Int32, ints32, model.Float64, []any{0.5, -0.0, math.NaN(), nil})
		a, b := c("a", model.Int32), c("b", model.Float64)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), bin(ast.Add, lossy(a, model.Int8), lit(model.Int64, math.MaxInt64)),
			bin(ast.Add, a, b), bin(ast.Mul, lossy(a, model.Int16), a), bin(ast.Sub, b, a))})
	},
	sqlgen.TKleene: func(t *testing.T, d engine) {
		tab := pairs("t", model.Bool, bools, model.Bool, bools)
		a, b := c("a", model.Bool), c("b", model.Bool)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), bin(ast.And, a, b), bin(ast.Or, a, b), un(ast.Not, a))})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: filter(scan("t"), bin(ast.Or, a, un(ast.Not, b)))})
	},
	sqlgen.TWhenNull: func(t *testing.T, d engine) {
		tab := pairs("t", model.Bool, bools, model.Int64, []any{int64(1), nil})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), when(c("a", model.Bool), c("b", model.Int64), lit(model.Int64, 2)),
			when(bin(ast.Gt, c("b", model.Int64), lit(model.Int64, 0)), lit(model.String, "y"), lit(model.String, "n")))})
	},
	sqlgen.TFillWeak: func(t *testing.T, d engine) {
		tab := table("t", col{"i", model.Int32, ints32}, col{"f", model.Float64, floats[:len(ints32)]})
		i, f := c("i", model.Int32), c("f", model.Float64)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), fill(i, lit(model.Float64, 3.0)), fill(i, lit(model.Int64, 5)),
			fill(lossy(i, model.Int8), lit(model.Int64, 5000)), fill(i, lit(model.Float64, 2.5)), fill(f, lit(model.Int64, -1)),
			fill(f, lit(model.Float64, math.NaN())))})
	},
	sqlgen.TCastExact: func(t *testing.T, d engine) {
		tab := table("t", col{"f", model.Float64, append(append([]any{}, floats...), 1.5, -1.5, 2.5, 32767.0, 32768.0)})
		f := c("f", model.Float64)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), lossy(f, model.Int8), lossy(f, model.Int16),
			lossy(f, model.Int32), lossy(f, model.Int64))})
	},
	sqlgen.TCastRange: func(t *testing.T, d engine) {
		tab := table("t", col{"x", model.Int64, append(append([]any{}, ints64...), int64(127), int64(128), int64(-32769),
			int64(math.MaxInt32), int64(math.MaxInt32)+1, int64(math.MinInt32)-1)})
		x := c("x", model.Int64)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), lossy(x, model.Int8), lossy(x, model.Int16),
			lossy(x, model.Int32), lossy(x, model.Date), lossy(lossy(x, model.Int16), model.Int8))})
	},
	sqlgen.TParseInt: func(t *testing.T, d engine) {
		tab := table("t", col{"s", model.String, strs})
		s := c("s", model.String)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), lossy(s, model.Int8), lossy(s, model.Int16),
			lossy(s, model.Int32), lossy(s, model.Int64))})
	},
	sqlgen.TParseFloat: func(t *testing.T, d engine) {
		var ss []any
		ss = append(ss, strs...)
		for _, f := range someFloats(100) {
			ss = append(ss, fmt.Sprint(f), fmt.Sprintf("%.17e", f))
		}
		tab := table("t", col{"s", model.String, ss})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), lossy(c("s", model.String), model.Float64))})
	},
	sqlgen.TParseBool: func(t *testing.T, d engine) {
		tab := table("t", col{"s", model.String, strs})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), lossy(c("s", model.String), model.Bool))})
	},
	sqlgen.TParseDate: func(t *testing.T, d engine) {
		tab := table("t", col{"s", model.String, strs}, col{"d", model.Date, append(append([]any{}, days...), make([]any, len(strs)-len(days))...)})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), lossy(c("s", model.String), model.Date),
			lossy(cast(c("d", model.Date), model.String), model.Date))})
	},
	sqlgen.TDateDays: func(t *testing.T, d engine) {
		tab := table("t", col{"d", model.Date, days}, col{"k", model.Int32, ints32[:len(days)]})
		dd := c("d", model.Date)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sortBy(filter(scan("t"), bin(ast.Gt, dd, lit(model.Date, int32(-1)))),
			ast.SortKey{Col: "d", Desc: true})})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: group(scan("t"), nil, false, agg(ast.Min, dd), agg(ast.Max, dd),
			agg(ast.NUnique, dd))})
	},
	sqlgen.TDateText: func(t *testing.T, d engine) {
		tab := table("t", col{"d", model.Date, days})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), cast(c("d", model.Date), model.String))})
	},
	sqlgen.TFloatText: func(t *testing.T, d engine) {
		tab := table("t", col{"f", model.Float64, floatSeq(300)})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), cast(c("f", model.Float64), model.String))})
	},
	sqlgen.TText: func(t *testing.T, d engine) {
		tab := table("t", col{"i", model.Int64, ints64}, col{"b", model.Bool, append(append([]any{}, bools...), make([]any, len(ints64)-3)...)})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), cast(c("i", model.Int64), model.String),
			cast(c("b", model.Bool), model.String), cast(lossy(c("i", model.Int64), model.Int8), model.String))})
	},
	sqlgen.TSum: func(t *testing.T, d engine) {
		tab := table("t", col{"k", model.Int64, []any{int64(1), int64(1), int64(2), int64(2), int64(3), nil}},
			col{"x", model.Int64, []any{int64(math.MaxInt64), int64(math.MaxInt64), int64(math.MinInt64), int64(-1), nil, int64(5)}},
			col{"b", model.Bool, []any{true, true, false, nil, nil, true}})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: group(scan("t"), []ast.Expr{c("k", model.Int64)}, false,
			agg(ast.Sum, c("x", model.Int64)), agg(ast.Sum, c("b", model.Bool)), agg(ast.Sum, lossy(c("x", model.Int64), model.Int16)))})
	},
	sqlgen.TFloatAgg: func(t *testing.T, d engine) {
		tab := table("t", col{"k", model.Int64, []any{int64(1), int64(1), int64(1), int64(1), int64(2), int64(2), int64(3)}},
			col{"x", model.Float64, []any{1e16, 1.0, -1e16, 1.0, math.Copysign(0, -1), nil, nil}})
		x := c("x", model.Float64)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: group(scan("t"), []ast.Expr{c("k", model.Int64)}, true,
			agg(ast.Sum, x), agg(ast.Mean, x))})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: group(scan("t"), nil, false, agg(ast.Sum, x), agg(ast.Mean, x))})
		big := table("b", col{"x", model.Float64, floatSeq(3000)})
		agree(t, d, []*model.Table{big}, &ast.Query{Root: group(scan("b"), nil, false, agg(ast.Sum, c("x", model.Float64)),
			agg(ast.Mean, c("x", model.Float64)))})
	},
	sqlgen.TExtremum: func(t *testing.T, d engine) {
		tab := table("t", col{"k", model.Int64, []any{int64(1), int64(1), int64(1), int64(2), int64(2), int64(3), int64(3), int64(4)}},
			col{"f", model.Float64, []any{math.Copysign(0, -1), 0.0, math.NaN(), 0.0, math.Copysign(0, -1), math.NaN(), nil, 1.0}},
			col{"s", model.String, []any{"b", "a\x00", "a", "", nil, "é", "z", nil}},
			col{"b", model.Bool, []any{true, false, nil, true, true, nil, nil, false}})
		f, s, b := c("f", model.Float64), c("s", model.String), c("b", model.Bool)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: group(scan("t"), []ast.Expr{c("k", model.Int64)}, true,
			agg(ast.Min, f), agg(ast.Max, f), agg(ast.Min, s), agg(ast.Max, s), agg(ast.Min, b), agg(ast.Max, b))})
	},
	sqlgen.TMeanInt: func(t *testing.T, d engine) {
		tab := table("t", col{"k", model.Int64, []any{int64(1), int64(1), int64(1), int64(2), int64(2), int64(3)}},
			col{"x", model.Int64, []any{int64(math.MaxInt64), int64(math.MaxInt64), int64(3), int64(-7), int64(2), nil}})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: group(scan("t"), []ast.Expr{c("k", model.Int64)}, false,
			agg(ast.Mean, c("x", model.Int64)), agg(ast.Mean, lossy(c("x", model.Int64), model.Int32)))})
	},
	sqlgen.TNUnique: func(t *testing.T, d engine) {
		tab := table("t", col{"f", model.Float64, append(append([]any{}, floats...), math.NaN(), nil)},
			col{"s", model.String, append(append([]any{}, strs[:len(floats)]...), "a", nil)})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: group(scan("t"), nil, false, agg(ast.NUnique, c("f", model.Float64)),
			agg(ast.NUnique, c("s", model.String)))})
	},
	sqlgen.TCount: func(t *testing.T, d engine) {
		tab := table("t", col{"k", model.Int32, ints32}, col{"x", model.Int64, ints64[:len(ints32)]})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: group(scan("t"), []ast.Expr{lossy(c("k", model.Int32), model.Int8)}, false,
			agg(ast.Count, c("x", model.Int64)), agg(ast.Len, nil), agg(ast.NUnique, c("x", model.Int64)))})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: filter(scan("t"), bin(ast.Gt, c("x", model.Int64), lit(model.Int64, 0))), Count: true})
	},
	sqlgen.TGroupNull: func(t *testing.T, d engine) {
		tab := table("t", col{"k", model.Int64, []any{nil, int64(1), nil, int64(2), int64(1)}}, col{"j", model.Int32, []any{nil, nil, int64(3), nil, nil}})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: group(scan("t"), []ast.Expr{c("k", model.Int64), c("j", model.Int32)}, false,
			agg(ast.Len, nil))})
	},
	sqlgen.TMaintain: func(t *testing.T, d engine) {
		tab := table("t", col{"k", model.Int64, []any{int64(3), int64(1), nil, int64(3), int64(2), int64(1), nil}})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: group(scan("t"), []ast.Expr{c("k", model.Int64)}, true, agg(ast.Len, nil))})
	},
	sqlgen.TWithSeq: func(t *testing.T, d engine) {
		tab := table("t", col{"a", model.Int64, ints64})
		a := c("a", model.Int64)
		x1 := bin(ast.Add, a, lit(model.Int64, 1))
		x2 := bin(ast.Mul, c("x1", model.Int64), lit(model.Int64, 2))
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: &ast.WithColumns{In: scan("t"), Cols: []ast.Named{{Name: "x1", E: x1},
			{Name: "x2", E: x2}, {Name: "a", E: bin(ast.Sub, c("x2", model.Int64), lit(model.Int64, 1))}}}})
	},
	sqlgen.TJoinNull: func(t *testing.T, d engine) {
		l := table("l", col{"k", model.Int64, []any{nil, int64(1), nil, int64(2)}})
		r := table("r", col{"j", model.Int64, []any{nil, int64(1), nil}})
		for _, how := range []ast.JoinHow{ast.Inner, ast.Left, ast.Semi, ast.Anti} {
			agree(t, d, []*model.Table{l, r}, &ast.Query{Root: &ast.Join{L: scan("l"), R: scan("r"), How: how, LeftOn: "k", RightOn: "j"}})
		}
	},
	sqlgen.TJoinShape: func(t *testing.T, d engine) {
		l := table("l", col{"k", model.Int32, []any{int64(1), int64(2), int64(math.MaxInt32), nil}}, col{"v", model.Float64, []any{0.5, nil, -0.0, 1.0}})
		r := table("r", col{"k", model.Int64, []any{int64(math.MaxInt32), int64(1), int64(1), int64(9)}}, col{"w", model.Bool, []any{true, nil, false, true}},
			col{"v", model.Float64, []any{1.0, 2.0, nil, 3.0}})
		for _, how := range []ast.JoinHow{ast.Inner, ast.Left} {
			agree(t, d, []*model.Table{l, r}, &ast.Query{Root: &ast.Join{L: scan("l"), R: scan("r"), How: how, LeftOn: "k", RightOn: "k"}})
			agree(t, d, []*model.Table{l, r}, &ast.Query{Root: &ast.Join{L: scan("l"), R: filter(scan("r"), c("w", model.Bool)), How: how,
				LeftOn: "v", RightOn: "v"}})
		}
	},
	sqlgen.TJoinOrder: func(t *testing.T, d engine) {
		l := table("l", col{"k", model.Int64, []any{int64(2), int64(1), int64(2), nil, int64(3), int64(1)}}, col{"i", model.Int32, []any{int64(0), int64(1), int64(2), int64(3), int64(4), int64(5)}})
		r := table("r", col{"j", model.Int64, []any{int64(1), int64(2), int64(1), int64(2), int64(1)}}, col{"o", model.Int32, []any{int64(0), int64(1), int64(2), int64(3), int64(4)}})
		for _, how := range []ast.JoinHow{ast.Inner, ast.Left} {
			agree(t, d, []*model.Table{l, r}, &ast.Query{Root: &ast.Join{L: scan("l"), R: scan("r"), How: how, LeftOn: "k", RightOn: "j", MaintainOrder: true}})
		}
		for _, how := range []ast.JoinHow{ast.Semi, ast.Anti} {
			agree(t, d, []*model.Table{l, r}, &ast.Query{Root: &ast.Join{L: scan("l"), R: scan("r"), How: how, LeftOn: "k", RightOn: "j"}})
		}
	},
	sqlgen.TSlice: func(t *testing.T, d engine) {
		var vals []any
		for i := range 10 {
			vals = append(vals, int64(i))
		}
		tab := table("t", col{"x", model.Int64, vals})
		for _, off := range []int{0, 1, 7, 9, 10, 11, -1, -3, -10, -11, -100} {
			for _, n := range []int{-1, 0, 1, 2, 5, 100} {
				agree(t, d, []*model.Table{tab}, &ast.Query{Root: &ast.Slice{In: scan("t"), Offset: off, Len: n}})
			}
		}
		for _, n := range []int{0, 1, 9, 10, 11} {
			agree(t, d, []*model.Table{tab}, &ast.Query{Root: &ast.Head{In: scan("t"), N: n}})
		}
	},
	sqlgen.TOrderPromise: func(t *testing.T, d engine) {
		tab := table("t", col{"x", model.Int64, ints64}, col{"f", model.Float64, floats[:len(ints64)]})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: &ast.Head{In: sortBy(scan("t"), ast.SortKey{Col: "f", Desc: true}), N: 5}})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: filter(scan("t"), bin(ast.Lt, c("x", model.Int64), lit(model.Int64, 3)))})
	},
}

// engine is what agree reads an answer from: a DB, or ursusEngine.
type engine interface {
	Load(tables []*model.Table) error
	Answer(ctx context.Context, q *ast.Query, s *sqlgen.SQL) (*arrow.Schema, []arrow.RecordBatch, error)
}

// ursusEngine answers a query by running its tree in ursus at the reference and
// exporting the frame as Arrow: the rules' queries run on the laptop, which never
// links DuckDB, and are held to agree across ursusConfigs before CI meets them.
type ursusEngine struct{ tables []*model.Table }

func (e *ursusEngine) Load(tables []*model.Table) error { e.tables = tables; return nil }

func (e *ursusEngine) Answer(ctx context.Context, q *ast.Query, s *sqlgen.SQL) (*arrow.Schema, []arrow.RecordBatch, error) {
	lf := build.Frame(q.Root, build.RecordsOf(e.tables, 1))
	opts := build.Options(model.Reference())
	if q.Count {
		n, err := lf.Count(ctx, opts...)
		if err != nil {
			return nil, nil, err
		}
		b := array.NewInt64Builder(memory.NewGoAllocator())
		defer b.Release()
		b.Append(n)
		arr := b.NewArray()
		defer arr.Release()
		schema := arrow.NewSchema([]arrow.Field{{Name: "n", Type: arrow.PrimitiveTypes.Int64}}, nil)
		return schema, []arrow.RecordBatch{array.NewRecordBatch(schema, []arrow.Array{arr}, 1)}, nil
	}
	df, err := lf.Collect(ctx, opts...)
	if err != nil {
		return nil, nil, err
	}
	rec, err := df.Record()
	if err != nil {
		return nil, nil, err
	}
	return rec.Schema(), []arrow.RecordBatch{rec}, nil
}

// TestTranslationRulesAgainstUrsus runs every rule's queries with ursus in
// DuckDB's place: each builds, translates, and gives one answer across
// ursusConfigs, so TestTranslationRules meets only DuckDB's differences on CI.
func TestTranslationRulesAgainstUrsus(t *testing.T) {
	for _, r := range sqlgen.Rules {
		if fn, ok := ruleCases[r.ID]; ok {
			t.Run(strings.ReplaceAll(r.ID, " ", "_"), func(t *testing.T) { fn(t, &ursusEngine{}) })
		}
	}
}

// TestTranslationRules runs each rule's queries, chosen to meet its edges, in
// ursus and in DuckDB, which must agree: the rule's expected answer is ursus's,
// never a typed table. Every rule but the ones no query reaches has queries.
func TestTranslationRules(t *testing.T) {
	d := open(t)
	for _, r := range sqlgen.Rules {
		fn, ok := ruleCases[r.ID]
		switch {
		case sqlgen.Unused(r.ID):
			continue
		case !ok:
			t.Errorf("%s has no test", r.ID)
			continue
		}
		t.Run(strings.ReplaceAll(r.ID, " ", "_"), func(t *testing.T) { fn(t, d) })
	}
	for id := range ruleCases {
		if !strings.HasPrefix(id, "R-") && !strings.HasPrefix(id, "T-") {
			t.Errorf("a test of no rule %q", id)
		}
	}
}
