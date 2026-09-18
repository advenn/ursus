package data_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// See data.CheckNonNullable. One line per test binary, run before any test starts.
func init() {
	data.CheckNonNullable = true
	data.CheckTimeRange = true
}

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
	// FATAL, not Skip. This used to skip when the premise stopped holding, which
	// means the day Builder.Finish starts returning the no-storage form this test
	// would have gone GREEN rather than red — announcing success for a check it had
	// silently stopped performing. The premise is the point of the test, so losing
	// it is a failure to report, not a reason to stand down.
	if cols[0].Validity().IsAllSet() {
		t.Fatal("Builder.Finish now returns the no-storage all-set form, so this " +
			"test's premise is gone and checkNonNullable's NullCount fallback may " +
			"no longer be needed — re-derive it rather than deleting this test")
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
	// Built at the dtype's OWN storage width. This used to make every column from
	// []int64, including the Int32 one in the "wrong type" case below — a column
	// whose values did not match its declared type, which is the structural mistake
	// NewFixed now panics on. The case still tests what it always tested: a column
	// whose type disagrees with the SCHEMA.
	col := func(name string, dt dtype.DataType, n int) *data.Column {
		if dt.Physical().ID() == dtype.TypeInt32 {
			return data.NewFixed(name, dt, make([]int32, n), bitmap.AllSet(n))
		}
		return data.NewFixed(name, dt, make([]int64, n), bitmap.AllSet(n))
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

// TestNewFixedChecksItsValuesAgainstItsType is the write-side half of the check
// Values has always performed on the read side.
//
// The asymmetry was not academic: a cumulative sum handed NewFixed a []float64 under
// a Duration dtype, and since the two are the same width, neither the declared type
// nor the row count could disagree — an hour came back as about 152 years. Values
// would have caught it on the way out, but the column had already been published.
func TestNewFixedChecksItsValuesAgainstItsType(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func()
		want string
	}{
		{"float values under a temporal type", func() {
			data.NewFixed("d", dtype.Duration(dtype.Nano), []float64{1}, bitmap.AllSet(1))
		}, "values are Float64"},
		{"int64 values under a Date", func() {
			data.NewFixed("d", dtype.Date, []int64{1}, bitmap.AllSet(1))
		}, "values are Int64"},
		{"int64 values under a Decimal", func() {
			data.NewFixed("d", dtype.Decimal(10, 2), []int64{1}, bitmap.AllSet(1))
		}, "values are Int64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("a column whose values disagree with its type was built")
				}
				if msg := fmt.Sprint(r); !strings.Contains(msg, tc.want) {
					t.Errorf("panic should say %q: %v", tc.want, msg)
				}
			}()
			tc.call()
		})
	}
}

// TestNewFixedPermitsTheIntendedPuns: every column stored as a different type than it
// declares must still be buildable, which is what Physical() is for.
func TestNewFixedPermitsTheIntendedPuns(t *testing.T) {
	data.NewFixed("date", dtype.Date, []int32{1}, bitmap.AllSet(1))
	data.NewFixed("time", dtype.Time(dtype.Nano), []int64{1}, bitmap.AllSet(1))
	data.NewFixed("ts", dtype.Datetime(dtype.Micro, "UTC"), []int64{1}, bitmap.AllSet(1))
	data.NewFixed("dur", dtype.Duration(dtype.Second), []int64{1}, bitmap.AllSet(1))
	data.NewFixed("dec", dtype.Decimal(10, 2), []i128.Int128{{}}, bitmap.AllSet(1))
	data.NewFixed("i128", dtype.Int128, []i128.Int128{{}}, bitmap.AllSet(1))
	data.NewFixed("enum", dtype.Enum("a", "b"), []uint32{1}, bitmap.AllSet(1))
}
