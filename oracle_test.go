package ursus_test

// The recorded oracle (step 496, context_files/oracle-v2-scope.md §2): Polars 2.1.0's
// answers to ursus's questions, as data. bench/scripts/oracle/<group>.py asks Polars
// each question of a group and writes testdata/oracle/<group>.json: the input frames,
// the answer frame or the refusal, and how to compare. oracle_<group>_test.go
// registers ursus's spelling of each question in oracleGroups, and TestOracle runs
// every case of every group: it builds the inputs through the public constructors,
// asks ursus, builds the recorded answer the same way, and compares the two frames
// with ursustest.AssertFrameEqual, dtypes checked and floats by their bits (NaN equal
// to NaN, -0 apart from 0) unless the case says why a tolerance.
//
// A case whose answer ursus gives on purpose otherwise is named in oracleDifferences
// with its reason, and the test asserts ursus's documented answer instead.
//
// The format's types are spelled as ursus prints them (dtype.DataType.String()); the
// values are JSON: integers as numbers, an Int128 and a Decimal's unscaled integer as
// strings, floats as numbers or "NaN", "inf", "-inf", "-0.0", a Date as days, a
// Datetime, Duration or Time as ticks of its unit, Binary as base64, a Categorical or
// Enum as its text, a List or Array as a list, a Struct as an object by field name.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/ursustest"
)

// oraclePolars is the version every recorded answer is Polars'.
const oraclePolars = "2.1.0"

type oracleFile struct {
	Polars    string       `json:"polars"`
	Generator string       `json:"generator"`
	Cases     []oracleCase `json:"cases"`
}

type oracleCase struct {
	Name   string                 `json:"name"`
	Inputs map[string]oracleFrame `json:"inputs"`
	Params map[string]any         `json:"params"`
	Want   oracleWant             `json:"want"`
	Order  string                 `json:"order"` // "rows" or "set"
	Float  string                 `json:"float"` // "bits" or "rel:1e-9"
	Note   string                 `json:"note"`
}

type oracleFrame struct {
	Columns []oracleColumn `json:"columns"`
}

type oracleColumn struct {
	Name   string `json:"name"`
	DType  string `json:"dtype"`
	Values []any  `json:"values"`
}

type oracleWant struct {
	Columns []oracleColumn `json:"columns"`
	Error   *struct {
		Polars   string `json:"polars"`   // Polars' exception, its class and first line
		Contains string `json:"contains"` // what ursus's refusal must name, if anything
	} `json:"error"`
}

// oracleInputs is a case's input frames by name, df the usual one, and its params,
// an input that is not a frame (step 498: a CSV's text).
type oracleInputs struct {
	t      *testing.T
	frames map[string]*ursus.LazyFrame
	params map[string]any
}

func (in oracleInputs) df() *ursus.LazyFrame { return in.frame("df") }

// text is the case's param name, a string.
func (in oracleInputs) text(name string) string {
	v, ok := in.params[name].(string)
	if !ok {
		in.t.Fatalf("the case has no text param %q", name)
	}
	return v
}

func (in oracleInputs) frame(name string) *ursus.LazyFrame {
	f, ok := in.frames[name]
	if !ok {
		in.t.Fatalf("the case has no input %q", name)
	}
	return f
}

// eager is a question answered eagerly (Describe), as the registry's lazy frame.
func (in oracleInputs) eager(df *ursus.DataFrame, err error) *ursus.LazyFrame {
	if err != nil {
		in.t.Fatal(err)
	}
	return df.Lazy()
}

// oracleQuery is ursus's spelling of one recorded question.
type oracleQuery = func(in oracleInputs) *ursus.LazyFrame

// oracleFinding is the registry's entry for a case recorded and not asserted: ursus
// answers otherwise than Polars 2.1.0, the difference is documented nowhere, and it is
// the maintainer's to decide (a wave's record lists it under Found). The test skips
// it, naming the finding.
func oracleFinding(what string) oracleQuery {
	return func(in oracleInputs) *ursus.LazyFrame {
		in.t.Skip("a finding, not asserted: " + what)
		return nil
	}
}

