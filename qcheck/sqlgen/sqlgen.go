package sqlgen

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/model"
)

// RID is the hidden column a registered table holds each row's place in.
const RID = "__rid"

// Excluded is a query sqlgen does not translate, by a rule naming why: a shape
// DuckDB answers wrongly, or one a refusal rule says the AST never makes.
type Excluded struct {
	Rule string
	Why  string
}

func (e *Excluded) Error() string { return e.Rule + ": " + e.Why }

// SQL is a query's translation.
type SQL struct {
	// Text is the query: its answer's columns are the root frame's, named as ast
	// names them, or one BIGINT "n" for a Count.
	Text string
	// Counts is a query of two columns, "i" and "n": the rows of Nodes[i]'s frame,
	// for the anti-vacuity counts.
	Counts string
	// Nodes are the query's frame operations, ast.Walk's order, Scan among them.
	Nodes []ast.Frame
	// Fired counts each rule's firings.
	Fired map[string]int
}

// Type is DuckDB's type for t: a Date is an INTEGER of days (T-DATE-DAYS), an
// Int128 a HUGEINT, a Uint64 a UBIGINT.
func Type(t model.Type) string {
	switch t {
	case model.Bool:
		return "BOOLEAN"
	case model.Int8:
		return "TINYINT"
	case model.Int16:
		return "SMALLINT"
	case model.Int32, model.Date:
		return "INTEGER"
	case model.Int64:
		return "BIGINT"
	case model.Int128:
		return "HUGEINT"
	case model.Uint64:
		return "UBIGINT"
	case model.Float64:
		return "DOUBLE"
	case model.String:
		return "VARCHAR"
	}
	panic("sqlgen: no DuckDB type for " + t.String())
}

// Quote is name as a DuckDB identifier.
func Quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// Translate is q's SQL over tables. A query sqlgen does not translate is an
// *Excluded; any other error is sqlgen's or the AST's own bug.
func Translate(q *ast.Query, tables map[string]*model.Table) (s *SQL, err error) {
	if err := ast.Check(q, tables); err != nil {
		return nil, fmt.Errorf("sqlgen: an ill-formed query: %w", err)
	}
	b := &builder{tables: tables, fired: map[string]int{}, names: map[ast.Frame]string{}}
	defer func() {
		if v := recover(); v != nil {
			ex, ok := v.(*Excluded)
			if !ok {
				panic(v)
			}
			s, err = nil, ex
		}
	}()
	root := b.frame(q.Root)
	info := b.info(q.Root)
	with := "WITH " + strings.Join(b.ctes, ",\n")
	var text string
	if q.Count {
		text = with + "\nSELECT CAST(count(*) AS BIGINT) AS \"n\" FROM " + root
	} else {
		cols := make([]string, len(info.Cols))
		for i, c := range info.Cols {
			cols[i] = Quote(c.Name)
		}
		text = with + "\nSELECT " + strings.Join(cols, ", ") + " FROM " + root
		if info.Ordered {
			b.fire(TOrderPromise)
			text += ` ORDER BY "__o"`
		}
	}
	var counts []string
	for i, f := range b.nodes {
		counts = append(counts, fmt.Sprintf("SELECT %d AS \"i\", count(*) AS \"n\" FROM %s", i, b.names[f]))
	}
	return &SQL{Text: text, Counts: with + "\n" + strings.Join(counts, "\nUNION ALL ") + "\nORDER BY \"i\"",
		Nodes: b.nodes, Fired: b.fired}, nil
}

type builder struct {
	tables map[string]*model.Table
	ctes   []string
	fired  map[string]int
	nodes  []ast.Frame
	names  map[ast.Frame]string
}

func (b *builder) fire(id string) { b.fired[id]++ }

func (b *builder) refuse(rule, why string, a ...any) {
	panic(&Excluded{Rule: rule, Why: fmt.Sprintf(why, a...)})
}

func (b *builder) info(f ast.Frame) *ast.Info {
	in, err := ast.Schema(f, b.tables)
	if err != nil {
		panic(fmt.Sprintf("sqlgen: %v", err))
	}
	return in
}

// cte adds a CTE of body and returns its name.
func (b *builder) cte(body string) string {
	name := fmt.Sprintf("q%d", len(b.ctes)+1)
	b.ctes = append(b.ctes, name+" AS ("+body+")")
	return name
}

