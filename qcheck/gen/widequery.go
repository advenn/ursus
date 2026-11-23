package gen

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/model"
)

// The wide operations (step 539): the string and temporal namespaces' functions,
// arithmetic and comparison across the unsigned integers, Float32, Decimal and the
// temporal types, and casts among them, drawn only in a wide case.
//
// Where ursus refuses a value while a query runs, the generator keeps the value
// away, as step 512 kept strict casts away: a refusal's presence depends on how
// many rows a Head lets through, which differs by batch size and the optimizer's
// limit pushdown. So Truncate, Round, OffsetBy and temporal arithmetic read a
// column whose base column is tame or wild, never extreme (boundedCols), whose
// values a period, an interval or a wild Duration cannot carry out of their type;
// a Duration's Sum, Mean and product only a tame one's.

// wideType is anyType's i-th wide type, its parameters drawn.
func (g *qgen) wideType(i int) model.Type {
	switch i {
	case 0:
		return model.Uint8
	case 1:
		return model.Uint16
	case 2:
		return model.Uint32
	case 3:
		return model.Uint64
	case 4:
		return model.Float32
	case 5:
		z := 0
		if g.r.IntN(2) == 0 {
			z = 1 + g.r.IntN(len(model.Zones)-1)
		}
		return model.Datetime(model.Units[g.r.IntN(4)], z)
	case 6:
		return model.Duration(model.Units[g.r.IntN(4)])
	case 7:
		return model.Time(model.Units[g.r.IntN(4)])
	}
	return Decimals[g.r.IntN(len(Decimals))]
}

// pickType draws from core's types, and in a wide case from extra's too.
func (g *qgen) pickType(core []model.Type, extra func() []model.Type) model.Type {
	if !g.wide {
		return core[g.r.IntN(len(core))]
	}
	all := append(slices.Clone(core), extra()...)
	return all[g.r.IntN(len(all))]
}

// sumTypes are the wide types a Sum is drawn of: the unsigned integers, Float32, a
// Decimal of 18 digits or fewer, whose sum of 8193 rows keeps within 38, and the
// Durations a tame column holds.
func (g *qgen) sumTypes(in *ast.Info) []model.Type {
	out := []model.Type{model.Uint8, model.Uint32, model.Uint64, model.Float32, g.sumDecimal(in)}
	return append(out, g.tameDurations(in)...)
}

func (g *qgen) meanTypes(in *ast.Info) []model.Type {
	out := []model.Type{model.Uint16, model.Uint64, model.Float32, g.sumDecimal(in)}
	return append(out, g.tameDurations(in)...)
}

// sumDecimal is a Decimal of in's columns of 18 digits or fewer, or Decimal(12, 4).
func (g *qgen) sumDecimal(in *ast.Info) model.Type {
	for _, c := range in.Cols {
		if c.Type.IsDecimal() && c.Type.Prec() <= 18 && !c.Noisy {
			return c.Type
		}
	}
	return model.Decimal(12, 4)
}

func (g *qgen) tameDurations(in *ast.Info) []model.Type {
	var out []model.Type
	for _, c := range in.Cols {
		if c.Type.IsDuration() && g.bounded(c, model.Tame) && !slices.Contains(out, c.Type) {
			out = append(out, c.Type)
		}
	}
	return out
}

// bounded is whether c holds its base column's values as read, a column whose
// profile is at most p.
func (g *qgen) bounded(c ast.Column, p model.Profile) bool {
	if c.Origin == "" || c.Noisy {
		return false
	}
	tname, cname, _ := strings.Cut(c.Origin, ".")
	t := g.tables[tname]
	if t == nil {
		return false
	}
	mc := t.Col(cname)
	return mc != nil && mc.Profile <= p
}

// boundedCols are in's columns of type t bounded by p.
func (g *qgen) boundedCols(in *ast.Info, t model.Type, p model.Profile) []ast.Column {
	var out []ast.Column
	for _, c := range in.Cols {
		if c.Type == t && g.bounded(c, p) {
			out = append(out, c)
		}
	}
	return out
}

// boundedOf is a bounded column of in whose type ok takes, or nil.
func (g *qgen) boundedOf(in *ast.Info, p model.Profile, ok func(model.Type) bool) *ast.Col {
	var cs []ast.Column
	for _, c := range in.Cols {
		if ok(c.Type) && g.bounded(c, p) {
			cs = append(cs, c)
		}
	}
	if len(cs) == 0 {
		return nil
	}
	c := cs[g.r.IntN(len(cs))]
	return &ast.Col{Name: c.Name, T: c.Type}
}

