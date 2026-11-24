package shrink

import (
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"strconv"
	"strings"

	goast "go/ast"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/build"
	"github.com/advenn/ursus/qcheck/model"
)

// TestName is the name of c's emitted test.
func TestName(c *Case) string {
	src := strings.ToUpper(c.Source[:1]) + c.Source[1:]
	return fmt.Sprintf("TestQcheck%s%d", src, c.Seed)
}

// EmitGo is c as a Go test in package ursus_test, one file: its tables built from
// their rows, its query as Query.Go writes it, run under the reference
// configuration and the failing one, and the two answers held to agree, as
// ursustest.AssertFrameEqual compares them, in order where the query promises one,
// as multisets where it does not. It is a differential: the expected answer is the
// reference's, never a typed table. A rerun runs the failing configuration twice; a
// schema finding compares CollectSchema with the AST's types; an allocation or a
// leak is bounded as fuzzcheck bounds it; and an internal error fails it under
// either configuration. A DuckDB case's test is the same differential, DuckDB's
// answer in its comment, as the laptop and the root's tests link no DuckDB.
//
// A noisy column, a float Sum or Mean of a group-by on several threads, is dropped
// before the answers are compared where the thread counts differ: its last bits
// move with them (feature-gaps §1). A finding in a noisy column is not emitted.
func EmitGo(c *Case, detail string) (string, error) {
	tables, err := c.TableMap()
	if err != nil {
		return "", err
	}
	info, err := ast.Schema(c.Query.Root, tables)
	if err != nil {
		return "", err
	}
	_, bounds := ast.Bounded(c.Query)
	var noisy []string
	for _, col := range info.Cols {
		if col.Noisy {
			noisy = append(noisy, col.Name)
		}
	}
	ref := c.Reference
	if c.Mode != ModeDifferential {
		ref = model.Reference()
	}
	threadsDiffer := c.Mode != ModeRerun && c.Mode != ModeSchema && ref.Threads != c.Config.Threads
	if threadsDiffer && c.Signature.What == "cell" {
		for name := range bounds {
			if producerIsNoisy(c, info, name) {
				return "", fmt.Errorf("the finding is in a noisy column, whose bound a Go test does not hold")
			}
		}
	}

	var b strings.Builder
	name := TestName(c)
	fmt.Fprintf(&b, "package ursus_test\n\nimport (\n")
	for _, imp := range allImports {
		fmt.Fprintf(&b, "\t%q\n", imp)
	}
	b.WriteString(")\n\n")
	writeDoc(&b, c, name, detail)
	fmt.Fprintf(&b, "func %s(t *testing.T) {\n", name)
	b.WriteString("\tctx := t.Context()\n")

	// The tables.
	var recNames []string
	for _, t := range c.Tables {
		held, err := t.Held()
		if err != nil {
			return "", err
		}
		rn := t.Name + "rec"
		recNames = append(recNames, rn)
		writeTable(&b, held, rn)
	}

	// The query, over the tables' records as the closure's arguments.
	src := c.Query.Go()
	qsrc := src[:strings.LastIndex(src, "\n")]
	params := make([]string, len(c.Tables))
	for i, t := range c.Tables {
		params[i] = t.Name
	}
	result := "*ursus.DataFrame"
	call := "q.Collect(ctx, opts...)"
	if c.Query.Count {
		result, call = "int64", "q.Count(ctx, opts...)"
	}
	if c.Mode != ModeSchema {
		fmt.Fprintf(&b, "\trun := func(%s []arrow.RecordBatch, opts ...ursus.CollectOption) (%s, error) {\n",
			strings.Join(params, ", "), result)
		b.WriteString(indentLines(qsrc, "\t\t") + "\n")
		fmt.Fprintf(&b, "\t\treturn %s\n\t}\n", call)
	}

	ms, err := c.Models()
	if err != nil {
		return "", err
	}
	args := func(cfg model.Config) string {
		var parts []string
		for i, m := range ms {
			parts = append(parts, recordsArg(m, recNames[i], cfg.Records))
		}
		return strings.Join(append(parts, optionsSrc(cfg)), ", ")
	}
	internal := "\tfor _, e := range []error{%s} {\n\t\tif errors.Is(e, ursus.ErrInternal) {\n" +
		"\t\t\tt.Fatalf(\"an internal error: %%v\", e)\n\t\t}\n\t}\n"
	compareFrames := func(want, got string, ordered bool) {
		if !c.Query.Count && threadsDiffer && len(noisy) > 0 {
			fmt.Fprintf(&b, "\t// A noisy column's last bits move with the thread count (feature-gaps §1): %s, not compared.\n",
				strings.Join(quoteAll(noisy), ", "))
			for _, df := range []string{want, got} {
				fmt.Fprintf(&b, "\tif %s, err = %s.Drop(ctx, %s); err != nil {\n\t\tt.Fatal(err)\n\t}\n", df, df,
					strings.Join(quoteAll(noisy), ", "))
			}
		}
		switch {
		case c.Query.Count:
			fmt.Fprintf(&b, "\tif %s != %s {\n\t\tt.Fatalf(\"Count %%d, the reference's %%d\", %s, %s)\n\t}\n", got, want, got, want)
		case ordered:
			fmt.Fprintf(&b, "\tursustest.AssertFrameEqual(t, %s, %s)\n", got, want)
		default:
			fmt.Fprintf(&b, "\tursustest.AssertFrameEqual(t, %s, %s, ursustest.IgnoreRowOrder())\n", got, want)
		}
	}

	switch c.Mode {
	case ModeSchema:
		fmt.Fprintf(&b, "\tframe := func(%s []arrow.RecordBatch) *ursus.LazyFrame {\n", strings.Join(params, ", "))
		b.WriteString(indentLines(qsrc, "\t\t") + "\n\t\treturn q\n\t}\n")
		var recs []string
		for _, rn := range recNames {
			recs = append(recs, "[]arrow.RecordBatch{"+rn+"}")
		}
		fmt.Fprintf(&b, "\ts, err := frame(%s).CollectSchema(ctx)\n\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n",
			strings.Join(recs, ", "))
		b.WriteString("\t// The AST's types, qcheck's rules for what it generates (step 512 §3).\n\twant := []string{")
		for i, col := range info.Cols {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strconv.Quote(col.Name + " " + build.DType(col.Type).String()))
		}
		b.WriteString("}\n\tvar got []string\n\tfor i := range s.Len() {\n" +
			"\t\tgot = append(got, s.Field(i).Name+\" \"+s.Field(i).Type.String())\n\t}\n" +
			"\tif !slices.Equal(got, want) {\n\t\tt.Fatalf(\"schema %v, the AST's %v\", got, want)\n\t}\n")
	case ModeRerun:
		fmt.Fprintf(&b, "\tfirst, firstErr := run(%s)\n", args(c.Config))
		fmt.Fprintf(&b, "\tsecond, secondErr := run(%s)\n", args(c.Config))
		fmt.Fprintf(&b, internal, "firstErr, secondErr")
		b.WriteString("\tif (firstErr == nil) != (secondErr == nil) {\n\t\tt.Fatalf(\"%v, then %v\", firstErr, secondErr)\n\t}\n")
		b.WriteString("\tif firstErr != nil {\n\t\treturn\n\t}\n")
		compareFrames("first", "second", true)
	default:
		alloc := c.Signature.Kind == KindAlloc
		leak := c.Signature.Kind == KindLeak
		fmt.Fprintf(&b, "\twant, wantErr := run(%s)\n", args(ref))
		if alloc {
			b.WriteString("\tvar before, after runtime.MemStats\n\truntime.ReadMemStats(&before)\n")
		}
		if leak {
			b.WriteString("\tbase := runtime.NumGoroutine()\n")
		}
		fmt.Fprintf(&b, "\tgot, gotErr := run(%s)\n", args(c.Config))
		if alloc {
			b.WriteString("\truntime.ReadMemStats(&after)\n" +
				"\t// fuzzcheck's bound: an allocation far past what the query needs.\n" +
				"\tif n := after.TotalAlloc - before.TotalAlloc; n > 1<<30 {\n" +
				"\t\tt.Errorf(\"allocated %.1f MiB, past 1 GiB\", float64(n)/(1<<20))\n\t}\n")
		}
		if leak {
			b.WriteString("\tfor i := 0; runtime.NumGoroutine() > base && i < 200; i++ {\n" +
				"\t\ttime.Sleep(5 * time.Millisecond)\n\t}\n" +
				"\tif n := runtime.NumGoroutine() - base; n > 0 {\n" +
				"\t\tt.Errorf(\"%d goroutine(s) left behind\", n)\n\t}\n")
		}
		fmt.Fprintf(&b, internal, "wantErr, gotErr")
		fmt.Fprintf(&b, "\tif (wantErr == nil) != (gotErr == nil) {\n\t\tt.Fatalf(\"the reference: %%v; under %s: %%v\", wantErr, gotErr)\n\t}\n",
			strings.ReplaceAll(c.Config.String(), "%", "%%"))
		b.WriteString("\tif wantErr != nil {\n\t\treturn\n\t}\n")
		compareFrames("want", "got", info.Ordered)
	}
	b.WriteString("}\n")
	return tidy(b.String())
}