// rename is a CTE of from's __o and its columns tmp[i] named names[i].
func (b *builder) rename(from string, tmp, names []string) string {
	cols := []string{`"__o"`}
	for i := range tmp {
		cols = append(cols, Quote(tmp[i])+" AS "+Quote(names[i]))
	}
	return b.cte("SELECT " + strings.Join(cols, ", ") + " FROM " + from)
}

func colList(in *ast.Info, prefix string) []string {
	out := make([]string, len(in.Cols))
	for i, c := range in.Cols {
		out[i] = prefix + Quote(c.Name)
	}
	return out
}

// frame translates f and returns the CTE that holds it: __o, then its columns.
func (b *builder) frame(f ast.Frame) string {
	if name, ok := b.names[f]; ok {
		return name
	}
	var name string
	switch f := f.(type) {
	case *ast.Scan:
		t := b.tables[f.Table]
		cols := []string{Quote(RID) + ` AS "__o"`}
		for _, c := range t.Cols {
			cols = append(cols, Quote(c.Name))
			if c.Type == model.Date {
				b.fire(TDateDays)
			}
		}
		name = b.cte("SELECT " + strings.Join(cols, ", ") + " FROM " + Quote(t.Name))
	case *ast.Filter:
		in := b.frame(f.In)
		name = b.cte("SELECT * FROM " + in + " WHERE " + b.expr(f.Pred))
	case *ast.Select:
		in := b.frame(f.In)
		cols := []string{`"__o"`}
		var tmp, names []string
		lits := true
		for i, n := range f.Cols {
			t := fmt.Sprintf("__c%d", i)
			cols = append(cols, b.cast(n.E, n.E.Type())+" AS "+Quote(t))
			tmp, names = append(tmp, t), append(names, n.Name)
			if len(ast.Reads(n.E)) > 0 {
				lits = false
			}
		}
		if lits {
			b.fire(RSelectLit)
		}
		name = b.rename(b.cte("SELECT "+strings.Join(cols, ", ")+" FROM "+in), tmp, names)
	case *ast.WithColumns:
		name = b.frame(f.In)
		if len(f.Cols) > 1 {
			b.fire(TWithSeq)
		}
		// One column at a time: each expression reads the columns the ones before it
		// added or replaced (T-WITH-SEQ).
		for i := range f.Cols {
			prev := &ast.WithColumns{In: f.In, Cols: f.Cols[:i]}
			in := b.info(prev)
			if i == 0 {
				in = b.info(f.In)
			}
			n := f.Cols[i]
			cols := []string{`"__o"`}
			var tmp, names []string
			replaced := false
			for j, c := range in.Cols {
				t := fmt.Sprintf("__c%d", j)
				e := Quote(c.Name)
				if c.Name == n.Name {
					e, replaced = b.cast(n.E, n.E.Type()), true
				}
				cols = append(cols, e+" AS "+Quote(t))
				tmp, names = append(tmp, t), append(names, c.Name)
			}
			if !replaced {
				t := fmt.Sprintf("__c%d", len(in.Cols))
				cols = append(cols, b.cast(n.E, n.E.Type())+" AS "+Quote(t))
				tmp, names = append(tmp, t), append(names, n.Name)
			}
			name = b.rename(b.cte("SELECT "+strings.Join(cols, ", ")+" FROM "+name), tmp, names)
		}
	case *ast.GroupBy:
		name = b.groupBy(f)
	case *ast.Sort:
		name = b.sort(f)
	case *ast.Head:
		in := b.frame(f.In)
		name = b.cte(fmt.Sprintf(`SELECT * FROM %s ORDER BY "__o" LIMIT %d`, in, f.N))
	case *ast.Slice:
		name = b.slice(f)
	case *ast.Join:
		name = b.join(f)
	default:
		panic(fmt.Sprintf("sqlgen: unknown frame %T", f))
	}
	b.names[f] = name
	b.nodes = append(b.nodes, f)
	return name
}