// oracleGroups is the registry: a group's name, as its JSON file is named, to its
// cases' names and ursus's spelling of each. Each oracle_<group>_test.go fills its
// own entry in an init, so groups never edit one file.
var oracleGroups = map[string]map[string]oracleQuery{}

// oracleDifference is ursus's documented answer where it differs from Polars' on
// purpose: cells names the column and row of each value ursus gives instead, in the
// format's own spelling (a float as a float64 or "NaN", an integer as an int, nil a
// null); dtypes names a column whose type ursus gives otherwise; names a column ursus
// names otherwise, the recorded name to ursus's. why cites the doc.
type oracleDifference struct {
	why    string
	cells  map[string]map[int]any
	dtypes map[string]string
	names  map[string]string
}

// oracleDifferences are the cases, as group/name, where ursus answers otherwise than
// Polars on purpose. A difference found and not meant is fixed, not listed.
var oracleDifferences = map[string]oracleDifference{
	// A window by time holding only nulls: Polars sums it to 0.0, although its
	// min_samples of 1 asks for a value, as it answers an empty window under
	// closed="left"; ursus answers null (RollingSumBy's doc; step 184).
	"expressions/rolling_sum_by_1d": {
		why:   "a window by time of only nulls is null here, 0.0 in Polars",
		cells: map[string]map[int]any{"r": {2: nil}},
	},
	// A Datetime's components are each an Int32 here (expr_dt.go), where Polars' hour
	// is an Int8.
	"expressions/convert_tokyo_hour": {
		why:    "dt.hour is an Int32 here, as every component is; Polars' is an Int8",
		dtypes: map[string]string{"r": "Int32"},
	},
	// A count is a Uint64 here, so that it cannot wrap at 2^32 rows where Sum was
	// widened to 128 bits not to (internal/expr.ResolveAggBinding); Polars' is a
	// UInt32. A frame's ValueCounts counts with Len.
	"expressions/value_counts": {
		why:    "a count is a Uint64 here, Polars' a UInt32",
		dtypes: map[string]string{"count": "Uint64"},
	},
	// A centred rank whose window's last row is null: Polars answers null at the
	// row, ursus the row's own rank among the window's values (step 284).
	"rollmore/rank_min_3_center_null_after": {
		why:   "a centred window ending in a null ranks its value here, where Polars answers null",
		cells: map[string]map[int]any{"x": {1: 2}},
	},

	// --- step 497, expressions and math ---------------------------------------------
	// Len is a count, a Uint64 here (value_counts above); Polars' is a UInt32.
	"scalar_byhand/gbd_kolkata_1h":    {why: "Len is a Uint64 here, Polars' a UInt32", dtypes: map[string]string{"n": "Uint64"}},
	"scalar_byhand/gbd_ny_spring_gen": {why: "Len is a Uint64 here, Polars' a UInt32", dtypes: map[string]string{"n": "Uint64"}},
	// ursus's casts are exact or refused, and null when lossy (feature-gaps §9): a
	// float past a Float32's range, whose nearest float32 is an infinity, and a float
	// with a fraction cast to an integer, which Polars overflows and truncates.
	"castround_byhand/f64_1e39_to_f32_lossy": {
		why:   "a float past a Float32's range is null when lossy here; Polars' cast overflows to an infinity",
		cells: map[string]map[int]any{"x": {0: nil}},
	},
	"int128exact/float_to_i128_lossy": {
		why:   "a float with a fraction is null when cast lossily to an integer here; Polars truncates it to 3",
		cells: map[string]map[int]any{"f": {0: nil}},
	},
	// An integer Sum is an Int128 here (internal/expr.ResolveAggBinding; feature-gaps
	// §9), Polars' an Int64, a Boolean's a UInt32; abs, neg, sign and a fold over sums
	// keep the width.
	"math/sum_abs_neg_sign_sqrt": {
		why:    "an integer Sum is an Int128 here, Polars' an Int64; abs, neg and sign keep it",
		dtypes: map[string]string{"abs": "Int128", "neg": "Int128", "sign": "Int128"},
	},
	"fold/fold_agg": {why: "a fold over integer Sums is an Int128 here, Polars' an Int64", dtypes: map[string]string{"literal": "Int128"}},
	"exprargs/lists_agg": {
		why:    "a Boolean's Sum is an Int128 here, as every integer Sum is; Polars counts to a UInt32",
		dtypes: map[string]string{"n_in": "Int128", "n_has": "Int128"},
	},
	"exprargs/lists_agg_gen": {
		why:    "a Boolean's Sum is an Int128 here, as every integer Sum is; Polars counts to a UInt32",
		dtypes: map[string]string{"n_in": "Int128", "n_has": "Int128"},
	},
	// SumHorizontal widens an integer sum to Int128 and answers null for a row of
	// nulls, as Sum does of an all-null group (its doc; feature-gaps §9); Polars'
	// sum_horizontal keeps the operand's type and sums the row to 0.
	"variadic/sum_horizontal_cols":      {why: "SumHorizontal of integers is an Int128 here, Polars' an Int64", dtypes: map[string]string{"a": "Int128"}},
	"variadic/all_minus_sum_horizontal": {why: "SumHorizontal of integers is an Int128 here, Polars' an Int64", dtypes: map[string]string{"a": "Int128", "b": "Int128"}},
	"variadic/horizontal_gen":           {why: "SumHorizontal of integers is an Int128 here, Polars' an Int64", dtypes: map[string]string{"sum": "Int128"}},
	"variadic/eval_sum_horizontal":      {why: "SumHorizontal of integers is an Int128 here, Polars' an Int64", dtypes: map[string]string{"x": "Int128"}},
	"variadic/eval_gen":                 {why: "SumHorizontal of integers is an Int128 here, Polars' an Int64", dtypes: map[string]string{"sum": "List(Int128)"}},
	"variadic/cumulative_eval_max_horizontal": {
		why:    "a Sum inside the body is an Int128 here, and MaxHorizontal keeps it; Polars' is an Int64",
		dtypes: map[string]string{"x": "Int128"},
	},
	"variadic/sum_horizontal_all_null": {
		why:    "SumHorizontal of a row of nulls is null here, as Sum's of an all-null group; Polars sums it to 0",
		dtypes: map[string]string{"a": "Int128"},
		cells:  map[string]map[int]any{"a": {0: nil}},
	},
	// Div and Pow always return a float (feature-gaps §9); Polars' integer power is
	// an Int64.
	"math/pow": {why: "Pow always returns a float here; Polars' integer square is an Int64", dtypes: map[string]string{"sq": "Float64"}},
}

