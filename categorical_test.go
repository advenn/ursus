package ursus_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/selector"
	"github.com/advenn/ursus/ursustest"
)

// TestCategorical is step 351: Polars' Categorical, text with a type of its own,
// ordered as its text is, its codes in the order its values first appear. Every answer
// is Polars 1.44.1's but where noted.
func TestCategorical(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(strs("s", "b", "a", "b", nil), strs("t", "b", "x", "y", "z"), i64s("v", 1, 2, 3, 4)).
		WithColumns(c("s").Cast(ursus.Categorical).Alias("c"))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		col  string
		want string
	}{
		{"the type", lf.Select(c("c")), "c", "b a b <nil> Categorical"},
		{"sorted as its text", lf.Select(c("c").Sort()), "c", "<nil> a b b Categorical"},
		{"the least and the most", lf.Select(c("c").Min().Alias("lo")), "lo", "a Categorical"},
		{"== a text", lf.Select(c("c").Eq("a").Alias("e")), "e", "false true false <nil> Bool"},
		{"< a text", lf.Select(c("c").Lt("b").Alias("e")), "e", "false true false <nil> Bool"},
		{"== a String column", lf.Select(c("c").Eq(c("t")).Alias("e")), "e", "true false false <nil> Bool"},
		{"< a String column", lf.Select(c("c").Lt(c("t")).Alias("e")), "e", "false true true <nil> Bool"},
		{"its codes", lf.Select(c("c").ToPhysical()), "c", "0 1 0 <nil> Uint32"},
		{"Cat().Physical", lf.Select(c("c").Cat().Physical()), "c", "0 1 0 <nil> Uint32"},
		// A null takes no code, before a new value too.
		{"codes after a null", ursus.Frame(strs("s", nil, "b", "a")).Select(c("s").Cast(ursus.Categorical).ToPhysical()), "s", "<nil> 0 1 Uint32"},
		{"its mode", lf.Select(c("c").Mode(false)), "c", "b Categorical"},
		{"as text", lf.Select(c("c").Cast(ursus.String)), "c", "b a b <nil> String"},
		{"as an Enum", lf.Select(c("c").Cast(ursus.Enum("a", "b"))), "c", "b a b <nil> Enum(a, b)"},
		{"an Enum as one", lf.Select(c("c").Cast(ursus.Enum("a", "b")).Cast(ursus.Categorical)), "c", "b a b <nil> Categorical"},
		{"a null filled", lf.Select(c("c").FillNullWith("z")), "c", "b a b z Categorical"},
		{"is_in", lf.Select(c("c").IsIn("a").Alias("e")), "e", "false true false <nil> Bool"},
		{"its rank", lf.Filter(c("c").IsNotNull()).Select(c("c").Rank(ursus.RankAverage, false).Alias("r")), "r", "2.5 1 2.5 Float64"},
		{"Cat().StartsWith", lf.Select(c("c").Cat().StartsWith("b")), "c", "true false true <nil> Bool"},
		{"Cat().EndsWith", lf.Select(c("c").Cat().EndsWith("a")), "c", "false true false <nil> Bool"},
		{"Cat().LenBytes", lf.Select(c("c").Cat().LenBytes()), "c", "1 1 1 <nil> Uint32"},
		{"Cat().LenChars", lf.Select(c("c").Cat().LenChars()), "c", "1 1 1 <nil> Uint32"},
		{"Cat().Slice, a String", lf.Select(c("c").Cat().Slice(0, 1)), "c", "b a b <nil> String"},
		{"grouped", lf.GroupBy(c("c")).MaintainOrder().Agg(c("v").Sum()), "c", "b a <nil> Categorical"},
		{"unique", lf.Select(c("c").Unique()).Sort(ursus.Asc(c("c"))), "c", "<nil> a b Categorical"},
		{"concatenated", ursus.Concat([]*ursus.LazyFrame{lf.Select(c("c")), lf.Select(c("c"))}), "c", "b a b <nil> b a b <nil> Categorical"},
		// A String's functions read its text, as an Enum's, where Polars refuses them
		// ("expected String type, got: cat"; feature-gaps §9).
		{"a String's function", lf.Select(c("c").Str().ToUpper()), "c", "B A B <nil> String"},
	} {
		if got := cells(t, tc.q, tc.col); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}

	// A join of two Categoricals, by their text; and of one and a String, by the text
	// they meet at, as an Enum's, where Polars refuses it ("datatypes of join keys
	// don't match"; feature-gaps §9).
	right := ursus.Frame(strs("c", "a"), i64s("w", 9)).WithColumns(c("c").Cast(ursus.Categorical))
	if got := cells(t, lf.Select(c("c"), c("v")).Join(right, ursus.JoinOn(c("c"))), "w"); got != "9 Int64" {
		t.Errorf("a join: %s", got)
	}
	if got := cells(t, lf.Select(c("c")).Join(ursus.Frame(strs("c", "a"), i64s("w", 7)), ursus.JoinOn(c("c"))), "w"); got != "7 Int64" {
		t.Errorf("a join on a String: %s", got)
	}

	// Refused as Polars refuses them.
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		// Polars reads a number as a category's code, and deprecates it; refused.
		{"to a number", lf.Select(c("c").Cast(ursus.Int64)), "Categorical"},
		// Polars deprecates it: "use Expr.unique()".
		{"get_categories", lf.Select(c("c").Cat().GetCategories()), ".Unique()"},
	} {
		if _, err := tc.q.Collect(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error saying %q", tc.name, err, tc.want)
		}
	}
}

