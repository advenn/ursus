package ursus_test

// Decimal arithmetic, against answers worked out by hand: v0.3-scope.md §3 item 2's
// "next thing", the precision rule for + - * /.
//
// The rule is exact or refused. + and - widen by a digit, * keeps every digit of the
// product, and either is refused past 38 digits; / is the nearest Float64 to the
// exact quotient. Each case states the result's type and its unscaled values, read
// off the column itself.

import (
	"fmt"
	"math"
	"math/big"
	"slices"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
)

// knownDecimalArithDefects names each case that answers wrongly today, with what it
// answers.
var knownDecimalArithDefects = map[string]string{}

// decimalValues reads a Decimal column's type and unscaled values, null as ∅.
func decimalValues(df *ursus.DataFrame, name string) (dtype.DataType, []string, error) {
	c, ok := df.Batch().ByName(name)
	if !ok {
		return dtype.DataType{}, nil, fmt.Errorf("no column %q", name)
	}
	if c.DType().ID() != dtype.TypeDecimal {
		return c.DType(), nil, fmt.Errorf("column %q is %s, want a Decimal", name, c.DType())
	}
	v, err := data.Values[i128.Int128](c)
	if err != nil {
		return c.DType(), nil, err
	}
	out := make([]string, c.Len())
	for i := range out {
		if c.Validity().Get(i) {
			out[i] = v[i].String()
		} else {
			out[i] = null
		}
	}
	return c.DType(), out, nil
}

// nearest is the nearest float64 to the decimal quotient (u1/10^s1) / (u2/10^s2).
func nearest(u1 string, s1 int, u2 string, s2 int) float64 {
	n, _ := new(big.Int).SetString(u1, 10)
	d, _ := new(big.Int).SetString(u2, 10)
	ten := big.NewInt(10)
	n.Mul(n, new(big.Int).Exp(ten, big.NewInt(int64(s2)), nil))
	d.Mul(d, new(big.Int).Exp(ten, big.NewInt(int64(s1)), nil))
	f, _ := new(big.Rat).SetFrac(n, d).Float64()
	return f
}

