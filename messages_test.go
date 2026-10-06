package ursus_test

// Refusals name what the caller wrote (step 111).
//
// audit.md's misleading errors, re-measured in step 108 and still open: a refusal
// that quoted a factor the caller never wrote (S15), that named a desugaring rather
// than the method called (S16, A11), and a udf failure that named its row's place in
// a batch as if it were the frame's (A15).

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

func TestRefusalsNameWhatTheCallerWrote(t *testing.T) {
	c := ursus.Col
	str := ursus.Frame(ursus.Values("s", []string{"a"}))
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	dur := ursus.Frame(ursus.Values("a", []time.Time{day}), ursus.Values("b", []time.Time{day.Add(time.Hour)})).
		Select(c("b").Sub(c("a")).Alias("d"))

	cases := []struct {
		name         string
		lf           *ursus.LazyFrame
		want, absent []string
	}{
		{"a Duration by a fraction quotes no factor", dur.Select(c("d").Mul(1.5)),
			[]string{"fractional", "Mul(factor)", "Cast"}, []string{"2.5"}},
		{"FillNan on a String names fill_nan", str.Select(c("s").FillNan(1.0)),
			[]string{"fill_nan() cannot be applied to a String column"}, nil},
		{"FillNullWith of the wrong type names fill_null", str.Select(c("s").FillNullWith(int64(1))),
			[]string{"fill_null() cannot be applied to a String column"}, nil},
		{"FillNull(mean) on a String names fill_null", str.Select(c("s").FillNull(ursus.FillMean)),
			[]string{"fill_null() cannot be applied to a String column"}, nil},
		{"CumSum of a String names cum_sum", str.Select(c("s").CumSum(false)),
			[]string{"cum_sum() is not defined for String"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.lf.CollectSchema(t.Context())
			if err == nil {
				t.Fatal("no error")
			}
			msg := err.Error()
			first := strings.SplitN(msg, "\n", 2)[0]
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("does not say %q:\n%s", w, msg)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(msg, a) {
					t.Errorf("says %q:\n%s", a, msg)
				}
			}
			// The headline is the caller's method, not the desugaring under it.
			for _, internal := range []string{"is_not_nan", "when:", " sum()"} {
				if strings.Contains(first, internal) {
					t.Errorf("the first line names %q: %s", internal, first)
				}
			}
		})
	}
}

// TestAUdfFailureNamesItsInput: under CollectBatches with batches of two, the fourth
// row is row 1 of its batch, and the error said "row 1" as if it were the frame's.
// The input value is what finds the row, so it is named first.
func TestAUdfFailureNamesItsInput(t *testing.T) {
	failsOnFour := func(x int64) (int64, error) {
		if x == 4 {
			return 0, errors.New("four is not allowed")
		}
		return x, nil
	}
	lf := ursus.Frame(ursus.Values("v", []int64{1, 2, 3, 4, 5})).
		Select(ursus.Col("v").MapElements("f", ursus.Int64, failsOnFour).Alias("y"))
	var err error
	for _, e := range lf.CollectBatches(t.Context(), ursus.WithBatchSize(2)) {
		if e != nil {
			err = e
			break
		}
	}
	if err == nil {
		t.Fatal("the udf's error was lost")
	}
	if !strings.Contains(err.Error(), `udf "f" failed on 4, row 1 of its batch`) {
		t.Errorf("does not name the input and say the row is the batch's:\n%v", err)
	}
}
