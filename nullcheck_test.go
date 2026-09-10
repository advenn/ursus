package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// The nullability boundary check runs for every batch this test binary builds.
//
// An init rather than a TestMain: it needs no m.Run() wrapper, and every test
// package that wants the check gets one line rather than a boilerplate function.
// The var is written once here, before any test starts, and never again — which is
// what keeps a package-level toggle safe under WithThreads(>1).
func init() { data.CheckNonNullable = true }

// The two tests below close teeth that did not bite: without them, the step would
// have fixed two defects and proved only one.

// TestPartialKernelsAreDeclaredNullable pins the predicate's TYPE sensitivity, not
// just its op list.
//
// kernel.arithmetic dispatches on the binding's OUTPUT physical type, so the same
// operator is partial or total depending on where it lands: integer `%` reaches
// arithNum and manufactures a null on a zero divisor, float `%` reaches arithFloat
// and produces NaN, and integer `/` is total because true division binds to a
// float. A predicate written against the op alone gets two of those three wrong,
// and only a schema assertion can tell.
func TestPartialKernelsAreDeclaredNullable(t *testing.T) {
	lf := ursus.Frame(
		ursus.Values("ia", []int64{7}),
		ursus.Values("ib", []int64{2}),
		ursus.Values("fa", []float64{7}),
		ursus.Values("fb", []float64{2}),
	)

	for _, tc := range []struct {
		name     string
		e        ursus.Expr
		nullable bool
	}{
		{"integer mod", ursus.Col("ia").Mod(ursus.Col("ib")), true},
		{"integer floordiv", ursus.Col("ia").FloorDiv(ursus.Col("ib")), true},
		// True division binds to Float64 whatever the operands are, so it never
		// reaches the integer kernel.
		{"integer div", ursus.Col("ia").Div(ursus.Col("ib")), false},
		{"integer add", ursus.Col("ia").Add(ursus.Col("ib")), false},
		{"float mod", ursus.Col("fa").Mod(ursus.Col("fb")), false},
		{"float floordiv", ursus.Col("fa").FloorDiv(ursus.Col("fb")), false},
		{"float pow", ursus.Col("fa").Pow(ursus.Col("fb")), false},
		// The case that settles it: neither "both operands are integers" nor "the
		// left operand is an integer" holds, and the kernel is still partial.
		{"duration floordiv", ursus.Col("ia").Cast(ursus.Duration(ursus.Second)).
			FloorDiv(ursus.Col("ib")), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := lf.Select(tc.e.Alias("k")).CollectSchema(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Field(0).Nullable; got != tc.nullable {
				t.Errorf("Nullable = %v, want %v", got, tc.nullable)
			}
		})
	}
}

// TestStrictTemporalWideningRefusesOverflow is the other half of the step.
//
// rescaleTemporal's doc has always said overflow "produces a NULL rather than a
// wrapped instant", and it took no strict parameter, so a STRICT cast — which
// Cast.Field declares non-nullable — handed back nulls. The four other lossy paths
// in that file all refuse under strict; this one now does too.
//
// int64 nanoseconds span about 1677 to 2262, so a second-resolution instant ten
// billion seconds from the epoch (~2286) does not fit.
func TestStrictTemporalWideningRefusesOverflow(t *testing.T) {
	secs := ursus.Datetime(ursus.Second, "UTC")
	nanos := ursus.Datetime(ursus.Nano, "UTC")

	lf := func() *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("n", []int64{10_000_000_000})).
			Select(ursus.Col("n").Cast(secs).Alias("ts"))
	}

	_, err := lf().Select(ursus.Col("ts").Cast(nanos).Alias("wide")).
		Collect(t.Context())
	if err == nil {
		t.Fatal("a strict widening cast past the int64 nanosecond horizon must refuse")
	}
	if !strings.Contains(err.Error(), "overflow") {
		t.Errorf("the error should say what happened: %v", err)
	}
	if errors.Is(err, uerr.ErrInternal) {
		t.Errorf("an unrepresentable value is a user error: %v", err)
	}

	// Non-strict keeps the documented behaviour: a null, and a schema that says so.
	df, err := lf().Select(ursus.Col("ts").CastLossy(nanos).Alias("wide")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("a lossy cast should null the overflow, not fail: %v", err)
	}
	if _, ok, err := df.At[int64](0, "wide"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("the overflowing instant should be null under a lossy cast")
	}
}