func (b *builder) groupBy(f *ast.GroupBy) string {
	in := b.frame(f.In)
	if len(f.Keys) > 0 {
		b.fire(TGroupNull)
	}
	if f.MaintainOrder {
		b.fire(TMaintain)
	}
	// The keys first, as columns beside the input's, so the aggregates read the
	// input's columns by their own names.
	pre := []string{"*"}
	var groups []string
	for i, k := range f.Keys {
		pre = append(pre, b.cast(k.E, k.E.Type())+fmt.Sprintf(` AS "__k%d"`, i))
		groups = append(groups, fmt.Sprintf(`"__k%d"`, i))
	}
	keyed := b.cte("SELECT " + strings.Join(pre, ", ") + " FROM " + in)
	cols := []string{`min("__o") AS "__o"`}
	var tmp, names []string
	for i, k := range f.Keys {
		t := fmt.Sprintf("__c%d", i)
		cols = append(cols, fmt.Sprintf(`"__k%d" AS %s`, i, Quote(t)))
		tmp, names = append(tmp, t), append(names, k.Name)
	}
	for i, a := range f.Aggs {
		t := fmt.Sprintf("__a%d", i)
		cols = append(cols, b.agg(a.E.(*ast.Agg))+" AS "+Quote(t))
		tmp, names = append(tmp, t), append(names, a.Name)
	}
	body := "SELECT " + strings.Join(cols, ", ") + " FROM " + keyed
	if len(groups) > 0 {
		body += " GROUP BY " + strings.Join(groups, ", ")
	}
	return b.rename(b.cte(body), tmp, names)
}

func (b *builder) sort(f *ast.Sort) string {
	in := b.frame(f.In)
	info := b.info(f.In)
	b.fire(TSortStable)
	var keys []string
	for _, k := range f.Keys {
		c := info.Col(k.Col)
		e := Quote(k.Col)
		if c.Type == model.Float64 {
			b.fire(TKeyZero)
			e = "u_fkey(" + e + ")"
		}
		dir := " ASC"
		if k.Desc {
			dir = " DESC"
		}
		nulls := " NULLS FIRST"
		if k.NullsLast {
			nulls = " NULLS LAST"
		}
		b.fire(TNulls)
		keys = append(keys, e+dir+nulls)
	}
	keys = append(keys, `"__o"`)
	cols := append([]string{`row_number() OVER (ORDER BY ` + strings.Join(keys, ", ") + `) AS "__s"`},
		colList(info, "")...)
	sorted := b.cte("SELECT " + strings.Join(cols, ", ") + " FROM " + in)
	return b.cte(`SELECT "__s" AS "__o", ` + strings.Join(colList(info, ""), ", ") + " FROM " + sorted)
}

func (b *builder) slice(f *ast.Slice) string {
	in := b.frame(f.In)
	info := b.info(f.In)
	b.fire(TSlice)
	var lo, hi string
	switch {
	case f.Offset >= 0:
		lo = strconv.Itoa(f.Offset)
		if f.Len >= 0 {
			hi = strconv.Itoa(f.Offset + f.Len)
		}
	default:
		lo = fmt.Sprintf(`"__n" - %d`, -f.Offset)
		if f.Len >= 0 {
			hi = fmt.Sprintf(`"__n" - %d`, -f.Offset-f.Len)
		} else {
			hi = `"__n"`
		}
	}
	where := `"__r" >= ` + lo
	if hi != "" {
		where += ` AND "__r" < ` + hi
	}
	cols := append([]string{`"__o"`}, colList(info, "")...)
	return b.cte("SELECT " + strings.Join(cols, ", ") + ` FROM (SELECT *, row_number() OVER (ORDER BY "__o") - 1 AS "__r", ` +
		`count(*) OVER () AS "__n" FROM ` + in + ") WHERE " + where)
}