// litOf is a literal of t whose value is drawn by profile p or a tamer one, never
// null: an operand a bounded column may meet.
func (g *qgen) litOf(t model.Type, p model.Profile) *ast.Lit {
	v := values{r: g.r, c: model.Column{Type: t, Profile: model.Profile(g.r.IntN(int(p) + 1))}, wide: true}
	v.pool()
	if t == model.Date {
		return &ast.Lit{T: t, V: int32(v.date())}
	}
	return wideLit(&v, t)
}

// wideLit is a literal of a wide type, by v's profile.
func wideLit(v *values, t model.Type) *ast.Lit {
	switch {
	case t.IsUnsigned():
		return &ast.Lit{T: t, V: v.unsigned(t.Bits())}
	case t == model.Float32:
		return &ast.Lit{T: t, V: v.float32()}
	case t.IsDatetime():
		return &ast.Lit{T: t, V: v.datetime(t)}
	case t.IsDuration():
		return &ast.Lit{T: t, V: v.duration(t.Unit())}
	case t.IsTime():
		return &ast.Lit{T: t, V: v.time(t.Unit())}
	case t.IsDecimal():
		return &ast.Lit{T: t, V: v.decimal(t)}
	}
	panic("gen: no literal of " + t.String())
}

// wideSample is the value at i of a wide column of type t, as a literal, or nil.
func wideSample(col arrow.Array, i int, t model.Type) *ast.Lit {
	switch a := col.(type) {
	case *array.Int8:
		return &ast.Lit{T: model.Int8, V: int64(a.Value(i))}
	case *array.Int16:
		return &ast.Lit{T: model.Int16, V: int64(a.Value(i))}
	case *array.Uint8:
		return &ast.Lit{T: model.Uint8, V: uint64(a.Value(i))}
	case *array.Uint16:
		return &ast.Lit{T: model.Uint16, V: uint64(a.Value(i))}
	case *array.Uint32:
		return &ast.Lit{T: model.Uint32, V: uint64(a.Value(i))}
	case *array.Uint64:
		return &ast.Lit{T: model.Uint64, V: a.Value(i)}
	case *array.Float32:
		return &ast.Lit{T: model.Float32, V: a.Value(i)}
	case *array.Timestamp:
		return &ast.Lit{T: t, V: int64(a.Value(i))}
	case *array.Duration:
		return &ast.Lit{T: t, V: int64(a.Value(i))}
	case *array.Time32:
		return &ast.Lit{T: t, V: int64(a.Value(i))}
	case *array.Time64:
		return &ast.Lit{T: t, V: int64(a.Value(i))}
	case *array.Decimal128:
		return &ast.Lit{T: t, V: a.Value(i).BigInt().String()}
	}
	return nil
}

// anyDecimal is a Decimal of in's columns, or one of Decimals.
func (g *qgen) anyDecimal(in *ast.Info) model.Type {
	var ds []model.Type
	for _, c := range in.Cols {
		if c.Type.IsDecimal() && !c.Noisy {
			ds = append(ds, c.Type)
		}
	}
	if len(ds) > 0 && g.r.IntN(3) > 0 {
		return ds[g.r.IntN(len(ds))]
	}
	return Decimals[g.r.IntN(len(Decimals))]
}

// column is an expression for a WithColumns or a Select: of a type drawn among
// in's, or, in a wide case sometimes, arithmetic whose type its operands decide, a
// Decimal's sum or a Datetime's difference.
func (g *qgen) column(in *ast.Info) ast.Expr {
	if g.wide && g.r.IntN(4) == 0 {
		if e := g.freeExpr(in); e != nil {
			return e
		}
	}
	return g.expr(in, g.anyType(in), 2)
}

// cmpPartner is a type compared with one of t: t, or another it meets.
func (g *qgen) cmpPartner(t model.Type) model.Type {
	var ts []model.Type
	switch {
	case t.IsNumeric() || t.IsDecimal():
		ts = []model.Type{model.Int64, model.Int32, model.Float64, model.Int8, model.Uint8, model.Uint32,
			model.Uint64, model.Float32, Decimals[g.r.IntN(len(Decimals))]}
	case t.IsTemporal():
		// Two units of one kind meet at the finer, a cast that refuses a value the
		// finer cannot hold while the query runs (dtype.PromoteExact's doc), which a
		// pruned filter never reaches: a comparison of temporals is of one type.
		return t
	}
	ts = slices.DeleteFunc(ts, func(o model.Type) bool { return !model.Comparable(t, o) })
	if len(ts) == 0 {
		return t
	}
	return ts[g.r.IntN(len(ts))]
}

