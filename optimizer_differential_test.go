package ursus_test

// The optimizer against itself, generated.
//
// TestPushdownSoundness compares twelve hand-picked queries with the optimizer on
// and off, and none of the twelve is a chained WithColumns — which is the most
// ordinary query that answered wrongly (audit.md §3, O1). A hand-picked list covers
// what its author thought of. This one composes query shapes instead: two dozen
// frame transforms, singly and in pairs, each followed by "tops" DERIVED from the
// shape's own result, and compares every one optimized against unoptimized.
//
// # Why the tops are derived
//
// A fixed threshold hides the defects this exists for. O1 filters on a column
// whose values the rewrite gets wrong; `v > 2` keeps every row either way and
// agrees. So each column is filtered at its MEDIAN in the unoptimized result, which
// splits the rows wherever the values happen to lie.
//
// # What it cannot see
//
// Anything wrong in both modes alike — the resolver's W1, literal identity (O3) —
// agrees with itself here. That is optimizer_byhand_test.go's job, against answers
// worked out by hand.

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/ursustest"
)

// diffFixture is eight rows, each column placed to reach a finding:
//
//	k   Int64    a null and duplicates
//	n   Int32    50000² overflows Int32 (O6), and a null
//	f   Float64  +0 and -0, NaN (O4), a null; row 7 equals row 0 but for -0 (O9)
//	s   String   "x" sits where ok is false (O8), and a null
//	ok  Bool     the guard, and a null
func diffFixture() *ursus.LazyFrame {
	negZero := math.Copysign(0, -1)
	return ursus.Frame(
		ursus.ValuesNullable("k", []int64{1, 2, 2, 3, 0, 4, 5, 1},
			[]bool{true, true, true, true, false, true, true, true}),
		ursus.ValuesNullable("n", []int32{10, 20, 20, 50000, 40, 0, 50000, 10},
			[]bool{true, true, true, true, true, false, true, true}),
		ursus.ValuesNullable("f", []float64{0, negZero, math.NaN(), -1.5, 0, 2.5, 2.5, negZero},
			[]bool{true, true, true, true, false, true, true, true}),
		ursus.ValuesNullable("s", []string{"1", "x", "3", "4", "", "3", "2", "1"},
			[]bool{true, true, true, true, false, true, true, true}),
		ursus.ValuesNullable("ok", []bool{true, false, true, true, false, true, false, true},
			[]bool{true, true, true, true, false, true, true, true}),
	)
}

// transform is one step of a query shape. apply picks its columns from the
// schema and reports false when the columns it needs are not there.
type transform struct {
	name string
	// unordered: the output order is not promised, so results compare as
	// multisets from here on.
	unordered bool
	// sensitive: which rows or values come out depends on the input's order.
	// After an unordered step only the schema and height can be compared.
	sensitive bool
	apply     func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool)
}

func isInt(t dtype.DataType) bool   { return t.IsInteger() }
func isFloat(t dtype.DataType) bool { return t.IsFloat() }
func isStr(t dtype.DataType) bool   { return t.ID() == dtype.TypeString }
func isBool(t dtype.DataType) bool  { return t.IsBool() }

// pick returns the names of the columns whose type satisfies ok, in schema order.
func pick(s *ursus.Schema, ok func(dtype.DataType) bool) []string {
	var out []string
	for _, f := range s.FieldSlice() {
		if ok(f.Type) {
			out = append(out, f.Name)
		}
	}
	return out
}

func hasCol(s *ursus.Schema, name string) bool { return slices.Contains(s.Names(), name) }

func dbl64() func(int64) (int64, error) { return func(v int64) (int64, error) { return v * 2, nil } }

