package ursus_test

// The open silent wrong answers a v0.3 user can reach, by hand: audit.md's A16 and
// O14.
//
// A16: Diff on an unsigned column subtracted at that width and wrapped. O14: a udf
// that ends its goroutine with runtime.Goexit — which no recover sees, and which
// testing's FailNow calls — closed that worker's lane as though the stream had
// ended, and the rows after it vanished with no error.

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// knownOpenRowDefects names each case that answers wrongly today, with what it
// answers.
var knownOpenRowDefects = map[string]string{}

// textOf is a column's type and its values as text, null as ∅.
func textOf(t *testing.T, df *ursus.DataFrame, name string) (string, []string) {
	t.Helper()
	typ := ""
	for _, f := range df.Schema().All() {
		if f.Name == name {
			typ = f.Type.String()
		}
	}
	s, err := df.Lazy().Select(ursus.Col(name).Cast(ursus.String)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	col, err := s.Column[string](name)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, s.Height())
	for i := range out {
		if v, ok := col.Get(i); ok {
			out[i] = v
		} else {
			out[i] = null
		}
	}
	return typ, out
}

// collectAside runs lf in a goroutine of its own, so a Goexit that reaches the
// caller's goroutine ends that one rather than the test, under a deadline, so a hang
// reads as one.
func collectAside(t *testing.T, lf *ursus.LazyFrame, opts ...ursus.CollectOption) (df *ursus.DataFrame, err error, how string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	returned := false
	go func() {
		defer close(done)
		df, err = lf.Collect(ctx, opts...)
		returned = true
	}()
	<-done
	switch {
	case !returned:
		return nil, nil, "the calling goroutine ended"
	case ctx.Err() != nil:
		return nil, nil, "hung until the deadline"
	}
	return df, err, ""
}

func TestOpenRowsByHand(t *testing.T) {
	c := ursus.Col
	diffs := func(col *ursus.Column, typ string, want ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err := ursus.Frame(col).Select(c("x").Diff(1)).Collect(t.Context())
			if err != nil {
				return err.Error()
			}
			gotT, got := textOf(t, df, "x")
			if gotT != typ || !slices.Equal(got, want) {
				return fmt.Sprintf("%s %v, want %s %v", gotT, got, typ, want)
			}
			return ""
		}
	}
	// exitOn is a udf that ends its goroutine at the row holding 3.
	exitOn := func(e ursus.Expr) ursus.Expr {
		return e.MapElements("exit_on_3", ursus.Int64, func(v int64) (int64, error) {
			if v == 3 {
				runtime.Goexit()
			}
			return v, nil
		})
	}
	five := func() *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("k", []int64{1, 2, 3, 4, 5}), ursus.Values("g", []int64{0, 1, 0, 1, 0}))
	}
	refusesGoexit := func(lf *ursus.LazyFrame) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err, how := collectAside(t, lf, ursus.WithThreads(4), ursus.WithBatchSize(1))
			switch {
			case how != "":
				return how
			case err == nil:
				return fmt.Sprintf("answered %d rows; want an error", df.Height())
			case !strings.Contains(err.Error(), "Goexit"):
				return fmt.Sprintf("an error that does not say Goexit: %v", err)
			}
			return ""
		}
	}

	cases := []ioCase{
		// --- A16 ---
		{"Diff over UInt8 widens to Int16", diffs(ursus.Values("x", []uint8{3, 1, 255, 0}),
			"Int16", null, "-2", "254", "-255")},
		{"Diff over UInt16 widens to Int32", diffs(ursus.Values("x", []uint16{3, 1, math.MaxUint16, 0}),
			"Int32", null, "-2", "65534", "-65535")},
		{"Diff over UInt32 widens to Int64", diffs(ursus.Values("x", []uint32{3, 1, math.MaxUint32, 0}),
			"Int64", null, "-2", "4294967294", "-4294967295")},
		{"Diff over UInt64 widens to Int128", diffs(ursus.Values("x", []uint64{3, 1, math.MaxUint64, 0}),
			"Int128", null, "-2", "18446744073709551614", "-18446744073709551615")},
		// Signed integers keep their width and wrap, as every integer operation in
		// ursus does, and as Polars' diff does.
		{"control: Diff over Int8 stays Int8", diffs(ursus.Values("x", []int8{-100, 100, 0}),
			"Int8", null, "-56", "-100")},
		{"control: Diff over a Datetime is a Duration", func(t *testing.T) string {
			ts := []time.Time{time.Unix(0, 0).UTC(), time.Unix(90, 0).UTC()}
			df, err := ursus.Frame(ursus.Values("x", ts)).Select(c("x").Diff(1)).Collect(t.Context())
			if err != nil {
				return err.Error()
			}
			if typ := df.Schema().Field(0).Type; typ.ID() != ursus.Duration(ursus.Micro).ID() {
				return fmt.Sprintf("type %s, want a Duration", typ)
			}
			return ""
		}},

		// --- O14 ---
		{"Goexit in a parallel Select is an error", refusesGoexit(five().Select(exitOn(c("k"))))},
		{"Goexit in a parallel aggregation is an error", refusesGoexit(five().GroupBy(c("g")).
			Agg(exitOn(c("k")).Sum()))},
		{"Goexit in a parallel join probe is an error", refusesGoexit(five().Join(
			ursus.Frame(ursus.Values("k", []int64{10, 20}), ursus.Values("r", []int64{1, 2})),
			ursus.JoinOn(exitOn(c("k"))), ursus.JoinHow(ursus.JoinLeft)))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrong := tc.run(t)
			why, known := knownOpenRowDefects[tc.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownOpenRowDefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %s", why, wrong)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownOpenRowDefects {
		if !slices.ContainsFunc(cases, func(c ioCase) bool { return c.name == name }) {
			t.Errorf("knownOpenRowDefects names %q, which is not a case", name)
		}
	}
}