// producerIsNoisy is whether the case's differing column is a noisy one.
func producerIsNoisy(c *Case, info *ast.Info, name string) bool {
	col := info.Col(name)
	return col != nil && col.Noisy && (c.Signature.Op == "Sum" || c.Signature.Op == "Mean") &&
		c.Signature.Type == model.Float64.String()
}

var allImports = []string{
	"errors", "math", "runtime", "slices", "testing", "time",
	"github.com/apache/arrow-go/v18/arrow", "github.com/apache/arrow-go/v18/arrow/array",
	"github.com/apache/arrow-go/v18/arrow/decimal128", "github.com/apache/arrow-go/v18/arrow/memory",
	"github.com/advenn/ursus", "github.com/advenn/ursus/dtype", "github.com/advenn/ursus/ursustest",
}

func writeDoc(b *strings.Builder, c *Case, name, detail string) {
	var lines []string
	from := "seed " + strconv.FormatUint(c.Seed, 10)
	lines = append(lines, fmt.Sprintf("%s is qcheck %s's case of %s, shrunk by qcheck shrink (step 514).", name, c.Source, from))
	if c.Shrunk != nil {
		lines = append(lines, fmt.Sprintf("From %v to %v.", c.Shrunk.From, c.Shrunk.To))
	}
	lines = append(lines, fmt.Sprintf("It failed as %v.", c.Signature))
	if c.Open != "" {
		lines = append(lines, "Open: "+c.Open+".")
	}
	switch c.Mode {
	case ModeDifferential, ModeRun:
		lines = append(lines, fmt.Sprintf("A differential: the reference, %v, against %v.", c.Reference, c.Config))
	case ModeDuck:
		lines = append(lines, fmt.Sprintf("A DuckDB finding, ursus under %v; its test is the differential against "+
			"the reference, and DuckDB's answer is below.", c.Config))
	case ModeRerun:
		lines = append(lines, fmt.Sprintf("Two runs under %v, which must agree.", c.Config))
	}
	for _, l := range lines {
		b.WriteString(wrapComment(l))
	}
	if detail != "" {
		b.WriteString("//\n")
		for _, l := range strings.Split(strings.TrimRight(detail, "\n"), "\n") {
			if len(l) > 200 {
				l = l[:200] + "…"
			}
			b.WriteString("//\t" + l + "\n")
		}
	}
}

