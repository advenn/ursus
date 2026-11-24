// Package build turns a qcheck query into an ursus LazyFrame over ScanArrowRecords,
// and a configuration into the CollectOptions it runs under. It uses ursus's public
// API alone, as a caller would.
package build

import (
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/model"
)

// DType is ursus's type for t.
func DType(t model.Type) dtype.DataType {
	switch t.Kind() {
	case model.Bool:
		return dtype.Bool
	case model.Int8:
		return dtype.Int8
	case model.Int16:
		return dtype.Int16
	case model.Int32:
		return dtype.Int32
	case model.Int64:
		return dtype.Int64
	case model.Int128:
		return dtype.Int128
	case model.Uint8:
		return dtype.Uint8
	case model.Uint16:
		return dtype.Uint16
	case model.Uint32:
		return dtype.Uint32
	case model.Uint64:
		return dtype.Uint64
	case model.Float32:
		return dtype.Float32
	case model.Float64:
		return dtype.Float64
	case model.String:
		return dtype.String
	case model.Date:
		return dtype.Date
	case model.DatetimeKind:
		return dtype.Datetime(Unit(t.Unit()), t.Zone())
	case model.DurationKind:
		return dtype.Duration(Unit(t.Unit()))
	case model.TimeKind:
		return dtype.Time(Unit(t.Unit()))
	case model.DecimalKind:
		return dtype.Decimal(uint8(t.Prec()), uint8(t.Scale()))
	}
	panic("build: no ursus type for " + t.String())
}

// Unit is ursus's unit for u.
func Unit(u model.Unit) dtype.TimeUnit {
	return [...]dtype.TimeUnit{dtype.Second, dtype.Milli, dtype.Micro, dtype.Nano}[u]
}

// TypeOf is the model type of an ursus type, or Invalid: the model type whose
// String is ursus's own for it.
func TypeOf(d dtype.DataType) model.Type {
	t, ok := model.ParseType(d.String())
	if !ok || !DType(t).Equal(d) {
		return model.Invalid
	}
	return t
}

// Records cuts t's record into k records, at points drawn from its seed and k, an
// empty record among them sometimes; k of 1 is the record itself.
func Records(t *model.Table, k int) []arrow.RecordBatch {
	if k <= 1 {
		return []arrow.RecordBatch{t.Rec}
	}
	r := rand.New(rand.NewPCG(t.Seed, uint64(k)))
	n := int(t.Rec.NumRows())
	cuts := make([]int, k-1)
	for i := range cuts {
		cuts[i] = r.IntN(n + 1)
	}
	cuts = append(cuts, 0, n)
	sortInts(cuts)
	out := make([]arrow.RecordBatch, 0, k)
	for i := 1; i < len(cuts); i++ {
		out = append(out, t.Rec.NewSlice(int64(cuts[i-1]), int64(cuts[i])))
	}
	return out
}

// RecordsOf is each table's records, cut into k as Records cuts them, by name.
func RecordsOf(tables []*model.Table, k int) map[string][]arrow.RecordBatch {
	out := make(map[string][]arrow.RecordBatch, len(tables))
	for _, t := range tables {
		out[t.Name] = Records(t, k)
	}
	return out
}

func sortInts(s []int) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Options are the CollectOptions cfg runs under, always WithMemoryLimit(0).
func Options(cfg model.Config) []ursus.CollectOption {
	opts := []ursus.CollectOption{
		ursus.WithThreads(cfg.Threads),
		ursus.WithBatchSize(cfg.Batch),
		ursus.WithMemoryLimit(0),
	}
	if cfg.OptOff {
		opts = append(opts, ursus.WithOptFlags(ursus.NoOptFlags()))
	}
	if cfg.Verify {
		opts = append(opts, ursus.WithVerify())
	}
	return opts
}

