package ursus_test

// CSV and I/O, against answers worked out by hand: audit.md §5, I8–I10, I19, I21, I22
// and I27, with S25 and S21's String casts, which share their parsers. Since step 502
// each data answer is Polars 2.1.0's, recorded in testdata/oracle/io_byhand.json by
// bench/scripts/oracle/io_byhand.py and spelled in oracle_io_byhand_test.go. What
// stays here is a Go API's answer and the rule ursus alone has: i128.Parse's
// overflow, and I22, a frame with rows and no columns refused by the writers. One with
// neither is written as nothing, where Polars writes a bare newline: the recorded
// i22_no_rows_no_columns_written asserts it since step 507, asked of Polars without a
// header.

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// knownIODefects names each case that answers wrongly today, with what it answers.
var knownIODefects = map[string]string{}

type ioCase struct {
	name string
	run  func(t *testing.T) string // what is wrong, or ""
}

// csvText writes lf as CSV.
func csvText(t *testing.T, lf *ursus.LazyFrame) (string, error) {
	t.Helper()
	var b bytes.Buffer
	err := lf.WriteCSV(t.Context(), &b)
	return b.String(), err
}

// i128Col is a column of Int128 values given as decimal text.
func i128Col(t *testing.T, dt dtype.DataType, name string, vals ...string) *ursus.Column {
	t.Helper()
	out := make([]i128.Int128, len(vals))
	for i, s := range vals {
		v, ok := i128.Parse(s)
		if !ok {
			t.Fatalf("i128.Parse(%q)", s)
		}
		out[i] = v
	}
	return data.NewFixed(name, dt, out, bitmap.AllSet(len(vals)))
}

// ioEqual collects got and want and reports how they differ.
func ioEqual(t *testing.T, got, want *ursus.LazyFrame) string {
	t.Helper()
	g, err := got.Collect(t.Context())
	if err != nil {
		return err.Error()
	}
	w, err := want.Collect(t.Context())
	if err != nil {
		t.Fatalf("the hand answer: %v", err)
	}
	return framesDiffer(t, g, w)
}

func refusal(df *ursus.DataFrame, err error, kind error, words ...string) string {
	switch {
	case err == nil && df != nil:
		return fmt.Sprintf("answered %s; want a refusal", strings.Join(strings.Fields(df.String()), " "))
	case err == nil:
		return "answered; want a refusal"
	case !errors.Is(err, kind):
		return fmt.Sprintf("a %s error, want %v: %v", kindOf(err), kind, err)
	}
	for _, w := range words {
		if !strings.Contains(err.Error(), w) {
			return fmt.Sprintf("the refusal does not say %q: %v", w, err)
		}
	}
	return ""
}

func TestIOByHand(t *testing.T) {
	ints := func(name string, v []int64) *ursus.LazyFrame { return ursus.Frame(ursus.Values(name, v)) }

	cases := []ioCase{
		{"I19 i128.Parse refuses an overflow", func(t *testing.T) string {
			for _, s := range []string{"170141183460469231731687303715884105728", "-170141183460469231731687303715884105729",
				"999999999999999999999999999999999999999999"} {
				if v, ok := i128.Parse(s); ok {
					return fmt.Sprintf("i128.Parse(%s) = %s", s, v)
				}
			}
			return ""
		}},

		// --- I22: a frame with rows and no columns is refused by the writers ---
		{"I22 CSV refuses a frame with rows and no columns", func(t *testing.T) string {
			_, err := csvText(t, ints("a", []int64{1, 2, 3}).Select())
			return refusal(nil, err, ursus.ErrValue, "3 rows")
		}},
		{"I22 Parquet refuses a frame with rows and no columns", func(t *testing.T) string {
			var b bytes.Buffer
			err := ints("a", []int64{1, 2, 3}).Select().WriteParquet(t.Context(), &b)
			return refusal(nil, err, ursus.ErrValue, "3 rows")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrong := tc.run(t)
			why, known := knownIODefects[tc.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownIODefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %s", why, wrong)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownIODefects {
		if !slices.ContainsFunc(cases, func(c ioCase) bool { return c.name == name }) {
			t.Errorf("knownIODefects names %q, which is not a case", name)
		}
	}
}