// readOracleGroup decodes one group's file; numbers stay text, so 1<<62 and every
// float's digits survive.
func readOracleGroup(t *testing.T, path string) oracleFile {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var f oracleFile
	if err := d.Decode(&f); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return f
}

// TestOracle runs every recorded case of every group as the subtest group/name.
func TestOracle(t *testing.T) {
	paths, err := filepath.Glob("testdata/oracle/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no recorded groups: %v", err)
	}
	recorded := map[string]bool{}
	for _, path := range paths {
		group := strings.TrimSuffix(filepath.Base(path), ".json")
		recorded[group] = true
		f := readOracleGroup(t, path)
		if f.Polars != oraclePolars {
			t.Errorf("%s is recorded under Polars %s, not %s", path, f.Polars, oraclePolars)
		}
		reg, ok := oracleGroups[group]
		if !ok {
			t.Errorf("%s is recorded and no oracle_%s_test.go registers the group", path, group)
			continue
		}
		asked := map[string]bool{}
		for _, tc := range f.Cases {
			asked[tc.Name] = true
			q, spelled := reg[tc.Name]
			t.Run(group+"/"+tc.Name, func(t *testing.T) {
				if !spelled {
					t.Fatalf("recorded in %s and spelled nowhere in oracle_%s_test.go", path, group)
				}
				diff, differs := oracleDifferences[group+"/"+tc.Name]
				if differs {
					t.Logf("ursus's documented difference: %s", diff.why)
				}
				oracleRun(t, tc, q, diff)
			})
		}
		for name := range reg {
			if !asked[name] {
				t.Errorf("%s/%s is spelled here and asked of Polars nowhere", group, name)
			}
		}
	}
	for group := range oracleGroups {
		if !recorded[group] {
			t.Errorf("the group %s is registered and testdata/oracle/%s.json does not exist", group, group)
		}
	}
	for key, d := range oracleDifferences {
		group, name, _ := strings.Cut(key, "/")
		if _, ok := oracleGroups[group][name]; !ok || d.why == "" {
			t.Errorf("oracleDifferences names %s, which is not a registered case with a reason", key)
		}
	}
}