// mixedSign is an arithmetic of an unsigned integer and a signed one meeting at t.
func (g *qgen) mixedSign(in *ast.Info, t model.Type, depth int) (ast.Expr, error) {
	var us []model.Type
	for _, u := range []model.Type{model.Uint8, model.Uint16, model.Uint32} {
		if 2*u.Bits() <= t.Bits() {
			us = append(us, u)
		}
	}
	if len(us) == 0 {
		return nil, fmt.Errorf("no unsigned integer meets a signed one at %s", t)
	}
	op := []ast.BinOp{ast.Add, ast.Sub, ast.Mul, ast.FloorDiv, ast.Mod}[g.r.IntN(5)]
	l, r := g.expr(in, us[g.r.IntN(len(us))], depth-1), g.expr(in, t, depth-1)
	if g.r.IntN(2) == 0 {
		l, r = r, l
	}
	return ast.NewBin(op, l, r)
}

// castTo is a cast to t from a wide type, strict where none can fail, else lossy.
func (g *qgen) castTo(in *ast.Info, t model.Type, depth int) (ast.Expr, error) {
	froms := []model.Type{model.Uint8, model.Uint64, model.Float32, model.Int8, model.String, model.Int64,
		g.anyDecimal(in), g.wideType(5), g.wideType(6), g.wideType(7), model.Date}
	from := froms[g.r.IntN(len(froms))]
	for _, c := range in.Cols {
		if g.r.IntN(len(in.Cols)+2) == 0 && !c.Noisy && c.Type != model.Int128 {
			from = c.Type
		}
	}
	strict := ast.StrictCast(from, t)
	if !strict && !ast.LossyCast(from, t) {
		return nil, fmt.Errorf("no cast of %s to %s", from, t)
	}
	return ast.NewCast(g.expr(in, from, depth-1), t, !strict)
}

// uintExpr is an unsigned integer of type t.
func (g *qgen) uintExpr(in *ast.Info, t model.Type, depth int) (ast.Expr, error) {
	var nar []model.Type
	for _, u := range []model.Type{model.Uint8, model.Uint16, model.Uint32, model.Uint64} {
		if u.Bits() <= t.Bits() {
			nar = append(nar, u)
		}
	}
	sub := func() ast.Expr { return g.expr(in, nar[g.r.IntN(len(nar))], depth-1) }
	switch g.r.IntN(10) {
	case 0, 1:
		op := []ast.BinOp{ast.Add, ast.Sub, ast.Mul}[g.r.IntN(3)]
		return ast.NewBin(op, g.expr(in, t, depth-1), sub())
	case 2:
		op := []ast.BinOp{ast.FloorDiv, ast.Mod}[g.r.IntN(2)]
		var r ast.Expr = sub()
		if g.r.IntN(2) == 0 {
			r = &ast.Lit{T: model.Uint8, V: uint64([]int{0, 1, 2, 3, 7, 10, 255}[g.r.IntN(7)])}
		}
		return ast.NewBin(op, g.expr(in, t, depth-1), r)
	case 3:
		return ast.NewUn(ast.Abs, g.expr(in, t, depth-1))
	case 4:
		var els ast.Expr = sub()
		if g.r.IntN(4) == 0 {
			els = &ast.Lit{T: t, Null: true}
		}
		return ast.NewWhen(g.expr(in, model.Bool, depth-1), g.expr(in, t, depth-1), els)
	case 5:
		var with ast.Expr
		switch g.r.IntN(3) {
		case 0:
			with = g.lit(t)
		case 1:
			// A weak literal: an Int64 that fits keeps t.
			with = &ast.Lit{T: model.Int64, V: int64(g.r.IntN(300))}
		default:
			with = sub()
		}
		return ast.NewFillNull(g.expr(in, t, depth-1), with)
	case 6:
		froms := []model.Type{model.Bool}
		for _, u := range nar {
			if u != t {
				froms = append(froms, u)
			}
		}
		return ast.NewCast(g.expr(in, froms[g.r.IntN(len(froms))], depth-1), t, false)
	case 7:
		froms := []model.Type{model.String, model.Int64, model.Int32, model.Float64, model.Float32, model.Uint64,
			g.anyDecimal(in)}
		return ast.NewCast(g.expr(in, froms[g.r.IntN(len(froms))], depth-1), t, true)
	case 8:
		return g.fnExpr(in, t, depth)
	}
	return g.leaf(in, t), nil
}

