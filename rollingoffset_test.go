package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestRollingOffsetRenders (step 313): a rolling group's Offset is where each row's
// window starts, as Polars' offset is, (t + offset, t + offset + period], minus the
// period by default; NoOffset is Polars' "0d". The windows are Polars 2.1.0's since
// step 500, recorded in the rollingoffset group of testdata/oracle and spelled in
// oracle_rollingoffset_test.go, which took this file's table. What stays asserts the
// plan: an offset of none is shown, as the default is not.
func TestRollingOffsetRenders(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	ints := ursus.Frame(i64s("t", 1, 3, 5, 6), i64s("v", 1, 2, 3, 4))
	every := ursus.Every
	for _, tc := range []struct {
		o    ursus.RollingOptions
		want string
	}{
		{ursus.RollingOptions{Period: every("2i"), Offset: ursus.NoOffset()}, "offset=0s"},
		{ursus.RollingOptions{Period: every("2i"), Offset: every("-1i")}, "offset=-1i"},
	} {
		plan, err := ints.Rolling(c("t"), tc.o).Agg(ursus.Len()).Explain(ctx)
		if err != nil || !strings.Contains(plan, tc.want) {
			t.Errorf("the plan says nothing of %q: %v\n%s", tc.want, err, plan)
		}
	}
	if plan, err := ints.Rolling(c("t"), ursus.RollingOptions{Period: every("2i")}).Agg(ursus.Len()).Explain(ctx); err != nil ||
		strings.Contains(plan, "offset=") {
		t.Errorf("the default offset is shown: %v\n%s", err, plan)
	}
}
