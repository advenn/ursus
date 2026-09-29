package ursus_test

// Parquet pruning against itself, generated.
//
// Pruning skips a row group when its statistics PROVE no row can match, and a
// wrong proof is silent: the Filter above the scan still runs, so a skipped group
// that held matching rows simply loses them. TestParquetPruningIsSound compares a
// hand-picked list of predicates over integers and plain strings, with no nulls,
// no NaN, no nested column, and none of I2–I5 (audit.md §5) was on it.
//
// This derives its predicates from the data instead — every comparison at every
// distinct value of every flat column, and just beyond each end, and at the values
// statistics are worst at (NaN, ±Inf, ±0, "", a string too long for its max to be
// kept) — and runs each with pruning on and off. The fixtures are built to be
// adversarial: an all-null group, an all-NaN group, groups whose min equals their
// max, "" next to a 5000-byte string, and a struct in front of the flat columns.
//
// # It must still prune
//
// "Never prune" passes every comparison, so each class of predicate must also be
// seen SKIPPING a row group, counted by the source itself.

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source/parquet"
	"github.com/advenn/ursus/ursustest"
)

var bigString = strings.Repeat("z", 5000)

// pruneFlatFixture is six row groups of four rows:
//
//	g0  i 1-4          f 0, -0, 1.5, 2       s a-d
//	g1  i all null     f NaN, 1, 1, NaN      s "", <5000 bytes>, e, f
//	g2  i 5 ×4         f +Inf, 3, 4, 5       s g ×4
//	g3  i 6, 7, -, 8   f -Inf, -1, -2, -3    s "" ×4
//	g4  i 9-12         f NaN ×4              s h-k
//	g5  i 13-16        f 6-9                 s all null
func pruneFlatFixture(t *testing.T) string {
	t.Helper()
	nan, inf := math.NaN(), math.Inf(1)
	nz := math.Copysign(0, -1)
	ok := func(n int) []bool {
		v := make([]bool, n)
		for i := range v {
			v[i] = true
		}
		return v
	}
	iValid := ok(24)
	for _, r := range []int{4, 5, 6, 7, 14} {
		iValid[r] = false
	}
	sValid := ok(24)
	for _, r := range []int{20, 21, 22, 23} {
		sValid[r] = false
	}
	ids := make([]int64, 24)
	for i := range ids {
		ids[i] = int64(i)
	}
	lf := ursus.Frame(
		ursus.Values("id", ids),
		ursus.ValuesNullable("i", []int64{1, 2, 3, 4, 0, 0, 0, 0, 5, 5, 5, 5, 6, 7, 0, 8, 9, 10, 11, 12, 13, 14, 15, 16}, iValid),
		ursus.Values("f", []float64{0, nz, 1.5, 2, nan, 1, 1, nan, inf, 3, 4, 5, -inf, -1, -2, -3, nan, nan, nan, nan, 6, 7, 8, 9}),
		ursus.Values("f32", []float32{0.5, 1, 1.5, 2, 1, 1, 1, 1, 2.5, 3, 3.5, 4, -1, -0.5, 0, 0.25, 5, 6, 7, 8, 0.1, 0.2, 0.3, 0.4}),
		ursus.ValuesNullable("s", []string{"a", "b", "c", "d", "", bigString, "e", "f", "g", "g", "g", "g",
			"", "", "", "", "h", "i", "j", "k", "", "", "", ""}, sValid),
	)
	return sinkParquet(t, t.TempDir(), "flat.parquet", lf, ursus.WithRowGroupRows(4))
}