// f32Expr is a Float32.
func (g *qgen) f32Expr(in *ast.Info, depth int) (ast.Expr, error) {
	t := model.Float32
	meets := []model.Type{model.Float32, model.Float32, model.Int8, model.Int16, model.Uint8, model.Uint16}
	sub := func() ast.Expr { return g.expr(in, meets[g.r.IntN(len(meets))], depth-1) }
	switch g.r.IntN(9) {
	case 0, 1:
		op := []ast.BinOp{ast.Add, ast.Sub, ast.Mul, ast.FloorDiv, ast.Mod}[g.r.IntN(5)]
		l, r := g.expr(in, t, depth-1), sub()
		if g.r.IntN(2) == 0 {
			l, r = r, l
		}
		return ast.NewBin(op, l, r)
	case 2:
		return ast.NewBin(ast.Div, g.expr(in, t, depth-1), sub())
	case 3:
		return ast.NewUn([]ast.UnOp{ast.Neg, ast.Abs}[g.r.IntN(2)], g.expr(in, t, depth-1))
	case 4:
		return ast.NewWhen(g.expr(in, model.Bool, depth-1), g.expr(in, t, depth-1), sub())
	case 5:
		var with ast.Expr
		switch g.r.IntN(3) {
		case 0:
			with = g.lit(t)
		case 1:
			// A weak Float64 literal that Float32 holds keeps it.
			with = &ast.Lit{T: model.Float64, V: []float64{2.5, -0.5, math.NaN(), 0.1, 1e300, 16777217}[g.r.IntN(6)]}
		default:
			with = sub()
		}
		return ast.NewFillNull(g.expr(in, t, depth-1), with)
	case 6:
		from := []model.Type{model.Int8, model.Int16, model.Int32, model.Int64, model.Uint64, model.Bool}[g.r.IntN(6)]
		return ast.NewCast(g.expr(in, from, depth-1), t, false)
	case 7:
		from := []model.Type{model.String, model.Float64}[g.r.IntN(2)]
		return ast.NewCast(g.expr(in, from, depth-1), t, true)
	}
	return g.leaf(in, t), nil
}

// decExpr is a Decimal of type t.
func (g *qgen) decExpr(in *ast.Info, t model.Type, depth int) (ast.Expr, error) {
	switch g.r.IntN(8) {
	case 0:
		var els ast.Expr = g.expr(in, t, depth-1)
		if g.r.IntN(4) == 0 {
			els = &ast.Lit{T: t, Null: true}
		}
		return ast.NewWhen(g.expr(in, model.Bool, depth-1), g.expr(in, t, depth-1), els)
	case 1:
		var with ast.Expr = g.lit(t)
		if g.r.IntN(2) == 0 {
			with = g.expr(in, t, depth-1)
		}
		return ast.NewFillNull(g.expr(in, t, depth-1), with)
	case 2:
		return ast.NewUn([]ast.UnOp{ast.Neg, ast.Abs}[g.r.IntN(2)], g.expr(in, t, depth-1))
	case 3:
		var froms []model.Type
		for _, f := range append([]model.Type{model.Int8, model.Int16, model.Int32, model.Int64, model.Uint8,
			model.Uint32, model.Uint64}, Decimals...) {
			if ast.StrictCast(f, t) && f != t {
				froms = append(froms, f)
			}
		}
		if len(froms) == 0 {
			return nil, fmt.Errorf("no strict cast to %s", t)
		}
		return ast.NewCast(g.expr(in, froms[g.r.IntN(len(froms))], depth-1), t, false)
	case 4:
		froms := []model.Type{model.String, model.Float64, model.Float32, model.Int64, model.Uint64, g.anyDecimal(in)}
		return ast.NewCast(g.expr(in, froms[g.r.IntN(len(froms))], depth-1), t, true)
	}
	return g.leaf(in, t), nil
}

// The periods Truncate and Round take, and the intervals OffsetBy moves by, each
// kept where its type holds it (ast.NewFn).
var (
	periods   = []int64{1, 1e3, 1e6, 1e9, 15e9, 60e9, 900e9, 3_600e9, 86_400e9, 7 * 86_400e9}
	intervals = []string{"1ns", "-1ns", "1us", "1ms", "-1ms", "1s", "-1s", "90m", "1h", "-36h", "1d", "-1d", "2w",
		"1mo", "-1mo", "13mo", "1y", "-1y"}
)

