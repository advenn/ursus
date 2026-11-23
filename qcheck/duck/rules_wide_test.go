package duck

import (
	"math"
	"testing"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/model"
	"github.com/advenn/ursus/qcheck/sqlgen"
)

// The wide rules' queries (step 539), one case a T- rule, each at its edges, as
// ruleCases' are: ursus and DuckDB must agree, the expected answer ursus's. A
// temporal value is its ticks, a Decimal its unscaled digits, as table takes them.
// Arithmetic and strict casts keep to values ursus answers, as agree fails on a
// refusal: a widening that can overflow is lossy, and a sum stays in range.

func fn(op ast.FnOp, x ast.Expr, s []string, i []int64, b bool) ast.Expr {
	return must(ast.NewFn(op, x, s, i, b))
}

const (
	usPerDay = int64(86_400e6)
	kolkata  = 4 // model.Zones' index of Asia/Kolkata
	utc      = 1
)

var (
	u8s  = []any{uint64(0), uint64(1), uint64(2), uint64(127), uint64(128), uint64(200), uint64(254), uint64(255), nil}
	u16s = []any{uint64(0), uint64(1), uint64(255), uint64(256), uint64(32768), uint64(65534), uint64(65535), nil}
	u64s = []any{uint64(0), uint64(1), uint64(1 << 32), uint64(1 << 63), uint64(math.MaxUint64 - 1), uint64(math.MaxUint64), nil}
	i16s = []any{int64(math.MinInt16), int64(-129), int64(-1), int64(0), int64(1), int64(255), int64(math.MaxInt16), nil}
	f32s = []any{float32(math.NaN()), float32(math.Inf(-1)), float32(-math.MaxFloat32), float32(-1.5),
		float32(math.Copysign(0, -1)), float32(0), nil, float32(0.1), float32(1.5), float32(16777216),
		float32(math.MaxFloat32), float32(math.Inf(1)), float32(math.SmallestNonzeroFloat32)}
	// decs are Decimal(12, 4)'s unscaled values: its ends, a scale's worth, a sign.
	decs = []any{"0", "1", "-1", "5", "-5", "5000", "-5000", "12345", "99999999", "-100000000",
		"999999999999", "-999999999999", nil}
	// instants are moderate Datetime(us) ticks: before and after the epoch, a day's
	// edges, a leap day, a month's last day, a half day for Round.
	instants = []any{int64(-usPerDay - 1), int64(-1), int64(0), int64(1), int64(usPerDay / 2),
		int64(19753*usPerDay + 3_600e6), int64(19782*usPerDay + 1), int64(19722*usPerDay + usPerDay - 1),
		int64(1_700_000_000_123_456), int64(-62135596800e6), int64(253402300799e6), nil}
	extremes = []any{int64(math.MinInt64), int64(math.MinInt64 + 1), int64(-1), int64(0), int64(1),
		int64(math.MaxInt64 - 1), int64(math.MaxInt64), nil}
	// civilDays are Date days: the epoch's neighbours, 0001-01-01, a month's last day,
	// a leap day, 9999-12-31.
	civilDays = []any{int64(-719162), int64(-1), int64(0), int64(1), int64(19753), int64(19782), int64(19722),
		int64(2932896), nil}
)

func init() {
	for id, f := range wideRuleCases {
		ruleCases[id] = f
	}
}