// pruneNestedFixture is three row groups of two rows, with a struct and a list IN
// FRONT of flat columns, so that a top-level index read as a leaf index lands on
// the wrong column. Leaves: st.a 0, st.b 1, c 2, l.element 3, k 4. The ranges are
// disjoint, so reading the wrong leaf's statistics prunes groups that match.
//
//	g0  st {1,100} {2,101}    c 1, 2   l [1000], []          k 20, 21
//	g1  st null {null,102}    c 3, 4   l null, [1001, null]  k 22, 23
//	g2  st {5,103} {6,104}    c 5, 6   l [], []              k 24, 25
func pruneNestedFixture(t *testing.T) string {
	t.Helper()
	fields := schema.FieldList{
		structNode(t, "st", opt, i64Node(t, "a", opt), i64Node(t, "b", opt)),
		i64Node(t, "c", opt),
		listNode(t, "l", opt, i64Node(t, "element", opt)),
		i64Node(t, "k", opt),
	}
	return writeRaw(t, t.TempDir(), "nested.parquet", fields,
		[]rawCol{
			{vals: []int64{1, 2}, defs: []int16{2, 2}}, {vals: []int64{100, 101}, defs: []int16{2, 2}},
			{vals: []int64{1, 2}, defs: []int16{1, 1}},
			{vals: []int64{1000}, defs: []int16{3, 1}, reps: []int16{0, 0}},
			{vals: []int64{20, 21}, defs: []int16{1, 1}},
		},
		[]rawCol{
			{vals: []int64{}, defs: []int16{0, 1}}, {vals: []int64{102}, defs: []int16{0, 2}},
			{vals: []int64{3, 4}, defs: []int16{1, 1}},
			{vals: []int64{1001}, defs: []int16{0, 3, 2}, reps: []int16{0, 0, 1}},
			{vals: []int64{22, 23}, defs: []int16{1, 1}},
		},
		[]rawCol{
			{vals: []int64{5, 6}, defs: []int16{2, 2}}, {vals: []int64{103, 104}, defs: []int16{2, 2}},
			{vals: []int64{5, 6}, defs: []int16{1, 1}},
			{vals: []int64{}, defs: []int16{1, 1}, reps: []int16{0, 0}},
			{vals: []int64{24, 25}, defs: []int16{1, 1}},
		})
}

// prunePred is one generated predicate, and what it is expected to reach.
type prunePred struct {
	name  string
	pred  ursus.Expr
	class string // the defect a mismatch is attributed to; see classifyPrune
	skips string // the class of pruning it exercises, for the anti-vacuity floor
}

// cmpOps is every comparison statistics can prune.
var cmpOps = []struct {
	name string
	of   func(l, r ursus.Expr) ursus.Expr
}{
	{"==", func(l, r ursus.Expr) ursus.Expr { return l.Eq(r) }},
	{"!=", func(l, r ursus.Expr) ursus.Expr { return l.Ne(r) }},
	{"<", func(l, r ursus.Expr) ursus.Expr { return l.Lt(r) }},
	{"<=", func(l, r ursus.Expr) ursus.Expr { return l.Le(r) }},
	{">", func(l, r ursus.Expr) ursus.Expr { return l.Gt(r) }},
	{">=", func(l, r ursus.Expr) ursus.Expr { return l.Ge(r) }},
}

// classifyPrune attributes a comparison to the defect it would show, by fixture,
// column and operator. Counted, like the optimizer differential's classes: a new
// defect that lands in an old class changes that class's count.
func classifyPrune(fixture, col, op string) string {
	switch {
	// l's IsNull read column c's null count — l is the third top-level field and c
	// the third leaf — so it is I2's wrong leaf, not I5's nested counting.
	case fixture == "nested" && (col == "c" || col == "k" || (col == "l" && op == "is_null")):
		return "I2"
	case fixture == "nested" && op == "is_not_null" && (col == "st" || col == "l"):
		return "I5"
	case fixture == "flat" && (col == "f" || col == "f32") && op == "!=":
		return "I3"
	case fixture == "flat" && col == "s" && (op == ">" || op == ">=" || op == "!=" || op == "=="):
		return "I4"
	}
	return ""
}

func renderLit(v any) string {
	switch x := v.(type) {
	case string:
		if len(x) > 12 {
			return fmt.Sprintf("<%d bytes>", len(x))
		}
		return fmt.Sprintf("%q", x)
	case float32:
		return fmt.Sprintf("f32(%v)", x)
	}
	return fmt.Sprint(v)
}

// comparisons builds every comparison of col against each literal, both ways
// round for the first and last.
func comparisons(fixture, col, skips string, lits []any) []prunePred {
	var out []prunePred
	for li, v := range lits {
		for _, op := range cmpOps {
			lit := litOf(v)
			name := fmt.Sprintf("%s: %s %s %s", fixture, col, op.name, renderLit(v))
			out = append(out, prunePred{name: name, pred: op.of(ursus.Col(col), lit),
				class: classifyPrune(fixture, col, op.name), skips: skips})
			if li == 0 || li == len(lits)-1 {
				// The flipped form: the pruner must turn `lit < col` into `col > lit`.
				out = append(out, prunePred{
					name:  fmt.Sprintf("%s: %s %s %s", fixture, renderLit(v), op.name, col),
					pred:  op.of(lit, ursus.Col(col)),
					class: classifyPrune(fixture, col, flipped(op.name)), skips: skips})
			}
		}
	}
	return out
}

