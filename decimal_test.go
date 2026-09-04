package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"ursus"
	"ursus/dtype"
	"ursus/i128"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/source/memsrc"
	"ursus/internal/uerr"
)

// Decimal was declarable before step 3 and had nowhere to be stored: DataType
// carried the precision and scale, but Physical() had no case for it, so a Decimal
// column could not be read by any kernel. Worse, IsOrdered() already returned true,
// so Sort(Col("price")) passed plan-time validation and then failed in the sort
// comparator — the plan-accepts / kernel-rejects divergence IsOrdered's own doc
// warns about.
//
// Adding `case TypeDecimal: return Int128` to Physical() fixes all of it at once,
// because everything that dispatches on the physical type already handles Int128.
// The tests below are in two halves: what that unlocks, and the three guards on
// the operations for which the unscaled integer is the WRONG answer.

// dec makes an unscaled Int128 from a whole number of units.
func dec(units int64) i128.Int128 { return i128.FromInt64(units) }

// prices is a Decimal(10,2) column: 12.34, 5.00, null, -0.05, 100.00.
func prices(t *testing.T) *ursus.LazyFrame {
	t.Helper()

	dt := dtype.Decimal(10, 2)
	schema, err := dtype.NewSchema(
		dtype.Field{Name: "price", Type: dt, Nullable: true},
		dtype.Field{Name: "sku", Type: dtype.String},
	)
	if err != nil {
		t.Fatal(err)
	}

	valid := bitmap.NewBuilder(5)
	for _, v := range []bool{true, true, false, true, true} {
		valid.Append(v)
	}
	b, err := data.NewBatch(schema, []*data.Column{
		data.NewFixed("price", dt,
			[]i128.Int128{dec(1234), dec(500), dec(0), dec(-5), dec(10000)},
			valid.Finish()),
		data.NewString("sku", []string{"a", "b", "c", "d", "e"}, bitmap.AllSet(5)),
	})
	if err != nil {
		t.Fatal(err)
	}
	src, err := memsrc.New(schema, b)
	if err != nil {
		t.Fatal(err)
	}
	return ursus.Scan(src)
}

// TestDecimalRendersScaled is the one that would fail most invisibly. A Decimal is
// stored as its unscaled integer, so without a render case 12.34 prints as "1234"
// — not a rounding error but a factor of 100, and entirely plausible on screen.
func TestDecimalRendersScaled(t *testing.T) {
	df, err := prices(t).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := df.String()

	for _, want := range []string{"12.34", "5.00", "null", "-0.05", "100.00"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered frame is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "1234 ") {
		t.Errorf("a Decimal printed as its unscaled storage:\n%s", got)
	}
	if !strings.Contains(got, "Decimal(10, 2)") {
		t.Errorf("the dtype header should show precision and scale:\n%s", got)
	}
}

// TestDecimalSortsAndFilters covers what the Physical() case unlocks. Ordering
// works on the unscaled integers precisely because every value in a column shares
// one scale, so comparing units is comparing values.
func TestDecimalSortsAndFilters(t *testing.T) {
	df, err := prices(t).
		Sort(ursus.Desc(ursus.Col("price")).NullsLast()).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("sorting a Decimal must work now that it has a physical type: %v", err)
	}

	skus, err := df.Column[string]("sku")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := range skus.Len() {
		v, _ := skus.Get(i)
		got = append(got, v)
	}
	// 100.00, 12.34, 5.00, -0.05, then the null.
	want := []string{"e", "a", "b", "d", "c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sort order = %v, want %v\n%s", got, want, df)
	}
}

// TestDecimalGroupsAndAggregatesPositionally: Min, Max, First, Last, Count and
// NUnique all work, because they select or count rather than compute — the value
// they carry keeps its own scale.
func TestDecimalMinMaxAndCount(t *testing.T) {
	df, err := prices(t).
		GroupBy(ursus.Lit(1).Alias("k")).
		Agg(
			ursus.Col("price").Min().Alias("lo"),
			ursus.Col("price").Max().Alias("hi"),
			ursus.Col("price").Count().Alias("n"),
			ursus.Col("price").NUnique().Alias("distinct"),
		).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	got := df.String()
	if !strings.Contains(got, "-0.05") || !strings.Contains(got, "100.00") {
		t.Errorf("min/max should keep the Decimal type and scale:\n%s", got)
	}
	n, _, err := df.At[uint64](0, "n")
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 { // count skips the null
		t.Errorf("count = %d, want 4\n%s", n, got)
	}
}

// TestDecimalGuards covers the three narrowing guards. Each one blocks a path that
// became REACHABLE the moment Decimal got a physical type, and each would return a
// number wrong by a factor of 10^scale rather than merely imprecise.
//
// All three fail at plan time with an actionable message. That matters more than
// usual here: the alternative is not an error, it is a plausible wrong number.
func TestDecimalGuards(t *testing.T) {
	cases := []struct {
		name string
		lf   func() *ursus.LazyFrame
		kind error
		want string
	}{
		{
			// 12.34 * 12.34 = 152.2756, scale 4. Promote returns the operand type
			// unchanged, so the result would be labelled scale 2 and print as
			// 15227.56.
			name: "multiplication changes the scale",
			lf:   func() *ursus.LazyFrame { return prices(t).Select(ursus.Col("price").Mul(ursus.Col("price"))) },
			kind: uerr.ErrType,
			want: "scale",
		},
		{
			// Would hand back the unscaled integer: 12.34 as 1234.
			name: "cast to Int64 would return unscaled units",
			lf:   func() *ursus.LazyFrame { return prices(t).Select(ursus.Col("price").Cast(dtype.Int64)) },
			kind: uerr.ErrUnsupported,
			want: "unscaled",
		},
		{
			// Would bind Acc: Float64 and then ask an Int128 column for []float64,
			// failing at runtime as an internal error.
			name: "sum has no committed precision rule",
			lf: func() *ursus.LazyFrame {
				return prices(t).GroupBy(ursus.Col("sku")).Agg(ursus.Col("price").Sum())
			},
			kind: uerr.ErrUnsupported,
			want: "precision and scale",
		},
		{
			name: "mean has no committed precision rule",
			lf: func() *ursus.LazyFrame {
				return prices(t).GroupBy(ursus.Col("sku")).Agg(ursus.Col("price").Mean())
			},
			kind: uerr.ErrUnsupported,
			want: "precision and scale",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.lf().Collect(t.Context())
			if err == nil {
				t.Fatal("expected an error; the unscaled integer would have leaked as a plausible wrong number")
			}
			if !errors.Is(err, c.kind) {
				t.Errorf("kind = %v, want %v: %v", err, c.kind, err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("message should explain %q:\n%v", c.want, err)
			}
			if errors.Is(err, uerr.ErrInternal) {
				t.Errorf("must be a user-facing refusal, not a reported ursus bug:\n%v", err)
			}
		})
	}
}

// TestDecimalAdditionIsExact: + and - ARE allowed, because equal scales mean the
// unscaled integers add directly. Refusing them alongside * would be over-broad.
func TestDecimalAdditionIsExact(t *testing.T) {
	df, err := prices(t).
		Select(ursus.Col("price").Add(ursus.Col("price")).Alias("doubled")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("Decimal + Decimal at the same scale is exact and must be allowed: %v", err)
	}
	if got := df.Schema().Field(0).Type; got != dtype.Decimal(10, 2) {
		t.Errorf("output type = %s, want Decimal(10, 2)", got)
	}
	if !strings.Contains(df.String(), "24.68") {
		t.Errorf("12.34 + 12.34 should be 24.68:\n%s", df)
	}
}