// moveFn is a Truncate, Round or OffsetBy of a bounded column of t, an instant.
func (g *qgen) moveFn(in *ast.Info, t model.Type) (ast.Expr, error) {
	cs := g.boundedCols(in, t, model.Wild)
	if len(cs) == 0 {
		return nil, fmt.Errorf("no bounded %s", t)
	}
	c := cs[g.r.IntN(len(cs))]
	x := &ast.Col{Name: c.Name, T: c.Type}
	switch g.r.IntN(3) {
	case 0:
		return ast.NewFn(ast.DtTruncate, x, nil, []int64{periods[g.r.IntN(len(periods))]}, false)
	case 1:
		return ast.NewFn(ast.DtRound, x, nil, []int64{periods[g.r.IntN(len(periods))]}, false)
	}
	return ast.NewFn(ast.DtOffsetBy, x, []string{intervals[g.r.IntN(len(intervals))]}, nil, false)
}

// anyMove is a Truncate, Round or OffsetBy of a bounded instant column of in, of
// any type.
func (g *qgen) anyMove(in *ast.Info) (ast.Expr, error) {
	c := g.boundedOf(in, model.Wild, model.Type.IsInstant)
	if c == nil {
		return nil, fmt.Errorf("no bounded instant")
	}
	return g.moveFn(in, c.T)
}

// durationOperand is a bounded Duration, a column or a literal, a wild one's at
// most, or a tame one's.
func (g *qgen) durationOperand(in *ast.Info, p model.Profile) ast.Expr {
	if c := g.boundedOf(in, p, model.Type.IsDuration); c != nil && g.r.IntN(2) == 0 {
		return c
	}
	return g.litOf(model.Duration(model.Units[g.r.IntN(4)]), p)
}

// datetimeExpr is a Datetime of type t.
func (g *qgen) datetimeExpr(in *ast.Info, t model.Type, depth int) (ast.Expr, error) {
	switch g.r.IntN(9) {
	case 0:
		var els ast.Expr = g.expr(in, t, depth-1)
		if g.r.IntN(4) == 0 {
			els = &ast.Lit{T: t, Null: true}
		}
		return ast.NewWhen(g.expr(in, model.Bool, depth-1), g.expr(in, t, depth-1), els)
	case 1:
		var with ast.Expr = g.lit(t)
		if g.r.IntN(2) == 0 {
			with = g.expr(in, t, depth-1)
		}
		return ast.NewFillNull(g.expr(in, t, depth-1), with)
	case 2, 3:
		return g.castTo(in, t, depth)
	case 4, 5:
		return g.moveFn(in, t)
	case 6:
		cs := g.boundedCols(in, t, model.Wild)
		if len(cs) == 0 {
			return nil, fmt.Errorf("no bounded %s", t)
		}
		c := cs[g.r.IntN(len(cs))]
		x, d := ast.Expr(&ast.Col{Name: c.Name, T: t}), g.durationOperand(in, model.Wild)
		switch g.r.IntN(3) {
		case 0:
			return ast.NewBin(ast.Sub, x, d)
		case 1:
			return ast.NewBin(ast.Add, d, x)
		}
		return ast.NewBin(ast.Add, x, d)
	case 7:
		if t.Zone() == "" {
			return nil, fmt.Errorf("no zone to convert to")
		}
		from := model.Datetime(t.Unit(), 1+g.r.IntN(len(model.Zones)-1))
		return ast.NewFn(ast.DtConvertTimeZone, g.expr(in, from, depth-1), []string{t.Zone()}, nil, false)
	}
	return g.leaf(in, t), nil
}

// durationExpr is a Duration of type t.
func (g *qgen) durationExpr(in *ast.Info, t model.Type, depth int) (ast.Expr, error) {
	u := t.Unit()
	switch g.r.IntN(8) {
	case 0:
		var els ast.Expr = g.expr(in, t, depth-1)
		if g.r.IntN(4) == 0 {
			els = &ast.Lit{T: t, Null: true}
		}
		return ast.NewWhen(g.expr(in, model.Bool, depth-1), g.expr(in, t, depth-1), els)
	case 1:
		var with ast.Expr = g.lit(t)
		if g.r.IntN(2) == 0 {
			with = g.expr(in, t, depth-1)
		}
		return ast.NewFillNull(g.expr(in, t, depth-1), with)
	case 2, 3:
		return g.castTo(in, t, depth)
	case 4:
		// An instant less one of its own type: a Datetime or a Time of t's unit, or
		// a Date less a Date, in seconds.
		x := g.boundedOf(in, model.Wild, func(o model.Type) bool {
			return (o.IsDatetime() || o.IsTime()) && o.Unit() == u || o == model.Date && u == model.Sec
		})
		if x == nil {
			return nil, fmt.Errorf("no bounded instant of %s", u)
		}
		var y ast.Expr = g.litOf(x.T, model.Wild)
		if c := g.boundedOf(in, model.Wild, func(o model.Type) bool { return o == x.T }); c != nil && g.r.IntN(2) == 0 {
			y = c
		}
		return ast.NewBin(ast.Sub, x, y)
	case 5:
		x := g.boundedOf(in, model.Wild, func(o model.Type) bool { return o == t })
		if x == nil {
			return nil, fmt.Errorf("no bounded %s", t)
		}
		var y ast.Expr = g.litOf(model.Duration(model.Units[g.r.IntN(int(u)+1)]), model.Wild)
		op := []ast.BinOp{ast.Add, ast.Sub}[g.r.IntN(2)]
		return ast.NewBin(op, x, y)
	case 6:
		x := g.boundedOf(in, model.Tame, func(o model.Type) bool { return o == t })
		if x == nil {
			return nil, fmt.Errorf("no tame %s", t)
		}
		k := &ast.Lit{T: model.Int64, V: int64(g.r.IntN(7) - 3)}
		if g.r.IntN(2) == 0 {
			return ast.NewBin(ast.Mul, k, x)
		}
		return ast.NewBin(ast.Mul, x, k)
	}
	return g.leaf(in, t), nil
}

