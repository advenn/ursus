package ursus_test

// Int128 is where every integer Sum lands, so its casts and its own sum are the
// boundary where a 128-bit total becomes a number a caller reads. Two promises
// meet there, and both were broken:
//
//   - Cast is documented to fail "on the first unrepresentable value". Out of
//     Int128 it converted to Int64 through a float64, so a sum above 2^53 came back
//     rounded; into Int128 from a float it truncated, where the same cast into
//     Int64 refuses.
//   - The i128 package argues that its wrapping Add cannot wrap, because reaching
//     2^127 takes 2^63 rows of maximal Int64. True of inputs of 64 bits or fewer;
//     false of an Int128 column, where two rows are enough.
//
// None of them errors. Each is a plausible number of the right type. Since step 497
// the casts and the Int128 column's sums are Polars 2.1.0's answers, recorded in the
// int128exact group of testdata/oracle (oracle_int128exact_test.go); an Int64
// column's Sum, which Polars totals as an Int64 and wraps past 2^63 where ursus
// widens it (feature-gaps.md §9), is held to math/big over seeded values. What
// stays here asserts the refusals.

import (
	"errors"
	"math"
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source/memsrc"
)

// int128Frame is one Int128 column, "v", holding vals. The public API cannot build
// one — Values has no Int128 case — so it comes from the layer below, as it would
// from an Arrow or Parquet file.
func int128Frame(t *testing.T, vals ...i128.Int128) *ursus.LazyFrame {
	t.Helper()
	schema, err := dtype.NewSchema(dtype.Field{Name: "v", Type: dtype.Int128})
	if err != nil {
		t.Fatal(err)
	}
	b, err := data.NewBatch(schema, []*data.Column{
		data.NewFixed("v", dtype.Int128, vals, bitmap.AllSet(len(vals))),
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

// i128Of parses a base-10 Int128, failing the test on a malformed literal.
func i128Of(t *testing.T, s string) i128.Int128 {
	t.Helper()
	v, ok := i128.Parse(s)
	if !ok {
		t.Fatalf("i128.Parse(%q) failed", s)
	}
	return v
}

// only collects a one-row, one-column result and renders its single cell, or
// "null". An error is returned rather than failed on, because a refusal is the
// right answer for half of these cases.
func only[T any](t *testing.T, lf *ursus.LazyFrame, render func(T) string) (string, error) {
	t.Helper()
	df, err := lf.Collect(t.Context())
	if err != nil {
		return "", err
	}
	col, err := df.Column[T](df.Schema().Names()[0])
	if err != nil {
		t.Fatal(err)
	}
	v, ok := col.Get(0)
	if !ok {
		return "null", nil
	}
	return render(v), nil
}

func TestInt128BoundaryIsExactOrRefused(t *testing.T) {
	// 2^126 + 5, four of which total 2^128 + 20. Wrapping modulo 2^128 lands on
	// 20: a perfectly ordinary number, which no range check on the RESULT can
	// distinguish from a true 20.
	quarter := i128Of(t, "85070591730234615865843651857942052869")
	s128 := func(v i128.Int128) string { return v.String() }

	for _, c := range []struct {
		name string
		run  func() (string, error)
	}{
		{"float-int128-strict", func() (string, error) {
			// A fraction is refused; the lossy null and a whole float are recorded.
			return only(t, ursus.Frame(ursus.Values("f", []float64{3.7})).
				Select(ursus.Col("f").Cast(ursus.Int128)), s128)
		}},
		{"sum-wraps-once", func() (string, error) {
			return only(t, int128Frame(t, i128.Max, i128.One).
				GroupBy().Agg(ursus.Col("v").Sum()), s128)
		}},
		{"sum-wraps-fully", func() (string, error) {
			return only(t, int128Frame(t, quarter, quarter, quarter, quarter).
				GroupBy().Agg(ursus.Col("v").Sum()), s128)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.run()
			switch {
			case err == nil:
				t.Errorf("= %s, want a refusal", got)
			case !errors.Is(err, ursus.ErrValue):
				t.Errorf("refused with the wrong kind: %v", err)
			}
		})
	}
}

// TestInt64SumWidensExactly: every integer Sum is an Int128 (ResolveAggBinding),
// where Polars totals an Int64 column as an Int64 and wraps past 2^63, so Polars has
// no answer for a column whose total passes it (feature-gaps.md §9). math/big is the
// oracle: 300 seeded Int64s of the whole range, the extremes among them, summed
// whole, by group and as running totals, and a total cast to an Int64 or a Uint64
// where it fits, refused where it does not.
func TestInt64SumWidensExactly(t *testing.T) {
	c := ursus.Col
	rng := rand.New(rand.NewPCG(497, 128))
	vals := []int64{math.MaxInt64, math.MaxInt64, math.MinInt64, 1 << 53, 1<<53 + 1, -1, 0, 1, math.MaxInt64}
	for range 300 {
		vals = append(vals, rng.Int64()) // the whole range, both signs
	}
	groups := make([]int64, len(vals))
	byGroup := map[int64]*big.Int{}
	total := new(big.Int)
	prefixes := make([]string, len(vals))
	for i, v := range vals {
		groups[i] = int64(i % 7)
		if byGroup[groups[i]] == nil {
			byGroup[groups[i]] = new(big.Int)
		}
		byGroup[groups[i]].Add(byGroup[groups[i]], big.NewInt(v))
		total.Add(total, big.NewInt(v))
		prefixes[i] = total.String()
	}
	frame := ursus.Frame(ursus.Values("g", groups), ursus.Values("v", vals))
	s128 := func(v i128.Int128) string { return v.String() }

	got, err := only(t, frame.GroupBy().Agg(c("v").Sum()), s128)
	if err != nil || got != total.String() {
		t.Errorf("Sum = %s, %v; want %s", got, err, total)
	}
	df, err := frame.Select(c("v").CumSum(false)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	col, err := df.Column[i128.Int128]("v")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range prefixes {
		if v, ok := col.Get(i); !ok || v.String() != want {
			t.Errorf("CumSum row %d = %s (present %v), want %s", i, v, ok, want)
			break
		}
	}
	grouped, err := frame.GroupBy(c("g")).MaintainOrder().Agg(c("v").Sum()).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sums, err := grouped.Column[i128.Int128]("v")
	if err != nil {
		t.Fatal(err)
	}
	for i := range grouped.Height() {
		g, _, _ := grouped.At[int64](i, "g")
		if v, ok := sums.Get(i); !ok || v.String() != byGroup[g].String() {
			t.Errorf("group %d's Sum = %s (present %v), want %s", g, v, ok, byGroup[g])
		}
	}

	// The first three total MaxInt64 - 1, an Int64; the first two 2^64 - 2, a Uint64
	// and not an Int64.
	three := ursus.Frame(ursus.Values("v", vals[:3]))
	if got, err := only(t, three.GroupBy().Agg(c("v").Sum().Cast(ursus.Int64)), func(v int64) string { return big.NewInt(v).String() }); err != nil ||
		got != new(big.Int).Sub(big.NewInt(math.MaxInt64), big.NewInt(1)).String() {
		t.Errorf("Sum of [Max, Max, Min] cast to Int64 = %s, %v", got, err)
	}
	two := ursus.Frame(ursus.Values("v", vals[:2]))
	if got, err := only(t, two.GroupBy().Agg(c("v").Sum().Cast(ursus.Uint64)), func(v uint64) string { return new(big.Int).SetUint64(v).String() }); err != nil ||
		got != "18446744073709551614" {
		t.Errorf("Sum of [Max, Max] cast to Uint64 = %s, %v; want 18446744073709551614", got, err)
	}
	if _, err := two.GroupBy().Agg(c("v").Sum().Cast(ursus.Int64)).Collect(t.Context()); !errors.Is(err, ursus.ErrValue) {
		t.Errorf("Sum of [Max, Max] cast to Int64 = %v, want ErrValue", err)
	}
}

// TestInt128CumSumRefusesAWrappedPrefix: a cumulative publishes every prefix, so a
// running total that wraps has no value to show for that row — even if the total
// comes back into range later. It is refused where sum() of the same rows is not
// (the recorded i128_sum_max_1_minus_1 answers Max).
func TestInt128CumSumRefusesAWrappedPrefix(t *testing.T) {
	_, err := int128Frame(t, i128.Max, i128.One, i128.FromInt64(-1)).
		Select(ursus.Col("v").CumSum(false)).Collect(t.Context())
	if !errors.Is(err, ursus.ErrValue) {
		t.Fatalf("a prefix of Max+1 = %v, want ErrValue", err)
	}
}
