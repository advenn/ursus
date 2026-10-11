package ursus_test

// Step 184, scope item R4: 0.5's expressions against Polars' answers. They were
// produced once, by bench/scripts/oracle_expressions.py in the bench's environment,
// and checked in as testdata/oracle/expressions.json: the calendar, rolling windows
// of rows and by time, the ewm functions, Cut and QCut, ConcatStr, the bit counts,
// Describe and ValueCounts. Each case names a Polars expression; oracleExprs gives
// ursus's spelling of it.

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

type oracleColumn struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Values []any  `json:"values"`
}

type oracleFile struct {
	Polars string `json:"polars"`
	Cases  []struct {
		Name    string         `json:"name"`
		Columns []oracleColumn `json:"columns"`
		Want    oracleColumn   `json:"want"`
	} `json:"cases"`
	Frames struct {
		Describe struct {
			Columns    []oracleColumn       `json:"columns"`
			Statistics []string             `json:"statistics"`
			Answers    map[string][]*string `json:"answers"`
		} `json:"describe"`
		ValueCounts struct {
			Columns []oracleColumn `json:"columns"`
			Pairs   [][]any        `json:"pairs"`
		} `json:"value_counts"`
	} `json:"frames"`
}

func readOracle(t *testing.T) oracleFile {
	t.Helper()
	b, err := os.ReadFile("testdata/oracle/expressions.json")
	if err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber() // 1<<62 does not survive a float64
	var f oracleFile
	if err := d.Decode(&f); err != nil {
		t.Fatal(err)
	}
	return f
}

// oracleType is a column kind's ursus type.
func oracleType(t *testing.T, kind string) dtype.DataType {
	t.Helper()
	switch {
	case kind == "f64":
		return dtype.Float64
	case kind == "str":
		return dtype.String
	case kind == "bool":
		return dtype.Bool
	case kind == "date":
		return dtype.Date
	case strings.HasPrefix(kind, "datetime_us"):
		_, tz, _ := strings.Cut(kind, ":")
		return dtype.Datetime(dtype.Micro, tz)
	}
	for _, it := range []dtype.DataType{dtype.Int8, dtype.Int16, dtype.Int32, dtype.Int64,
		dtype.Uint8, dtype.Uint16, dtype.Uint32, dtype.Uint64} {
		if strings.EqualFold(it.String(), kind) {
			return it
		}
	}
	t.Fatalf("no ursus type for the kind %q", kind)
	return dtype.Null
}

// oracleFrame builds a case's columns, each in its own type.
func oracleFrame(t *testing.T, cols []oracleColumn) *ursus.LazyFrame {
	t.Helper()
	var built []*ursus.Column
	var cast []ursus.Expr
	for _, c := range cols {
		ok := make([]bool, len(c.Values))
		for i, v := range c.Values {
			ok[i] = v != nil
		}
		switch c.Kind {
		case "f64":
			vals := make([]float64, len(c.Values))
			for i, v := range c.Values {
				vals[i] = oracleFloat(v)
			}
			built = append(built, ursus.ValuesNullable(c.Name, vals, ok))
		case "str":
			vals := make([]string, len(c.Values))
			for i, v := range c.Values {
				vals[i], _ = v.(string)
			}
			built = append(built, ursus.ValuesNullable(c.Name, vals, ok))
		case "bool":
			vals := make([]bool, len(c.Values))
			for i, v := range c.Values {
				vals[i], _ = v.(bool)
			}
			built = append(built, ursus.ValuesNullable(c.Name, vals, ok))
		default:
			// Integers, and instants as their ticks, cast to the case's type.
			vals := make([]int64, len(c.Values))
			for i, v := range c.Values {
				vals[i] = oracleInt(t, v)
			}
			built = append(built, ursus.ValuesNullable(c.Name, vals, ok))
		}
		cast = append(cast, ursus.Col(c.Name).Cast(oracleType(t, c.Kind)))
	}
	return ursus.Frame(built...).Select(cast...)
}

