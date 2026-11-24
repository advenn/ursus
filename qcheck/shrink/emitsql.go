package shrink

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/model"
	"github.com/advenn/ursus/qcheck/sqlgen"
)

// SQLSettings are what package duck sets before anything else (duck.Settings):
// one thread, IEEE floats, the insertion order kept. A test of package duck, which
// only a build with DuckDB has Settings in, holds the two alike.
var SQLSettings = []string{
	"SET threads TO 1",
	"SET ieee_floating_point_ops TO true",
	"SET preserve_insertion_order TO true",
	"SET memory_limit TO '2GB'",
}

// EmitSQL is c as a DuckDB script: duck's settings, sqlgen's macros, each table
// created with its __rid and its rows, a Date as its days as duck.Load registers it,
// and the query's SQL, sqlgen's translation of its bounded tree, as package compare
// runs it.
func EmitSQL(c *Case) (string, error) {
	tables, err := c.TableMap()
	if err != nil {
		return "", err
	}
	bq, _ := ast.Bounded(c.Query)
	sq, err := sqlgen.Translate(bq, tables)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "-- qcheck %s's case of seed %d, shrunk by qcheck shrink (step 514).\n", c.Source, c.Seed)
	fmt.Fprintf(&b, "-- It failed as %v; ursus ran under %v.\n", c.Signature, c.Config)
	if c.Open != "" {
		fmt.Fprintf(&b, "-- Open: %s.\n", c.Open)
	}
	b.WriteString("\n-- duck.Settings\n")
	for _, s := range SQLSettings {
		b.WriteString(s + ";\n")
	}
	b.WriteString("\n-- sqlgen.Macros\n")
	for _, m := range sqlgen.Macros {
		b.WriteString(m + ";\n")
	}
	for _, t := range c.Tables {
		held, err := t.Held()
		if err != nil {
			return "", err
		}
		cols := []string{sqlgen.Quote(sqlgen.RID) + " BIGINT"}
		for _, col := range held.Cols {
			cols = append(cols, sqlgen.Quote(col.Name)+" "+sqlgen.Type(col.Type))
		}
		fmt.Fprintf(&b, "\nCREATE OR REPLACE TABLE %s (%s);\n", sqlgen.Quote(held.Name), strings.Join(cols, ", "))
		for r := range held.Rows {
			vals := []string{strconv.Itoa(r)}
			for _, col := range held.Cols {
				vals = append(vals, sqlValue(col.Type, col.Values[r]))
			}
			fmt.Fprintf(&b, "INSERT INTO %s VALUES (%s);\n", sqlgen.Quote(held.Name), strings.Join(vals, ", "))
		}
	}
	b.WriteString("\n-- The query (sqlgen.Translate of the bounded tree).\n")
	b.WriteString(strings.TrimRight(sq.Text, "; \n") + ";\n")
	return b.String(), nil
}

func sqlValue(t model.Type, v Value) string {
	if v.Null {
		return "NULL"
	}
	if t == model.Date {
		return "CAST('" + strconv.FormatInt(v.I, 10) + "' AS INTEGER)"
	}
	return sqlgen.Lit(newLit(t, v))
}

// EmitPolars is c's query as a Polars snippet, where its operations map, and the
// operations that do not. It is a reading aid, not an oracle: ursus's semantics are
// its own where they differ (feature-gaps §9), integer overflow, a zero divisor and
// an integer Sum's type among them.
func EmitPolars(c *Case) (string, []string, error) {
	p := &polars{}
	var b strings.Builder
	fmt.Fprintf(&b, "# qcheck %s's case of seed %d, shrunk by qcheck shrink (step 514).\n", c.Source, c.Seed)
	fmt.Fprintf(&b, "# It failed as %v. A reading aid: ursus's semantics are its own where they\n", c.Signature)
	b.WriteString("# differ from Polars' (feature-gaps §9).\nimport polars as pl\n\n")
	for _, t := range c.Tables {
		held, err := t.Held()
		if err != nil {
			return "", nil, err
		}
		var cols, schema, dates []string
		for _, col := range held.Cols {
			vals := make([]string, len(col.Values))
			for i, v := range col.Values {
				vals[i] = pyValue(col.Type, v)
			}
			cols = append(cols, fmt.Sprintf("%s: [%s]", pyString(col.Name), strings.Join(vals, ", ")))
			pt := pyType(col.Type)
			if held, cast := p.heldAs(col.Type); held != "" {
				pt = held
				dates = append(dates, fmt.Sprintf("pl.col(%s).cast(%s)", pyString(col.Name), cast))
			}
			schema = append(schema, fmt.Sprintf("%s: %s", pyString(col.Name), pt))
		}
		fmt.Fprintf(&b, "%s = pl.DataFrame({%s}, schema={%s})", held.Name, strings.Join(cols, ", "), strings.Join(schema, ", "))
		if len(dates) > 0 {
			fmt.Fprintf(&b, ".with_columns(%s)", strings.Join(dates, ", "))
		}
		b.WriteString("\n")
	}
	q := p.frame(c.Query.Root)
	if c.Query.Count {
		fmt.Fprintf(&b, "\nprint(%s.select(pl.len()).collect().item())\n", q)
	} else {
		fmt.Fprintf(&b, "\nprint(%s.collect())\n", q)
	}
	return b.String(), p.unmapped, nil
}