// wrapComment is s as // lines of at most 80 columns.
func wrapComment(s string) string {
	var b strings.Builder
	line := "//"
	for _, w := range strings.Fields(s) {
		if len(line)+1+len(w) > 80 && line != "//" {
			b.WriteString(line + "\n")
			line = "//"
		}
		line += " " + w
	}
	b.WriteString(line + "\n")
	return b.String()
}

func indentLines(s, by string) string {
	return by + strings.ReplaceAll(s, "\n", "\n"+by)
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strconv.Quote(s)
	}
	return out
}

// writeTable writes the statements that build t's record as rn.
func writeTable(b *strings.Builder, t *Table, rn string) {
	fmt.Fprintf(b, "\t%s := func() arrow.RecordBatch {\n", rn)
	b.WriteString("\t\tb := array.NewRecordBuilder(memory.NewGoAllocator(), arrow.NewSchema([]arrow.Field{\n")
	for _, c := range t.Cols {
		fmt.Fprintf(b, "\t\t\t{Name: %q, Type: %s, Nullable: true},\n", c.Name, arrowTypeSrc(c.Type))
	}
	b.WriteString("\t\t}, nil))\n\t\tdefer b.Release()\n")
	for i, c := range t.Cols {
		if len(c.Values) == 0 {
			continue
		}
		builder, elem := builderSrc(c.Type)
		vals := make([]string, len(c.Values))
		valid := make([]string, len(c.Values))
		nulls := false
		for r, v := range c.Values {
			valid[r] = strconv.FormatBool(!v.Null)
			if v.Null {
				nulls = true
				v = Value{}
			}
			vals[r] = valueSrc(c.Type, v)
		}
		validSrc := "nil"
		if nulls {
			validSrc = "[]bool{" + strings.Join(valid, ", ") + "}"
		}
		fmt.Fprintf(b, "\t\tb.Field(%d).(*array.%s).AppendValues([]%s{%s}, %s)\n", i, builder, elem,
			strings.Join(vals, ", "), validSrc)
	}
	b.WriteString("\t\treturn b.NewRecordBatch()\n\t}()\n")
	fmt.Fprintf(b, "\tdefer %s.Release()\n", rn)
}