// litOf makes a literal of a generated value, keeping its Go type: a float32
// literal and a float64 one are different questions for a Float32 column.
func litOf(v any) ursus.Expr {
	switch x := v.(type) {
	case int64:
		return ursus.Lit(x)
	case float64:
		return ursus.Lit(x)
	case float32:
		return ursus.Lit(x)
	case string:
		return ursus.Lit(x)
	}
	panic(fmt.Sprintf("litOf: %T", v))
}

func flipped(op string) string {
	switch op {
	case "<":
		return ">"
	case "<=":
		return ">="
	case ">":
		return "<"
	case ">=":
		return "<="
	}
	return op
}

// distinctInts, distinctFloats and distinctStrings read a column's distinct
// non-null values from an unpruned read, so the predicates follow the data.
func distinctInts(t *testing.T, df *ursus.DataFrame, col string) []any {
	t.Helper()
	vals := readAll[int64](df, col)
	slices.Sort(vals)
	vals = slices.Compact(vals)
	out := []any{vals[0] - 1}
	for _, v := range vals {
		out = append(out, v)
	}
	return append(out, vals[len(vals)-1]+1)
}

func distinctFloats(t *testing.T, df *ursus.DataFrame, col string, f32 bool) []any {
	t.Helper()
	var vals []float64
	if f32 {
		for _, v := range readAll[float32](df, col) {
			vals = append(vals, float64(v))
		}
	} else {
		vals = readAll[float64](df, col)
	}
	var finite []float64
	for _, v := range vals {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			finite = append(finite, v)
		}
	}
	sort.Float64s(finite)
	finite = slices.Compact(finite)
	cands := append([]float64{math.Nextafter(finite[0], math.Inf(-1))}, finite...)
	cands = append(cands, math.Nextafter(finite[len(finite)-1], math.Inf(1)),
		math.NaN(), math.Inf(1), math.Inf(-1), 0, math.Copysign(0, -1))
	var out []any
	for _, v := range cands {
		out = append(out, v)
		if f32 {
			// A Float32 column is compared with a float32 literal too: the
			// statistics are float32, and the literal must be widened the same way.
			out = append(out, float32(v))
		}
	}
	return out
}

func distinctStrings(t *testing.T, df *ursus.DataFrame, col string) []any {
	t.Helper()
	vals := readAll[string](df, col)
	slices.Sort(vals)
	vals = slices.Compact(vals)
	out := []any{}
	for _, v := range vals {
		out = append(out, v)
	}
	return append(out, "\x00", "a", bigString+"z")
}

