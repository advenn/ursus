package ursus_test

// Decimal arithmetic: v0.3-scope.md §3 item 2's "next thing", the precision rule for
// + - * /.
//
// The rule is exact or refused. + and - widen by a digit, * keeps every digit of the
// product, and either is refused past 38 digits; / is the nearest Float64 to the
// exact quotient. Since step 497 the sums, differences, products by an integer,
// comparisons, concat and conditional are Polars 2.1.0's answers, recorded in the
// decimal_arith_byhand group of testdata/oracle and spelled in
// oracle_decimal_arith_byhand_test.go (Polars types each Decimal(38, s); its digits
// are cast to ursus's width in the query); a product of two Decimals and every
// quotient, which Polars rounds to the wider scale, are decimal_arith_test.go's
// against big.Rat, over the types' edges and seeded values, the hand cases among
// them. What stays asserts the refusals, and a join on Decimal keys of two scales,
// which Polars refuses and ursus matches at the value.

import (
	"fmt"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
)

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
	refused := func(lf *ursus.LazyFrame, kind error, words ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err := lf.Collect(t.Context())
			return refusal(df, err, kind, words...)
		}
	}

	cases := []ioCase{
		// --- past 38 digits, or a scale past 38, is refused ---
		{"+ past 38 digits is refused", refused(ursus.Frame(dec("a", D(38, 0), max38)).
			Select(c("a").Add(c("a"))), ursus.ErrValue, "38")},
		{"* past 38 digits is refused", refused(ursus.Frame(dec("a", D(38, 0), "100000000000000000000000000000000000"),
			dec("b", D(38, 0), "1000")).Select(c("a").Mul(c("b"))), ursus.ErrValue, "38")},
		{"* with a scale past 38 is refused at plan", refused(ursus.Frame(dec("a", D(38, 20), "1"),
			dec("b", D(38, 20), "1")).Select(c("a").Mul(c("b"))), ursus.ErrType, "scale")},

		// --- an Int128 beside a Decimal is refused ---
		{"a + an Int128 is refused", refused(ab().Select(c("a").Add(c("i").Cast(ursus.Int128))), ursus.ErrType, "Int128")},

		// --- a join meets two scales exactly, and refuses a float ---
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
		{"a join of a Decimal key to a Float64 key is refused", refused(
			ursus.Frame(dec("k", D(10, 2), "100")).Join(ursus.Frame(ursus.Values("k", []float64{1})),
				ursus.JoinOn(c("k"))), ursus.ErrType)},

		// --- still refused ---
		{"// is refused", refused(ab().Select(c("a").FloorDiv(c("b"))), ursus.ErrType)},
		{"% is refused", refused(ab().Select(c("a").Mod(c("b"))), ursus.ErrType)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if wrong := tc.run(t); wrong != "" {
				t.Error(wrong)
			}
		})
	}
}
