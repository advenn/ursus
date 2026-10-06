package ursus_test

// Int128 arithmetic is exact or refused, with * // % alongside + - (step 100).
//
// Every integer Sum, Bool Sum and UInt64 Diff is an Int128, and Int64 * UInt64
// promotes to one, but only + and - were implemented: `Col("x").Sum().Mul(2)` was
// refused at plan time. And + and - wrapped, so the type that exists to keep a sum
// exact gave a wrapped sum for Max + Max.

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source/memsrc"
)

// int128FrameValid is int128Frame with nulls: valid[i] false makes row i null,
// whatever vals[i] holds.
func int128FrameValid(t *testing.T, vals []i128.Int128, valid []bool) *ursus.LazyFrame {
	t.Helper()
	schema := dtype.MustSchema(dtype.Of("v", dtype.Int128))
	vb := bitmap.NewBuilder(len(valid))
	for _, v := range valid {
		vb.Append(v)
	}
	b, err := data.NewBatch(schema, []*data.Column{data.NewFixed("v", dtype.Int128, vals, vb.Finish())})
	if err != nil {
		t.Fatal(err)
	}
	src, err := memsrc.New(schema, b)
	if err != nil {
		t.Fatal(err)
	}
	return ursus.Scan(src)
}

// knownInt128ArithDefects names each case that answers wrongly today, with what it
// answers.
var knownInt128ArithDefects = map[string]string{}

type int128ArithCase struct {
	name string
	lf   func(t *testing.T) *ursus.LazyFrame
	run  func(t *testing.T, lf *ursus.LazyFrame) string
}

func TestInt128Arithmetic(t *testing.T) {
	c := ursus.Col
	values := func(want ...string) func(t *testing.T, lf *ursus.LazyFrame) string {
		return func(t *testing.T, lf *ursus.LazyFrame) string {
			df, err := lf.Collect(t.Context())
			if err != nil {
				return err.Error()
			}
			typ, got := textOf(t, df, "y")
			if typ != "Int128" {
				return fmt.Sprintf("type %s, want Int128", typ)
			}
			if !slices.Equal(got, want) {
				return fmt.Sprintf("%v, want %v", got, want)
			}
			return ""
		}
	}
	refused := func(t *testing.T, lf *ursus.LazyFrame) string {
		_, err := lf.Collect(t.Context())
		if err == nil {
			return "ran, where the exact answer is outside Int128"
		}
		if !errors.Is(err, ursus.ErrValue) {
			return "refused, but not as a value error: " + err.Error()
		}
		return ""
	}
	maxes := ursus.Frame(ursus.Values("x", []int64{math.MaxInt64, math.MaxInt64}))
	signs := func(t *testing.T) *ursus.LazyFrame {
		return int128Frame(t, i128.FromInt64(-7), i128.FromInt64(7), i128.FromInt64(-7), i128.FromInt64(7), i128.FromInt64(6))
	}
	divisors := ursus.Frame(ursus.Values("d", []int64{2, -2, -2, 2, 0}))

	cases := []int128ArithCase{
		{"a sum times two", func(t *testing.T) *ursus.LazyFrame {
			return maxes.GroupBy().Agg(c("x").Sum().Mul(2).Alias("y"))
		}, values("36893488147419103228")},
		{"a sum times itself", func(t *testing.T) *ursus.LazyFrame {
			return ursus.Frame(ursus.Values("x", []int64{math.MaxInt64})).
				GroupBy().Agg(c("x").Sum().Mul(c("x").Sum()).Alias("y"))
		}, values("85070591730234615847396907784232501249")},
		{"Int64 * UInt64, which promotes to Int128", func(t *testing.T) *ursus.LazyFrame {
			return ursus.Frame(ursus.Values("a", []int64{-3}), ursus.Values("b", []uint64{math.MaxUint64})).
				Select(c("a").Mul(c("b")).Alias("y"))
		}, values("-55340232221128654845")},
		{"floor division, every sign, and a zero divisor is null", func(t *testing.T) *ursus.LazyFrame {
			return signs(t).HStack(divisors).Select(c("v").FloorDiv(c("d")).Alias("y"))
		}, values("-4", "-4", "3", "3", "∅")},
		{"modulo takes the divisor's sign, and a zero divisor is null", func(t *testing.T) *ursus.LazyFrame {
			return signs(t).HStack(divisors).Select(c("v").Mod(c("d")).Alias("y"))
		}, values("1", "-1", "-1", "1", "∅")},
		{"a sum floor-divided by a literal", func(t *testing.T) *ursus.LazyFrame {
			return maxes.GroupBy().Agg(c("x").Sum().FloorDiv(-3).Alias("y"))
		}, values("-6148914691236517205")},
		{"Min % -1 is 0", func(t *testing.T) *ursus.LazyFrame {
			return int128Frame(t, i128.Min).Select(c("v").Mod(-1).Alias("y"))
		}, values("0")},

		{"a product past Int128 is refused", func(t *testing.T) *ursus.LazyFrame {
			return maxes.GroupBy().Agg(c("x").Sum().Mul(c("x").Sum()).Alias("y"))
		}, refused},
		{"Max + Max is refused, not wrapped", func(t *testing.T) *ursus.LazyFrame {
			return int128Frame(t, i128.Max).Select(c("v").Add(c("v")).Alias("y"))
		}, refused},
		{"Min - 1 is refused, not wrapped", func(t *testing.T) *ursus.LazyFrame {
			return int128Frame(t, i128.Min).Select(c("v").Sub(1).Alias("y"))
		}, refused},
		{"Min // -1 is refused", func(t *testing.T) *ursus.LazyFrame {
			return int128Frame(t, i128.Min).Select(c("v").FloorDiv(-1).Alias("y"))
		}, refused},
		{"an overflow in a null row is not judged", func(t *testing.T) *ursus.LazyFrame {
			// Max sits under the null, where nobody can see it.
			return int128FrameValid(t, []i128.Int128{i128.Max, i128.One}, []bool{false, true}).
				Select(c("v").Add(c("v")).Alias("y"))
		}, values("∅", "2")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrong := tc.run(t, tc.lf(t))
			why, known := knownInt128ArithDefects[tc.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownInt128ArithDefects (%s)", why)
			case known:
				t.Logf("known defect (%s)", why)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownInt128ArithDefects {
		if !slices.ContainsFunc(cases, func(c int128ArithCase) bool { return c.name == name }) {
			t.Errorf("knownInt128ArithDefects names %q, which is not a case", name)
		}
	}
}