// oracleRun asks ursus one case's question and compares with the recorded answer.
func oracleRun(t *testing.T, tc oracleCase, q oracleQuery, diff oracleDifference) {
	t.Helper()
	in := oracleInputs{t: t, frames: map[string]*ursus.LazyFrame{}, params: tc.Params}
	for name, f := range tc.Inputs {
		in.frames[name] = oracleBuild(t, f.Columns)
	}
	got, err := q(in).Collect(t.Context())
	if tc.Want.Error != nil {
		if err == nil {
			t.Fatalf("answered where Polars refused (%s):\n%v", tc.Want.Error.Polars, got)
		}
		if c := tc.Want.Error.Contains; c != "" && !strings.Contains(err.Error(), c) {
			t.Fatalf("refused with %q, which does not name %q (Polars: %s)", err, c, tc.Want.Error.Polars)
		}
		return
	}
	if err != nil {
		t.Fatalf("refused where Polars answers: %v", err)
	}
	want, err := oracleBuild(t, oracleDiffer(t, tc.Want.Columns, diff)).Collect(t.Context())
	if err != nil {
		t.Fatalf("building the recorded answer: %v", err)
	}
	got, want = oracleComparable(t, got), oracleComparable(t, want)

	tol := oracleTolerance(t, tc.Float)
	var opts []ursustest.Option
	switch {
	case tc.Order == "set" && tol > 0:
		// AssertFrameEqual refuses IgnoreRowOrder beside a tolerance, so both sides
		// are sorted on every column first.
		got, want = oracleSorted(t, got), oracleSorted(t, want)
	case tc.Order == "set":
		opts = append(opts, ursustest.IgnoreRowOrder())
	case tc.Order != "rows" && tc.Order != "":
		t.Fatalf("order %q is neither rows nor set", tc.Order)
	}
	if tol > 0 {
		opts = append(opts, ursustest.WithTolerance(0, tol))
	}
	ursustest.AssertFrameEqual(t, got, want, opts...)
}

// oracleTolerance is 0 for bits, else the relative tolerance "rel:<x>" names.
func oracleTolerance(t *testing.T, f string) float64 {
	t.Helper()
	switch {
	case f == "" || f == "bits":
		return 0
	case strings.HasPrefix(f, "rel:"):
		tol, err := strconv.ParseFloat(strings.TrimPrefix(f, "rel:"), 64)
		if err != nil || tol <= 0 {
			t.Fatalf("float %q is not a tolerance", f)
		}
		return tol
	}
	t.Fatalf("float %q is neither bits nor rel:<x>", f)
	return 0
}

// oracleDiffer applies ursus's documented difference to the recorded columns.
func oracleDiffer(t *testing.T, cols []oracleColumn, diff oracleDifference) []oracleColumn {
	t.Helper()
	if diff.cells == nil && diff.dtypes == nil && diff.names == nil {
		return cols
	}
	out := make([]oracleColumn, len(cols))
	for i, c := range cols {
		out[i] = c
		if dt, ok := diff.dtypes[c.Name]; ok {
			out[i].DType = dt
		}
		if name, ok := diff.names[c.Name]; ok {
			out[i].Name = name
		}
		if cells, ok := diff.cells[c.Name]; ok {
			vals := make([]any, len(c.Values))
			copy(vals, c.Values)
			for row, v := range cells {
				if row < 0 || row >= len(vals) {
					t.Fatalf("the difference names row %d of %q, which has %d", row, c.Name, len(vals))
				}
				vals[row] = v
			}
			out[i].Values = vals
		}
	}
	return out
}