func (b *builder) join(f *ast.Join) string {
	l, r := b.frame(f.L), b.frame(f.R)
	li, ri := b.info(f.L), b.info(f.R)
	lk, rk := li.Col(f.LeftOn), ri.Col(f.RightOn)
	kt, ok := ast.JoinKeyType(lk.Type, rk.Type)
	if !ok {
		b.refuse(RJoinKeys, "a join of %s and %s", lk.Type, rk.Type)
	}
	b.fire(TJoinNull)
	lref, rref := "l."+Quote(f.LeftOn), "r."+Quote(f.RightOn)
	var on string
	switch {
	case kt == model.Float64:
		b.fire(TKeyZero)
		on = "u_fkey(" + lref + ") = u_fkey(" + rref + ")"
	case kt.IsSignedInt():
		on = "CAST(" + lref + " AS BIGINT) = CAST(" + rref + " AS BIGINT)"
	default:
		on = lref + " = " + rref
	}
	if f.How == ast.Semi || f.How == ast.Anti {
		if f.How == ast.Anti {
			b.fire(TJoinOrder)
			return b.cte("SELECT l.* FROM " + l + " AS l WHERE NOT EXISTS (SELECT 1 FROM " + r + " AS r WHERE " + on + ")")
		}
		b.fire(TJoinOrder)
		return b.cte("SELECT l.* FROM " + l + " AS l WHERE EXISTS (SELECT 1 FROM " + r + " AS r WHERE " + on + ")")
	}
	b.fire(TJoinShape)
	if f.MaintainOrder {
		b.fire(TJoinOrder)
	}
	coalesce := f.LeftOn == f.RightOn
	out := b.info(f)
	cols := []string{`row_number() OVER (ORDER BY l."__o", r."__o" NULLS FIRST) AS "__o"`}
	var tmp []string
	add := func(e string) {
		t := fmt.Sprintf("__c%d", len(tmp))
		cols = append(cols, e+" AS "+Quote(t))
		tmp = append(tmp, t)
	}
	for _, c := range li.Cols {
		e := "l." + Quote(c.Name)
		if coalesce && c.Name == f.LeftOn && c.Type != kt {
			e = "CAST(" + e + " AS " + Type(kt) + ")"
		}
		add(e)
	}
	for _, c := range ri.Cols {
		if coalesce && c.Name == f.RightOn {
			continue
		}
		add("r." + Quote(c.Name))
	}
	if len(tmp) != len(out.Cols) {
		panic(fmt.Sprintf("sqlgen: a join of %d columns, ast's %d", len(tmp), len(out.Cols)))
	}
	names := make([]string, len(out.Cols))
	for i, c := range out.Cols {
		names[i] = c.Name
	}
	how := " JOIN "
	if f.How == ast.Left {
		how = " LEFT JOIN "
	}
	joined := b.cte("SELECT " + strings.Join(cols, ", ") + " FROM " + l + " AS l" + how + r + " AS r ON " + on)
	return b.rename(joined, tmp, names)
}

// --- expressions ------------------------------------------------------------------

// cast is e's SQL as type t, which e's own type meets t at (T-PROMOTE).
func (b *builder) cast(e ast.Expr, t model.Type) string {
	s := b.expr(e)
	from := e.Type()
	if from == t {
		return s
	}
	switch {
	case from.IsInt() && t.IsInt(), from.IsInt() && t == model.Float64:
		b.fire(TPromote)
		return "CAST(" + s + " AS " + Type(t) + ")"
	case from == model.Float64 && t.IsSignedInt():
		// A weak fill's whole float, which fits t (fill.go).
		b.fire(TFillWeak)
		return "CAST(" + s + " AS " + Type(t) + ")"
	}
	b.refuse(RPromote, "%s does not meet %s", from, t)
	return ""
}

// cmpType is the type a comparison casts both sides to: Promote's for the types
// qcheck compares (expr/resolve.go resolveComparison).
func cmpType(l, r model.Type) model.Type {
	switch {
	case l == r:
		return l
	case l == model.Float64 || r == model.Float64:
		return model.Float64
	case l == model.Int128 || r == model.Int128 || l == model.Uint64 || r == model.Uint64:
		return model.Int128
	case l.IsSignedInt() && r.IsSignedInt():
		t, _ := model.Promote(l, r)
		return t
	}
	return model.Invalid
}

func (b *builder) expr(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Col:
		return Quote(e.Name)
	case *ast.Lit:
		b.fire(TLitType)
		return Lit(e)
	case *ast.Bin:
		return b.bin(e)
	case *ast.Un:
		return b.un(e)
	case *ast.When:
		b.fire(TWhenNull)
		return "(CASE WHEN " + b.expr(e.Cond) + " THEN " + b.cast(e.Then, e.T) + " ELSE " + b.cast(e.Else, e.T) + " END)"
	case *ast.FillNull:
		b.fire(TFillWeak)
		return "COALESCE(" + b.cast(e.X, e.T) + ", " + b.cast(e.With, e.T) + ")"
	case *ast.Cast:
		return b.convert(e)
	case *ast.Agg:
		panic("sqlgen: an aggregate outside a group-by")
	}
	panic(fmt.Sprintf("sqlgen: unknown expression %T", e))
}

