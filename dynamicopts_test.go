package ursus_test

// Step 187: GroupByDynamic's Label, StartBy and IncludeBoundaries, which were declared
// only to be refused. The windows each option gives are the dynamicopts group's,
// Polars 2.1.0's group_by_dynamic (oracle_dynamicopts_test.go); ursus keeps a dynamic
// group's empty windows, where Polars drops them, so only windows that hold a row are
// compared. Here stay the refusals.

import (
	"testing"
	"time"

	"github.com/advenn/ursus"
)

func TestDynamicOptionsRefuseWhatPolarsRefuses(t *testing.T) {
	c := ursus.Col
	frame := ursus.Frame(ursus.Values("t", []time.Time{time.Unix(0, 0)}), ursus.Values("v", []int64{1}))
	for _, tc := range []struct {
		o    ursus.DynamicOptions
		want string
	}{
		{ursus.DynamicOptions{Every: ursus.Every("1h"), Label: "middle"}, "is not one of"},
		{ursus.DynamicOptions{Every: ursus.Every("1h"), StartBy: "midweek"}, "is not one of"},
		// Polars documents a weekday for a grid of weeks alone, and answers others by
		// a rule it does not document: every 5h from Friday at 20:00.
		{ursus.DynamicOptions{Every: ursus.Every("5h"), StartBy: "friday"}, "only a grid of weeks"},
		{ursus.DynamicOptions{Every: ursus.Every("7d"), StartBy: "monday"}, "only a grid of weeks"},
	} {
		assertUserError(t, frame.GroupByDynamic(c("t"), tc.o).Agg(c("v").Sum()), tc.want)
	}
}