func transforms() []transform {
	c := ursus.Col
	first := func(s *ursus.Schema, ok func(dtype.DataType) bool) (string, bool) {
		p := pick(s, ok)
		if len(p) == 0 {
			return "", false
		}
		return p[0], true
	}
	join := func(name string, how ursus.JoinKind, unordered bool) transform {
		return transform{name: name, unordered: unordered,
			apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
				if !hasCol(s, "k") || !hasCol(s, "s") {
					return nil, false
				}
				right := lf.Filter(c("k").Gt(1)).Select(c("k"), c("s").Alias("rs"))
				return lf.Join(right, ursus.JoinOn(c("k")), ursus.JoinHow(how)), true
			}}
	}
	return []transform{
		{name: "filter-guard", apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			b, ok := first(s, isBool)
			return lf.Filter(c(b)), ok
		}},
		{name: "filter-int", apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			i, ok := first(s, isInt)
			return lf.Filter(c(i).Gt(2)), ok
		}},
		// O1's three forms. wc-chain redefines an existing column and reads it
		// back; wc-inplace redefines the column it reads; wc-new reads a name the
		// input does not have.
		{name: "wc-chain", apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			ints := pick(s, isInt)
			if len(ints) < 2 {
				return nil, false
			}
			return lf.WithColumns(c(ints[0]).Cast(ursus.Int64).Add(1).Alias(ints[1]),
				c(ints[1]).Mul(2).Alias("v")), true
		}},
		{name: "wc-inplace", apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			i, ok := first(s, isInt)
			return lf.WithColumns(c(i).Add(10).Alias(i), c(i).Mul(2).Alias("y")), ok
		}},
		{name: "wc-new", apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			i, ok := first(s, isInt)
			return lf.WithColumns(c(i).Mul(3).Alias("w"), c("w").Sub(1).Alias("w1")), ok
		}},
		{name: "wc-udf", apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			i, ok := first(s, isInt)
			return lf.WithColumns(
				c(i).Cast(ursus.Int64).MapElements("dbl", ursus.Int64, dbl64()).Alias("q"),
				c("q").Add(1).Alias("q1")), ok
		}},
		// A Select is parallel: the control that composition must not reach.
		{name: "select", apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			i, ok := first(s, isInt)
			if !ok {
				return nil, false
			}
			exprs := []ursus.Expr{c(i).Add(1).Alias(i), c(i).Mul(2).Alias("m")}
			for _, name := range s.Names() {
				if name != i && name != "m" {
					exprs = append(exprs, c(name))
				}
			}
			return lf.Select(exprs...), true
		}},
		{name: "rename", apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			i, ok := first(s, isInt)
			return lf.Rename(map[string]string{i: i + "_r"}), ok
		}},
		{name: "sort", apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			keys := []ursus.SortKey{}
			for _, name := range s.Names() {
				keys = append(keys, ursus.Asc(c(name)).NullsLast())
			}
			return lf.Sort(keys...), len(keys) > 0
		}},
		{name: "slice", sensitive: true, apply: func(lf *ursus.LazyFrame, _ *ursus.Schema) (*ursus.LazyFrame, bool) {
			return lf.Slice(1, 4), true
		}},
		{name: "tail", sensitive: true, apply: func(lf *ursus.LazyFrame, _ *ursus.Schema) (*ursus.LazyFrame, bool) {
			return lf.Tail(5), true
		}},
		{name: "reverse", apply: func(lf *ursus.LazyFrame, _ *ursus.Schema) (*ursus.LazyFrame, bool) {
			return lf.Reverse(), true
		}},
		{name: "rowindex", sensitive: true, apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			return lf.WithRowIndex("idx", 0), !hasCol(s, "idx")
		}},
		{name: "unique", unordered: true, apply: func(lf *ursus.LazyFrame, _ *ursus.Schema) (*ursus.LazyFrame, bool) {
			return lf.Unique(), true
		}},
		{name: "unique-subset", unordered: true, sensitive: true, apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			str, ok := first(s, isStr)
			return lf.Unique(str), ok
		}},
		{name: "groupby", unordered: true, sensitive: true, apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			i, ok := first(s, isInt)
			if !ok {
				return nil, false
			}
			aggs := []ursus.Expr{ursus.Len().Alias("cnt")}
			if f, ok := first(s, isFloat); ok {
				aggs = append(aggs, c(f).Sum().Alias("fsum"))
			}
			if str, ok := first(s, isStr); ok {
				aggs = append(aggs, c(str).First().Alias("sfirst"))
			}
			return lf.GroupBy(c(i)).Agg(aggs...), true
		}},
		{name: "concat-filter", apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			i, ok := first(s, isInt)
			return lf.Concat(lf.Filter(c(i).Gt(2))), ok
		}},
		{name: "concat-widen", apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			if !hasCol(s, "n") {
				return nil, false
			}
			return lf.WithColumns(c("n").Cast(ursus.Int64).Alias("n")).Concat(lf), true
		}},
		{name: "concat-diag", apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			i, ok := first(s, isInt)
			if !ok || s.Len() < 2 {
				return nil, false
			}
			return ursus.Concat([]*ursus.LazyFrame{lf, lf.Filter(c(i).Gt(2)).Drop(s.Names()[s.Len()-1])},
				ursus.WithConcatMode(ursus.ConcatDiagonal)), true
		}},
		join("join-inner", ursus.JoinInner, true),
		join("join-left", ursus.JoinLeft, true),
		join("join-full", ursus.JoinFull, true),
		join("join-semi", ursus.JoinSemi, true),
		join("join-anti", ursus.JoinAnti, true),
		// O6: the key is Int32 on the left and Int64 on the right, so the output
		// key is Int64 and a filter above must not run at Int32.
		{name: "join-widen", unordered: true, apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			if !hasCol(s, "n") || !hasCol(s, "k") {
				return nil, false
			}
			right := lf.Select(c("n").Cast(ursus.Int64).Alias("n"), c("k").Alias("rk"))
			return lf.Join(right, ursus.JoinOn(c("n"))), true
		}},
		// O4: a float equality through the cross-join collapse, and NaN on both sides.
		{name: "joinwhere", unordered: true, apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			f, ok := first(s, isFloat)
			if !ok {
				return nil, false
			}
			return lf.JoinWhere(lf.Select(c(f).Alias("fr")), c(f).Eq(c("fr"))), true
		}},
		{name: "whereexists", apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			f, ok := first(s, isFloat)
			if !ok {
				return nil, false
			}
			return lf.WhereExists(lf.Select(c(f).Alias("fr")), c(f).Eq(c("fr"))), true
		}},
		// O5: NullsEqual on a cross join, then an equality filter that must drop
		// null = null.
		{name: "cross-nulls", unordered: true, apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			if !hasCol(s, "k") {
				return nil, false
			}
			return lf.Join(lf.Select(c("k").Alias("kr")), ursus.JoinHow(ursus.JoinCross),
				ursus.JoinNullsEqual(true)).Filter(c("k").Eq(c("kr"))), true
		}},
		{name: "dropnulls", apply: func(lf *ursus.LazyFrame, _ *ursus.Schema) (*ursus.LazyFrame, bool) {
			return lf.DropNulls(), true
		}},
		{name: "fillnull", apply: func(lf *ursus.LazyFrame, _ *ursus.Schema) (*ursus.LazyFrame, bool) {
			return lf.FillNull(0), true
		}},
		{name: "unpivot", unordered: true, apply: func(lf *ursus.LazyFrame, s *ursus.Schema) (*ursus.LazyFrame, bool) {
			if !hasCol(s, "k") || !hasCol(s, "n") {
				return nil, false
			}
			return lf.Unpivot(ursus.UnpivotOptions{On: []string{"k", "n"}}), true
		}},
	}
}