// generatePrunePreds reads each fixture once, unpruned, and derives the predicates
// from what is in it.
func generatePrunePreds(t *testing.T, flat, nested string) map[string][]prunePred {
	t.Helper()
	read := func(p string) *ursus.DataFrame {
		df, err := ursus.ScanParquet(p, ursus.WithPruning(false)).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return df
	}
	fdf, ndf := read(flat), read(nested)
	c := ursus.Col

	var fp []prunePred
	fp = append(fp, comparisons("flat", "i", "int", distinctInts(t, fdf, "i"))...)
	fp = append(fp, comparisons("flat", "f", "float64", distinctFloats(t, fdf, "f", false))...)
	fp = append(fp, comparisons("flat", "f32", "float32", distinctFloats(t, fdf, "f32", true))...)
	fp = append(fp, comparisons("flat", "s", "string", distinctStrings(t, fdf, "s"))...)
	for _, col := range []string{"i", "f", "f32", "s"} {
		fp = append(fp,
			prunePred{name: "flat: " + col + " is_null", pred: c(col).IsNull(), skips: "is_null"},
			prunePred{name: "flat: " + col + " is_not_null", pred: c(col).IsNotNull()})
	}
	// Pairs across columns, at each column's median.
	fp = append(fp,
		prunePred{name: "flat: i > 8 and s == g", pred: c("i").Gt(int64(8)).And(c("s").Eq(ursus.Lit("g")))},
		prunePred{name: "flat: i > 8 or s == g", pred: c("i").Gt(int64(8)).Or(c("s").Eq(ursus.Lit("g")))},
		prunePred{name: "flat: f < 0 or i is_null", pred: c("f").Lt(0.0).Or(c("i").IsNull())},
		prunePred{name: "flat: f != 1 and f32 > 1", class: "I3",
			pred: c("f").Ne(1.0).And(c("f32").Gt(float32(1)))},
		prunePred{name: "flat: s > a or i == 5", class: "I4",
			pred: c("s").Gt(ursus.Lit("a")).Or(c("i").Eq(int64(5)))},
	)

	var np []prunePred
	np = append(np, comparisons("nested", "c", "nested-flat", distinctInts(t, ndf, "c"))...)
	np = append(np, comparisons("nested", "k", "nested-flat", distinctInts(t, ndf, "k"))...)
	for _, col := range []string{"st", "c", "l", "k"} {
		np = append(np,
			prunePred{name: "nested: " + col + " is_null", pred: c(col).IsNull(),
				class: classifyPrune("nested", col, "is_null")},
			prunePred{name: "nested: " + col + " is_not_null", pred: c(col).IsNotNull(),
				class: classifyPrune("nested", col, "is_not_null")})
	}
	return map[string][]prunePred{flat: fp, nested: np}
}

// knownPruningMismatches counts, per defect, the generated predicates pruning
// answers differently today. Checked both ways, like the optimizer differential.
var knownPruningMismatches = map[string]knownMismatch{
	// Empty since step 71: I3 and I4 (an empty string max, a float !=) and I2 and
	// I5 (the wrong leaf, and nested columns) each had a class here.
}

func TestParquetPruningAgreesWithoutIt(t *testing.T) {
	start := time.Now()
	flat, nested := pruneFlatFixture(t), pruneNestedFixture(t)
	skipped := map[string]int{}
	var ms []mismatch
	queries := 0

	for path, preds := range generatePrunePreds(t, flat, nested) {
		// The unpruned side is one full read, filtered in memory per predicate: it
		// shares no Parquet read path with the pruned side, so it is the stronger
		// oracle as well as the cheaper one.
		whole, err := ursus.ScanParquet(path, ursus.WithPruning(false)).
			Collect(t.Context(), ursus.WithOptFlags(plan.NoFlags()))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range preds {
			queries++
			src := parquet.New([]parquet.Opener{fileOpener(t, path)}, path, parquet.DefaultOptions())
			on, errOn := ursus.Scan(src).Filter(p.pred).
				Collect(t.Context(), ursus.WithVerify(), ursus.WithThreads(1))
			off, errOff := whole.Filter(t.Context(), p.pred)
			var diff string
			switch {
			case errOn != nil || errOff != nil:
				if (errOn == nil) != (errOff == nil) || kindOf(errOn) != kindOf(errOff) {
					diff = fmt.Sprintf("errors differ: %v vs %v", errOn, errOff)
				}
			default:
				diff = framesDiffer(t, on, off, ursustest.CheckNullability())
			}
			if diff != "" {
				ms = append(ms, mismatch{name: p.name, detail: firstLine(diff), class: p.class})
			}
			if _, s := src.RowGroupStats(); s > 0 && p.skips != "" {
				skipped[p.skips] += s
			}
		}
	}
	t.Logf("%d predicates in %v: %d mismatches", queries, time.Since(start).Round(time.Millisecond), len(ms))

	for _, p := range judge(ms, knownPruningMismatches) {
		t.Error(p)
	}
	if queries < 400 {
		t.Errorf("only %d predicates — the generator has stopped deriving them", queries)
	}
	// Anti-vacuity: every class must actually skip something. A pruner that never
	// prunes, or never prunes floats, agrees with itself everywhere and fails here.
	// Float64 and Float32 are separate classes: with one "float" class, a pruner
	// that stopped pruning Float64 alone still passed, on Float32's skips.
	for _, class := range []string{"int", "float64", "float32", "string", "is_null", "nested-flat"} {
		if skipped[class] == 0 {
			t.Errorf("no %s predicate skipped a row group — pruning has stopped pruning", class)
		}
	}
}