func arrowTypeSrc(t model.Type) string {
	switch {
	case t.IsDatetime():
		return fmt.Sprintf("&arrow.TimestampType{Unit: %s, TimeZone: %q}", arrowUnitSrc(t.Unit()), t.Zone())
	case t.IsDuration():
		return fmt.Sprintf("&arrow.DurationType{Unit: %s}", arrowUnitSrc(t.Unit()))
	case t.IsTime() && t.Unit() <= model.Milli:
		return fmt.Sprintf("&arrow.Time32Type{Unit: %s}", arrowUnitSrc(t.Unit()))
	case t.IsTime():
		return fmt.Sprintf("&arrow.Time64Type{Unit: %s}", arrowUnitSrc(t.Unit()))
	case t.IsDecimal():
		return fmt.Sprintf("&arrow.Decimal128Type{Precision: %d, Scale: %d}", t.Prec(), t.Scale())
	}
	switch t {
	case model.Int64, model.Int32, model.Int8, model.Int16, model.Uint8, model.Uint16, model.Uint32, model.Uint64,
		model.Float32, model.Float64:
		return "arrow.PrimitiveTypes." + t.String()
	case model.String:
		return "arrow.BinaryTypes.String"
	case model.Bool:
		return "arrow.FixedWidthTypes.Boolean"
	case model.Date:
		return "arrow.FixedWidthTypes.Date32"
	}
	panic("shrink: no table column of " + t.String())
}

func arrowUnitSrc(u model.Unit) string {
	return [...]string{"arrow.Second", "arrow.Millisecond", "arrow.Microsecond", "arrow.Nanosecond"}[u]
}

func builderSrc(t model.Type) (builder, elem string) {
	switch {
	case t.IsDatetime():
		return "TimestampBuilder", "arrow.Timestamp"
	case t.IsDuration():
		return "DurationBuilder", "arrow.Duration"
	case t.IsTime() && t.Unit() <= model.Milli:
		return "Time32Builder", "arrow.Time32"
	case t.IsTime():
		return "Time64Builder", "arrow.Time64"
	case t.IsDecimal():
		return "Decimal128Builder", "decimal128.Num"
	}
	switch t {
	case model.Int64, model.Int32, model.Int8, model.Int16, model.Uint8, model.Uint16, model.Uint32, model.Uint64,
		model.Float32, model.Float64:
		return t.String() + "Builder", strings.ToLower(t.String())
	case model.String:
		return "StringBuilder", "string"
	case model.Bool:
		return "BooleanBuilder", "bool"
	case model.Date:
		return "Date32Builder", "arrow.Date32"
	}
	panic("shrink: no table column of " + t.String())
}

