package ursus_test

// IsIn answers what Eq answers, generated over every column type a value can be
// built in and every Go type IsIn accepts.
//
// `x.IsIn(v)` and `x.Eq(v)` ask the same question about one value, so they must
// refuse the same pairs and match the same rows. They differ only where equality
// itself differs — IsIn uses grouping equality, so NaN matches NaN — and the pool
// holds no NaN. The oracle is Eq, which shares no code with IsIn's probe set.

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// knownIsInDefects counts, per column type and Go type, the values for which IsIn
// and Eq disagree.
var knownIsInDefects = map[string]int{
	"Bool IsIn float32":               6,
	"Bool IsIn float64":               8,
	"Bool IsIn int":                   10,
	"Bool IsIn int16":                 8,
	"Bool IsIn int32":                 8,
	"Bool IsIn int64":                 10,
	"Bool IsIn int8":                  4,
	"Bool IsIn string":                3,
	"Bool IsIn time.Time":             2,
	"Bool IsIn uint":                  9,
	"Bool IsIn uint16":                7,
	"Bool IsIn uint32":                8,
	"Bool IsIn uint64":                9,
	"Bool IsIn uint8":                 5,
	"Date IsIn bool":                  2,
	"Date IsIn float32":               6,
	"Date IsIn float64":               8,
	"Date IsIn int":                   10,
	"Date IsIn int16":                 8,
	"Date IsIn int32":                 8,
	"Date IsIn int64":                 10,
	"Date IsIn int8":                  4,
	"Date IsIn string":                3,
	"Date IsIn time.Time":             2,
	"Date IsIn uint":                  9,
	"Date IsIn uint16":                7,
	"Date IsIn uint32":                8,
	"Date IsIn uint64":                9,
	"Date IsIn uint8":                 5,
	"Datetime(ns, UTC) IsIn bool":     2,
	"Datetime(ns, UTC) IsIn float32":  6,
	"Datetime(ns, UTC) IsIn float64":  8,
	"Datetime(ns, UTC) IsIn int":      10,
	"Datetime(ns, UTC) IsIn int16":    8,
	"Datetime(ns, UTC) IsIn int32":    8,
	"Datetime(ns, UTC) IsIn int64":    10,
	"Datetime(ns, UTC) IsIn int8":     4,
	"Datetime(ns, UTC) IsIn string":   3,
	"Datetime(ns, UTC) IsIn uint":     9,
	"Datetime(ns, UTC) IsIn uint16":   7,
	"Datetime(ns, UTC) IsIn uint32":   8,
	"Datetime(ns, UTC) IsIn uint64":   9,
	"Datetime(ns, UTC) IsIn uint8":    5,
	"Datetime(s, UTC) IsIn bool":      2,
	"Datetime(s, UTC) IsIn float32":   6,
	"Datetime(s, UTC) IsIn float64":   8,
	"Datetime(s, UTC) IsIn int":       10,
	"Datetime(s, UTC) IsIn int16":     8,
	"Datetime(s, UTC) IsIn int32":     8,
	"Datetime(s, UTC) IsIn int64":     10,
	"Datetime(s, UTC) IsIn int8":      4,
	"Datetime(s, UTC) IsIn string":    3,
	"Datetime(s, UTC) IsIn time.Time": 1,
	"Datetime(s, UTC) IsIn uint":      9,
	"Datetime(s, UTC) IsIn uint16":    7,
	"Datetime(s, UTC) IsIn uint32":    8,
	"Datetime(s, UTC) IsIn uint64":    9,
	"Datetime(s, UTC) IsIn uint8":     5,
	"Duration(ns) IsIn bool":          2,
	"Duration(ns) IsIn float32":       6,
	"Duration(ns) IsIn float64":       8,
	"Duration(ns) IsIn int":           10,
	"Duration(ns) IsIn int16":         8,
	"Duration(ns) IsIn int32":         8,
	"Duration(ns) IsIn int64":         10,
	"Duration(ns) IsIn int8":          4,
	"Duration(ns) IsIn string":        3,
	"Duration(ns) IsIn time.Time":     2,
	"Duration(ns) IsIn uint":          9,
	"Duration(ns) IsIn uint16":        7,
	"Duration(ns) IsIn uint32":        8,
	"Duration(ns) IsIn uint64":        9,
	"Duration(ns) IsIn uint8":         5,
	"Float32 IsIn bool":               2,
	"Float32 IsIn string":             3,
	"Float32 IsIn time.Time":          2,
	"Float64 IsIn bool":               2,
	"Float64 IsIn string":             3,
	"Float64 IsIn time.Time":          2,
	"Int128 IsIn bool":                2,
	"Int128 IsIn float32":             1,
	"Int128 IsIn float64":             2,
	"Int128 IsIn string":              3,
	"Int128 IsIn time.Time":           2,
	"Int16 IsIn bool":                 2,
	"Int16 IsIn float32":              1,
	"Int16 IsIn float64":              3,
	"Int16 IsIn int":                  2,
	"Int16 IsIn int64":                2,
	"Int16 IsIn string":               3,
	"Int16 IsIn time.Time":            2,
	"Int16 IsIn uint":                 2,
	"Int16 IsIn uint32":               1,
	"Int16 IsIn uint64":               2,
	"Int32 IsIn bool":                 2,
	"Int32 IsIn float32":              1,
	"Int32 IsIn float64":              3,
	"Int32 IsIn int":                  2,
	"Int32 IsIn int64":                2,
	"Int32 IsIn string":               3,
	"Int32 IsIn time.Time":            2,
	"Int32 IsIn uint":                 2,
	"Int32 IsIn uint32":               1,
	"Int32 IsIn uint64":               2,
	"Int64 IsIn bool":                 2,
	"Int64 IsIn float32":              1,
	"Int64 IsIn float64":              2,
	"Int64 IsIn string":               3,
	"Int64 IsIn time.Time":            2,
	"Int8 IsIn bool":                  2,
	"Int8 IsIn float32":               2,
	"Int8 IsIn float64":               4,
	"Int8 IsIn int":                   6,
	"Int8 IsIn int16":                 4,
	"Int8 IsIn int32":                 4,
	"Int8 IsIn int64":                 6,
	"Int8 IsIn string":                3,
	"Int8 IsIn time.Time":             2,
	"Int8 IsIn uint":                  6,
	"Int8 IsIn uint16":                4,
	"Int8 IsIn uint32":                5,
	"Int8 IsIn uint64":                6,
	"Int8 IsIn uint8":                 2,
	"String IsIn bool":                2,
	"String IsIn float32":             6,
	"String IsIn float64":             8,
	"String IsIn int":                 10,
	"String IsIn int16":               8,
	"String IsIn int32":               8,
	"String IsIn int64":               10,
	"String IsIn int8":                4,
	"String IsIn time.Time":           2,
	"String IsIn uint":                9,
	"String IsIn uint16":              7,
	"String IsIn uint32":              8,
	"String IsIn uint64":              9,
	"String IsIn uint8":               5,
	"Uint16 IsIn bool":                2,
	"Uint16 IsIn float32":             2,
	"Uint16 IsIn float64":             4,
	"Uint16 IsIn int":                 3,
	"Uint16 IsIn int16":               1,
	"Uint16 IsIn int32":               1,
	"Uint16 IsIn int64":               3,
	"Uint16 IsIn int8":                1,
	"Uint16 IsIn string":              3,
	"Uint16 IsIn time.Time":           2,
	"Uint16 IsIn uint":                2,
	"Uint16 IsIn uint32":              1,
	"Uint16 IsIn uint64":              2,
	"Uint32 IsIn bool":                2,
	"Uint32 IsIn float32":             2,
	"Uint32 IsIn float64":             4,
	"Uint32 IsIn int":                 2,
	"Uint32 IsIn int16":               1,
	"Uint32 IsIn int32":               1,
	"Uint32 IsIn int64":               2,
	"Uint32 IsIn int8":                1,
	"Uint32 IsIn string":              3,
	"Uint32 IsIn time.Time":           2,
	"Uint32 IsIn uint":                1,
	"Uint32 IsIn uint64":              1,
	"Uint64 IsIn bool":                2,
	"Uint64 IsIn float32":             2,
	"Uint64 IsIn float64":             3,
	"Uint64 IsIn int":                 1,
	"Uint64 IsIn int16":               1,
	"Uint64 IsIn int32":               1,
	"Uint64 IsIn int64":               1,
	"Uint64 IsIn int8":                1,
	"Uint64 IsIn string":              3,
	"Uint64 IsIn time.Time":           2,
	"Uint8 IsIn bool":                 2,
	"Uint8 IsIn float32":              3,
	"Uint8 IsIn float64":              5,
	"Uint8 IsIn int":                  5,
	"Uint8 IsIn int16":                3,
	"Uint8 IsIn int32":                3,
	"Uint8 IsIn int64":                5,
	"Uint8 IsIn int8":                 1,
	"Uint8 IsIn string":               3,
	"Uint8 IsIn time.Time":            2,
	"Uint8 IsIn uint":                 4,
	"Uint8 IsIn uint16":               2,
	"Uint8 IsIn uint32":               3,
	"Uint8 IsIn uint64":               4,
}

