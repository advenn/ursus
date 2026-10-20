package ursus_test

import (
	"testing"

	"github.com/advenn/ursus"
)

// TestFrameTableAlignsByColumnsNotBytes holds the printed table to terminal columns
// (step 296): a letter outside ASCII is one column, an East Asian wide one two, and
// a combining mark none, as Polars' table measures them. The table padded by bytes,
// so a row with "Zoë" came out a column short.
func TestFrameTableAlignsByColumnsNotBytes(t *testing.T) {
	df, err := ursus.Frame(
		ursus.Values("s", []string{"ab", "Zoë", "数据", "é"}),
		ursus.Values("名前", []int64{1, 2, 3, 4}),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := "shape: (4, 2)\n" +
		"┌────────┬───────┐\n" +
		"│ s      │ 名前  │\n" +
		"│ String │ Int64 │\n" +
		"├────────┼───────┤\n" +
		"│ \"ab\"   │ 1     │\n" +
		"│ \"Zoë\"  │ 2     │\n" +
		"│ \"数据\" │ 3     │\n" +
		"│ \"é\"    │ 4     │\n" +
		"└────────┴───────┘\n"
	if got := df.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