// wrapFn is the macro that wraps a HUGEINT to t's width.
func wrapFn(t model.Type) string {
	return fmt.Sprintf("u_w%d", t.Bits())
}

func (b *builder) bin(e *ast.Bin) string {
	lt, rt := e.L.Type(), e.R.Type()
	if (e.Op <= ast.Mod) && (lt == model.Int128 || rt == model.Int128 || lt == model.Uint64 || rt == model.Uint64) {
		b.refuse(RI128Arith, "%s of %s and %s", e.Op, lt, rt)
	}
	switch e.Op {
	case ast.Add, ast.Sub, ast.Mul:
		op := map[ast.BinOp]string{ast.Add: " + ", ast.Sub: " - ", ast.Mul: " * "}[e.Op]
		if e.T == model.Float64 {
			return "(" + b.cast(e.L, e.T) + op + b.cast(e.R, e.T) + ")"
		}
		b.fire(TIntWrap)
		return wrapFn(e.T) + "(CAST(" + b.expr(e.L) + " AS HUGEINT)" + op + "CAST(" + b.expr(e.R) + " AS HUGEINT))"
	case ast.Div:
		b.fire(TDivFloat)
		return "(" + b.cast(e.L, model.Float64) + " / " + b.cast(e.R, model.Float64) + ")"
	case ast.FloorDiv:
		b.fire(TFloor)
		if e.T == model.Float64 {
			return "floor(" + b.cast(e.L, e.T) + " / " + b.cast(e.R, e.T) + ")"
		}
		b.fire(TIntWrap)
		return wrapFn(e.T) + "(u_idiv(" + b.expr(e.L) + ", " + b.expr(e.R) + "))"
	case ast.Mod:
		if e.T == model.Float64 {
			b.fire(TFloatMod)
			return "u_fmod(" + b.cast(e.L, e.T) + ", " + b.cast(e.R, e.T) + ")"
		}
		b.fire(TFloor)
		return "CAST(u_imod(" + b.expr(e.L) + ", " + b.expr(e.R) + ") AS " + Type(e.T) + ")"
	case ast.And:
		b.fire(TKleene)
		return "(" + b.expr(e.L) + " AND " + b.expr(e.R) + ")"
	case ast.Or:
		b.fire(TKleene)
		return "(" + b.expr(e.L) + " OR " + b.expr(e.R) + ")"
	}
	// A comparison, at the type both sides meet at.
	ct := cmpType(lt, rt)
	if ct == model.Invalid {
		b.refuse(RPromote, "a comparison of %s and %s", lt, rt)
	}
	if lt != rt {
		b.fire(TCmpPromote)
	}
	l, r := b.cmpSide(e.L, ct), b.cmpSide(e.R, ct)
	if ct == model.Float64 {
		b.fire(TIEEECmp)
		fn := map[ast.BinOp]string{ast.Eq: "u_feq", ast.Ne: "u_fne", ast.Lt: "u_flt", ast.Le: "u_fle",
			ast.Gt: "u_fgt", ast.Ge: "u_fge"}[e.Op]
		return fn + "(" + l + ", " + r + ")"
	}
	op := map[ast.BinOp]string{ast.Eq: " = ", ast.Ne: " <> ", ast.Lt: " < ", ast.Le: " <= ",
		ast.Gt: " > ", ast.Ge: " >= "}[e.Op]
	return "(" + l + op + r + ")"
}

// cmpSide is a comparison's operand at ct.
func (b *builder) cmpSide(e ast.Expr, ct model.Type) string {
	if e.Type() == ct {
		return b.expr(e)
	}
	return "CAST(" + b.expr(e) + " AS " + Type(ct) + ")"
}