func oracleFloat(v any) float64 {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case string: // "NaN"
		return math.NaN()
	}
	return 0
}

func oracleInt(t *testing.T, v any) int64 {
	t.Helper()
	if v == nil {
		return 0
	}
	n, err := strconv.ParseInt(string(v.(json.Number)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// oracleExprs is ursus's spelling of each case's Polars expression; the case's frame
// supplies its columns.
func oracleExprs() map[string]ursus.Expr {
	c, every := ursus.Col, ursus.Every
	return map[string]ursus.Expr{
		"rolling_sum_3":              c("x").RollingSum(3),
		"rolling_mean_3_min1":        c("x").RollingMean(3, ursus.MinSamples(1)),
		"rolling_min_4_min2":         c("x").RollingMin(4, ursus.MinSamples(2)),
		"rolling_max_2":              c("x").RollingMax(2),
		"rolling_var_3_min2":         c("x").RollingVar(3, ursus.MinSamples(2)),
		"rolling_std_4_min1_ddof0":   c("x").RollingStd(4, ursus.MinSamples(1), ursus.Ddof(0)),
		"rolling_sum_3_center":       c("x").RollingSum(3, ursus.RollingCenter()),
		"rolling_mean_4_center_min1": c("x").RollingMean(4, ursus.RollingCenter(), ursus.MinSamples(1)),
		"rolling_sum_by_1d":          c("x").RollingSumBy(c("t"), every("1d")),
		"rolling_max_by_40d":         c("x").RollingMaxBy(c("t"), every("40d")),

		"ewm_mean_alpha05":           c("x").EwmMean(ursus.EwmAlpha(0.5)),
		"ewm_mean_span3_noadjust":    c("x").EwmMean(ursus.EwmSpan(3), ursus.EwmAdjust(false)),
		"ewm_mean_com2_ignore_nulls": c("x").EwmMean(ursus.EwmCom(2), ursus.IgnoreNulls()),
		"ewm_mean_alpha05_min3":      c("x").EwmMean(ursus.EwmAlpha(0.5), ursus.MinSamples(3)),
		"ewm_std_halflife2":          c("x").EwmStd(ursus.EwmHalfLife(2)),
		"ewm_var_alpha03_bias":       c("x").EwmVar(ursus.EwmAlpha(0.3), ursus.Biased()),
		"ewm_var_span4_noadjust":     c("x").EwmVar(ursus.EwmSpan(4), ursus.EwmAdjust(false)),

		"cut_0_1":         c("x").Cut([]float64{0, 1}),
		"cut_labels":      c("x").Cut([]float64{0, 1}, ursus.CutLabels("lo", "mid", "hi")),
		"cut_left_closed": c("x").Cut([]float64{0, 1}, ursus.CutLeftClosed()),
		"qcut_quartiles":  c("x").QCut([]float64{0.25, 0.5, 0.75}),
		"qcut_3":          c("x").QCutN(3),

		"concat_str_dash": ursus.ConcatStr("-", c("a"), c("b")),
		"concat_str_int":  ursus.ConcatStr("", c("a"), c("i")),

		"count_ones":      c("i").BitwiseCountOnes(),
		"count_zeros":     c("i").BitwiseCountZeros(),
		"leading_zeros":   c("i").BitwiseLeadingZeros(),
		"leading_ones":    c("i").BitwiseLeadingOnes(),
		"trailing_zeros":  c("i").BitwiseTrailingZeros(),
		"trailing_ones":   c("i").BitwiseTrailingOnes(),
		"count_ones_int8": c("i").BitwiseCountOnes(),

		"offset_by_1mo_dates":     c("d").Dt().OffsetBy(every("1mo")),
		"offset_by_minus1y_dates": c("d").Dt().OffsetBy(every("-1y")),
		"offset_by_1d12h":         c("t").Dt().OffsetBy(every("1d12h")),
		"truncate_1w":             c("t").Dt().Truncate(every("1w")),
		"truncate_1mo":            c("t").Dt().Truncate(every("1mo")),
		"truncate_1q_dates":       c("d").Dt().Truncate(every("1q")),
		"round_1h":                c("t").Dt().Round(every("1h")),
		"round_1d":                c("t").Dt().Round(every("1d")),
		"month_start":             c("t").Dt().MonthStart(),
		"month_end_dates":         c("d").Dt().MonthEnd(),
		"is_leap_year_dates":      c("d").Dt().IsLeapYear(),
		"convert_tokyo_hour":      c("t").Dt().ConvertTimeZone("Asia/Tokyo").Dt().Hour(),
		"truncate_1d_london": c("t").Dt().ConvertTimeZone("Europe/London").Dt().Truncate(every("1d")).
			Dt().ConvertTimeZone("UTC"),
		"replace_zone_new_york": c("t").Dt().ReplaceTimeZone("America/New_York", ursus.AmbiguousEarliest,
			ursus.NonExistentNull).Dt().ConvertTimeZone("UTC"),
		"strftime": c("t").Dt().Strftime("%Y-%m-%d %H:%M %a %j"),
	}
}

// oracleDifferences are the rows where ursus answers otherwise than Polars on
// purpose, each with ursus's answer and the doc that states it. A difference found and
// not meant is fixed, not listed: step 184 found three, fixed two, and lists one.
var oracleDifferences = map[string]map[int]any{
	// A window by time holding only nulls: Polars sums it to 0.0, although its
	// min_samples of 1 asks for a value, as it answers an empty window under
	// closed="left"; ursus answers null (RollingSumBy's doc).
	"rolling_sum_by_1d": {2: nil},
}

func TestExpressionsAnswerAsPolarsOracle(t *testing.T) {
	f := readOracle(t)
	exprs := oracleExprs()
	if len(f.Cases) < 40 {
		t.Fatalf("only %d cases in the oracle", len(f.Cases))
	}
	for _, tc := range f.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			e, ok := exprs[tc.Name]
			if !ok {
				t.Fatalf("no ursus spelling of the Polars case %q", tc.Name)
			}
			df, err := oracleFrame(t, tc.Columns).Select(e.Alias("r")).Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			got := oracleRead(t, df, tc.Want.Kind)
			if len(got) != len(tc.Want.Values) {
				t.Fatalf("%d rows, want %d", len(got), len(tc.Want.Values))
			}
			for i, w := range tc.Want.Values {
				if ours, ok := oracleDifferences[tc.Name][i]; ok {
					if !oracleSame(t, tc.Want.Kind, got[i], ours) {
						t.Errorf("row %d: %v, where ursus's stated difference is %v", i, got[i], ours)
					}
					continue
				}
				if !oracleSame(t, tc.Want.Kind, got[i], w) {
					t.Errorf("row %d: %v, Polars %v (all: %v, Polars %v)", i, got[i], w, got, tc.Want.Values)
					break
				}
			}
		})
	}
	for name := range exprs {
		if !slices.ContainsFunc(f.Cases, func(c struct {
			Name    string         `json:"name"`
			Columns []oracleColumn `json:"columns"`
			Want    oracleColumn   `json:"want"`
		}) bool {
			return c.Name == name
		}) {
			t.Errorf("%q is spelled here and asked of Polars nowhere", name)
		}
	}
}

