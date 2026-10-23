package selector_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	cs "github.com/advenn/ursus/selector"
)

// TestSelectorsAsPolars (step 322): each selector, and their algebra, over one
// schema of eight types, against Polars 1.44.1's expand_selector.
func TestSelectorsAsPolars(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("id", []int64{1}), ursus.Values("Name", []string{"a"}),
		ursus.Values("price", []float64{1.5}), ursus.Values("qty", []int64{2}),
		ursus.Values("when", []time.Time{time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}),
		ursus.Values("ok", []bool{true}), ursus.Values("at", []time.Time{time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}),
		ursus.Values("raw", [][]byte{{1}})).
		WithColumns(c("when").Cast(ursus.Date), c("at").Cast(ursus.Datetime(ursus.Micro, "")))
	for _, tc := range []struct {
		name string
		s    cs.Selector
		want string
	}{
		{"numeric", cs.Numeric(), "id price qty"},
		{"integer", cs.Integer(), "id qty"},
		{"float", cs.Float(), "price"},
		{"temporal", cs.Temporal(), "when at"},
		{"string", cs.String(), "Name"},
		{"boolean", cs.Boolean(), "ok"},
		{"starts_with p", cs.StartsWith("p"), "price"},
		{"ends_with e", cs.EndsWith("e"), "Name price"},
		{"contains a", cs.Contains("a"), "Name at raw"},
		{"matches", cs.Matches("^[a-z]{2}$"), "id ok at"},
		{"first", cs.First(), "id"},
		{"last", cs.Last(), "raw"},
		{"by_index 0, -1", cs.ByIndex(0, -1), "id raw"},
		{"by_name, as written", cs.ByName("qty", "id"), "qty id"},
		{"numeric - id", cs.Numeric().Difference(cs.ByName("id")), "price qty"},
		{"~numeric", cs.Numeric().Complement(), "Name when ok at raw"},
		{"numeric & starts_with q", cs.Numeric().Intersect(cs.StartsWith("q")), "qty"},
		{"numeric | string", cs.Numeric().Union(cs.String()), "id Name price qty"},
		{"numeric ^ integer", cs.Numeric().SymmetricDifference(cs.Integer()), "price"},
		{"numeric, id excluded", cs.Numeric().Exclude("id", "nope"), "price qty"},
		{"alpha", cs.Alpha(), "id Name price qty when ok at raw"},
		{"digit", cs.Digit(), ""},
		{"datetime", cs.Datetime(), "at"},
		{"date", cs.Date(), "when"},
		{"binary", cs.Binary(), "raw"},
		{"exclude", cs.All().Exclude("id", "qty"), "Name price when ok at raw"},
		{"all", cs.All(), "id Name price qty when ok at raw"},
		{"by_dtype", cs.ByDType(ursus.Int64, ursus.Bool), "id qty ok"},
	} {
		s, err := lf.Select(tc.s.Expr()).CollectSchema(ctx)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := strings.Join(s.Names(), " "); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}

	// A selector is an expression: each column doubled, its name kept.
	df, err := lf.Select(cs.Numeric().Expr().Mul(2)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Columns(); !slices.Equal(got, []string{"id", "price", "qty"}) {
		t.Errorf("numeric doubled: %q", got)
	}
	if v, _, _ := df.At[float64](0, "price"); v != 3 {
		t.Errorf("price doubled is %v", v)
	}

	t.Run("refused", func(t *testing.T) {
		// Polars: "nope" not found.
		if _, err := lf.Select(cs.ByName("nope").Expr()).CollectSchema(ctx); err == nil || !strings.Contains(err.Error(), `"nope"`) {
			t.Errorf("an unknown name: %v", err)
		}
		if _, err := lf.Select(cs.ByIndex(10).Expr()).CollectSchema(ctx); err == nil {
			t.Error("a place past the end was taken")
		}
		if _, err := lf.Select(cs.Matches("(").Expr()).CollectSchema(ctx); err == nil {
			t.Error("a bad pattern was taken")
		}
	})

	t.Run("explained", func(t *testing.T) {
		if got := cs.Numeric().Difference(cs.ByName("id")).String(); got != `(cols(numeric) - cols("id"))` {
			t.Errorf("the selector renders as %q", got)
		}
	})

	t.Run("expanded", func(t *testing.T) {
		// Polars' expand_selector: the names, in the expression's order.
		s, err := lf.CollectSchema(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got, err := cs.ByName("qty", "id").Union(cs.Float()).Expand(s)
		if err != nil || strings.Join(got, " ") != "id price qty" {
			t.Errorf("a union of names and floats expands to %q, %v", got, err)
		}
		if got, err := cs.ByName("qty", "id").Expand(s); err != nil || strings.Join(got, " ") != "qty id" {
			t.Errorf("names alone expand to %q, %v", got, err)
		}
		if _, err := cs.ByName("nope").Expand(s); err == nil {
			t.Error("an unknown name expanded")
		}
	})
}
