package ursus_test

// Step 178: three open audit rows. S26: integer arithmetic wraps, as Polars'; S21:
// ToUpper and ToLower map case fully, as Polars'; I26: Rows decodes a List. The
// expected answers are Polars 1.44.1's, from the bench's own environment.

import (
	"math"
	"slices"
	"testing"

	"github.com/advenn/ursus"
)

// TestIntegerArithmeticWrapsAsPolars pins S26's decision: an integer result that
// leaves its type wraps, MinInt // −1 included, as Polars answers every one.
func TestIntegerArithmeticWrapsAsPolars(t *testing.T) {
	c := ursus.Col
	df, err := ursus.Frame(ursus.Values("a", []int64{math.MaxInt64, math.MinInt64})).Select(
		c("a").Add(int64(1)).Alias("add"), c("a").Mul(int64(-1)).Alias("mul"),
		c("a").FloorDiv(int64(-1)).Alias("fd"), c("a").Mod(int64(-1)).Alias("mod"),
		c("a").Sub(int64(1)).Alias("sub"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"add": "-9223372036854775808 -9223372036854775807 ",
		"mul": "-9223372036854775807 -9223372036854775808 ",
		"fd":  "-9223372036854775807 -9223372036854775808 ",
		"mod": "0 0 ",
		"sub": "9223372036854775806 9223372036854775807 ",
	} {
		if got := rendered172(t, df, name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	df, err = ursus.Frame(ursus.Values("b", []int32{math.MaxInt32, math.MinInt32})).Select(
		c("b").Add(ursus.Lit(int32(1))).Alias("add"), c("b").FloorDiv(ursus.Lit(int32(-1))).Alias("fd"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if a, f := rendered172(t, df, "add"), rendered172(t, df, "fd"); a != "-2147483648 -2147483647 " ||
		f != "-2147483647 -2147483648 " {
		t.Errorf("Int32: add %q, floor_div %q", a, f)
	}
}

func TestCaseMappingIsFullAsPolars(t *testing.T) {
	c := ursus.Col
	for _, tc := range []struct{ in, upper, lower string }{
		{"ß", "SS", "ß"},
		{"straße", "STRASSE", "straße"},
		{"ﬁx", "FIX", "ﬁx"},
		{"ŉ", "ʼN", "ŉ"},
		{"ǰ", "J̌", "ǰ"},
		{"\u0390", "\u0399\u0308\u0301", "\u0390"},
		{"ᾳ", "ΑΙ", "ᾳ"},
		{"İstanbul", "İSTANBUL", "i̇stanbul"},
		{"ΑΣ", "ΑΣ", "ας"},
		{"ΑΣ.", "ΑΣ.", "ας."},
		{"Σ", "Σ", "σ"},
		{"ΑΣΑ", "ΑΣΑ", "ασα"},
		{"ΑΣ1", "ΑΣ1", "ας1"},
		{"ΟΔΟΣ ΟΔΟΣ", "ΟΔΟΣ ΟΔΟΣ", "οδος οδος"},
		{"ΑΣ'Α", "ΑΣ'Α", "ασ'α"},
		{"ΑΣ·", "ΑΣ·", "ας·"},
		{"aΣb", "AΣB", "aσb"},
		{"AΣ", "AΣ", "aς"},
		{"Σa", "ΣA", "σa"},
		{"ΣΣ", "ΣΣ", "σς"},
		{"ΑΣ́", "ΑΣ́", "ας́"},
		{"́Σ", "́Σ", "́σ"},
		// A mark or a soft hyphen between is skipped, either side.
		{"Α\u0301Σ", "Α\u0301Σ", "α\u0301ς"},
		{"ΑΣ\u0301Α", "ΑΣ\u0301Α", "ασ\u0301α"},
		{"Α\u00adΣ", "Α\u00adΣ", "α\u00adς"},
		{"abc", "ABC", "abc"},
		{"ÀÉÎ", "ÀÉÎ", "àéî"},
	} {
		df, err := ursus.Frame(ursus.Values("s", []string{tc.in})).Select(
			c("s").Str().ToUpper().Alias("u"), c("s").Str().ToLower().Alias("l")).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		u, _ := df.Column[string]("u")
		l, _ := df.Column[string]("l")
		if got, _ := u.Get(0); got != tc.upper {
			t.Errorf("upper of %q: %q, want %q", tc.in, got, tc.upper)
		}
		if got, _ := l.Get(0); got != tc.lower {
			t.Errorf("lower of %q: %q, want %q", tc.in, got, tc.lower)
		}
	}
}

// TestRowsDecodesAList: I26. A List decodes into a slice, its null elements as zero
// or, through a pointer, nil; a null list into a nil slice; a list of lists into a
// slice of slices; and a Binary column into a []byte.
func TestRowsDecodesAList(t *testing.T) {
	c := ursus.Col
	df, err := ursus.Frame(
		ursus.Values("k", []string{"a", "a", "a", "b", "c"}),
		ursus.ValuesNullable("v", []int64{1, 0, 3, 4, 0}, []bool{true, false, true, true, false}),
	).GroupBy(c("k")).Agg(c("v").Implode()).Sort(ursus.Asc(c("k"))).
		WithColumns(c("v").Implode().Over(ursus.Lit(1)).Alias("vv"), c("k").Cast(ursus.Binary).Alias("bin")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		K   string
		V   []int64
		VP  []*int64 `ursus:"v"`
		VV  [][]int64
		Bin []byte
	}
	rows, err := df.Rows[row]()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || !slices.Equal(rows[0].V, []int64{1, 0, 3}) || !slices.Equal(rows[1].V, []int64{4}) ||
		!slices.Equal(rows[2].V, []int64{0}) {
		t.Fatalf("v: %+v", rows)
	}
	if rows[0].VP[1] != nil || *rows[0].VP[2] != 3 || rows[2].VP[0] != nil {
		t.Errorf("a null element through a pointer: %v", rows[0].VP)
	}
	if len(rows[1].VV) != 3 || !slices.Equal(rows[1].VV[1], []int64{4}) {
		t.Errorf("a list of lists: %v", rows[1].VV)
	}
	if string(rows[2].Bin) != "c" {
		t.Errorf("binary: %q", rows[2].Bin)
	}

	// A null list is a nil slice; an empty one is empty.
	type one struct{ L []int64 }
	lists, err := ursus.Frame(ursus.Values("k", []int64{1, 2})).
		WithColumns(c("k").Implode().Over(c("k")).Alias("L")).Select(c("L").Shift(1)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err := lists.Rows[one]()
	if err != nil {
		t.Fatal(err)
	}
	if got[0].L != nil || !slices.Equal(got[1].L, []int64{1}) {
		t.Errorf("a null list: %+v", got)
	}
}

// TestRowsReadsTheUrsusTag: a field tagged `ursus:"name"` reads that column, though
// its own name matches none. The module's rename had made the tag key the module
// path, so no tag was read and this was refused.
func TestRowsReadsTheUrsusTag(t *testing.T) {
	df, err := ursus.Frame(ursus.Values("unit_price", []float64{2.5}), ursus.Values("id", []int64{7})).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		Price float64 `ursus:"unit_price"`
		Key   int64   `ursus:"id,omitempty"`
		Skip  string  `ursus:"-"`
	}
	rows, err := df.Rows[row]()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Price != 2.5 || rows[0].Key != 7 {
		t.Errorf("%+v", rows)
	}
}