// Frame is f as a LazyFrame, each table read from recs.
func Frame(f ast.Frame, recs map[string][]arrow.RecordBatch) *ursus.LazyFrame {
	switch f := f.(type) {
	case *ast.Scan:
		return ursus.ScanArrowRecords(recs[f.Table]...)
	case *ast.Filter:
		return Frame(f.In, recs).Filter(Expr(f.Pred))
	case *ast.Select:
		return Frame(f.In, recs).Select(named(f.Cols)...)
	case *ast.WithColumns:
		return Frame(f.In, recs).WithColumns(named(f.Cols)...)
	case *ast.GroupBy:
		gb := Frame(f.In, recs).GroupBy(named(f.Keys)...)
		if f.MaintainOrder {
			gb = gb.MaintainOrder()
		}
		return gb.Agg(named(f.Aggs)...)
	case *ast.Sort:
		keys := make([]ursus.SortKey, len(f.Keys))
		for i, k := range f.Keys {
			if k.Desc {
				keys[i] = ursus.Desc(ursus.Col(k.Col))
			} else {
				keys[i] = ursus.Asc(ursus.Col(k.Col))
			}
			if k.NullsLast {
				keys[i] = keys[i].NullsLast()
			}
		}
		return Frame(f.In, recs).Sort(keys...)
	case *ast.Head:
		return Frame(f.In, recs).Head(f.N)
	case *ast.Slice:
		return Frame(f.In, recs).Slice(f.Offset, f.Len)
	case *ast.Join:
		opts := []ursus.JoinOption{ursus.JoinHow(joinKind(f.How))}
		if f.LeftOn == f.RightOn {
			opts = append(opts, ursus.JoinOn(ursus.Col(f.LeftOn)))
		} else {
			opts = append(opts, ursus.JoinLeftOn(ursus.Col(f.LeftOn)), ursus.JoinRightOn(ursus.Col(f.RightOn)))
		}
		if f.MaintainOrder {
			opts = append(opts, ursus.JoinMaintainOrder(ursus.JoinOrderLeft))
		}
		return Frame(f.L, recs).Join(Frame(f.R, recs), opts...)
	}
	panic(fmt.Sprintf("build: unknown frame %T", f))
}

func joinKind(h ast.JoinHow) ursus.JoinKind {
	switch h {
	case ast.Left:
		return ursus.JoinLeft
	case ast.Semi:
		return ursus.JoinSemi
	case ast.Anti:
		return ursus.JoinAnti
	}
	return ursus.JoinInner
}

func named(cols []ast.Named) []ursus.Expr {
	out := make([]ursus.Expr, len(cols))
	for i, n := range cols {
		out[i] = Expr(n.E)
		if c, ok := n.E.(*ast.Col); !ok || c.Name != n.Name {
			out[i] = out[i].Alias(n.Name)
		}
	}
	return out
}