// oracleComparable casts a Categorical or Enum column to its text, so two such
// columns compare by their values and not their codes; the dtypes were checked by
// the schema comparison before.
func oracleComparable(t *testing.T, df *ursus.DataFrame) *ursus.DataFrame {
	t.Helper()
	s := df.Schema()
	var exprs []ursus.Expr
	cast := false
	for _, f := range s.All() {
		e := ursus.Col(f.Name)
		if id := f.Type.ID(); id == dtype.TypeCategorical || id == dtype.TypeEnum {
			e = e.Cast(ursus.String)
			cast = true
		}
		exprs = append(exprs, e)
	}
	if !cast {
		return df
	}
	out, err := df.Select(t.Context(), exprs...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// oracleSorted orders a frame by every column, for a set comparison under a
// tolerance.
func oracleSorted(t *testing.T, df *ursus.DataFrame) *ursus.DataFrame {
	t.Helper()
	var keys []ursus.SortKey
	for _, f := range df.Schema().All() {
		keys = append(keys, ursus.Asc(ursus.Col(f.Name)))
	}
	out, err := df.Lazy().Sort(keys...).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// --- the loader --------------------------------------------------------------------

// oracleBuild builds a recorded frame: each column through the public constructors
// in its own type, side by side.
func oracleBuild(t *testing.T, cols []oracleColumn) *ursus.LazyFrame {
	t.Helper()
	if len(cols) == 0 {
		t.Fatal("a recorded frame of no columns")
	}
	frames := make([]*ursus.LazyFrame, len(cols))
	for i, c := range cols {
		frames[i] = oracleColumnFrame(t, c.Name, oracleDType(t, c.DType), c.Values)
	}
	return frames[0].HStack(frames[1:]...)
}

// oracleHelper prefixes the loader's own columns, clear of any recorded name.
const oracleHelper = "__oracle_"

// oracleColumnFrame is one column, as a frame of that column alone.
func oracleColumnFrame(t *testing.T, name string, dt dtype.DataType, vals []any) *ursus.LazyFrame {
	t.Helper()
	ok := make([]bool, len(vals))
	for i, v := range vals {
		ok[i] = v != nil
	}
	column := func(col *ursus.Column) *ursus.LazyFrame { return ursus.Frame(col) }
	cast := func(col *ursus.Column) *ursus.LazyFrame {
		return ursus.Frame(col).Select(ursus.Col(name).Cast(dt))
	}
	switch dt.ID() {
	case dtype.TypeBool:
		return column(ursus.ValuesNullable(name, oracleEach(t, vals, oracleBool), ok))
	case dtype.TypeString:
		return column(ursus.ValuesNullable(name, oracleEach(t, vals, oracleString), ok))
	case dtype.TypeBinary:
		return column(ursus.ValuesNullable(name, oracleEach(t, vals, oracleBytes), ok))
	case dtype.TypeFloat64:
		return column(ursus.ValuesNullable(name, oracleEach(t, vals, oracleFloat), ok))
	case dtype.TypeFloat32:
		// The recorded double is exactly a float32's value, so the cast is exact.
		return cast(ursus.ValuesNullable(name, oracleEach(t, vals, oracleFloat), ok))
	case dtype.TypeInt64:
		return column(ursus.ValuesNullable(name, oracleEach(t, vals, oracleInt), ok))
	case dtype.TypeInt8, dtype.TypeInt16, dtype.TypeInt32,
		dtype.TypeDate, dtype.TypeDatetime, dtype.TypeDuration, dtype.TypeTime:
		// Narrower integers, and days or ticks, as an Int64 cast to the type.
		return cast(ursus.ValuesNullable(name, oracleEach(t, vals, oracleInt), ok))
	case dtype.TypeUint8, dtype.TypeUint16, dtype.TypeUint32, dtype.TypeUint64:
		return cast(ursus.ValuesNullable(name, oracleEach(t, vals, oracleUint), ok))
	case dtype.TypeInt128, dtype.TypeCategorical, dtype.TypeEnum:
		// Text parsed or categorised by the cast.
		return cast(ursus.ValuesNullable(name, oracleEach(t, vals, oracleString), ok))
	case dtype.TypeDecimal:
		// The unscaled integer, written with its point, parsed by the cast.
		scale := dt.Scale()
		text := oracleEach(t, vals, func(t *testing.T, v any) string {
			return dtype.FormatDecimal(oracleString(t, v), scale)
		})
		return cast(ursus.ValuesNullable(name, text, ok))
	case dtype.TypeNull:
		return ursus.Frame(ursus.Values(oracleHelper+"n", make([]int64, len(vals)))).
			Select(ursus.Null(ursus.NullT).Alias(name))
	case dtype.TypeList:
		return oracleListFrame(t, name, dt, vals)
	case dtype.TypeArray:
		lists := oracleListFrame(t, name, dtype.List(dt.Inner()), vals)
		return lists.Select(ursus.Col(name).List().ToArray(dt.Size()).Alias(name))
	case dtype.TypeStruct:
		return oracleStructFrame(t, name, dt, vals)
	}
	t.Fatalf("the loader does not build a %s column", dt)
	return nil
}

// oracleListFrame builds a List column from its rows' element lists: the elements
// flattened into one column of the inner type beside their row, imploded over the
// row, one row kept a list; an empty list and a null list, which have no element,
// hold one placeholder each and are made empty or null after.
func oracleListFrame(t *testing.T, name string, dt dtype.DataType, vals []any) *ursus.LazyFrame {
	t.Helper()
	var elems []any
	var row []int64
	var first, empty, isnull []bool
	for r, v := range vals {
		list, _ := v.([]any)
		if v != nil && list == nil {
			t.Fatalf("%s row %d: %v is not a list", name, r, v)
		}
		if len(list) == 0 {
			elems = append(elems, nil)
			row, first = append(row, int64(r)), append(first, true)
			empty, isnull = append(empty, v != nil), append(isnull, v == nil)
			continue
		}
		for j, e := range list {
			elems = append(elems, e)
			row, first = append(row, int64(r)), append(first, j == 0)
			empty, isnull = append(empty, false), append(isnull, false)
		}
	}
	e, rowC, firstC, emptyC, nullC := oracleHelper+"e", oracleHelper+"row", oracleHelper+"first",
		oracleHelper+"empty", oracleHelper+"null"
	flat := oracleColumnFrame(t, e, dt.Inner(), elems).HStack(ursus.Frame(
		ursus.Values(rowC, row), ursus.Values(firstC, first), ursus.Values(emptyC, empty), ursus.Values(nullC, isnull)))
	c := ursus.Col
	return flat.WithColumns(c(e).Implode().Over(c(rowC)).Alias(name)).Filter(c(firstC)).Select(
		ursus.When(c(nullC)).Then(ursus.Null(dt)).
			When(c(emptyC)).Then(c(name).List().Head(0)).
			Otherwise(c(name)).Alias(name))
}

// oracleStructFrame builds a Struct column from its rows' objects: each field a
// column of its own type, gathered by Struct, and a null row made null after.
func oracleStructFrame(t *testing.T, name string, dt dtype.DataType, vals []any) *ursus.LazyFrame {
	t.Helper()
	fields := dt.Fields()
	isnull := make([]bool, len(vals))
	parts := make([]*ursus.LazyFrame, len(fields))
	exprs := make([]ursus.Expr, len(fields))
	for i, f := range fields {
		fv := make([]any, len(vals))
		for r, v := range vals {
			if v == nil {
				isnull[r] = true
				continue
			}
			obj, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("%s row %d: %v is not an object", name, r, v)
			}
			fv[r] = obj[f.Name]
		}
		parts[i] = oracleColumnFrame(t, f.Name, f.Type, fv)
		exprs[i] = ursus.Col(f.Name)
	}
	nullC := oracleHelper + "null"
	base := ursus.Frame(ursus.Values(nullC, isnull)).HStack(parts...)
	return base.Select(ursus.When(ursus.Col(nullC)).Then(ursus.Null(dt)).Otherwise(ursus.Struct(exprs...)).Alias(name))
}

// oracleEach reads each value with read, a null as the zero value.
func oracleEach[T any](t *testing.T, vals []any, read func(*testing.T, any) T) []T {
	t.Helper()
	out := make([]T, len(vals))
	for i, v := range vals {
		if v != nil {
			out[i] = read(t, v)
		}
	}
	return out
}

func oracleBool(t *testing.T, v any) bool {
	t.Helper()
	b, ok := v.(bool)
	if !ok {
		t.Fatalf("%v (%T) is not a Boolean", v, v)
	}
	return b
}

// oracleString reads a string, or a number's text, as an Int128 or Decimal written
// in Go by a difference entry.
func oracleString(t *testing.T, v any) string {
	t.Helper()
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case int, int64, uint64:
		return fmt.Sprint(x)
	}
	t.Fatalf("%v (%T) is not text", v, v)
	return ""
}

func oracleBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(oracleString(t, v))
	if err != nil {
		t.Fatalf("%v is not base64: %v", v, err)
	}
	return b
}