// coreTransforms are composed in pairs; the rest run singly.
var coreTransforms = []string{
	"filter-guard", "filter-int", "wc-chain", "wc-inplace", "select", "rename",
	"sort", "slice", "unique", "unique-subset", "groupby", "concat-filter",
}

// top is a final step derived from the shape's unoptimized result.
type top struct {
	name      string
	sensitive bool
	lf        *ursus.LazyFrame
}

// tops derives the final steps for a shape: a median filter and a special filter
// per column, a single-column Select per column, and Head, Len and the bare shape.
// few limits them to the last column and the first of each kind, for pairs.
func tops(lf *ursus.LazyFrame, df *ursus.DataFrame, few bool) []top {
	c := ursus.Col
	s := df.Schema()
	names := s.Names()
	cols := names
	if few && len(names) > 0 {
		cols = []string{names[len(names)-1]}
		for _, ok := range []func(dtype.DataType) bool{isInt, isStr} {
			if p := pick(s, ok); len(p) > 0 && !slices.Contains(cols, p[0]) {
				cols = append(cols, p[0])
			}
		}
	}
	out := []top{{name: "none", lf: lf}, {name: "len", lf: lf.GroupBy().Agg(ursus.Len().Alias("_n"))}}
	if !few {
		out = append(out, top{name: "head3", sensitive: true, lf: lf.Head(3)})
	}
	for _, name := range cols {
		f := fieldOf(s, name)
		if pred, ok := medianPred(df, f); ok {
			out = append(out, top{name: "median(" + name + ")", lf: lf.Filter(pred)})
		}
		switch {
		case f.Type.ID() == dtype.TypeInt32 || f.Type.ID() == dtype.TypeInt64:
			out = append(out, top{name: "square(" + name + ")",
				lf: lf.Filter(c(name).Mul(c(name)).Gt(int64(1_000_000_000)))})
		case isFloat(f.Type):
			out = append(out, top{name: "recip(" + name + ")",
				lf: lf.Filter(ursus.Lit(1.0).Div(c(name)).Lt(0.0))})
		case isStr(f.Type):
			out = append(out, top{name: "cast(" + name + ")",
				lf: lf.Filter(c(name).Cast(ursus.Int64).Gt(1))})
		}
		if !few {
			out = append(out, top{name: "select(" + name + ")", lf: lf.Select(c(name))})
		}
	}
	return out
}