// Expr is e as an ursus expression.
func Expr(e ast.Expr) ursus.Expr {
	switch e := e.(type) {
	case *ast.Col:
		return ursus.Col(e.Name)
	case *ast.Lit:
		return lit(e)
	case *ast.Bin:
		l, r := Expr(e.L), Expr(e.R)
		switch e.Op {
		case ast.Add:
			return l.Add(r)
		case ast.Sub:
			return l.Sub(r)
		case ast.Mul:
			return l.Mul(r)
		case ast.Div:
			return l.Div(r)
		case ast.FloorDiv:
			return l.FloorDiv(r)
		case ast.Mod:
			return l.Mod(r)
		case ast.Eq:
			return l.Eq(r)
		case ast.Ne:
			return l.Ne(r)
		case ast.Lt:
			return l.Lt(r)
		case ast.Le:
			return l.Le(r)
		case ast.Gt:
			return l.Gt(r)
		case ast.Ge:
			return l.Ge(r)
		case ast.And:
			return l.And(r)
		case ast.Or:
			return l.Or(r)
		}
	case *ast.Un:
		x := Expr(e.X)
		switch e.Op {
		case ast.Not:
			return x.Not()
		case ast.Neg:
			return x.Neg()
		case ast.Abs:
			return x.Abs()
		case ast.IsNull:
			return x.IsNull()
		case ast.IsNotNull:
			return x.IsNotNull()
		}
	case *ast.When:
		return ursus.When(Expr(e.Cond)).Then(Expr(e.Then)).Otherwise(Expr(e.Else))
	case *ast.FillNull:
		return fill(Expr(e.X), e.With)
	case *ast.Cast:
		if e.Lossy {
			return Expr(e.X).CastLossy(DType(e.To))
		}
		return Expr(e.X).Cast(DType(e.To))
	case *ast.Fn:
		return fn(e, Expr(e.X))
	case *ast.Agg:
		if e.Op == ast.Len {
			return ursus.Len()
		}
		x := Expr(e.X)
		switch e.Op {
		case ast.Sum:
			return x.Sum()
		case ast.Mean:
			return x.Mean()
		case ast.Min:
			return x.Min()
		case ast.Max:
			return x.Max()
		case ast.Count:
			return x.Count()
		case ast.NUnique:
			return x.NUnique()
		}
	}
	panic(fmt.Sprintf("build: unknown expression %T", e))
}

func lit(e *ast.Lit) ursus.Expr {
	if e.Null {
		return ursus.Null(DType(e.T))
	}
	switch v := e.V.(type) {
	case string:
		if e.T == model.String {
			return ursus.Lit(v)
		}
	case bool:
		return ursus.Lit(v)
	case float64:
		return ursus.Lit(v)
	case float32:
		return ursus.Lit(v)
	}
	switch e.T {
	case model.Int8:
		return ursus.Lit(int8(e.V.(int64)))
	case model.Int16:
		return ursus.Lit(int16(e.V.(int64)))
	case model.Int32:
		return ursus.Lit(int32(e.V.(int64)))
	case model.Int64:
		return ursus.Lit(e.V.(int64))
	case model.Uint8:
		return ursus.Lit(uint8(e.V.(uint64)))
	case model.Uint16:
		return ursus.Lit(uint16(e.V.(uint64)))
	case model.Uint32:
		return ursus.Lit(uint32(e.V.(uint64)))
	case model.Uint64:
		return ursus.Lit(e.V.(uint64))
	}
	switch {
	case e.T == model.Date:
		return ursus.Lit(e.V.(int32)).Cast(dtype.Date)
	case e.T.IsDatetime(), e.T.IsDuration(), e.T.IsTime():
		return ursus.Lit(e.V.(int64)).Cast(DType(e.T))
	case e.T.IsDecimal():
		return ursus.Lit(ast.DecimalText(e.V.(string), e.T.Scale())).Cast(DType(e.T))
	}
	panic("build: no literal of " + e.T.String())
}

// scalar is a non-null literal as the Go scalar of its type, which ursus types as
// the literal's own (feature-gaps §9) and a fill lifts to a weak literal; none for a
// Date, a temporal or a Decimal, which are a scalar cast.
func scalar(e *ast.Lit) (any, bool) {
	switch e.T {
	case model.Int8:
		return int8(e.V.(int64)), true
	case model.Int16:
		return int16(e.V.(int64)), true
	case model.Int32:
		return int32(e.V.(int64)), true
	case model.Int64:
		return e.V.(int64), true
	case model.Uint8:
		return uint8(e.V.(uint64)), true
	case model.Uint16:
		return uint16(e.V.(uint64)), true
	case model.Uint32:
		return uint32(e.V.(uint64)), true
	case model.Uint64:
		return e.V.(uint64), true
	case model.Float32:
		return e.V.(float32), true
	case model.Float64:
		return e.V.(float64), true
	case model.String:
		return e.V.(string), true
	case model.Bool:
		return e.V.(bool), true
	}
	return nil, false
}

