package ursus_test

// ursus.Null(ursus.NullT) from the outside.
//
// This is not an exotic spelling. cond.go recommends it — "so:
// Otherwise(ursus.Null(ursus.NullT))" — and it is the only way to write a null that
// adopts whatever type the other side has. Every expression below was either an
// "ursus bug, please report it" or a structurally invalid column before step 55.

import (
	"testing"

	"github.com/advenn/ursus"
)

func nullFrame() *ursus.LazyFrame {
	return ursus.Frame(ursus.Values("x", []int64{1, 2, 3}))
}

// TestUntypedNullThroughTheBoolUnaryOps.
//
// not() was the bad one: it produced a Bool column — the promised type — of ZERO
// rows, so the user was told "expression produced 0 rows for a 3-row batch; this is
// a bug in ursus". The four NaN predicates failed outright with "is_nan on non-float
// Null", also as an ursus bug, despite ResolveUnary having an explicit arm admitting
// Null and promising Bool.
func TestUntypedNullThroughTheBoolUnaryOps(t *testing.T) {
	null := func() ursus.Expr { return ursus.Null(ursus.NullT) }

	cases := []struct {
		name string
		e    ursus.Expr
	}{
		{"not", null().Not()},
		{"is_nan", null().IsNan()},
		{"is_not_nan", null().IsNotNan()},
		{"is_finite", null().IsFinite()},
		{"is_infinite", null().IsInfinite()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			df, err := nullFrame().Select(c.e.Alias("v")).
				Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatalf("%s of a null is a null, not an error: %v", c.name, err)
			}
			// The row count is the assertion. A one-row answer broadcast to the
			// batch is right; a zero-row one is the defect, and it satisfied every
			// type check.
			if df.Height() != 3 {
				t.Fatalf("height = %d, want 3\n%s", df.Height(), df)
			}
			v, err := df.Column[bool]("v")
			if err != nil {
				t.Fatal(err)
			}
			for i := range 3 {
				if _, ok := v.Get(i); ok {
					t.Errorf("row %d is not null; %s of a null is null", i, c.name)
				}
			}
		})
	}
}

// TestUntypedNullThroughTheNullPredicates is the other side, and it is what stops
// the fix above from being "make everything null".
//
// is_null and is_not_null are total: the null-ness IS the answer, so their result is
// never null and is the same for every row of an all-null column.
func TestUntypedNullThroughTheNullPredicates(t *testing.T) {
	for _, c := range []struct {
		name string
		e    ursus.Expr
		want bool
	}{
		{"is_null", ursus.Null(ursus.NullT).IsNull(), true},
		{"is_not_null", ursus.Null(ursus.NullT).IsNotNull(), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			df, err := nullFrame().Select(c.e.Alias("v")).
				Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			v, err := df.Column[bool]("v")
			if err != nil {
				t.Fatal(err)
			}
			for i := range df.Height() {
				got, ok := v.Get(i)
				if !ok {
					t.Fatalf("row %d is null; a null predicate answers for every row", i)
				}
				if got != c.want {
					t.Fatalf("row %d = %v, want %v", i, got, c.want)
				}
			}
		})
	}
}

// TestTwoUntypedNullsCompare pins the pair that answer differently.
//
// Both were "comparison is not implemented for Null" — a refusal that arrived after
// CollectSchema and Explain had already promised Bool.
func TestTwoUntypedNullsCompare(t *testing.T) {
	null := func() ursus.Expr { return ursus.Null(ursus.NullT) }

	t.Run("== is three-valued", func(t *testing.T) {
		df, err := nullFrame().Select(null().Eq(null()).Alias("v")).
			Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		v, err := df.Column[bool]("v")
		if err != nil {
			t.Fatal(err)
		}
		for i := range df.Height() {
			if _, ok := v.Get(i); ok {
				t.Fatalf("row %d of null == null is not null; == is three-valued", i)
			}
		}
	})

	// The one that would be wrong if both families shared an answer. <=> is the
	// operator for which "null == null is true and the result is never null" — that
	// is the entire reason it exists as a separate kernel family.
	t.Run("<=> treats nulls as data", func(t *testing.T) {
		df, err := nullFrame().Select(null().EqMissing(null()).Alias("v")).
			Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		v, err := df.Column[bool]("v")
		if err != nil {
			t.Fatal(err)
		}
		for i := range df.Height() {
			got, ok := v.Get(i)
			if !ok {
				t.Fatalf("row %d of null <=> null is null; <=> is never null", i)
			}
			if !got {
				t.Fatalf("row %d of null <=> null is false, want true", i)
			}
		}
	})
}