// timeExpr is a Time of type t.
func (g *qgen) timeExpr(in *ast.Info, t model.Type, depth int) (ast.Expr, error) {
	switch g.r.IntN(6) {
	case 0:
		return ast.NewWhen(g.expr(in, model.Bool, depth-1), g.expr(in, t, depth-1), g.expr(in, t, depth-1))
	case 1:
		var with ast.Expr = g.lit(t)
		if g.r.IntN(2) == 0 {
			with = g.expr(in, t, depth-1)
		}
		return ast.NewFillNull(g.expr(in, t, depth-1), with)
	case 2:
		return g.castTo(in, t, depth)
	case 3:
		from := []model.Type{model.String, model.Int64}[g.r.IntN(2)]
		return ast.NewCast(g.expr(in, from, depth-1), t, true)
	case 4:
		return g.fnExpr(in, t, depth)
	}
	return g.leaf(in, t), nil
}

// freeExpr is arithmetic whose type its operands decide: a Decimal's sum,
// difference, product or quotient; an instant's difference, or an instant moved by
// a Duration; two Durations; an unsigned integer beside a signed one; Float32
// beside an integer. Nil when none could be drawn.
func (g *qgen) freeExpr(in *ast.Info) ast.Expr {
	for range 10 {
		var e ast.Expr
		var err error
		switch g.r.IntN(6) {
		case 0:
			l := g.expr(in, g.anyDecimal(in), 1)
			rts := []model.Type{g.anyDecimal(in), model.Int32, model.Int8, model.Uint8, model.Int64}
			r := g.expr(in, rts[g.r.IntN(len(rts))], 1)
			if g.r.IntN(2) == 0 {
				l, r = r, l
			}
			e, err = ast.NewBin([]ast.BinOp{ast.Add, ast.Sub, ast.Mul, ast.Div}[g.r.IntN(4)], l, r)
		case 1:
			x := g.boundedOf(in, model.Wild, func(o model.Type) bool { return o.IsInstant() || o.IsTime() })
			if x == nil {
				continue
			}
			var y ast.Expr = g.litOf(x.T, model.Wild)
			if c := g.boundedOf(in, model.Wild, func(o model.Type) bool { return o == x.T }); c != nil {
				y = c
			}
			e, err = ast.NewBin(ast.Sub, x, y)
		case 2:
			x := g.boundedOf(in, model.Wild, model.Type.IsInstant)
			if x == nil {
				continue
			}
			op := []ast.BinOp{ast.Add, ast.Sub}[g.r.IntN(2)]
			e, err = ast.NewBin(op, x, g.durationOperand(in, model.Wild))
		case 3:
			x := g.boundedOf(in, model.Wild, model.Type.IsDuration)
			if x == nil {
				continue
			}
			op := []ast.BinOp{ast.Add, ast.Sub}[g.r.IntN(2)]
			e, err = ast.NewBin(op, x, g.durationOperand(in, model.Wild))
		case 4:
			us := []model.Type{model.Uint8, model.Uint16, model.Uint32, model.Uint64}
			ss := []model.Type{model.Int8, model.Int16, model.Int32, model.Int64}
			l, r := g.expr(in, us[g.r.IntN(4)], 1), g.expr(in, ss[g.r.IntN(4)], 1)
			if g.r.IntN(2) == 0 {
				l, r = r, l
			}
			e, err = ast.NewBin([]ast.BinOp{ast.Add, ast.Sub, ast.Mul, ast.FloorDiv, ast.Mod, ast.Div}[g.r.IntN(6)], l, r)
		default:
			others := []model.Type{model.Int32, model.Int64, model.Uint16, model.Float64, model.Int8}
			l, r := g.expr(in, model.Float32, 1), g.expr(in, others[g.r.IntN(len(others))], 1)
			if g.r.IntN(2) == 0 {
				l, r = r, l
			}
			e, err = ast.NewBin([]ast.BinOp{ast.Add, ast.Sub, ast.Mul, ast.Div}[g.r.IntN(4)], l, r)
		}
		if err == nil && e != nil {
			return e
		}
	}
	return nil
}