func fieldOf(s *ursus.Schema, name string) dtype.Field {
	for _, f := range s.FieldSlice() {
		if f.Name == name {
			return f
		}
	}
	return dtype.Field{}
}

// medianPred filters a column at its median non-null, non-NaN value.
func medianPred(df *ursus.DataFrame, f dtype.Field) (ursus.Expr, bool) {
	c := ursus.Col(f.Name)
	switch {
	case isBool(f.Type):
		return c, true
	case isStr(f.Type):
		vals := readAll[string](df, f.Name)
		if len(vals) == 0 {
			return ursus.Expr{}, false
		}
		sort.Strings(vals)
		return c.Eq(ursus.Lit(vals[len(vals)/2])), true
	}
	var vals []float64
	switch f.Type.ID() {
	case dtype.TypeInt32:
		for _, v := range readAll[int32](df, f.Name) {
			vals = append(vals, float64(v))
		}
	case dtype.TypeInt64:
		for _, v := range readAll[int64](df, f.Name) {
			vals = append(vals, float64(v))
		}
	case dtype.TypeUint32:
		for _, v := range readAll[uint32](df, f.Name) {
			vals = append(vals, float64(v))
		}
	case dtype.TypeUint64:
		for _, v := range readAll[uint64](df, f.Name) {
			vals = append(vals, float64(v))
		}
	case dtype.TypeFloat64:
		for _, v := range readAll[float64](df, f.Name) {
			if !math.IsNaN(v) {
				vals = append(vals, v)
			}
		}
	case dtype.TypeInt128:
		for _, v := range readAll[ursus.Int128Value](df, f.Name) {
			vals = append(vals, v.Float64())
		}
	default:
		return ursus.Expr{}, false
	}
	if len(vals) == 0 {
		return ursus.Expr{}, false
	}
	sort.Float64s(vals)
	m := vals[len(vals)/2]
	if isFloat(f.Type) {
		return c.Gt(m), true
	}
	return c.Gt(int64(math.Floor(m))), true
}

// readAll returns a column's non-null values, or nil if it cannot be read as T.
func readAll[T any](df *ursus.DataFrame, name string) []T {
	col, err := df.Column[T](name)
	if err != nil {
		return nil
	}
	var out []T
	for i := range col.Len() {
		if v, ok := col.Get(i); ok {
			out = append(out, v)
		}
	}
	return out
}

