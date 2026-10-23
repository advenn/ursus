package ursus_test

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestInsertColumn is C5 of v0.6.7-scope.md (step 302): Polars'
// DataFrame.insert_column, of an expression or Go values. Every answer is Polars
// 1.44.1's.
func TestInsertColumn(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(
		ursus.Values("a", []int64{1, 2, 3}),
		ursus.Values("b", []string{"x", "y", "z"}),
		ursus.ValuesNullable("c", []float64{1.5, 0, 2.5}, []bool{true, false, true}),
	)
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"values at 1", lf.InsertColumn(1, ursus.Values("n", []int64{7, 8, 9})),
			"a,n,b,c\n1,7,x,1.5\n2,8,y,\n3,9,z,2.5\n"},
		{"an expression at 0", lf.InsertColumn(0, c("a").Mul(10).Alias("a10")),
			"a10,a,b,c\n10,1,x,1.5\n20,2,y,\n30,3,z,2.5\n"},
		{"at the width", lf.InsertColumn(3, c("a").Alias("z")), "a,b,c,z\n1,x,1.5,1\n2,y,,2\n3,z,2.5,3\n"},
		{"at -1", lf.InsertColumn(-1, c("a").Alias("m1")), "a,b,m1,c\n1,x,1,1.5\n2,y,2,\n3,z,3,2.5\n"},
		{"at -3", lf.InsertColumn(-3, c("a").Alias("m3")), "m3,a,b,c\n1,1,x,1.5\n2,2,y,\n3,3,z,2.5\n"},
		// An aggregate and a literal are broadcast, as Polars' are.
		{"an aggregate", lf.InsertColumn(1, c("a").Sum().Alias("s")), "a,s,b,c\n1,6,x,1.5\n2,6,y,\n3,6,z,2.5\n"},
		{"a literal", lf.InsertColumn(1, ursus.Lit(int64(5)).Alias("five")),
			"a,five,b,c\n1,5,x,1.5\n2,5,y,\n3,5,z,2.5\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}

	t.Run("refused", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			q    *ursus.LazyFrame
			want string
		}{
			// Polars: column index 4 is out of range (frame has 3 columns).
			{"past the width", lf.InsertColumn(4, c("a").Alias("z")), "column index 4 is out of range: the frame has 3 columns"},
			{"past the start", lf.InsertColumn(-4, c("a").Alias("z")), "column index -4 is out of range"},
			// Polars: duplicate output name 'a'.
			{"a name the frame has", lf.InsertColumn(1, c("a")), `the frame has a column "a" already`},
			{"values of a name the frame has", lf.InsertColumn(1, ursus.Values("b", []int64{1, 2, 3})), `"b" already`},
			// Polars: unable to add a column of length 2 to a DataFrame of height 3.
			{"too few values", lf.InsertColumn(1, ursus.Values("n", []int64{7, 8})), "height"},
			{"two columns", lf.InsertColumn(1, c("a", "c").Mul(2)), "inserts one column, and the expression gives 2"},
			{"no column", lf.InsertColumn[*ursus.Column](1, nil), "the column is nil"},
		} {
			_, err := tc.q.Collect(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
			}
		}
	})

	t.Run("eager", func(t *testing.T) {
		df, err := lf.Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		got, err := df.InsertColumn(t.Context(), 2, ursus.Values("n", []string{"p", "q", "r"}))
		if err != nil {
			t.Fatal(err)
		}
		want, err := lf.InsertColumn(2, ursus.Values("n", []string{"p", "q", "r"})).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
		if got, err = df.InsertColumn(t.Context(), 0, c("b").Str().ToUpper().Alias("B")); err != nil {
			t.Fatal(err)
		}
		if names := strings.Join(got.Schema().Names(), ","); names != "B,a,b,c" {
			t.Errorf("columns %s", names)
		}
	})
}