// oracleFloat reads a float: a JSON number, or "NaN", "inf", "-inf", "-0.0".
func oracleFloat(t *testing.T, v any) float64 {
	t.Helper()
	switch x := v.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(x.String(), 64)
		if err != nil {
			t.Fatalf("%v is not a float: %v", x, err)
		}
		return f
	case string:
		switch x {
		case "NaN":
			return math.NaN()
		case "inf":
			return math.Inf(1)
		case "-inf":
			return math.Inf(-1)
		case "-0.0":
			return math.Copysign(0, -1)
		}
	case float64:
		return x
	case int:
		return float64(x)
	}
	t.Fatalf("%v (%T) is not a float", v, v)
	return 0
}

func oracleInt(t *testing.T, v any) int64 {
	t.Helper()
	switch x := v.(type) {
	case json.Number:
		n, err := strconv.ParseInt(x.String(), 10, 64)
		if err != nil {
			t.Fatalf("%v is not an integer: %v", x, err)
		}
		return n
	case int:
		return int64(x)
	case int64:
		return x
	}
	t.Fatalf("%v (%T) is not an integer", v, v)
	return 0
}

func oracleUint(t *testing.T, v any) uint64 {
	t.Helper()
	switch x := v.(type) {
	case json.Number:
		n, err := strconv.ParseUint(x.String(), 10, 64)
		if err != nil {
			t.Fatalf("%v is not an unsigned integer: %v", x, err)
		}
		return n
	case int:
		return uint64(x)
	case uint64:
		return x
	}
	t.Fatalf("%v (%T) is not an unsigned integer", v, v)
	return 0
}