type polars struct{ unmapped []string }

func (p *polars) frame(f ast.Frame) string {
	switch f := f.(type) {
	case *ast.Scan:
		return f.Table + ".lazy()"
	case *ast.Filter:
		return p.frame(f.In) + ".filter(" + p.expr(f.Pred) + ")"
	case *ast.Select:
		return p.frame(f.In) + ".select(" + p.named(f.Cols) + ")"
	case *ast.WithColumns:
		// ursus's WithColumns reads the columns the ones before it added, Polars' the
		// input's: one with_columns a column.
		out := p.frame(f.In)
		for _, n := range f.Cols {
			out += ".with_columns(" + p.named([]ast.Named{n}) + ")"
		}
		return out
	case *ast.GroupBy:
		if len(f.Keys) == 0 {
			return p.frame(f.In) + ".select(" + p.named(f.Aggs) + ")"
		}
		mo := ""
		if f.MaintainOrder {
			mo = ", maintain_order=True"
		}
		return p.frame(f.In) + ".group_by(" + p.named(f.Keys) + mo + ").agg(" + p.named(f.Aggs) + ")"
	case *ast.Sort:
		var by, desc, nl []string
		for _, k := range f.Keys {
			by = append(by, pyString(k.Col))
			desc = append(desc, pyBool(k.Desc))
			nl = append(nl, pyBool(k.NullsLast))
		}
		return fmt.Sprintf("%s.sort([%s], descending=[%s], nulls_last=[%s], maintain_order=True)", p.frame(f.In),
			strings.Join(by, ", "), strings.Join(desc, ", "), strings.Join(nl, ", "))
	case *ast.Head:
		return fmt.Sprintf("%s.head(%d)", p.frame(f.In), f.N)
	case *ast.Slice:
		length := "None"
		if f.Len >= 0 {
			length = strconv.Itoa(f.Len)
		}
		return fmt.Sprintf("%s.slice(%d, %s)", p.frame(f.In), f.Offset, length)
	case *ast.Join:
		how := strings.ToLower(f.How.String())
		on := "on=" + pyString(f.LeftOn) + ", coalesce=True"
		if f.LeftOn != f.RightOn {
			on = fmt.Sprintf("left_on=%s, right_on=%s, coalesce=False", pyString(f.LeftOn), pyString(f.RightOn))
		}
		mo := ""
		if f.MaintainOrder {
			mo = `, maintain_order="left"`
		}
		return fmt.Sprintf("%s.join(%s, %s, how=%q%s)", p.frame(f.L), p.frame(f.R), on, how, mo)
	}
	p.unmapped = append(p.unmapped, fmt.Sprintf("%T", f))
	return "None"
}

func (p *polars) named(ns []ast.Named) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = p.expr(n.E)
		if c, ok := n.E.(*ast.Col); !ok || c.Name != n.Name {
			parts[i] += ".alias(" + pyString(n.Name) + ")"
		}
	}
	return strings.Join(parts, ", ")
}

var pyBin = map[ast.BinOp]string{ast.Add: "+", ast.Sub: "-", ast.Mul: "*", ast.Div: "/", ast.FloorDiv: "//",
	ast.Mod: "%", ast.Eq: "==", ast.Ne: "!=", ast.Lt: "<", ast.Le: "<=", ast.Gt: ">", ast.Ge: ">=",
	ast.And: "&", ast.Or: "|"}