// TestHashRows is C5 of v0.6.7-scope.md (step 302): Polars' DataFrame.hash_rows, and
// an expression of it. The numbers are ursus's own (v0.6.6-scope.md §6.3), so the test
// holds the properties Polars' have: rows equal as a group-by sees them hash alike,
// other rows apart, the seed and the columns' order count, -0 is 0 and NaN NaN.
func TestHashRows(t *testing.T) {
	c := ursus.Col
	nan := math.NaN()
	lf := ursus.Frame(
		ursus.ValuesNullable("a", []int64{1, 2, 1, 0, 0}, []bool{true, true, true, false, false}),
		ursus.ValuesNullable("b", []string{"x", "y", "x", "", ""}, []bool{true, true, true, false, false}),
		ursus.ValuesNullable("c", []float64{1.5, 0, 1.5, 0, nan}, []bool{true, true, true, false, true}),
	)
	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	hashes := func(t *testing.T, df *ursus.DataFrame, seed uint64) []uint64 {
		t.Helper()
		s, err := df.HashRows(t.Context(), seed)
		if err != nil {
			t.Fatal(err)
		}
		if s.Name() != "" || s.Len() != df.Height() || s.NullCount() != 0 {
			t.Fatalf("a Series %q of %d values and %d nulls", s.Name(), s.Len(), s.NullCount())
		}
		return s.Values()
	}
	h := hashes(t, df, 0)
	if h[0] != h[2] {
		t.Errorf("equal rows hash apart: %d and %d", h[0], h[2])
	}
	// Rows 0, 1, 3 and 4 differ: 3 is every column null, 4 has a NaN for a null.
	for _, pair := range [][2]int{{0, 1}, {0, 3}, {3, 4}, {1, 4}} {
		if h[pair[0]] == h[pair[1]] {
			t.Errorf("rows %d and %d hash alike", pair[0], pair[1])
		}
	}
	if slices.Equal(hashes(t, df, 1), h) {
		t.Error("a seed changes nothing")
	}
	swapped, err := lf.Select(c("b"), c("a"), c("c")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(hashes(t, swapped, 0), h) {
		t.Error("the columns' order counts for nothing")
	}

	t.Run("as a group-by keys", func(t *testing.T) {
		zeros, err := ursus.Frame(ursus.Values("f", []float64{0, math.Copysign(0, -1), nan, nan})).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		z := hashes(t, zeros, 0)
		if z[0] != z[1] || z[2] != z[3] || z[0] == z[2] {
			t.Errorf("0, -0, NaN, NaN hash %v", z)
		}
	})

	t.Run("an expression", func(t *testing.T) {
		// The frame's hashes, one column's Hash, a batch at a time, and a literal
		// broadcast.
		got, err := lf.WithColumns(ursus.Lit(int64(5)).Alias("five")).
			Select(ursus.HashRows(0, c("a"), c("b"), c("c")).Alias("h"), ursus.HashRows(0, c("a")).Alias("one"),
				c("a").Hash(0).Alias("hash"), ursus.HashRows(9, c("a")).Alias("a9")).
			Collect(t.Context(), ursus.WithBatchSize(1))
		if err != nil {
			t.Fatal(err)
		}
		col := func(name string) []uint64 {
			s, err := got.Column[uint64](name)
			if err != nil {
				t.Fatal(err)
			}
			return s.Values()
		}
		if !slices.Equal(col("h"), h) {
			t.Errorf("the expression %v, the frame's %v", col("h"), h)
		}
		if !slices.Equal(col("one"), col("hash")) {
			t.Errorf("one column's row hash %v, its Hash %v", col("one"), col("hash"))
		}
		// A literal operand is a row met to the batch's, of five rows here: the hash
		// of a column of it.
		whole, err := lf.WithColumns(ursus.Lit(int64(5)).Alias("five")).
			Select(ursus.HashRows(9, ursus.Lit(int64(5)), c("a")).Alias("lit"),
				ursus.HashRows(9, c("five"), c("a")).Alias("column")).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		lit, err := whole.Column[uint64]("lit")
		if err != nil {
			t.Fatal(err)
		}
		column, err := whole.Column[uint64]("column")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(lit.Values(), column.Values()) || slices.Equal(lit.Values(), col("a9")) {
			t.Errorf("a literal operand %v, a column of it %v, none %v", lit.Values(), column.Values(), col("a9"))
		}
	})

	t.Run("a List", func(t *testing.T) {
		// A List is hashed as a key is since step 344, element by element: rows 0 and
		// 2 are alike, a List among them.
		lists := lf.WithColumns(c("b").Str().Split(",").Alias("l"))
		ld, err := lists.Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if h := hashes(t, ld, 0); h[0] != h[2] || h[0] == h[1] {
			t.Errorf("a List's rows hashed %v", h)
		}
		none, err := ursus.Frame().Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := none.HashRows(t.Context(), 0); err == nil || !strings.Contains(err.Error(), "hash_rows of no expression") {
			t.Errorf("no column: got %v", err)
		}
	})
}