// --- comparison ------------------------------------------------------------------

// stopRecording unwinds a recorder's Fatal.
type stopRecording struct{}

// recorder is a testing.TB that records failures instead of failing the test, so
// ursustest.AssertFrameEqual — which calls Fatalf and relies on it not returning —
// can be asked whether two frames differ.
type recorder struct {
	testing.TB
	failed bool
	msgs   []string
}

func (r *recorder) Helper()             {}
func (r *recorder) Log(...any)          {}
func (r *recorder) Logf(string, ...any) {}
func (r *recorder) Fail()               { r.failed = true }
func (r *recorder) Error(a ...any)      { r.failed = true; r.msgs = append(r.msgs, fmt.Sprint(a...)) }
func (r *recorder) Errorf(f string, a ...any) {
	r.failed = true
	r.msgs = append(r.msgs, fmt.Sprintf(f, a...))
}
func (r *recorder) FailNow()       { r.failed = true; panic(stopRecording{}) }
func (r *recorder) Fatal(a ...any) { r.Error(a...); panic(stopRecording{}) }
func (r *recorder) Fatalf(f string, a ...any) {
	r.Errorf(f, a...)
	panic(stopRecording{})
}

// framesDiffer reports how got differs from want, or "" if it does not.
func framesDiffer(t testing.TB, got, want *ursus.DataFrame, opts ...ursustest.Option) string {
	r := &recorder{TB: t}
	func() {
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(stopRecording); !ok {
					panic(p)
				}
			}
		}()
		ursustest.AssertFrameEqual(r, got, want, opts...)
	}()
	if !r.failed {
		return ""
	}
	if len(r.msgs) == 0 {
		return "differ"
	}
	return strings.Join(r.msgs, "; ")
}

var errKinds = []struct {
	name string
	err  error
}{
	{"type", ursus.ErrType}, {"schema", ursus.ErrSchema}, {"value", ursus.ErrValue},
	{"unsupported", ursus.ErrUnsupported}, {"io", ursus.ErrIO},
	{"resource", ursus.ErrResource}, {"internal", ursus.ErrInternal},
}

func kindOf(err error) string {
	for _, k := range errKinds {
		if errors.Is(err, k.err) {
			return k.name
		}
	}
	return "other"
}

// compareMode is how much of two results can be compared.
type compareMode int

const (
	exact    compareMode = iota // row order is promised
	multiset                    // rows compare regardless of order
	shape                       // only the schema and height are determined
)

// outcome of one query.
type outcome struct {
	agree     bool
	bothErr   bool
	optOnly   bool // the optimizer turned an error into a result: O12's class
	nonEmpty  bool
	mismatch  string
	unoptimal *ursus.DataFrame
}

func compare(t testing.TB, lf *ursus.LazyFrame, mode compareMode) outcome {
	off, errOff := lf.Collect(t.Context(), ursus.WithOptFlags(plan.NoFlags()), ursus.WithThreads(1))
	on, errOn := lf.Collect(t.Context(), ursus.WithVerify(), ursus.WithThreads(1))
	return judgeOne(t, on, errOn, off, errOff, mode)
}

