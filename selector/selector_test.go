package selector_test

import (
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	cs "github.com/advenn/ursus/selector"
)

// TestSelectorsAsPolars (step 322): each selector, and their algebra, over one
// schema of eight types. The columns each selects, Expand's names and a selector as
// an expression are Polars 2.1.0's answers, recorded in the selector group of
// testdata/oracle and spelled in oracle_selector_test.go since step 503, which took
// this test's twenty-eight expansions. What stays asserts the refusals and a
// selector's rendering.
func TestSelectorsAsPolars(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("id", []int64{1}), ursus.Values("Name", []string{"a"}),
		ursus.Values("price", []float64{1.5}), ursus.Values("qty", []int64{2}),
		ursus.Values("when", []time.Time{time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}),
		ursus.Values("ok", []bool{true}), ursus.Values("at", []time.Time{time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}),
		ursus.Values("raw", [][]byte{{1}})).
		WithColumns(c("when").Cast(ursus.Date), c("at").Cast(ursus.Datetime(ursus.Micro, "")))

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
		// Polars' expand_selector refuses a name the schema has not.
		s, err := lf.CollectSchema(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cs.ByName("nope").Expand(s); err == nil {
			t.Error("an unknown name expanded")
		}
	})
}