// TestUntypedNullSurvivesBroadcast is the simplest expression in this file and the
// one that was most broken.
//
//	Select(ursus.Null(ursus.NullT))
//
// A literal evaluates to a LENGTH-1 column, so putting one beside a batch's other
// columns is a broadcast, and a broadcast is a Take. kernel.Take had no arm for a
// column whose type says there are no values, so the documented spelling failed
// with "take is not implemented for Null" — after Explain had printed the plan.
//
// ursus.Null(ursus.Int64) worked throughout, which is why nothing noticed: the
// typed form takes the ordinary fixed-width path.
func TestUntypedNullSurvivesBroadcast(t *testing.T) {
	for _, c := range []struct {
		name string
		e    ursus.Expr
	}{
		{"untyped", ursus.Null(ursus.NullT)},
		{"typed", ursus.Null(ursus.Int64)},
	} {
		t.Run(c.name, func(t *testing.T) {
			df, err := nullFrame().Select(c.e.Alias("v")).
				Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatalf("selecting a null literal must work: %v", err)
			}
			if df.Height() != 3 {
				t.Fatalf("height = %d, want 3 — a literal broadcasts to the batch",
					df.Height())
			}
		})
	}
}

// TestUntypedNullThroughTheCallSurface.
//
// The contract matrix has no Call arm, so these were measured by hand rather than
// enumerated. Five of the six were already correct; is_in was not, and it failed in
// a way none of the others could: its probe set is encoded against the RECEIVER's
// type, so a Null receiver made compileCall try to cast Int64 probes to Null.
//
// The assertion is that CollectSchema's promise and Collect's answer agree, which
// is the contract the matrix asserts for everything that is not a Call.
func TestUntypedNullThroughTheCallSurface(t *testing.T) {
	null := func() ursus.Expr { return ursus.Null(ursus.NullT) }

	for _, c := range []struct {
		name string
		e    ursus.Expr
	}{
		{"is_in", null().IsIn(int64(1), int64(2))},
		{"str.to_upper", null().Str().ToUpper()},
		{"str.to_lower", null().Str().ToLower()},
		{"str.len_bytes", null().Str().LenBytes()},
		{"str.contains", null().Str().Contains("a", true)},
		{"str.starts_with", null().Str().StartsWith("a")},
	} {
		t.Run(c.name, func(t *testing.T) {
			lf := nullFrame().Select(c.e.Alias("v"))

			sch, err := lf.CollectSchema(t.Context())
			if err != nil {
				t.Fatalf("CollectSchema refused what ResolveCall admits: %v", err)
			}
			promised := sch.Field(0).Type

			df, err := lf.Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatalf("Explain and CollectSchema promised %s and Collect failed: %v",
					promised, err)
			}
			if got := df.Schema().Field(0).Type; got != promised {
				t.Errorf("promised %s, produced %s", promised, got)
			}
			if df.Height() != 3 {
				t.Errorf("height = %d, want 3", df.Height())
			}
		})
	}
}

// TestArithmeticOnAnUntypedNullIsRefused pins the narrowing from the user's side.
//
// These nine used to type-check, plan, render in Explain and then fail at execution
// — six of them as "a bug in ursus". neg() and abs() have refused a Null operand
// since they were written, and these are the same family.
//
// assertUserError rather than errors.Is, because errors.Is walks the cause chain and
// cannot tell a user mistake from an internal one. It also asserts that Explain
// fails, which is the half that matters here: the old behaviour was a plan that
// printed cleanly and could not run.
func TestArithmeticOnAnUntypedNullIsRefused(t *testing.T) {
	null := func() ursus.Expr { return ursus.Null(ursus.NullT) }

	for _, c := range []struct {
		name string
		e    ursus.Expr
	}{
		{"neg", null().Neg()},
		{"abs", null().Abs()},
		{"sign", null().Sign()},
		{"floor", null().Floor()},
		{"ceil", null().Ceil()},
		{"sqrt", null().Sqrt()},
		{"ln", null().Ln()},
		// round is the Call-surface twin of floor and ceil, and shared their
		// mistake. The contract matrix cannot see it — it has no Call arm — so this
		// case was found by following the pair rather than by measurement.
		{"round", null().Round(2)},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertUserError(t, nullFrame().Select(c.e.Alias("v")), "Null")
		})
	}
}
