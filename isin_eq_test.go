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
var knownIsInDefects = map[string]int{}

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
