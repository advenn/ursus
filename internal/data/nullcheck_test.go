package data_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// See data.CheckNonNullable. One line per test binary, run before any test starts.
func init() { data.CheckNonNullable = true }

// honest and lying build a one-column Int64 batch input, differing only in whether
// the column actually holds a null.
func nullCheckCols(t *testing.T, withNull bool) (*dtype.Schema, []*data.Column) {
	t.Helper()
	schema := dtype.MustSchema(dtype.NotNull("v", dtype.Int64))
	valid := bitmap.NewBuilder(2)
	valid.Append(true)
	valid.Append(!withNull)
	return schema, []*data.Column{
		data.NewFixed("v", dtype.Int64, []int64{1, 2}, valid.Finish()),
	}
}

// TestNonNullableCheckCatchesALie is the instrument's own test.
//
// The suite passing with the check on means nothing unless the check can fail. An
// all-green run from a broken check looks exactly like an all-green run from an
// honest engine, which is the failure mode this file exists to rule out.
func TestNonNullableCheckCatchesALie(t *testing.T) {
	schema, cols := nullCheckCols(t, true)

	_, err := data.NewBatch(schema, cols)
	if err == nil {
		t.Fatal("NewBatch accepted a non-nullable column holding a null")
	}
	for _, want := range []string{`"v"`, "non-nullable", "1 nulls of 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should mention %s: %v", want, err)
		}
	}
}

// TestNonNullableCheckCoversNewBatchRows. Six operators build populated batches
// through NewBatchRows — explode, unnest, unpivot, hstack and two kernel paths —
// and it performs none of NewBatch's four checks. A check that lived only in
// NewBatch would be blind to them, and blind in the newest operators.
//
// It panics rather than erroring because the signature has no error to return.
func TestNonNullableCheckCoversNewBatchRows(t *testing.T) {
	schema, cols := nullCheckCols(t, true)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("NewBatchRows accepted a non-nullable column holding a null")
		}
		if err, ok := r.(error); !ok || !strings.Contains(err.Error(), `"v"`) {
			t.Errorf("the panic should carry the naming error, got %v", r)
		}
	}()
	data.NewBatchRows(schema, cols, 2)
}

// TestNonNullableCheckIsOneDirectional. The check must not fire on the two shapes
// that are legal and common:
//
//   - a non-nullable column that holds no nulls, obviously;
//   - a NULLABLE column that holds no nulls, which is the ordinary result of a
//     filter and which the whole design goes out of its way to preserve —
//     memsrc.FromBatch and DataFrame.Lazy exist to stop nullability being
//     re-derived from data.
func TestNonNullableCheckIsOneDirectional(t *testing.T) {
	schema, cols := nullCheckCols(t, false)
	if _, err := data.NewBatch(schema, cols); err != nil {
		t.Errorf("a non-nullable column with no nulls is honest: %v", err)
	}

	nullable := dtype.MustSchema(dtype.Of("v", dtype.Int64))
	if _, err := data.NewBatch(nullable, cols); err != nil {
		t.Errorf("a nullable column with no nulls is fine: %v", err)
	}

	// And a nullable column that DOES hold nulls, which is the point of the flag.
	_, withNull := nullCheckCols(t, true)
	if _, err := data.NewBatch(nullable, withNull); err != nil {
		t.Errorf("a nullable column holding nulls is the normal case: %v", err)
	}
}

// TestNonNullableCheckNeedsTheCount is why IsAllSet alone is not the check.
//
// bitmap.Builder.Finish never returns the no-storage all-set form, so an honest
// column built through a builder carries a materialised all-ones bitmap and
// IsAllSet reports false for it. Deciding on IsAllSet alone would fire on every
// scanned and every concatenated column — the misuse its own doc warns about:
// "Use it to take a fast path, never to decide correctness."
func TestNonNullableCheckNeedsTheCount(t *testing.T) {
	schema, cols := nullCheckCols(t, false)
	if cols[0].Validity().IsAllSet() {
		t.Skip("Builder.Finish now returns the no-storage form; this test's premise is gone")
	}
	if _, err := data.NewBatch(schema, cols); err != nil {
		t.Errorf("a materialised all-ones bitmap is not a null: %v", err)
	}
}

// TestNonNullableCheckIsOffByDefault. It costs a popcount per column per batch on
// exactly the columns most likely to be declared non-nullable — a Parquet REQUIRED
// column always carries a materialised bitmap — so it is an invariant check for
// tests, not a guard for production.
func TestNonNullableCheckIsOffByDefault(t *testing.T) {
	data.CheckNonNullable = false
	defer func() { data.CheckNonNullable = true }()

	schema, cols := nullCheckCols(t, true)
	if _, err := data.NewBatch(schema, cols); err != nil {
		t.Errorf("with the toggle off the check must not run: %v", err)
	}
}