var isInIntPool = []int64{0, 1, -1, 127, 128, 255, 256, 5000, 1 << 31, 1<<53 + 1}

var isInFloatPool = []float64{0, 1, -1, 0.1, 0.5, 127, 256, 1<<53 + 2}

// intValues keeps the pool's values T holds exactly.
func intValues[T int8 | int16 | int32 | int64 | int | uint8 | uint16 | uint32 | uint64 | uint](pool []int64) []T {
	var out []T
	for _, v := range pool {
		if x := T(v); int64(x) == v && (x < 0) == (v < 0) {
			out = append(out, x)
		}
	}
	return out
}

func floatValues[T float32 | float64](pool []float64) []T {
	var out []T
	for _, v := range pool {
		if x := T(v); float64(x) == v {
			out = append(out, x)
		}
	}
	return out
}

// withNull is a column v of vals and one null row.
func withNull[T ursus.Literal](vals []T) *ursus.LazyFrame {
	var zero T
	valid := make([]bool, len(vals)+1)
	for i := range vals {
		valid[i] = true
	}
	return ursus.Frame(ursus.ValuesNullable("v", append(slices.Clone(vals), zero), valid))
}

// anyOf lifts each value to an any, for the literal pool.
func anyOf[T any](vs []T) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

// isInOf and eqOf call the generic methods at v's own Go type.
func isInOf(e ursus.Expr, v any) ursus.Expr {
	switch x := v.(type) {
	case int8:
		return e.IsIn(x)
	case int16:
		return e.IsIn(x)
	case int32:
		return e.IsIn(x)
	case int64:
		return e.IsIn(x)
	case int:
		return e.IsIn(x)
	case uint8:
		return e.IsIn(x)
	case uint16:
		return e.IsIn(x)
	case uint32:
		return e.IsIn(x)
	case uint64:
		return e.IsIn(x)
	case uint:
		return e.IsIn(x)
	case float32:
		return e.IsIn(x)
	case float64:
		return e.IsIn(x)
	case string:
		return e.IsIn(x)
	case bool:
		return e.IsIn(x)
	case time.Time:
		return e.IsIn(x)
	}
	panic(fmt.Sprintf("isInOf: %T", v))
}