// fnExpr is a function of the string or temporal namespace answering t, or an
// error when none does.
func (g *qgen) fnExpr(in *ast.Info, t model.Type, depth int) (ast.Expr, error) {
	str := func() ast.Expr { return g.expr(in, model.String, depth-1) }
	switch {
	case t == model.Bool:
		op := []ast.FnOp{ast.StrContains, ast.StrContains, ast.StrStartsWith, ast.StrEndsWith}[g.r.IntN(4)]
		if op == ast.StrContains && g.r.IntN(2) == 0 {
			return ast.NewFn(op, str(), []string{regexes[g.r.IntN(len(regexes))]}, nil, false)
		}
		return ast.NewFn(op, str(), []string{g.pattern(in)}, nil, true)
	case t == model.String:
		switch g.r.IntN(7) {
		case 0, 1:
			op := []ast.FnOp{ast.StrReplace, ast.StrReplaceAll}[g.r.IntN(2)]
			val := replacements[g.r.IntN(len(replacements))]
			if g.r.IntN(2) == 0 {
				return ast.NewFn(op, str(), []string{regexes[g.r.IntN(len(regexes))], val}, nil, false)
			}
			pat := g.pattern(in)
			if pat == "" {
				pat = "a"
			}
			return ast.NewFn(op, str(), []string{pat, val}, nil, true)
		case 2:
			offs := []int64{-10, -3, -1, 0, 1, 2, 5, 100}
			lens := []int64{0, 1, 2, 3, 10, 1000}
			return ast.NewFn(ast.StrSlice, str(), nil, []int64{offs[g.r.IntN(len(offs))], lens[g.r.IntN(len(lens))]}, false)
		case 3, 4:
			return ast.NewFn([]ast.FnOp{ast.StrToUpper, ast.StrToLower}[g.r.IntN(2)], str(), nil, nil, false)
		default:
			op := []ast.FnOp{ast.StrStripPrefix, ast.StrStripSuffix}[g.r.IntN(2)]
			return ast.NewFn(op, str(), []string{g.pattern(in)}, nil, false)
		}
	case t == model.Uint32:
		op := []ast.FnOp{ast.StrLenBytes, ast.StrLenChars, ast.StrSplitLen}[g.r.IntN(3)]
		var s []string
		if op == ast.StrSplitLen {
			s = []string{separators[g.r.IntN(len(separators))]}
		}
		return ast.NewFn(op, str(), s, nil, false)
	case t == model.Int64:
		if g.r.IntN(2) == 0 {
			return ast.NewFn(ast.StrToInteger, str(), nil, nil, false)
		}
		d := g.temporalOf(in, model.Type.IsDuration)
		var ops []ast.FnOp
		for op := ast.DtTotalDays; op <= ast.DtTotalNanoseconds; op++ {
			// A total in the Duration's unit or a coarser one, which int64 holds.
			if op.TotalUnitNanos() >= d.Type().Unit().Nanos() {
				ops = append(ops, op)
			}
		}
		return ast.NewFn(ops[g.r.IntN(len(ops))], d, nil, nil, false)
	case t == model.Int32:
		x := g.temporalOf(in, func(o model.Type) bool { return o.IsInstant() || o.IsTime() })
		if g.r.IntN(3) == 0 {
			if m, err := g.anyMove(in); err == nil {
				x = m
			}
		}
		var ops []ast.FnOp
		for op := ast.DtYear; op <= ast.DtOrdinalDay; op++ {
			if _, err := ast.NewFn(op, x, nil, nil, false); err == nil {
				ops = append(ops, op)
			}
		}
		return ast.NewFn(ops[g.r.IntN(len(ops))], x, nil, nil, false)
	case t == model.Date:
		if g.r.IntN(2) == 0 {
			return g.moveFn(in, t)
		}
		return ast.NewFn(ast.DtDate, g.temporalOf(in, model.Type.IsDatetime), nil, nil, false)
	case t.IsTime():
		z := g.r.IntN(len(model.Zones))
		return ast.NewFn(ast.DtTime, g.expr(in, model.Datetime(t.Unit(), z), depth-1), nil, nil, false)
	}
	return nil, fmt.Errorf("no function answers %s", t)
}