var pyAgg = map[ast.AggOp]string{ast.Sum: "sum", ast.Mean: "mean", ast.Min: "min", ast.Max: "max",
	ast.Count: "count", ast.NUnique: "n_unique"}

func (p *polars) expr(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Col:
		return "pl.col(" + pyString(e.Name) + ")"
	case *ast.Lit:
		if e.Null {
			return "pl.lit(None, dtype=" + pyType(e.T) + ")"
		}
		if held, cast := p.heldAs(e.T); held != "" {
			return fmt.Sprintf("pl.lit(%s, dtype=%s).cast(%s)", pyValue(e.T, litValue(e)), held, cast)
		}
		return "pl.lit(" + pyValue(e.T, litValue(e)) + ", dtype=" + pyType(e.T) + ")"
	case *ast.Bin:
		return "(" + p.expr(e.L) + " " + pyBin[e.Op] + " " + p.expr(e.R) + ")"
	case *ast.Un:
		switch e.Op {
		case ast.Not:
			return "~" + p.expr(e.X)
		case ast.Neg:
			return "(-" + p.expr(e.X) + ")"
		case ast.Abs:
			return p.expr(e.X) + ".abs()"
		case ast.IsNull:
			return p.expr(e.X) + ".is_null()"
		case ast.IsNotNull:
			return p.expr(e.X) + ".is_not_null()"
		}
	case *ast.When:
		return "pl.when(" + p.expr(e.Cond) + ").then(" + p.expr(e.Then) + ").otherwise(" + p.expr(e.Else) + ")"
	case *ast.FillNull:
		return p.expr(e.X) + ".fill_null(" + p.expr(e.With) + ")"
	case *ast.Cast:
		if e.X.Type() == model.String && e.To == model.Bool {
			p.unmapped = append(p.unmapped, "CastLossy of text to a Boolean: Polars casts no text to a Boolean")
		}
		strict := ""
		if e.Lossy {
			strict = ", strict=False"
		}
		return p.expr(e.X) + ".cast(" + pyType(e.To) + strict + ")"
	case *ast.Agg:
		if e.Op == ast.Len {
			return "pl.len()"
		}
		return p.expr(e.X) + "." + pyAgg[e.Op] + "()"
	case *ast.Fn:
		return p.fn(e)
	}
	p.unmapped = append(p.unmapped, fmt.Sprintf("%T", e))
	return "None"
}

// heldAs is how a table holds a type Polars reads otherwise, a Date as Int32 days
// and the temporals as Int64 ticks, a Time's in nanoseconds, a Decimal as text, and
// the cast that makes it the type; "" for one Polars reads as it is.
func (p *polars) heldAs(t model.Type) (held, cast string) {
	switch {
	case t == model.Date:
		return "pl.Int32", "pl.Date"
	case t.IsDatetime(), t.IsDuration(), t.IsTime():
		if t.Unit() == model.Sec && !t.IsTime() {
			p.unmapped = append(p.unmapped, t.String()+": Polars has no unit of seconds")
		}
		return "pl.Int64", pyType(t)
	case t.IsDecimal():
		return "pl.String", pyType(t)
	}
	return "", ""
}

