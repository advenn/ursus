package ursus_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestFromRows is V1 of v0.6.7-scope.md (step 317): a frame from Go structs, a column
// a field, as Polars builds one from dataclasses, and Rows' inverse.
func TestFromRows(t *testing.T) {
	ctx := t.Context()
	type point struct{ X, Y float64 }
	type sale struct {
		ID     int64     `ursus:"id"`
		Price  float64   `ursus:"unit_price"`
		Note   *string   // nil is null
		Tags   []string  // a List; nil a null list
		At     time.Time `ursus:"at"`
		Wait   time.Duration
		Where  point
		Small  int8
		Big    uint64
		Raw    []byte
		Skip   string `ursus:"-"`
		hidden int
	}
	note := "rush"
	rows := []sale{
		{ID: 1, Price: 9.5, Note: &note, Tags: []string{"a", "b"}, At: at(2024, 1, 2, 3), Wait: time.Second,
			Where: point{1, 2}, Small: -3, Big: 1 << 63, Raw: []byte{0, 1}, Skip: "x", hidden: 7},
		{ID: 2, Price: 0.5, Tags: []string{}, Where: point{3, 4}},
	}
	df, err := ursus.FromRows(rows).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := df.Columns(), []string{"id", "unit_price", "Note", "Tags", "at", "Wait", "Where", "Small", "Big", "Raw"}; !slices.Equal(got, want) {
		t.Errorf("the columns are %q, want %q", got, want)
	}
	types := map[string]string{"id": "Int64", "unit_price": "Float64", "Note": "String", "Tags": "List(String)",
		"at": "Datetime(ns, UTC)", "Wait": "Duration(ns)", "Where": "Struct", "Small": "Int8", "Big": "Uint64", "Raw": "Binary"}
	for _, f := range df.Schema().All() {
		if !strings.HasPrefix(f.Type.String(), types[f.Name]) {
			t.Errorf("%s is %s, want %s", f.Name, f.Type, types[f.Name])
		}
	}
	// And back: Rows decodes what FromRows encoded.
	back, err := df.Rows[sale]()
	if err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		w := rows[i]
		w.Skip, w.hidden = "", 0
		g := back[i]
		if g.ID != w.ID || g.Price != w.Price || (g.Note == nil) != (w.Note == nil) || g.Note != nil && *g.Note != *w.Note ||
			!slices.Equal(g.Tags, w.Tags) || !g.At.Equal(w.At) || g.Wait != w.Wait || g.Where != w.Where ||
			g.Small != w.Small || g.Big != w.Big || string(g.Raw) != string(w.Raw) {
			t.Errorf("row %d came back %+v, want %+v", i, g, w)
		}
	}
	// A nil list is a null, an empty one a list of none.
	nilTags, err := ursus.FromRows([]sale{{}, {Tags: []string{}}}).Select(ursus.Col("Tags").IsNull()).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, err := ursus.Frame(ursus.Values("Tags", []bool{true, false})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, nilTags, want)

	t.Run("a nil number is a null", func(t *testing.T) {
		type scored struct {
			Score *int64 `ursus:"score"`
			When  *time.Time
		}
		five := int64(5)
		df, err := ursus.FromRows([]scored{{Score: &five}, {}}).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"score", "When"} {
			c, _ := df.Schema().ByName(name)
			if n := df.ColumnAt(df.Schema().IndexOf(name)).NullCount(); n != 2-boolInt(name == "score") {
				t.Errorf("%s (%s) has %d nulls", name, c.Type, n)
			}
		}
		if v, ok, err := df.At[int64](0, "score"); err != nil || !ok || v != 5 {
			t.Errorf("the score is %d, %v, %v", v, ok, err)
		}
	})

	t.Run("a null instant is the zero time", func(t *testing.T) {
		// Rows gave the epoch, 1970-01-01, a plausible instant no row held.
		type stamp struct {
			At  time.Time `ursus:"at"`
			Day time.Time `ursus:"day"`
		}
		got, err := ursus.FromRows([]stamp{{}}).WithColumns(ursus.Col("day").Cast(ursus.Date)).CollectInto[stamp](ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !got[0].At.IsZero() || !got[0].Day.IsZero() {
			t.Errorf("a null instant decodes as %v and %v, want the zero time", got[0].At, got[0].Day)
		}
	})

	t.Run("no rows", func(t *testing.T) {
		df, err := ursus.FromRows([]sale(nil)).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if df.Height() != 0 || df.Width() != 10 {
			t.Errorf("%d rows of %d columns, want none of 10", df.Height(), df.Width())
		}
	})

	t.Run("refused", func(t *testing.T) {
		type withMap struct{ M map[string]int }
		if _, err := ursus.FromRows([]withMap{{}}).Collect(ctx); err == nil ||
			!strings.Contains(err.Error(), "field withMap.M") {
			t.Errorf("a map field: %v", err)
		}
		if _, err := ursus.FromRows([]int{1}).Collect(ctx); err == nil || !strings.Contains(err.Error(), "FromRows takes structs") {
			t.Errorf("ints: %v", err)
		}
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