func judgeOne(t testing.TB, on *ursus.DataFrame, errOn error, off *ursus.DataFrame, errOff error,
	mode compareMode) outcome {
	switch {
	case errOn != nil && errOff != nil:
		if kindOf(errOn) != kindOf(errOff) {
			return outcome{mismatch: fmt.Sprintf("both fail, as %s vs %s: %v", kindOf(errOn), kindOf(errOff), errOn)}
		}
		return outcome{agree: true, bothErr: true}
	case errOn != nil:
		return outcome{mismatch: "optimized fails: " + firstLine(errOn.Error()), unoptimal: off}
	case errOff != nil:
		return outcome{agree: true, optOnly: true, unoptimal: nil}
	}
	var diff string
	switch mode {
	case exact:
		diff = framesDiffer(t, on, off, ursustest.CheckNullability())
	case multiset:
		diff = framesDiffer(t, on, off, ursustest.CheckNullability(), ursustest.IgnoreRowOrder())
	case shape:
		if on.Height() != off.Height() {
			diff = fmt.Sprintf("height %d vs %d", on.Height(), off.Height())
		} else if on.Schema().String() != off.Schema().String() {
			diff = fmt.Sprintf("schema %s vs %s", on.Schema(), off.Schema())
		}
	}
	if diff != "" {
		return outcome{mismatch: firstLine(diff), unoptimal: off}
	}
	return outcome{agree: true, nonEmpty: off.Height() > 0, unoptimal: off}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// bisect names the rules that, enabled alone, reproduce a mismatch. The join arm
// of predicate pushdown only exists inside predicate pushdown, so it is enabled
// with it.
func bisect(t testing.TB, lf *ursus.LazyFrame, mode compareMode) []string {
	off, errOff := lf.Collect(t.Context(), ursus.WithOptFlags(plan.NoFlags()), ursus.WithThreads(1))
	var guilty []string
	flags := reflect.TypeOf(plan.Flags{})
	for i := range flags.NumField() {
		f := plan.NoFlags()
		v := reflect.ValueOf(&f).Elem()
		v.Field(i).SetBool(true)
		if flags.Field(i).Name == "JoinPredicatePushdown" {
			v.FieldByName("PredicatePushdown").SetBool(true)
		}
		on, errOn := lf.Collect(t.Context(), ursus.WithOptFlags(f), ursus.WithThreads(1))
		if o := judgeOne(t, on, errOn, off, errOff, mode); !o.agree {
			guilty = append(guilty, flags.Field(i).Name)
		}
	}
	return guilty
}

// --- the ratchet -------------------------------------------------------------------

// knownMismatch is how many queries a defect makes the optimizer answer
// differently today, and why.
type knownMismatch struct {
	n   int
	why string
}

// mismatch is one query the two modes answer differently, with the defect it is
// attributed to.
type mismatch struct {
	name, detail, class string
	guilty              []string
}

// classify attributes a mismatch to a known defect from the rules that reproduce
// it alone, its symptom and the transforms in its shape. It returns "" for one it
// cannot place, which fails the test.
//
// The attribution does not have to be subtle to be safe, because the ratchet is
// COUNTED: a new defect that lands in an old class changes that class's count, and
// the count is exact. What a wrong attribution can do is make the as-built's
// numbers per defect imprecise — not hide a mismatch.
func classify(name, detail string, guilty []string) string {
	chain, topName, _ := strings.Cut(name, " | ")
	has := func(t string) bool { return slices.Contains(strings.Split(chain, " > "), t) }
	switch {
	case slices.Equal(guilty, []string{"CollapseCrossJoin"}) && has("cross-nulls"):
		return "O5"
	case slices.Equal(guilty, []string{"CollapseCrossJoin"}):
		return "O4"
	case strings.Contains(detail, "changed the output schema"):
		return "P1"
	case strings.Contains(detail, "does not resolve") || strings.Contains(detail, "concat child"):
		return "O2"
	case has("join-widen"):
		return "O6"
	case strings.Contains(detail, "cannot parse"):
		for _, j := range []string{"join-inner", "join-left", "join-full", "join-semi", "join-anti"} {
			if has(j) {
				return "O8-join"
			}
		}
		if has("unique-subset") {
			return "O8b"
		}
		return "O8"
	case has("unique") && strings.HasPrefix(topName, "recip("):
		return "O9"
	case slices.Contains(guilty, "PredicatePushdown") &&
		(has("wc-chain") || has("wc-inplace") || has("wc-new") || has("wc-udf")):
		return "O1"
	}
	return ""
}

// knownDifferentialMismatches counts every query the optimizer answers differently
// today, by defect. Checked both ways: a class whose count changes is reported with
// its queries, a class with none left is stale, and an unattributed mismatch fails.
var knownDifferentialMismatches = map[string]knownMismatch{
	"O1": {64, "predicate pushdown substitutes a WithColumns definition verbatim, so one " +
		"that reads an earlier-defined column is evaluated against the input's column"},
	"O2":      {29, "projection pushdown narrows a Concat's children to different widths"},
	"O4":      {19, "the cross-join collapse turns IEEE == into hash equality: NaN matches NaN"},
	"O5":      {9, "the cross-join collapse keeps NullsEqual, so null keys match"},
	"O6":      {1, "a filter on a widened join key is pushed to the narrow side and runs there"},
	"O8":      {32, "merging stacked filters runs the outer one first, ahead of its guard"},
	"O8-join": {1, "a fallible filter is pushed into a join side, onto rows the join removes"},
	"O8b":     {4, "a fallible filter is pushed below a Distinct, ahead of a guard that stays"},
	"O9":      {1, "a filter below Unique can tell -0 from +0, which Unique merges"},
	"P1": {106, "projection pushdown stops reading a column a WithColumns redefines, and " +
		"the redefinition then lands at the end instead of in place"},
}

// judge compares the attributed mismatches with the ratchet and returns every
// problem. It is pure, so it can be tested on its own.
func judge(ms []mismatch, known map[string]knownMismatch) []string {
	var problems []string
	byClass := map[string][]string{}
	for _, m := range ms {
		if m.class == "" {
			problems = append(problems, fmt.Sprintf("unattributed mismatch: %s — %s — rules that reproduce it alone: %v",
				m.name, m.detail, m.guilty))
			continue
		}
		byClass[m.class] = append(byClass[m.class], m.name)
	}
	classes := make([]string, 0, len(known)+len(byClass))
	for c := range known {
		classes = append(classes, c)
	}
	for c := range byClass {
		if _, ok := known[c]; !ok {
			classes = append(classes, c)
		}
	}
	slices.Sort(classes)
	for _, c := range classes {
		got, k := byClass[c], known[c]
		slices.Sort(got)
		switch {
		case k.n == 0:
			problems = append(problems, fmt.Sprintf("%s: %d mismatches, and it is not listed: %v", c, len(got), got))
		case len(got) == 0:
			problems = append(problems, fmt.Sprintf("%s is stale (%s): nothing mismatches any more", c, k.why))
		case len(got) != k.n:
			problems = append(problems, fmt.Sprintf("%s: %d mismatches, listed %d: %v", c, len(got), k.n, got))
		}
	}
	return problems
}

// --- the test ----------------------------------------------------------------------

func TestOptimizerAgreesWithItself(t *testing.T) {
	start := time.Now()
	ts := transforms()
	byName := map[string]transform{}
	for _, tr := range ts {
		byName[tr.name] = tr
	}
	base := diffFixture()

	var (
		queries, bothErr, optOnly int
		mismatches                []mismatch
		// every transform and every top kind must appear in a query that agrees
		// and returns rows
		seenGood = map[string]bool{}
	)

	run := func(chain []transform, few bool) {
		lf := base
		unordered, sensitive := false, false
		var label []string
		for _, tr := range chain {
			s, err := lf.CollectSchema(t.Context())
			if err != nil {
				return
			}
			next, ok := tr.apply(lf, s)
			if !ok {
				return
			}
			if tr.sensitive && unordered {
				sensitive = true
			}
			unordered = unordered || tr.unordered
			lf = next
			label = append(label, tr.name)
		}
		off, err := lf.Collect(t.Context(), ursus.WithOptFlags(plan.NoFlags()), ursus.WithThreads(1))
		var tps []top
		if err != nil {
			tps = []top{{name: "none", lf: lf}}
		} else {
			tps = tops(lf, off, few)
		}
		for _, tp := range tps {
			mode := exact
			switch {
			case sensitive || (tp.sensitive && unordered):
				mode = shape
			case unordered:
				mode = multiset
			}
			name := strings.Join(label, " > ") + " | " + tp.name
			queries++
			o := compare(t, tp.lf, mode)
			switch {
			case o.mismatch != "":
				guilty := bisect(t, tp.lf, mode)
				mismatches = append(mismatches, mismatch{name: name, detail: o.mismatch,
					guilty: guilty, class: classify(name, o.mismatch, guilty)})
			case o.bothErr:
				bothErr++
			case o.optOnly:
				optOnly++
			case o.nonEmpty:
				for _, l := range label {
					seenGood["t:"+l] = true
				}
				kind := tp.name
				if i := strings.IndexByte(kind, '('); i >= 0 {
					kind = kind[:i]
				}
				seenGood["top:"+kind] = true
			}
		}
	}

	for _, tr := range ts {
		run([]transform{tr}, false)
	}
	for _, a := range coreTransforms {
		for _, b := range coreTransforms {
			run([]transform{byName[a], byName[b]}, true)
		}
	}
	for _, chain := range [][]string{
		{"wc-chain", "sort", "slice"},
		{"filter-guard", "wc-new", "concat-filter"},
		{"select", "wc-inplace", "filter-int"},
		{"join-left", "wc-chain", "unique"},
		{"concat-widen", "groupby", "sort"},
	} {
		var c []transform
		for _, n := range chain {
			c = append(c, byName[n])
		}
		run(c, true)
	}

	elapsed := time.Since(start)
	t.Logf("%d queries in %v: %d mismatches, %d fail in both modes, %d succeed only optimized",
		queries, elapsed.Round(time.Millisecond), len(mismatches), bothErr, optOnly)

	for _, p := range judge(mismatches, knownDifferentialMismatches) {
		t.Error(p)
	}

	// Anti-vacuity.
	if queries < 800 {
		t.Errorf("only %d queries — the generator has stopped composing", queries)
	}
	if bothErr*4 > queries {
		t.Errorf("%d of %d queries fail in both modes; a sweep that mostly errors compares nothing", bothErr, queries)
	}
	for _, tr := range ts {
		if !seenGood["t:"+tr.name] {
			t.Errorf("transform %s never appears in a query that agrees and returns rows", tr.name)
		}
	}
	for _, k := range []string{"none", "len", "head3", "median", "square", "recip", "cast", "select"} {
		if !seenGood["top:"+k] {
			t.Errorf("top %s never appears in a query that agrees and returns rows", k)
		}
	}
}

// TestDifferentialJudge: the ratchet's judge, which decides what the sweep reports.
// A judge that passed everything would make the sweep a no-op.
func TestDifferentialJudge(t *testing.T) {
	known := map[string]knownMismatch{
		"A": {n: 2, why: "two"},
		"B": {n: 1, why: "gone"},
		"C": {n: 1, why: "one"},
	}
	got := judge([]mismatch{
		{name: "a1", class: "A"}, {name: "a2", class: "A"},
		{name: "c1", class: "C"}, {name: "c2", class: "C"},
		{name: "d1", class: "D"},
		{name: "x", detail: "rows", guilty: []string{"PredicatePushdown"}},
	}, known)
	want := []string{
		"unattributed mismatch: x — rows — rules that reproduce it alone: [PredicatePushdown]",
		"B is stale (gone): nothing mismatches any more",
		"C: 2 mismatches, listed 1: [c1 c2]",
		"D: 1 mismatches, and it is not listed: [d1]",
	}
	if !slices.Equal(got, want) {
		t.Errorf("judge =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if p := judge([]mismatch{{name: "a1", class: "A"}, {name: "a2", class: "A"}},
		map[string]knownMismatch{"A": {n: 2}}); len(p) != 0 {
		t.Errorf("an exact match reported %v", p)
	}
}

// TestDifferentialRecorder: framesDiffer must see a difference AssertFrameEqual
// reports with Fatalf, and must not see one where there is none.
func TestDifferentialRecorder(t *testing.T) {
	a, err := ursus.Frame(ursus.Values("x", []int64{1, 2})).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ursus.Frame(ursus.Values("x", []int64{1, 3})).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	short, err := ursus.Frame(ursus.Values("x", []int64{1})).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if d := framesDiffer(t, a, a); d != "" {
		t.Errorf("a frame differs from itself: %s", d)
	}
	if framesDiffer(t, a, b) == "" {
		t.Error("a differing value was not seen")
	}
	if framesDiffer(t, a, short) == "" {
		t.Error("a differing height, which AssertFrameEqual reports with Fatalf, was not seen")
	}
}