func TestDecimalArithmeticByHand(t *testing.T) {
	c := ursus.Col
	D := dtype.Decimal
	dec := func(name string, dt dtype.DataType, unscaled ...string) *ursus.Column {
		return i128Col(t, dt, name, unscaled...)
	}
	// a = Decimal(10,2) [1.25, -3.50]; b = Decimal(12,3) [2.125, 0.375]; i = [2, 3].
	ab := func() *ursus.LazyFrame {
		return ursus.Frame(dec("a", D(10, 2), "125", "-350"), dec("b", D(12, 3), "2125", "375"),
			ursus.Values("i", []int64{2, 3}))
	}
	max38 := "99999999999999999999999999999999999999"
	wantDecimal := func(lf *ursus.LazyFrame, name string, typ dtype.DataType, unscaled ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err := lf.Collect(t.Context())
			if err != nil {
				return err.Error()
			}
			got, vals, err := decimalValues(df, name)
			if err != nil {
				return err.Error()
			}
			if got != typ || !slices.Equal(vals, unscaled) {
				return fmt.Sprintf("%s %v, want %s %v", got, vals, typ, unscaled)
			}
			return ""
		}
	}
	wantFloats := func(lf *ursus.LazyFrame, name string, want ...float64) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err := lf.Collect(t.Context())
			if err != nil {
				return err.Error()
			}
			if typ := df.Schema().Field(0).Type; typ != ursus.Float64 {
				return fmt.Sprintf("type %s, want Float64", typ)
			}
			col, err := df.Column[float64](name)
			if err != nil {
				return err.Error()
			}
			for i, w := range want {
				g, _ := col.Get(i)
				if !(g == w || (math.IsNaN(g) && math.IsNaN(w))) {
					return fmt.Sprintf("row %d = %v, want %v", i, g, w)
				}
			}
			return ""
		}
	}
	wantBools := func(lf *ursus.LazyFrame, name string, want ...bool) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err := lf.Collect(t.Context())
			if err != nil {
				return err.Error()
			}
			col, err := df.Column[bool](name)
			if err != nil {
				return err.Error()
			}
			for i, w := range want {
				if g, ok := col.Get(i); !ok || g != w {
					return fmt.Sprintf("row %d = %v (valid %v), want %v", i, g, ok, w)
				}
			}
			return ""
		}
	}
	refused := func(lf *ursus.LazyFrame, kind error, words ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err := lf.Collect(t.Context())
			return refusal(df, err, kind, words...)
		}
	}

	cases := []ioCase{
		// --- + and -: max integer digits + max scale + 1 ---
		{"a + b", wantDecimal(ab().Select(c("a").Add(c("b"))), "a", D(13, 3), "3375", "-3125")},
		{"a - b", wantDecimal(ab().Select(c("a").Sub(c("b"))), "a", D(13, 3), "-875", "-3875")},
		{"an identical pair widens by a digit", wantDecimal(ab().Select(c("a").Add(c("a"))), "a", D(11, 2), "250", "-700")},
		{"99999999.99 + 99999999.99", wantDecimal(ursus.Frame(dec("a", D(10, 2), "9999999999")).
			Select(c("a").Add(c("a"))), "a", D(11, 2), "19999999998")},
		{"+ past 38 digits is refused", refused(ursus.Frame(dec("a", D(38, 0), max38)).
			Select(c("a").Add(c("a"))), ursus.ErrValue, "38")},

		// --- *: every digit of the product ---
		{"a * b", wantDecimal(ab().Select(c("a").Mul(c("b"))), "a", D(22, 5), "265625", "-131250")},
		{"a * an Int64 column", wantDecimal(ab().Select(c("a").Mul(c("i"))), "a", D(29, 2), "250", "-1050")},
		{"a * a Go int", wantDecimal(ab().Select(c("a").Mul(2)), "a", D(29, 2), "250", "-700")},
		{"* past 38 digits is refused", refused(ursus.Frame(dec("a", D(38, 0), "100000000000000000000000000000000000"),
			dec("b", D(38, 0), "1000")).Select(c("a").Mul(c("b"))), ursus.ErrValue, "38")},
		{"* with a scale past 38 is refused at plan", refused(ursus.Frame(dec("a", D(38, 20), "1"),
			dec("b", D(38, 20), "1")).Select(c("a").Mul(c("b"))), ursus.ErrType, "scale")},

		// --- /: the nearest Float64 ---
		{"a / b", wantFloats(ab().Select(c("a").Div(c("b"))), "a",
			nearest("125", 2, "2125", 3), nearest("-350", 2, "375", 3))},
		{"1 / 3", wantFloats(ursus.Frame(dec("x", D(1, 0), "1"), dec("y", D(1, 0), "3")).
			Select(c("x").Div(c("y"))), "x", nearest("1", 0, "3", 0))},
		{"x / 0 is +Inf, and 0 / 0 NaN", wantFloats(ursus.Frame(dec("x", D(5, 2), "100", "0"), dec("y", D(5, 2), "0", "0")).
			Select(c("x").Div(c("y"))), "x", math.Inf(1), math.NaN())},

		// --- with an integer or a float ---
		{"a + an Int64 column", wantDecimal(ab().Select(c("a").Add(c("i"))), "a", D(22, 2), "325", "-50")},
		{"a + a Go int", wantDecimal(ab().Select(c("a").Add(1)), "a", D(22, 2), "225", "-250")},
		{"a + 0.5 is Float64", wantFloats(ab().Select(c("a").Add(0.5)), "a", 1.75, -3)},
		{"a + an Int128 is refused", refused(ab().Select(c("a").Add(c("i").Cast(ursus.Int128))), ursus.ErrType, "Int128")},

		// --- comparisons, joins, Concat, conditionals: meet exactly ---
		{"a > b", wantBools(ab().Select(c("a").Gt(c("b"))), "a", false, false)},
		{"1.00 == 1.000", wantBools(ursus.Frame(dec("a", D(10, 2), "100"), dec("b", D(12, 3), "1000")).
			Select(c("a").Eq(c("b"))), "a", true)},
		{"a == 1.25", wantBools(ab().Select(c("a").Eq(1.25)), "a", true, false)},
		{"a join of Decimal(10,2) to Decimal(12,3) keys", func(t *testing.T) string {
			df, err := ursus.Frame(dec("k", D(10, 2), "100", "250")).Join(
				ursus.Frame(dec("k", D(12, 3), "1000", "2505"), ursus.Values("r", []int64{1, 2})),
				ursus.JoinOn(c("k"))).Collect(t.Context())
			if err != nil {
				return err.Error()
			}
			if df.Height() != 1 {
				return fmt.Sprintf("%d rows, want 1: 1.00 matches 1.000, and 2.50 does not match 2.505", df.Height())
			}
			return ""
		}},
		{"Concat of Decimal(10,2) and Decimal(12,3)", wantDecimal(ursus.Concat([]*ursus.LazyFrame{
			ab().Select(c("a").Alias("x")), ab().Select(c("b").Alias("x"))}), "x", D(12, 3),
			"1250", "-3500", "2125", "375")},
		{"When of Decimal(10,2) and Decimal(12,3)", wantDecimal(ab().Select(
			ursus.When(c("i").Eq(2)).Then(c("a")).Otherwise(c("b")).Alias("x")), "x", D(12, 3), "1250", "375")},

		// --- still refused ---
		{"// is refused", refused(ab().Select(c("a").FloorDiv(c("b"))), ursus.ErrType)},
		{"% is refused", refused(ab().Select(c("a").Mod(c("b"))), ursus.ErrType)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrong := tc.run(t)
			why, known := knownDecimalArithDefects[tc.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownDecimalArithDefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %s", why, wrong)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownDecimalArithDefects {
		if !slices.ContainsFunc(cases, func(c ioCase) bool { return c.name == name }) {
			t.Errorf("knownDecimalArithDefects names %q, which is not a case", name)
		}
	}
}