func (b *builder) un(e *ast.Un) string {
	xt := e.X.Type()
	switch e.Op {
	case ast.Not:
		b.fire(TKleene)
		return "(NOT " + b.expr(e.X) + ")"
	case ast.IsNull:
		return "(" + b.expr(e.X) + " IS NULL)"
	case ast.IsNotNull:
		return "(" + b.expr(e.X) + " IS NOT NULL)"
	}
	if xt == model.Int128 || xt == model.Uint64 {
		b.refuse(RI128Arith, "%s of %s", e.Op, xt)
	}
	switch {
	case e.Op == ast.Neg && xt == model.Float64:
		return "(-(" + b.expr(e.X) + "))"
	case e.Op == ast.Abs && xt == model.Float64:
		return "abs(" + b.expr(e.X) + ")"
	case e.Op == ast.Neg:
		b.fire(TIntWrap)
		return wrapFn(xt) + "(-CAST(" + b.expr(e.X) + " AS HUGEINT))"
	case e.Op == ast.Abs:
		b.fire(TIntWrap)
		return wrapFn(xt) + "(abs(CAST(" + b.expr(e.X) + " AS HUGEINT)))"
	}
	panic(fmt.Sprintf("sqlgen: unknown unary %s", e.Op))
}

// floatRange is the half-open range of floats a float-to-integer cast keeps:
// powers of two, exact as doubles (castnum.go floatsToInt).
func floatRange(t model.Type) (lo, hi string) {
	bits := t.Bits()
	lo = fmt.Sprintf("CAST('%s' AS DOUBLE)", strconv.FormatFloat(-math.Ldexp(1, bits-1), 'f', -1, 64))
	hi = fmt.Sprintf("CAST('%s' AS DOUBLE)", strconv.FormatFloat(math.Ldexp(1, bits-1), 'f', -1, 64))
	return lo, hi
}

// intRange is t's least and greatest values, as BIGINT literals.
func intRange(t model.Type) string {
	bits := t.Bits()
	return fmt.Sprintf("CAST('%d' AS BIGINT), CAST('%d' AS BIGINT)", int64(-1)<<(bits-1), int64(1)<<(bits-1)-1)
}

// convert is a Cast or a CastLossy.
func (b *builder) convert(e *ast.Cast) string {
	from, to := e.X.Type(), e.To
	x := b.expr(e.X)
	if from == to {
		return x
	}
	if e.Lossy {
		switch {
		case from == model.String && to.IsSignedInt():
			b.fire(TParseInt)
			return "(CASE WHEN regexp_full_match(" + x + ", '[+-]?[0-9]+') THEN TRY_CAST(" + x + " AS " + Type(to) + ") END)"
		case from == model.String && to == model.Float64:
			b.fire(TParseFloat)
			return "u_pfloat(" + x + ")"
		case from == model.String && to == model.Date:
			b.fire(TParseDate)
			return "u_pdate(" + x + ")"
		case from == model.String && to == model.Bool:
			b.fire(TParseBool)
			return "u_pbool(" + x + ")"
		case from == model.Float64 && to.IsSignedInt():
			b.fire(TCastExact)
			lo, hi := floatRange(to)
			return "CAST(u_f2i(" + x + ", " + lo + ", " + hi + ") AS " + Type(to) + ")"
		case from.IsSignedInt() && to.IsSignedInt():
			b.fire(TCastRange)
			return "CAST(u_irange(" + x + ", " + intRange(to) + ") AS " + Type(to) + ")"
		case from == model.Int64 && to == model.Date:
			b.fire(TCastRange)
			b.fire(TDateDays)
			return "CAST(u_irange(" + x + ", " + intRange(model.Int32) + ") AS INTEGER)"
		}
		b.refuse(RCastStrict, "a lossy cast of %s to %s", from, to)
	}
	switch {
	case from.IsSignedInt() && to.IsSignedInt() && to.Bits() >= from.Bits(), from.IsSignedInt() && to == model.Float64:
		b.fire(TPromote)
		return "CAST(" + x + " AS " + Type(to) + ")"
	case (from.IsSignedInt() || from == model.Bool) && to == model.String:
		b.fire(TText)
		return "CAST(" + x + " AS VARCHAR)"
	case from == model.Bool && (to.IsSignedInt() || to == model.Float64):
		b.fire(TPromote)
		return "CAST(CAST(" + x + " AS INTEGER) AS " + Type(to) + ")"
	case from == model.Float64 && to == model.String:
		b.fire(TFloatText)
		return "u_ftext(" + x + ")"
	case from == model.Date && to == model.String:
		b.fire(TDateText)
		return "u_dtext(" + x + ")"
	case from == model.Date && (to == model.Int32 || to == model.Int64), from == model.Int32 && to == model.Date:
		b.fire(TDateDays)
		return "CAST(" + x + " AS " + Type(to) + ")"
	}
	b.refuse(RCastStrict, "a strict cast of %s to %s, which can fail", from, to)
	return ""
}