// fill is x.FillNullWith(with): a non-null literal as a Go scalar, which ursus lifts
// to a weak literal (fill.go), an expression as written.
func fill(x ursus.Expr, with ast.Expr) ursus.Expr {
	if l, ok := with.(*ast.Lit); ok && !l.Null {
		if s, ok := scalar(l); ok {
			switch v := s.(type) {
			case int8:
				return x.FillNullWith(v)
			case int16:
				return x.FillNullWith(v)
			case int32:
				return x.FillNullWith(v)
			case int64:
				return x.FillNullWith(v)
			case uint8:
				return x.FillNullWith(v)
			case uint16:
				return x.FillNullWith(v)
			case uint32:
				return x.FillNullWith(v)
			case uint64:
				return x.FillNullWith(v)
			case float32:
				return x.FillNullWith(v)
			case float64:
				return x.FillNullWith(v)
			case string:
				return x.FillNullWith(v)
			case bool:
				return x.FillNullWith(v)
			}
		}
	}
	return x.FillNullWith(Expr(with))
}

// fn is a string or temporal function of x.
func fn(e *ast.Fn, x ursus.Expr) ursus.Expr {
	s, d := x.Str(), x.Dt()
	switch e.Op {
	case ast.StrContains:
		return s.Contains(e.S[0], e.B)
	case ast.StrStartsWith:
		return s.StartsWith(e.S[0])
	case ast.StrEndsWith:
		return s.EndsWith(e.S[0])
	case ast.StrReplace:
		return s.Replace(e.S[0], e.S[1], e.B)
	case ast.StrReplaceAll:
		return s.ReplaceAll(e.S[0], e.S[1], e.B)
	case ast.StrLenBytes:
		return s.LenBytes()
	case ast.StrLenChars:
		return s.LenChars()
	case ast.StrSlice:
		return s.Slice(int(e.I[0]), int(e.I[1]))
	case ast.StrToUpper:
		return s.ToUpper()
	case ast.StrToLower:
		return s.ToLower()
	case ast.StrStripPrefix:
		return s.StripPrefix(e.S[0])
	case ast.StrStripSuffix:
		return s.StripSuffix(e.S[0])
	case ast.StrSplitLen:
		return s.Split(e.S[0]).List().Len()
	case ast.StrToInteger:
		return s.ToInteger()
	case ast.DtYear:
		return d.Year()
	case ast.DtMonth:
		return d.Month()
	case ast.DtDay:
		return d.Day()
	case ast.DtHour:
		return d.Hour()
	case ast.DtMinute:
		return d.Minute()
	case ast.DtSecond:
		return d.Second()
	case ast.DtMillisecond:
		return d.Millisecond()
	case ast.DtMicrosecond:
		return d.Microsecond()
	case ast.DtNanosecond:
		return d.Nanosecond()
	case ast.DtWeekday:
		return d.Weekday()
	case ast.DtOrdinalDay:
		return d.OrdinalDay()
	case ast.DtTruncate:
		return d.Truncate(time.Duration(e.I[0]))
	case ast.DtRound:
		return d.Round(time.Duration(e.I[0]))
	case ast.DtOffsetBy:
		return d.OffsetBy(ursus.Every(e.S[0]))
	case ast.DtDate:
		return d.Date()
	case ast.DtTime:
		return d.Time()
	case ast.DtConvertTimeZone:
		return d.ConvertTimeZone(e.S[0])
	case ast.DtTotalDays:
		return d.TotalDays()
	case ast.DtTotalHours:
		return d.TotalHours()
	case ast.DtTotalMinutes:
		return d.TotalMinutes()
	case ast.DtTotalSeconds:
		return d.TotalSeconds()
	case ast.DtTotalMilliseconds:
		return d.TotalMilliseconds()
	case ast.DtTotalMicroseconds:
		return d.TotalMicroseconds()
	case ast.DtTotalNanoseconds:
		return d.TotalNanoseconds()
	}
	panic(fmt.Sprintf("build: unknown function %s", e.Op))
}