// temporalOf is a temporal expression whose type ok takes: a column of in's, or a
// literal of one of the wide types.
func (g *qgen) temporalOf(in *ast.Info, ok func(model.Type) bool) ast.Expr {
	var cs []ast.Column
	for _, c := range in.Cols {
		if ok(c.Type) && !c.Noisy {
			cs = append(cs, c)
		}
	}
	if len(cs) > 0 && g.r.IntN(5) > 0 {
		c := cs[g.r.IntN(len(cs))]
		return &ast.Col{Name: c.Name, T: c.Type}
	}
	for {
		var t model.Type
		switch g.r.IntN(4) {
		case 0:
			t = model.Date
		case 1:
			t = g.wideType(6)
		case 2:
			t = g.wideType(7)
		default:
			t = g.wideType(5)
		}
		if ok(t) {
			return g.lit(t)
		}
	}
}

// The text the string functions take: patterns RE2 reads alike in Go and in
// DuckDB, values without the $ or \ a regex replacement expands, separators of one
// character or more.
var (
	regexes      = []string{"a.", "^a", "b$", "[0-9]+", "a|b", "(ab)+", "^$", ".", "[A-Z]", `\d`, `\s`, "x*", "é", "^.{3}$", "[^a-z]"}
	replacements = []string{"", "x", "XY", "é", "<>", "日"}
	separators   = []string{",", "a", " ", "ab", "é", "0"}
	patternWords = []string{"", "a", "b", "ab", "é", "日", "\x00", "zz", " ", "1", "-", "ß"}
)

// pattern is literal text to look for: a piece of a value a String column of in
// holds, or a word.
func (g *qgen) pattern(in *ast.Info) string {
	for _, c := range cols(in, model.String) {
		if g.r.IntN(2) == 0 {
			continue
		}
		if l := g.sample(c); l != nil {
			s := l.V.(string)
			n := utf8.RuneCountInString(s)
			if n == 0 {
				return ""
			}
			runes := []rune(s)
			from := g.r.IntN(n)
			to := min(n, from+1+g.r.IntN(3))
			if g.r.IntN(3) == 0 {
				from = 0
			} else if g.r.IntN(3) == 0 {
				to = n
			}
			return string(runes[from:to])
		}
	}
	return patternWords[g.r.IntN(len(patternWords))]
}

// wideFor is an expression of type t through the wide operations: a function of
// the string or temporal namespace answering t, or one answering an integer cast
// to t; a component of an instant compared; a cast from a wide type; an instant
// moved, a Duration's arithmetic.
func (g *qgen) wideFor(in *ast.Info, t model.Type, depth int) (ast.Expr, error) {
	switch {
	case t == model.Bool:
		if g.r.IntN(2) == 0 {
			return g.fnExpr(in, t, depth)
		}
		x, err := g.fnExpr(in, model.Int32, depth)
		if err != nil {
			return nil, err
		}
		op := ast.BinOp(int(ast.Eq) + g.r.IntN(6))
		return ast.NewBin(op, x, &ast.Lit{T: model.Int32, V: int64([]int{0, 1, 2, 3, 7, 12, 15, 30, 59, 2020, 2021,
			1970, 500_000_000}[g.r.IntN(13)])})
	case t.IsInt() && t != model.Int128:
		from := []model.Type{model.Int32, model.Int32, model.Int64, model.Uint32}[g.r.IntN(4)]
		x, err := g.fnExpr(in, from, depth)
		if err != nil {
			return nil, err
		}
		if x.Type() == t {
			return x, nil
		}
		return ast.NewCast(x, t, !ast.StrictCast(x.Type(), t))
	case t == model.String:
		if g.r.IntN(3) > 0 {
			return g.fnExpr(in, t, depth)
		}
		return g.castTo(in, t, depth)
	case t == model.Date, t.IsTime():
		if g.r.IntN(3) > 0 {
			return g.fnExpr(in, t, depth)
		}
		return g.castTo(in, t, depth)
	case t.IsDatetime():
		switch g.r.IntN(4) {
		case 0, 3:
			return g.moveFn(in, t)
		case 1:
			return g.datetimeExpr(in, t, depth)
		}
		return g.castTo(in, t, depth)
	case t.IsDuration():
		return g.durationExpr(in, t, depth)
	case t.IsDecimal(), t.IsFloat():
		return g.castTo(in, t, depth)
	}
	return nil, fmt.Errorf("no wide operation answers %s", t)
}