// --- the types -----------------------------------------------------------------------

// oracleDType parses a type spelled as ursus prints it: Int64, Datetime(us,
// Europe/Paris), Decimal(12, 4), List(Int64), Array(Float64, 3), Struct(a: Int64, b:
// String), Enum(a, b).
func oracleDType(t *testing.T, s string) dtype.DataType {
	t.Helper()
	s = strings.TrimSpace(s)
	name, args := s, []string(nil)
	if i := strings.IndexByte(s, '('); i >= 0 {
		if !strings.HasSuffix(s, ")") {
			t.Fatalf("the type %q does not close", s)
		}
		name, args = s[:i], oracleSplitArgs(t, s[i+1:len(s)-1])
	}
	simple := map[string]dtype.DataType{
		"Bool": dtype.Bool, "Int8": dtype.Int8, "Int16": dtype.Int16, "Int32": dtype.Int32, "Int64": dtype.Int64,
		"Uint8": dtype.Uint8, "Uint16": dtype.Uint16, "Uint32": dtype.Uint32, "Uint64": dtype.Uint64,
		"Int128": dtype.Int128, "Float32": dtype.Float32, "Float64": dtype.Float64, "String": dtype.String,
		"Binary": dtype.Binary, "Date": dtype.Date, "Categorical": dtype.Categorical, "Null": dtype.Null,
	}
	if dt, ok := simple[name]; ok && args == nil {
		return dt
	}
	n := func(k int) {
		if len(args) != k {
			t.Fatalf("the type %q takes %d arguments, not %d", s, k, len(args))
		}
	}
	atoi := func(a string) int {
		v, err := strconv.Atoi(a)
		if err != nil {
			t.Fatalf("the type %q: %q is not a number", s, a)
		}
		return v
	}
	switch name {
	case "Datetime":
		if len(args) == 1 {
			return dtype.Datetime(oracleUnit(t, args[0]), "")
		}
		n(2)
		return dtype.Datetime(oracleUnit(t, args[0]), args[1])
	case "Duration":
		n(1)
		return dtype.Duration(oracleUnit(t, args[0]))
	case "Time":
		n(1)
		return dtype.Time(oracleUnit(t, args[0]))
	case "Decimal":
		n(2)
		return dtype.Decimal(uint8(atoi(args[0])), uint8(atoi(args[1])))
	case "List":
		n(1)
		return dtype.List(oracleDType(t, args[0]))
	case "Array":
		n(2)
		return dtype.Array(oracleDType(t, args[0]), atoi(args[1]))
	case "Enum":
		cats := make([]string, len(args))
		for i, a := range args {
			cats[i] = oracleUnquote(t, a)
		}
		return dtype.Enum(cats...)
	case "Struct":
		fields := make([]dtype.Field, len(args))
		for i, a := range args {
			fname, rest := oracleQuotedPrefix(t, a)
			ftype, found := strings.CutPrefix(rest, ":")
			if !found {
				t.Fatalf("the field %q of %q has no type", a, s)
			}
			ftype = strings.TrimSpace(ftype)
			nullable := true
			if strings.HasSuffix(ftype, "!") {
				ftype, nullable = strings.TrimSuffix(ftype, "!"), false
			}
			fields[i] = dtype.Field{Name: fname, Type: oracleDType(t, ftype), Nullable: nullable}
		}
		return dtype.Struct(fields...)
	}
	t.Fatalf("no ursus type is spelled %q", s)
	return dtype.Null
}