func eqOf(e ursus.Expr, v any) ursus.Expr {
	switch x := v.(type) {
	case int8:
		return e.Eq(x)
	case int16:
		return e.Eq(x)
	case int32:
		return e.Eq(x)
	case int64:
		return e.Eq(x)
	case int:
		return e.Eq(x)
	case uint8:
		return e.Eq(x)
	case uint16:
		return e.Eq(x)
	case uint32:
		return e.Eq(x)
	case uint64:
		return e.Eq(x)
	case uint:
		return e.Eq(x)
	case float32:
		return e.Eq(x)
	case float64:
		return e.Eq(x)
	case string:
		return e.Eq(x)
	case bool:
		return e.Eq(x)
	case time.Time:
		return e.Eq(x)
	}
	panic(fmt.Sprintf("eqOf: %T", v))
}

func TestIsInAgreesWithEq(t *testing.T) {
	c := ursus.Col
	whole := time.Date(2024, 1, 1, 0, 0, 1, 0, time.UTC)
	frac := time.Date(2024, 1, 1, 0, 0, 1, 500_000_000, time.UTC)
	stamps := withNull([]time.Time{whole, frac, time.Date(2024, 1, 1, 13, 0, 0, 0, time.UTC)})

	columns := []struct {
		name string
		lf   *ursus.LazyFrame
	}{
		{"Int8", withNull(intValues[int8](isInIntPool))},
		{"Int16", withNull(intValues[int16](isInIntPool))},
		{"Int32", withNull(intValues[int32](isInIntPool))},
		{"Int64", withNull(intValues[int64](isInIntPool))},
		{"Uint8", withNull(intValues[uint8](isInIntPool))},
		{"Uint16", withNull(intValues[uint16](isInIntPool))},
		{"Uint32", withNull(intValues[uint32](isInIntPool))},
		{"Uint64", withNull(intValues[uint64](isInIntPool))},
		{"Int128", withNull(intValues[int64](isInIntPool)).Select(c("v").Cast(ursus.Int128))},
		{"Float32", withNull(floatValues[float32](isInFloatPool))},
		{"Float64", withNull(floatValues[float64](isInFloatPool))},
		{"String", withNull([]string{"1", "0.5", "a", "true"})},
		{"Bool", withNull([]bool{true, false})},
		{"Datetime(ns, UTC)", stamps},
		{"Datetime(s, UTC)", stamps.Select(c("v").Cast(ursus.Datetime(ursus.Second, "UTC")))},
		{"Date", stamps.Select(c("v").Cast(ursus.Datetime(ursus.Micro, "")).Cast(ursus.Date))},
		{"Duration(ns)", withNull(intValues[int64](isInIntPool)).Select(c("v").Cast(ursus.Duration(ursus.Nano)))},
	}

	var literals []any
	literals = append(literals, anyOf(intValues[int8](isInIntPool))...)
	literals = append(literals, anyOf(intValues[int16](isInIntPool))...)
	literals = append(literals, anyOf(intValues[int32](isInIntPool))...)
	literals = append(literals, anyOf(intValues[int64](isInIntPool))...)
	literals = append(literals, anyOf(intValues[int](isInIntPool))...)
	literals = append(literals, anyOf(intValues[uint8](isInIntPool))...)
	literals = append(literals, anyOf(intValues[uint16](isInIntPool))...)
	literals = append(literals, anyOf(intValues[uint32](isInIntPool))...)
	literals = append(literals, anyOf(intValues[uint64](isInIntPool))...)
	literals = append(literals, anyOf(intValues[uint](isInIntPool))...)
	literals = append(literals, anyOf(floatValues[float32](isInFloatPool))...)
	literals = append(literals, anyOf(floatValues[float64](isInFloatPool))...)
	literals = append(literals, "1", "0.5", "a", true, false, whole, frac)

	var pairs, answered int
	seen := map[string]bool{}
	for _, col := range columns {
		wrongBy := map[string][]string{}
		for _, v := range literals {
			key := fmt.Sprintf("%s IsIn %T", col.name, v)
			if !seen[key] {
				seen[key] = true
				pairs++
			}
			in, inErr := col.lf.Select(isInOf(c("v"), v)).Collect(t.Context())
			eq, eqErr := col.lf.Select(eqOf(c("v"), v)).Collect(t.Context())
			if eqErr == nil {
				answered++
			}
			var wrong string
			switch {
			case eqErr != nil && inErr == nil:
				wrong = "Eq refuses, IsIn answers"
			case eqErr == nil && inErr != nil:
				wrong = "Eq answers, IsIn refuses: " + inErr.Error()
			case eqErr != nil && kindOf(inErr) != kindOf(eqErr):
				wrong = fmt.Sprintf("Eq refuses with %s, IsIn with %s", kindOf(eqErr), kindOf(inErr))
			case eqErr != nil:
			default:
				if d := framesDiffer(t, in, eq); d != "" {
					wrong = d
				}
			}
			if wrong != "" {
				wrongBy[key] = append(wrongBy[key], fmt.Sprintf("%v: %s", v, wrong))
			}
		}
		for key := range seen {
			if !strings.HasPrefix(key, col.name+" IsIn ") {
				continue
			}
			wrong := wrongBy[key]
			n, known := knownIsInDefects[key]
			switch {
			case known && len(wrong) == 0:
				t.Errorf("%s answers correctly now; delete it from knownIsInDefects", key)
			case known && len(wrong) != n:
				t.Errorf("%s: %d wrong, the ratchet says %d: %v", key, len(wrong), n, wrong)
			case !known && len(wrong) > 0:
				t.Errorf("%s: %d wrong: %v", key, len(wrong), wrong)
			}
		}
	}
	for key := range knownIsInDefects {
		if !seen[key] {
			t.Errorf("knownIsInDefects names %q, which is not a pair", key)
		}
	}
	t.Logf("%d pairs, %d values Eq answers", pairs, answered)
	if pairs != len(columns)*15 || answered < 1000 {
		t.Fatalf("%d pairs, %d values Eq answers — the sweep has gone vacuous", pairs, answered)
	}
}