var wideRuleCases = map[string]func(t *testing.T, d engine){
	sqlgen.TUintWrap: func(t *testing.T, d engine) {
		for _, tc := range []struct {
			typ  model.Type
			vals []any
		}{{model.Uint8, u8s}, {model.Uint16, u16s}, {model.Uint64, u64s}} {
			tab := pairs("t", tc.typ, tc.vals, tc.typ, tc.vals)
			a, b := c("a", tc.typ), c("b", tc.typ)
			agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), bin(ast.Add, a, b), bin(ast.Sub, a, b),
				bin(ast.Mul, a, b), bin(ast.Lt, a, b))})
		}
		mixed := pairs("m", model.Int16, i16s, model.Uint8, u8s)
		a, b := c("a", model.Int16), c("b", model.Uint8)
		agree(t, d, []*model.Table{mixed}, &ast.Query{Root: sel(scan("m"), bin(ast.Add, a, b), bin(ast.Sub, b, a),
			bin(ast.Mul, a, b))})
	},
	sqlgen.TF32: func(t *testing.T, d engine) {
		tab := pairs("t", model.Float32, f32s, model.Float32, f32s)
		a, b := c("a", model.Float32), c("b", model.Float32)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), bin(ast.Add, a, b), bin(ast.Sub, a, b),
			bin(ast.Mul, a, b), bin(ast.Div, a, b), bin(ast.Lt, a, b), bin(ast.Eq, a, b))})
		mixed := pairs("m", model.Float32, f32s, model.Int16, i16s)
		x, i := c("a", model.Float32), c("b", model.Int16)
		agree(t, d, []*model.Table{mixed}, &ast.Query{Root: sel(scan("m"), bin(ast.Add, x, i), bin(ast.Mul, x, i))})
		one := table("f", col{"k", model.Int64, []any{int64(1), int64(1), int64(2), int64(2), int64(3), int64(3),
			int64(4), int64(4), int64(5), int64(5), int64(6), int64(6), int64(7)}}, col{"x", model.Float32, f32s})
		agree(t, d, []*model.Table{one}, &ast.Query{Root: group(scan("f"), []ast.Expr{c("k", model.Int64)}, true,
			agg(ast.Min, c("x", model.Float32)), agg(ast.Max, c("x", model.Float32)),
			agg(ast.NUnique, c("x", model.Float32)))})
		agree(t, d, []*model.Table{one}, &ast.Query{Root: sortBy(scan("f"), ast.SortKey{Col: "x"})})
	},
	sqlgen.TDec: func(t *testing.T, d engine) {
		dt := model.Decimal(12, 4)
		tab := pairs("t", dt, decs, dt, decs)
		a, b := c("a", dt), c("b", dt)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), bin(ast.Add, a, b), bin(ast.Sub, a, b),
			bin(ast.Mul, a, b), bin(ast.Lt, a, b), bin(ast.Eq, a, b))})
		mixed := pairs("m", dt, decs, model.Int32, ints32)
		x, i := c("a", dt), c("b", model.Int32)
		agree(t, d, []*model.Table{mixed}, &ast.Query{Root: sel(scan("m"), bin(ast.Add, x, i), bin(ast.Mul, x, i),
			bin(ast.Lt, x, i))})
		one := table("o", col{"x", dt, decs})
		x = c("x", dt)
		agree(t, d, []*model.Table{one}, &ast.Query{Root: sel(scan("o"), lossy(x, model.Decimal(5, 2)),
			cast(x, model.Decimal(18, 6)), cast(x, model.Decimal(38, 4)), lossy(x, model.Decimal(12, 0)))})
	},
	sqlgen.TDecFloat: func(t *testing.T, d engine) {
		// A Decimal becomes a Float64 by a cast (or a promotion sqlgen makes the
		// same way): its exact value's nearest double, at 12 digits and at 38.
		dt, wide := model.Decimal(12, 4), model.Decimal(38, 10)
		tab := table("t", col{"x", dt, decs}, col{"w", wide, []any{"0", "1", "-1", "5", "-5", "33333333333333333333",
			"10000000000000000000000000000000000000", "-99999999999999999999999999999999999999",
			"99999999999999999999999999999999999999", "12345678901234567890123456789012345678",
			"-12345678901234567890123456789012345678", "9007199254740993", nil}})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), cast(c("x", dt), model.Float64),
			cast(c("w", wide), model.Float64))})
	},
	sqlgen.TDecText: func(t *testing.T, d engine) {
		for _, dt := range []model.Type{model.Decimal(12, 4), model.Decimal(5, 2), model.Decimal(9, 0)} {
			vals := decs
			if dt.Prec() < 12 {
				vals = []any{"0", "1", "-1", "5", "-5", "99999", "-99999", "10", nil}
			}
			one := table("o", col{"x", dt, vals})
			agree(t, d, []*model.Table{one}, &ast.Query{Root: sel(scan("o"), cast(c("x", dt), model.String))})
		}
	},
	sqlgen.TDecSum: func(t *testing.T, d engine) {
		dt := model.Decimal(12, 4)
		ks := []any{int64(1), int64(1), int64(2), int64(2), int64(3), int64(3), int64(4), int64(4), int64(5),
			int64(5), int64(6), int64(6), int64(7)}
		tab := table("g", col{"k", model.Int64, ks}, col{"x", dt, decs})
		nulls := table("n", col{"k", model.Int64, []any{int64(1), int64(2)}}, col{"x", dt, []any{nil, nil}})
		x := c("x", dt)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: group(scan("g"), []ast.Expr{c("k", model.Int64)}, true,
			agg(ast.Sum, x))})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: group(scan("g"), nil, false, agg(ast.Sum, x))})
		agree(t, d, []*model.Table{nulls}, &ast.Query{Root: group(scan("n"), []ast.Expr{c("k", model.Int64)}, true,
			agg(ast.Sum, x))})
	},
	sqlgen.TTicks: func(t *testing.T, d engine) {
		dtus, dur := model.Datetime(model.Micro, 0), model.Duration(model.Nano)
		tm := model.Time(model.Milli)
		tods := []any{int64(0), int64(1), int64(43_200_000), int64(86_399_999), nil, int64(1), int64(0), nil}
		tab := table("t", col{"i", dtus, extremes}, col{"d", dur, extremes}, col{"m", tm, tods})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), c("i", dtus), c("d", dur), c("m", tm))})
		for _, k := range []string{"i", "d", "m"} {
			agree(t, d, []*model.Table{tab}, &ast.Query{Root: sortBy(scan("t"), ast.SortKey{Col: k})})
		}
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: group(scan("t"), nil, false, agg(ast.Min, c("i", dtus)),
			agg(ast.Max, c("d", dur)), agg(ast.Max, c("m", tm)))})
	},
	sqlgen.TRetime: func(t *testing.T, d engine) {
		secs := []any{int64(math.MinInt64), int64(-86_401), int64(-1), int64(0), int64(1), int64(9_223_372_036),
			int64(9_223_372_037), int64(math.MaxInt64), nil}
		nanos := []any{int64(math.MinInt64), int64(-1_000_000_001), int64(-1), int64(0), int64(1), int64(999_999_999),
			int64(math.MaxInt64), nil, int64(0)}
		ds, dn := model.Datetime(model.Sec, 0), model.Datetime(model.Nano, 0)
		vs, vn := model.Duration(model.Sec), model.Duration(model.Nano)
		tab := table("t", col{"ds", ds, secs}, col{"dn", dn, nanos}, col{"vs", vs, secs}, col{"vn", vn, nanos})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"),
			lossy(c("ds", ds), dn), cast(c("dn", dn), ds), cast(c("dn", dn), model.Datetime(model.Milli, 0)),
			lossy(c("vs", vs), model.Duration(model.Micro)), cast(c("vn", vn), vs), cast(c("vn", vn), model.Duration(model.Milli)))})
	},
	sqlgen.TInstantArith: func(t *testing.T, d engine) {
		ms := []any{int64(-86_400_000), int64(-1), int64(0), int64(1), int64(1_700_000_000_000), nil}
		us := []any{int64(-1), int64(0), int64(1_700_000_000_000_000), int64(usPerDay), nil}
		dms, dus := model.Datetime(model.Milli, 0), model.Datetime(model.Micro, 0)
		tab := pairs("t", dms, ms, dus, us)
		a, b := c("a", dms), c("b", dus)
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), bin(ast.Sub, a, b), bin(ast.Sub, b, a))})
		days := []any{int64(-719162), int64(-1), int64(0), int64(1), int64(19782), nil}
		dd := pairs("d", model.Date, days, model.Date, days)
		agree(t, d, []*model.Table{dd}, &ast.Query{Root: sel(scan("d"), bin(ast.Sub, c("a", model.Date), c("b", model.Date)))})
		durs := []any{int64(-86_400_000), int64(-1), int64(0), int64(1), int64(3_600_000), nil}
		vms := model.Duration(model.Milli)
		plus := pairs("p", dus, us, vms, durs)
		agree(t, d, []*model.Table{plus}, &ast.Query{Root: sel(scan("p"), bin(ast.Add, c("a", dus), c("b", vms)),
			bin(ast.Sub, c("a", dus), c("b", vms)))})
		dateDur := pairs("q", model.Date, days, model.Duration(model.Sec), []any{int64(-86_401), int64(0), int64(59), nil})
		agree(t, d, []*model.Table{dateDur}, &ast.Query{Root: sel(scan("q"),
			bin(ast.Add, c("a", model.Date), c("b", model.Duration(model.Sec))))})
		times := pairs("r", vms, durs, model.Int32, []any{int64(-7), int64(-1), int64(0), int64(1), int64(7), nil})
		agree(t, d, []*model.Table{times}, &ast.Query{Root: sel(scan("r"), bin(ast.Mul, c("a", vms), c("b", model.Int32)))})
	},
	sqlgen.TCivil: func(t *testing.T, d engine) {
		dtus, dtns := model.Datetime(model.Micro, 0), model.Datetime(model.Nano, 0)
		tns := model.Time(model.Nano)
		tods := []any{int64(0), int64(1), int64(1_001_001_001), int64(43_200e9), int64(86_399_999_999_999), nil,
			int64(59e9), int64(3_600e9 - 1), int64(0), int64(1), int64(2), nil}
		tab := table("t", col{"u", dtus, instants}, col{"n", dtns, instants}, col{"t", tns, tods})
		parts := []ast.FnOp{ast.DtYear, ast.DtMonth, ast.DtDay, ast.DtHour, ast.DtMinute, ast.DtSecond,
			ast.DtMillisecond, ast.DtMicrosecond, ast.DtNanosecond, ast.DtWeekday, ast.DtOrdinalDay}
		var es []ast.Expr
		for _, op := range parts {
			es = append(es, fn(op, c("u", dtus), nil, nil, false), fn(op, c("n", dtns), nil, nil, false))
		}
		for _, op := range []ast.FnOp{ast.DtHour, ast.DtMinute, ast.DtSecond, ast.DtMillisecond, ast.DtMicrosecond,
			ast.DtNanosecond} {
			es = append(es, fn(op, c("t", tns), nil, nil, false))
		}
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), es...)})
		dates := table("d", col{"x", model.Date, civilDays})
		es = nil
		for _, op := range []ast.FnOp{ast.DtYear, ast.DtMonth, ast.DtDay, ast.DtWeekday, ast.DtOrdinalDay} {
			es = append(es, fn(op, c("x", model.Date), nil, nil, false))
		}
		agree(t, d, []*model.Table{dates}, &ast.Query{Root: sel(scan("d"), es...)})
	},
	sqlgen.TGrid: func(t *testing.T, d engine) {
		dtus := model.Datetime(model.Micro, 0)
		one := table("u", col{"x", dtus, instants})
		var es []ast.Expr
		for _, p := range []int64{1e3, 1e9, 900e9, 3_600e9, 86_400e9, 7 * 86_400e9} {
			es = append(es, fn(ast.DtTruncate, c("x", dtus), nil, []int64{p}, false),
				fn(ast.DtRound, c("x", dtus), nil, []int64{p}, false))
		}
		agree(t, d, []*model.Table{one}, &ast.Query{Root: sel(scan("u"), es...)})
		dates := table("d", col{"x", model.Date, civilDays})
		agree(t, d, []*model.Table{dates}, &ast.Query{Root: sel(scan("d"),
			fn(ast.DtTruncate, c("x", model.Date), nil, []int64{7 * 86_400e9}, false),
			fn(ast.DtRound, c("x", model.Date), nil, []int64{7 * 86_400e9}, false))})
	},
	sqlgen.TOffset: func(t *testing.T, d engine) {
		dtus := model.Datetime(model.Micro, 0)
		one := table("u", col{"x", dtus, instants})
		var es []ast.Expr
		for _, iv := range []string{"1us", "-1ms", "90m", "-36h", "1d", "2w", "1mo", "-1mo", "13mo", "1y", "-1y"} {
			es = append(es, fn(ast.DtOffsetBy, c("x", dtus), []string{iv}, nil, false))
		}
		agree(t, d, []*model.Table{one}, &ast.Query{Root: sel(scan("u"), es...)})
		dates := table("d", col{"x", model.Date, civilDays[1 : len(civilDays)-2]})
		es = nil
		for _, iv := range []string{"1d", "-1d", "2w", "1mo", "-1mo", "1y"} {
			es = append(es, fn(ast.DtOffsetBy, c("x", model.Date), []string{iv}, nil, false))
		}
		agree(t, d, []*model.Table{dates}, &ast.Query{Root: sel(scan("d"), es...)})
	},
	sqlgen.TDateOf: func(t *testing.T, d engine) {
		dtus, zoned := model.Datetime(model.Micro, 0), model.Datetime(model.Micro, kolkata)
		tab := table("t", col{"n", dtus, instants}, col{"z", zoned, instants})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"),
			fn(ast.DtDate, c("n", dtus), nil, nil, false), fn(ast.DtTime, c("n", dtus), nil, nil, false),
			cast(c("z", zoned), model.Date), cast(c("n", dtus), model.Date),
			cast(c("z", zoned), model.Time(model.Micro)), cast(c("n", dtus), model.Time(model.Milli)))})
	},
	sqlgen.TZoneTicks: func(t *testing.T, d engine) {
		zu := model.Datetime(model.Micro, utc)
		tab := table("t", col{"x", zu, extremes})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"),
			fn(ast.DtConvertTimeZone, c("x", zu), []string{"Asia/Kolkata"}, nil, false),
			cast(c("x", zu), model.Datetime(model.Micro, kolkata)))})
	},
	sqlgen.TTotal: func(t *testing.T, d engine) {
		vals := []any{int64(math.MinInt64), int64(-86_400_000_000_001), int64(-1), int64(0), int64(1),
			int64(3_599_999_999_999), int64(math.MaxInt64), nil}
		dn, ds := model.Duration(model.Nano), model.Duration(model.Sec)
		tab := table("t", col{"n", dn, vals}, col{"s", ds, vals})
		var es []ast.Expr
		for _, op := range []ast.FnOp{ast.DtTotalDays, ast.DtTotalHours, ast.DtTotalMinutes, ast.DtTotalSeconds,
			ast.DtTotalMilliseconds, ast.DtTotalMicroseconds, ast.DtTotalNanoseconds} {
			es = append(es, fn(op, c("n", dn), nil, nil, false))
		}
		for _, op := range []ast.FnOp{ast.DtTotalDays, ast.DtTotalHours, ast.DtTotalSeconds} {
			es = append(es, fn(op, c("s", ds), nil, nil, false))
		}
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), es...)})
	},
	sqlgen.TInstantText: func(t *testing.T, d engine) {
		ds, dms, dns := model.Datetime(model.Sec, 0), model.Datetime(model.Milli, 0), model.Datetime(model.Nano, 0)
		secs := []any{int64(-62135596800), int64(-86_401), int64(-1), int64(0), int64(1), int64(1_700_000_000),
			int64(253402300799), nil}
		mss := []any{int64(-62135596800e3), int64(-1), int64(0), int64(1), int64(1_700_000_000_123), int64(-1_500),
			int64(253402300799e3), nil}
		nss := []any{int64(-1), int64(0), int64(1), int64(1_700_000_000_123_456_789), int64(-1_000_000_001),
			int64(math.MinInt64), int64(math.MaxInt64), nil}
		tus := model.Time(model.Micro)
		tods := []any{int64(0), int64(1), int64(43_200e6), int64(86_399_999_999), int64(1_000_000), int64(59_000_001),
			int64(3_600e6), nil}
		tab := table("t", col{"s", ds, secs}, col{"m", dms, mss}, col{"n", dns, nss}, col{"t", tus, tods})
		agree(t, d, []*model.Table{tab}, &ast.Query{Root: sel(scan("t"), cast(c("s", ds), model.String),
			cast(c("m", dms), model.String), cast(c("n", dns), model.String), cast(c("t", tus), model.String))})
	},
	sqlgen.TStrMatch: func(t *testing.T, d engine) {
		one := table("s", col{"x", model.String, strs})
		x := c("x", model.String)
		var es []ast.Expr
		for _, p := range []string{"", "a", "1", "é", "-", "nan"} {
			es = append(es, fn(ast.StrContains, x, []string{p}, nil, true), fn(ast.StrStartsWith, x, []string{p}, nil, false),
				fn(ast.StrEndsWith, x, []string{p}, nil, false))
		}
		for _, re := range []string{"^a", "[0-9]+", "a|b", `\d`, `\s`, "^.{3}$", "[^a-z]", "^$"} {
			es = append(es, fn(ast.StrContains, x, []string{re}, nil, false))
		}
		agree(t, d, []*model.Table{one}, &ast.Query{Root: sel(scan("s"), es...)})
	},
	sqlgen.TStrReplace: func(t *testing.T, d engine) {
		one := table("s", col{"x", model.String, strs})
		x := c("x", model.String)
		agree(t, d, []*model.Table{one}, &ast.Query{Root: sel(scan("s"),
			fn(ast.StrReplace, x, []string{"a", "XY"}, nil, true), fn(ast.StrReplaceAll, x, []string{"1", ""}, nil, true),
			fn(ast.StrReplaceAll, x, []string{".", "<>"}, nil, true), fn(ast.StrReplace, x, []string{"[0-9]+", "#"}, nil, false),
			fn(ast.StrReplaceAll, x, []string{"[0-9]", "日"}, nil, false), fn(ast.StrReplaceAll, x, []string{"x*", "-"}, nil, false))})
	},
	sqlgen.TStrLen: func(t *testing.T, d engine) {
		one := table("s", col{"x", model.String, strs})
		x := c("x", model.String)
		agree(t, d, []*model.Table{one}, &ast.Query{Root: sel(scan("s"), fn(ast.StrLenBytes, x, nil, nil, false),
			fn(ast.StrLenChars, x, nil, nil, false))})
	},
	sqlgen.TStrSlice: func(t *testing.T, d engine) {
		one := table("s", col{"x", model.String, strs})
		x := c("x", model.String)
		var es []ast.Expr
		for _, ol := range [][2]int64{{0, 0}, {0, 2}, {1, 1000}, {-3, 2}, {-1, 10}, {5, 3}, {-10, 3}, {100, 1}} {
			es = append(es, fn(ast.StrSlice, x, nil, []int64{ol[0], ol[1]}, false))
		}
		agree(t, d, []*model.Table{one}, &ast.Query{Root: sel(scan("s"), es...)})
	},
	sqlgen.TStrStrip: func(t *testing.T, d engine) {
		one := table("s", col{"x", model.String, strs})
		x := c("x", model.String)
		agree(t, d, []*model.Table{one}, &ast.Query{Root: sel(scan("s"),
			fn(ast.StrStripPrefix, x, []string{"1"}, nil, false), fn(ast.StrStripSuffix, x, []string{"0"}, nil, false),
			fn(ast.StrStripPrefix, x, []string{""}, nil, false), fn(ast.StrStripSuffix, x, []string{"nan"}, nil, false))})
	},
	sqlgen.TStrSplit: func(t *testing.T, d engine) {
		one := table("s", col{"x", model.String, strs})
		x := c("x", model.String)
		agree(t, d, []*model.Table{one}, &ast.Query{Root: sel(scan("s"), fn(ast.StrSplitLen, x, []string{","}, nil, false),
			fn(ast.StrSplitLen, x, []string{"a"}, nil, false), fn(ast.StrSplitLen, x, []string{"0"}, nil, false),
			fn(ast.StrSplitLen, x, []string{"ab"}, nil, false))})
	},
	sqlgen.TParseUint: func(t *testing.T, d engine) {
		one := table("s", col{"x", model.String, strs})
		x := c("x", model.String)
		agree(t, d, []*model.Table{one}, &ast.Query{Root: sel(scan("s"), lossy(x, model.Uint8), lossy(x, model.Uint16),
			lossy(x, model.Uint32), lossy(x, model.Uint64))})
	},
}