func oracleUnit(t *testing.T, s string) dtype.TimeUnit {
	t.Helper()
	switch s {
	case "s":
		return dtype.Second
	case "ms":
		return dtype.Milli
	case "us":
		return dtype.Micro
	case "ns":
		return dtype.Nano
	}
	t.Fatalf("%q is not a time unit", s)
	return dtype.Nano
}

// oracleSplitArgs splits a type's arguments at the commas outside brackets and
// quotes, each trimmed.
func oracleSplitArgs(t *testing.T, s string) []string {
	t.Helper()
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			q, err := strconv.QuotedPrefix(s[i:])
			if err != nil {
				t.Fatalf("the type arguments %q hold an unclosed quote", s)
			}
			i += len(q) - 1
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

// oracleQuotedPrefix splits a field's "name: Type" at the name, quoted or bare.
func oracleQuotedPrefix(t *testing.T, s string) (name, rest string) {
	t.Helper()
	if strings.HasPrefix(s, "\"") {
		q, err := strconv.QuotedPrefix(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		return oracleUnquote(t, q), s[len(q):]
	}
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i:]
}

func oracleUnquote(t *testing.T, s string) string {
	t.Helper()
	if !strings.HasPrefix(s, "\"") {
		return s
	}
	u, err := strconv.Unquote(s)
	if err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return u
}