// TestCategoricalAcrossBatches: a Categorical's codes are its frame's, the order its
// values first appear in over every batch, not each batch's own; its group-by keys
// spill and come back Categorical; it reads from CSV and NDJSON by a schema; a
// group-by's aggregation refuses its codes; and selector.Categorical selects it.
func TestCategoricalAcrossBatches(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	const n = 5000
	vals := make([]string, n)
	want := make([]string, n)
	codes := map[string]int{}
	for i := range vals {
		vals[i] = fmt.Sprintf("v%d", (i*7919)%97)
		if _, ok := codes[vals[i]]; !ok {
			codes[vals[i]] = len(codes)
		}
		want[i] = fmt.Sprint(codes[vals[i]])
	}
	lf := ursus.Frame(ursus.Values("s", vals)).Select(c("s").Cast(ursus.Categorical).Alias("c"), c("s"))
	got, err := lf.Select(c("c").Cat().Physical()).Collect(ctx, ursus.WithBatchSize(64))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := got.ToMaps()
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		if fmt.Sprint(r["c"]) != want[i] {
			t.Fatalf("row %d: code %v, want %s", i, r["c"], want[i])
		}
	}

	// A group-by of many of them past a memory limit spills, and its keys come back
	// Categorical.
	many := make([]string, 20000)
	for i := range many {
		many[i] = fmt.Sprintf("key-%05d", (i*7919)%20000)
	}
	q := ursus.Frame(ursus.Values("s", many)).Select(c("s").Cast(ursus.Categorical).Alias("c")).
		GroupBy(c("c")).Agg(ursus.Len().Alias("k"))
	spilled, stats, err := spillCollect(t, q, 32<<10, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Spills == 0 {
		t.Error("a group-by of 20,000 keys under 32 KiB did not spill")
	}
	ursustest.AssertFrameEqual(t, spilled, collect(t, q), ursustest.IgnoreRowOrder())
	if s := spilled.Schema().String(); !strings.HasPrefix(s, "{c: Categorical") {
		t.Errorf("spilled keys: %s", s)
	}

	// From CSV and NDJSON by a schema.
	schema := mustSchema(ursus.Field{Name: "c", Type: ursus.Categorical, Nullable: true})
	if got := cells(t, ursus.ScanCSVReader([]byte("c\nb\na\n"), "t", ursus.WithSchema(schema)), "c"); got != "b a Categorical" {
		t.Errorf("CSV: %s", got)
	}
	if got := cells(t, ursus.ScanNDJSONBytes([]byte("{\"c\":\"b\"}\n"), "t", ursus.WithNDJSONSchema(schema)), "c"); got != "b Categorical" {
		t.Errorf("NDJSON: %s", got)
	}

	// Codes in a group-by's aggregation would be its group's.
	if _, err := lf.GroupBy(c("s")).Agg(c("c").ToPhysical()).Collect(ctx); err == nil || !strings.Contains(err.Error(), "before the group-by") {
		t.Errorf("codes in an aggregation: %v", err)
	}
	if _, err := lf.Select(c("c").ToPhysical().Over(c("s"))).Collect(ctx); err == nil || !strings.Contains(err.Error(), "before the group-by or the window") {
		t.Errorf("codes in a window: %v", err)
	}

	if got := cells(t, lf.Select(selector.Categorical().Expr()).Head(2), "c"); !strings.HasSuffix(got, "Categorical") {
		t.Errorf("selector.Categorical: %s", got)
	}
}