// orderedList is x's values, not null, in the rows' order: a float aggregate's
// input (T-FLOAT-AGG).
func orderedList(x string) string {
	return "list(" + x + ` ORDER BY "__o") FILTER (WHERE ` + x + " IS NOT NULL)"
}

func (b *builder) agg(a *ast.Agg) string {
	if a.Op == ast.Len {
		b.fire(TCount)
		return "CAST(count(*) AS UBIGINT)"
	}
	xt := a.X.Type()
	x := b.expr(a.X)
	switch a.Op {
	case ast.Count:
		b.fire(TCount)
		return "CAST(count(" + x + ") AS UBIGINT)"
	case ast.NUnique:
		b.fire(TCount)
		b.fire(TNUnique)
		if xt == model.Float64 {
			b.fire(TKeyZero)
			x = "u_fkey(" + x + ")"
		}
		return "CAST(count(DISTINCT " + x + ") AS UBIGINT)"
	case ast.Sum:
		switch {
		case xt == model.Float64:
			b.fire(TFloatAgg)
			return "u_fsum(" + orderedList(x) + ")"
		case xt == model.Bool:
			b.fire(TSum)
			return "sum(CAST(CAST(" + x + " AS INTEGER) AS HUGEINT))"
		case xt.IsSignedInt():
			b.fire(TSum)
			return "sum(CAST(" + x + " AS HUGEINT))"
		}
	case ast.Mean:
		switch {
		case xt == model.Float64:
			b.fire(TFloatAgg)
			return "u_fmean(" + orderedList(x) + ")"
		case xt.IsSignedInt():
			b.fire(TMeanInt)
			return "u_imean(sum(CAST(" + x + " AS HUGEINT)), count(" + x + "))"
		}
		b.refuse(RMeanBool, "Mean of %s", xt)
	case ast.Min, ast.Max:
		b.fire(TExtremum)
		if xt == model.Float64 {
			fn := map[ast.AggOp]string{ast.Min: "u_fmin", ast.Max: "u_fmax"}[a.Op]
			return fn + "(" + orderedList(x) + ")"
		}
		return map[ast.AggOp]string{ast.Min: "min", ast.Max: "max"}[a.Op] + "(" + x + ")"
	}
	panic(fmt.Sprintf("sqlgen: unknown aggregate %s of %s", a.Op, xt))
}

// --- literals ---------------------------------------------------------------------

// Lit is a literal's SQL, at its AST type (T-LIT-TYPE): an integer from its
// decimal text, a float from its shortest text, which DuckDB reads to its bits, a
// Date as its days, text quoted or, where it holds a byte outside printable
// ASCII, decoded from its hex, as a NUL cannot be in a query's text.
func Lit(l *ast.Lit) string {
	if l.Null {
		return "CAST(NULL AS " + Type(l.T) + ")"
	}
	switch l.T {
	case model.Int8, model.Int16, model.Int32, model.Int64:
		return "CAST('" + strconv.FormatInt(l.V.(int64), 10) + "' AS " + Type(l.T) + ")"
	case model.Float64:
		return Float(l.V.(float64))
	case model.String:
		return String(l.V.(string))
	case model.Bool:
		if l.V.(bool) {
			return "true"
		}
		return "false"
	case model.Date:
		return "CAST('" + strconv.FormatInt(int64(l.V.(int32)), 10) + "' AS INTEGER)"
	}
	panic("sqlgen: no literal of " + l.T.String())
}

// Float is f as a DOUBLE literal of its bits.
func Float(f float64) string {
	switch {
	case math.IsNaN(f):
		return "CAST('nan' AS DOUBLE)"
	case math.IsInf(f, 1):
		return "CAST('inf' AS DOUBLE)"
	case math.IsInf(f, -1):
		return "CAST('-inf' AS DOUBLE)"
	}
	return "CAST('" + strconv.FormatFloat(f, 'g', -1, 64) + "' AS DOUBLE)"
}

// String is s as a VARCHAR literal.
func String(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return "decode(from_hex('" + hex.EncodeToString([]byte(s)) + "'))"
		}
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
