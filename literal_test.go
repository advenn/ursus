package ursus_test

import (
	"testing"

	"github.com/advenn/ursus"
)

// TestLiteralExpressionsInEveryOperator is the regression test for a crash found
// while testing something else.
//
// An expression with no column reference evaluates to a ONE-ROW column, because
// that is all the information there is. Six operators evaluate expressions against
// a batch and put the result alongside the batch's other columns; three of them
// broadcast the result to the batch height and three did not. The three that did
// not each failed differently, and one of them panicked:
//
//	GroupBy(Lit(1))    panic: index out of range [1] with length 1
//	Sort(Lit(1))       "a bug in ursus" — a batch row-count mismatch
//	Filter(Lit(true))  a mask shorter than the batch it filters
//
// All six now go through physical.evalColumn. Each case below covers one operator,
// so a seventh operator that forgets is caught by whichever of these it resembles.
func TestLiteralExpressionsInEveryOperator(t *testing.T) {
	frame := func() *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("x", []int64{1, 2, 3, 4}))
	}

	cases := []struct {
		name string
		lf   func() *ursus.LazyFrame
		rows int
	}{
		{"group by a literal", func() *ursus.LazyFrame {
			// The case that panicked: every row lands in one group, but the key
			// encoder still asks for row i of a one-row key column.
			return frame().GroupBy(ursus.Lit(1).Alias("k")).Agg(ursus.Len().Alias("n"))
		}, 1},

		{"sort by a literal", func() *ursus.LazyFrame {
			// Degenerate but legal, and it must not be an internal error.
			return frame().Sort(ursus.Asc(ursus.Lit(1)))
		}, 4},

		{"filter on a literal true", func() *ursus.LazyFrame {
			return frame().Filter(ursus.Lit(true))
		}, 4},

		{"filter on a literal false", func() *ursus.LazyFrame {
			return frame().Filter(ursus.Lit(false))
		}, 0},

		{"select only a literal", func() *ursus.LazyFrame {
			return frame().Select(ursus.Lit(7).Alias("seven"))
		}, 4},

		{"with_columns a literal", func() *ursus.LazyFrame {
			return frame().WithColumns(ursus.Lit(7).Alias("seven"))
		}, 4},

		{"aggregate a literal", func() *ursus.LazyFrame {
			return frame().GroupBy(ursus.Col("x")).Agg(ursus.Lit(1).Sum().Alias("ones"))
		}, 4},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			df, err := c.lf().Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatalf("a literal expression must not fail: %v", err)
			}
			if df.Height() != c.rows {
				t.Errorf("got %d rows, want %d\n%s", df.Height(), c.rows, df)
			}
		})
	}
}
