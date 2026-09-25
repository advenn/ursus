package ursus_test

// Int128 is where every integer Sum lands, so its casts and its own sum are the
// boundary where a 128-bit total becomes a number a caller reads. Two promises
// meet there, and both were broken:
//
//   - Cast is documented to fail "on the first unrepresentable value". Out of
//     Int128 it converted to Int64 through a float64, so a sum above 2^53 came back
//     rounded; into Int128 from a float it truncated, where the same cast into
//     Int64 refuses.
//   - The i128 package argues that its wrapping Add cannot wrap, because reaching
//     2^127 takes 2^63 rows of maximal Int64. True of inputs of 64 bits or fewer;
//     false of an Int128 column, where two rows are enough.
//
// None of them errors. Each is a plausible number of the right type.

import (
	"errors"
	"math"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source/memsrc"
)

// int128Frame is one Int128 column, "v", holding vals. The public API cannot build
// one — Values has no Int128 case — so it comes from the layer below, as it would
// from an Arrow or Parquet file.
func int128Frame(t *testing.T, vals ...i128.Int128) *ursus.LazyFrame {
	t.Helper()
	schema, err := dtype.NewSchema(dtype.Field{Name: "v", Type: dtype.Int128})
	if err != nil {
		t.Fatal(err)
	}
	b, err := data.NewBatch(schema, []*data.Column{
		data.NewFixed("v", dtype.Int128, vals, bitmap.AllSet(len(vals))),
	})
	if err != nil {
		t.Fatal(err)
	}
	src, err := memsrc.New(schema, b)
	if err != nil {
		t.Fatal(err)
	}
	return ursus.Scan(src)
}

// i128Of parses a base-10 Int128, failing the test on a malformed literal.
func i128Of(t *testing.T, s string) i128.Int128 {
	t.Helper()
	v, ok := i128.Parse(s)
	if !ok {
		t.Fatalf("i128.Parse(%q) failed", s)
	}
	return v
}

// only collects a one-row, one-column result and renders its single cell, or
// "null". An error is returned rather than failed on, because a refusal is the
// right answer for half of these cases.
func only[T any](t *testing.T, lf *ursus.LazyFrame, render func(T) string) (string, error) {
	t.Helper()
	df, err := lf.Collect(t.Context())
	if err != nil {
		return "", err
	}
	col, err := df.Column[T](df.Schema().Names()[0])
	if err != nil {
		t.Fatal(err)
	}
	v, ok := col.Get(0)
	if !ok {
		return "null", nil
	}
	return render(v), nil
}

// knownInexactInt128 names each case that answers wrongly today, with what it
// answers. Emptied by the commit that makes these exact or refused.
var knownInexactInt128 = map[string]string{
	"sum-cast-int64":      "9007199254740992 — 2^53+1 rounded to 2^53 through a float64",
	"sum-cast-uint64":     "refused, although 2^64-2 fits a Uint64",
	"float-int128-strict": "3 — truncated, where Cast(Int64) refuses 3.7",
	"float-int128-lossy":  "3 — truncated, where CastLossy(Int64) gives null",
	"sum-wraps-once":      "-170141183460469231731687303715884105728 — Max+1 wrapped to Min",
	"sum-wraps-fully":     "20 — 2^128+20 wrapped all the way round into range",
}

func TestInt128BoundaryIsExactOrRefused(t *testing.T) {
	const twoTo53 = int64(1) << 53
	// 2^126 + 5, four of which total 2^128 + 20. Wrapping modulo 2^128 lands on
	// 20: a perfectly ordinary number, which no range check on the RESULT can
	// distinguish from a true 20.
	quarter := i128Of(t, "85070591730234615865843651857942052869")

	i64 := func(v int64) string { return i128.FromInt64(v).String() }
	u64 := func(v uint64) string { return i128.FromUint64(v).String() }
	s128 := func(v i128.Int128) string { return v.String() }

	for _, c := range []struct {
		name string
		run  func() (string, error)
		want string // "" means: refused, with ErrValue
	}{
		{"sum-cast-int64", func() (string, error) {
			return only(t, ursus.Frame(ursus.Values("v", []int64{twoTo53, 1})).
				GroupBy().Agg(ursus.Col("v").Sum().Cast(ursus.Int64)), i64)
		}, "9007199254740993"},
		{"sum-cast-uint64", func() (string, error) {
			return only(t, ursus.Frame(ursus.Values("v", []int64{math.MaxInt64, math.MaxInt64})).
				GroupBy().Agg(ursus.Col("v").Sum().Cast(ursus.Uint64)), u64)
		}, "18446744073709551614"},
		{"float-int128-strict", func() (string, error) {
			return only(t, ursus.Frame(ursus.Values("f", []float64{3.7})).
				Select(ursus.Col("f").Cast(ursus.Int128)), s128)
		}, ""},
		{"float-int128-lossy", func() (string, error) {
			return only(t, ursus.Frame(ursus.Values("f", []float64{3.7})).
				Select(ursus.Col("f").CastLossy(ursus.Int128)), s128)
		}, "null"},
		{"float-int128-whole", func() (string, error) {
			// The control: a float that IS an integer converts, so the fix cannot
			// pass by refusing every float.
			return only(t, ursus.Frame(ursus.Values("f", []float64{-3e20})).
				Select(ursus.Col("f").Cast(ursus.Int128)), s128)
		}, "-300000000000000000000"},
		{"sum-wraps-once", func() (string, error) {
			return only(t, int128Frame(t, i128.Max, i128.One).
				GroupBy().Agg(ursus.Col("v").Sum()), s128)
		}, ""},
		{"sum-wraps-fully", func() (string, error) {
			return only(t, int128Frame(t, quarter, quarter, quarter, quarter).
				GroupBy().Agg(ursus.Col("v").Sum()), s128)
		}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.run()
			if err != nil && !errors.Is(err, ursus.ErrValue) {
				t.Fatalf("refused with the wrong kind: %v", err)
			}
			right := (c.want == "" && err != nil) || (c.want != "" && err == nil && got == c.want)
			why, known := knownInexactInt128[c.name]
			switch {
			case known && right:
				t.Errorf("answers correctly now (%s, %v); delete it from knownInexactInt128", got, err)
			case known:
				t.Logf("known inexact: %s (got %s, err %v)", why, got, err)
			case !right && c.want == "":
				t.Errorf("= %s, want a refusal", got)
			case !right:
				t.Errorf("= %s (err %v), want %s", got, err, c.want)
			}
		})
	}
}