// fn is a namespace's function as Polars', its name and arguments.
func (p *polars) fn(e *ast.Fn) string {
	x := p.expr(e.X)
	q := func(i int) string { return pyString(e.S[i]) }
	switch e.Op {
	case ast.StrContains:
		return fmt.Sprintf("%s.str.contains(%s, literal=%s)", x, q(0), pyBool(e.B))
	case ast.StrStartsWith:
		return x + ".str.starts_with(" + q(0) + ")"
	case ast.StrEndsWith:
		return x + ".str.ends_with(" + q(0) + ")"
	case ast.StrReplace:
		return fmt.Sprintf("%s.str.replace(%s, %s, literal=%s)", x, q(0), q(1), pyBool(e.B))
	case ast.StrReplaceAll:
		return fmt.Sprintf("%s.str.replace_all(%s, %s, literal=%s)", x, q(0), q(1), pyBool(e.B))
	case ast.StrLenBytes:
		return x + ".str.len_bytes()"
	case ast.StrLenChars:
		return x + ".str.len_chars()"
	case ast.StrSlice:
		return fmt.Sprintf("%s.str.slice(%d, %d)", x, e.I[0], e.I[1])
	case ast.StrToUpper:
		return x + ".str.to_uppercase()"
	case ast.StrToLower:
		return x + ".str.to_lowercase()"
	case ast.StrStripPrefix:
		return x + ".str.strip_prefix(" + q(0) + ")"
	case ast.StrStripSuffix:
		return x + ".str.strip_suffix(" + q(0) + ")"
	case ast.StrSplitLen:
		return x + ".str.split(" + q(0) + ").list.len()"
	case ast.StrToInteger:
		return x + ".str.to_integer(strict=False)"
	case ast.DtTruncate, ast.DtRound:
		return fmt.Sprintf("%s.dt.%s(\"%dns\")", x, strings.ToLower(strings.TrimPrefix(e.Op.String(), "Dt")), e.I[0])
	case ast.DtOffsetBy:
		return x + ".dt.offset_by(" + q(0) + ")"
	case ast.DtConvertTimeZone:
		return x + ".dt.convert_time_zone(" + q(0) + ")"
	case ast.DtOrdinalDay:
		return x + ".dt.ordinal_day()"
	}
	name := strings.TrimPrefix(e.Op.String(), "Dt")
	var snake []rune
	for i, r := range name {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				snake = append(snake, '_')
			}
			r += 'a' - 'A'
		}
		snake = append(snake, r)
	}
	return x + ".dt." + string(snake) + "()"
}

func pyType(t model.Type) string {
	switch {
	case t.IsDatetime():
		if z := t.Zone(); z != "" {
			return fmt.Sprintf("pl.Datetime(%q, %q)", t.Unit().String(), z)
		}
		return fmt.Sprintf("pl.Datetime(%q)", t.Unit().String())
	case t.IsDuration():
		return fmt.Sprintf("pl.Duration(%q)", t.Unit().String())
	case t.IsTime():
		return "pl.Time"
	case t.IsDecimal():
		return fmt.Sprintf("pl.Decimal(%d, %d)", t.Prec(), t.Scale())
	case t.IsUnsigned():
		return "pl.UInt" + strconv.Itoa(t.Bits())
	case t == model.Float32:
		return "pl.Float32"
	}
	switch t {
	case model.Bool:
		return "pl.Boolean"
	case model.Int8:
		return "pl.Int8"
	case model.Int16:
		return "pl.Int16"
	case model.Int32:
		return "pl.Int32"
	case model.Int64:
		return "pl.Int64"
	case model.Int128:
		return "pl.Int128"
	case model.Uint64:
		return "pl.UInt64"
	case model.Float64:
		return "pl.Float64"
	case model.String:
		return "pl.String"
	case model.Date:
		return "pl.Date"
	}
	return "None"
}

func pyValue(t model.Type, v Value) string {
	if v.Null {
		return "None"
	}
	switch {
	case t.IsUnsigned():
		return strconv.FormatUint(uint64(v.I), 10)
	case t.IsTime():
		return strconv.FormatInt(v.I*t.Unit().Nanos(), 10)
	case t.IsDatetime(), t.IsDuration():
		return strconv.FormatInt(v.I, 10)
	case t.IsDecimal():
		return pyString(ast.DecimalText(decimalOf(v), t.Scale()))
	case t == model.Float32:
		return pyValue(model.Float64, v)
	}
	switch t {
	case model.Int8, model.Int16, model.Int32, model.Int64, model.Date:
		return strconv.FormatInt(v.I, 10)
	case model.Float64:
		switch f := v.F; {
		case math.IsNaN(f):
			return `float("nan")`
		case math.IsInf(f, 1):
			return `float("inf")`
		case math.IsInf(f, -1):
			return `float("-inf")`
		}
		s := strconv.FormatFloat(v.F, 'g', -1, 64)
		if !strings.ContainsAny(s, ".e") {
			s += ".0"
		}
		return s
	case model.String:
		return pyString(v.S)
	case model.Bool:
		return pyBool(v.B)
	}
	return "None"
}

// pyString is s as a Python string literal: Go's ASCII quoting, whose escapes
// Python reads alike for UTF-8 text.
func pyString(s string) string { return strconv.QuoteToASCII(s) }

func pyBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}