// oracleRead reads column r as the oracle's kind writes it: a float, a string, a
// Boolean, or an integer — instants as ticks of microseconds, dates as days.
func oracleRead(t *testing.T, df *ursus.DataFrame, kind string) []any {
	t.Helper()
	c := ursus.Col("r")
	switch {
	case kind == "f64":
		col, err := df.Column[float64]("r")
		if err != nil {
			t.Fatal(err)
		}
		return oracleAll(col.Len(), col.Get)
	case kind == "str":
		col, err := df.Column[string]("r")
		if err != nil {
			t.Fatal(err)
		}
		return oracleAll(col.Len(), col.Get)
	case kind == "bool":
		col, err := df.Column[bool]("r")
		if err != nil {
			t.Fatal(err)
		}
		return oracleAll(col.Len(), col.Get)
	case strings.HasPrefix(kind, "datetime_us"):
		c = c.Cast(oracleType(t, kind))
	}
	ticks, err := df.Select(t.Context(), c.Cast(ursus.Int64).Alias("r"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := ticks.Column[int64]("r")
	if err != nil {
		t.Fatal(err)
	}
	return oracleAll(col.Len(), col.Get)
}

func oracleAll[T any](n int, get func(int) (T, bool)) []any {
	out := make([]any, n)
	for i := range n {
		if v, ok := get(i); ok {
			out[i] = v
		}
	}
	return out
}

// oracleSame compares one value: a float to 1e-9 relative, NaN equal to NaN, and
// everything else exactly.
func oracleSame(t *testing.T, kind string, got, want any) bool {
	t.Helper()
	if want == nil || got == nil {
		return want == nil && got == nil
	}
	switch kind {
	case "f64":
		g, w := got.(float64), oracleFloat(want)
		if math.IsNaN(w) || math.IsNaN(g) {
			return math.IsNaN(w) && math.IsNaN(g)
		}
		return math.Abs(g-w) <= 1e-9*math.Max(1, math.Abs(w))
	case "str", "bool":
		return got == want
	}
	return got.(int64) == oracleInt(t, want)
}

func TestDescribeAndValueCountsAnswerAsPolarsOracle(t *testing.T) {
	f := readOracle(t)
	desc := f.Frames.Describe
	df, err := oracleFrame(t, desc.Columns).Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stats, err := df.Column[string]("statistic")
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.Values(); !slices.Equal(got, desc.Statistics) {
		t.Fatalf("statistics %v, Polars %v", got, desc.Statistics)
	}
	for name, want := range desc.Answers {
		text, err := df.Select(t.Context(), ursus.Col(name).Cast(ursus.String))
		if err != nil {
			t.Fatal(err)
		}
		col, err := text.Column[string](name)
		if err != nil {
			t.Fatal(err)
		}
		for i, w := range want {
			g, ok := col.Get(i)
			switch {
			case w == nil && !ok:
			case w == nil || !ok:
				t.Errorf("%s's %s: %q (present %v), Polars %v", name, desc.Statistics[i], g, ok, w)
			default:
				gf, gerr := strconv.ParseFloat(g, 64)
				wf, werr := strconv.ParseFloat(*w, 64)
				if gerr == nil && werr == nil {
					if math.Abs(gf-wf) > 1e-9*math.Max(1, math.Abs(wf)) {
						t.Errorf("%s's %s: %v, Polars %v", name, desc.Statistics[i], gf, wf)
					}
				} else if g != *w {
					t.Errorf("%s's %s: %q, Polars %q", name, desc.Statistics[i], g, *w)
				}
			}
		}
	}

	vc := f.Frames.ValueCounts
	counts, err := oracleFrame(t, vc.Columns).ValueCounts(ursus.Col("s")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s, _ := counts.Column[string]("s")
	n, err := counts.Select(t.Context(), ursus.Col("count").Cast(ursus.Int64))
	if err != nil {
		t.Fatal(err)
	}
	nc, _ := n.Column[int64]("count")
	// Polars leaves the order of ties to its threads; the pairs are compared as a set,
	// and the counts as falling.
	var got, want []string
	for i := range s.Len() {
		v, ok := s.Get(i)
		if !ok {
			v = "∅"
		}
		k, _ := nc.Get(i)
		got = append(got, v+"="+strconv.FormatInt(k, 10))
		if i > 0 {
			if prev, _ := nc.Get(i - 1); prev < k {
				t.Errorf("counts rise at row %d", i)
			}
		}
	}
	for _, p := range vc.Pairs {
		v, ok := p[0].(string)
		if !ok {
			v = "∅"
		}
		want = append(want, v+"="+string(p[1].(json.Number)))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("value counts %v, Polars %v", got, want)
	}
}