func valueSrc(t model.Type, v Value) string {
	switch {
	case t.IsUnsigned():
		return strconv.FormatUint(uint64(v.I), 10)
	case t.IsDatetime(), t.IsDuration(), t.IsTime():
		return strconv.FormatInt(v.I, 10)
	case t == model.Float32:
		return ast.GoFloat32(float32(v.F))
	case t.IsDecimal():
		return decimalSrc(v)
	}
	switch t {
	case model.Int64, model.Int32, model.Int8, model.Int16, model.Date:
		return strconv.FormatInt(v.I, 10)
	case model.Float64:
		return ast.GoFloat(v.F)
	case model.String:
		return strconv.Quote(v.S)
	case model.Bool:
		return strconv.FormatBool(v.B)
	}
	panic("shrink: no value of " + t.String())
}

// recordsArg is the records a configuration reads table m in: its record, or the
// slices build.Records cuts it into.
func recordsArg(m *model.Table, rn string, k int) string {
	recs := build.Records(m, k)
	if len(recs) == 1 {
		return "[]arrow.RecordBatch{" + rn + "}"
	}
	parts := make([]string, len(recs))
	at := int64(0)
	for i, r := range recs {
		parts[i] = fmt.Sprintf("%s.NewSlice(%d, %d)", rn, at, at+r.NumRows())
		at += r.NumRows()
	}
	return "[]arrow.RecordBatch{" + strings.Join(parts, ", ") + "}"
}

// optionsSrc is cfg's CollectOptions as Go, as build.Options makes them.
func optionsSrc(cfg model.Config) string {
	parts := []string{fmt.Sprintf("ursus.WithThreads(%d)", cfg.Threads), fmt.Sprintf("ursus.WithBatchSize(%d)", cfg.Batch),
		"ursus.WithMemoryLimit(0)"}
	if cfg.OptOff {
		parts = append(parts, "ursus.WithOptFlags(ursus.NoOptFlags())")
	}
	if cfg.Verify {
		parts = append(parts, "ursus.WithVerify()")
	}
	return strings.Join(parts, ", ")
}

// tidy keeps the imports the source uses, declares err where a noisy column is
// dropped, and formats it.
func tidy(src string) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "emitted.go", src, parser.ParseComments)
	if err != nil {
		return "", fmt.Errorf("the emitted test does not parse: %w\n%s", err, src)
	}
	used := map[string]bool{}
	goast.Inspect(f, func(n goast.Node) bool {
		if sel, ok := n.(*goast.SelectorExpr); ok {
			if id, ok := sel.X.(*goast.Ident); ok {
				used[id.Name] = true
			}
		}
		return true
	})
	var keep []string
	for _, imp := range allImports {
		if used[imp[strings.LastIndex(imp, "/")+1:]] {
			keep = append(keep, imp)
		}
	}
	start := strings.Index(src, "import (\n")
	end := start + strings.Index(src[start:], ")\n")
	var block strings.Builder
	block.WriteString("import (\n")
	group := ""
	for i, imp := range keep {
		g := "std"
		switch {
		case strings.HasPrefix(imp, "github.com/advenn/"):
			g = "ursus"
		case strings.Contains(imp, "."):
			g = "others"
		}
		if i > 0 && g != group {
			block.WriteString("\n")
		}
		group = g
		fmt.Fprintf(&block, "\t%q\n", imp)
	}
	src = src[:start] + block.String() + src[end:]
	if strings.Contains(src, ".Drop(ctx,") {
		src = strings.Replace(src, "\tctx := t.Context()\n", "\tctx := t.Context()\n\tvar err error\n", 1)
	}
	out, err := format.Source([]byte(src))
	if err != nil {
		return "", fmt.Errorf("the emitted test does not format: %w\n%s", err, src)
	}
	return string(out), nil
}
