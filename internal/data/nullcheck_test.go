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

// TestNewBatchRowsRunsEveryCheck. NewBatchRows used to run NONE of NewBatch's four
// structural checks, and step 49 added only the fifth.
//
// The argument step 49 made for the nullability check — that a check living only in
// NewBatch "would be blind to a third of the engine and blind in exactly the newest
// operators" — applies verbatim to the other four, because the same six operators
// build populated batches through this constructor. It PANICS because the signature
// has no error to return.
func TestNewBatchRowsRunsEveryCheck(t *testing.T) {
	schema := dtype.MustSchema(
		dtype.Of("a", dtype.Int64),
		dtype.Of("b", dtype.Int64),
	)
	col := func(name string, dt dtype.DataType, n int) *data.Column {
		vals := make([]int64, n)
		return data.NewFixed(name, dt, vals, bitmap.AllSet(n))
	}

	for _, tc := range []struct {
		name string
		cols []*data.Column
		rows int
		want string
	}{
		{"wrong column count", []*data.Column{col("a", dtype.Int64, 2)}, 2,
			"1 columns but schema has 2"},
		{"wrong name", []*data.Column{
			col("a", dtype.Int64, 2), col("nope", dtype.Int64, 2)}, 2, `named "nope"`},
		{"wrong type", []*data.Column{
			col("a", dtype.Int64, 2), col("b", dtype.Int32, 2)}, 2, "schema says Int64"},
		{"ragged lengths", []*data.Column{
			col("a", dtype.Int64, 2), col("b", dtype.Int64, 3)}, 2, "has 3 rows"},
		{"row count disagrees with the columns", []*data.Column{
			col("a", dtype.Int64, 2), col("b", dtype.Int64, 2)}, 7,
			"declares 7 rows but its columns hold 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("NewBatchRows accepted a malformed batch")
				}
				err, ok := r.(error)
				if !ok || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("panic should mention %q, got %v", tc.want, r)
				}
			}()
			data.NewBatchRows(schema, tc.cols, tc.rows)
		})
	}
}

// TestNewBatchRowsKeepsTheZeroColumnCase is why the checks are gated on len(cols).
//
// The constructor exists for "a frame can legitimately have rows but no columns
// after Select() with nothing" — checkShape reports 0 rows for an empty column list,
// so deriving the height there would erase exactly the case it was written for.
func TestNewBatchRowsKeepsTheZeroColumnCase(t *testing.T) {
	schema := dtype.MustSchema()
	b := data.NewBatchRows(schema, nil, 5)
	if b.Rows() != 5 {
		t.Errorf("a zero-column batch kept %d rows, want 5", b.Rows())
	}
}
