package ursus_test

// The two properties that keep the temporal overflow guard narrow.
//
// Neither is about temporal arithmetic. They are about what the guard must NOT do:
// change ordinary integer arithmetic, and judge a row the query cannot see.

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestOrdinaryInt64ArithmeticStillWraps. Integer overflow is Go's semantics and
// ursus's: the guard is gated on a temporal OUTPUT type, and nothing else asserted
// that. Without this, widening the gate would pass every test in the repository.
func TestOrdinaryInt64ArithmeticStillWraps(t *testing.T) {
	df, err := ursus.Frame(ursus.Values("n", []int64{math.MaxInt64, 1 << 62, math.MinInt64})).
		Select(
			ursus.Col("n").Add(int64(1)).Alias("plus"),
			ursus.Col("n").Mul(int64(4)).Alias("times"),
		).Collect(t.Context())
	if err != nil {
		t.Fatalf("ordinary integer arithmetic must not be refused: %v", err)
	}

	want, err := ursus.Frame(
		ursus.Values("plus", []int64{math.MinInt64, (1 << 62) + 1, math.MinInt64 + 1}),
		ursus.Values("times", []int64{-4, 0, 0}),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, df, want)
}

// TestNullRowsAreNotJudgedForOverflow, and specifically the BROADCAST case.
//
// A length-1 literal's validity holds one bit, and bitmap.View.Get(i) for i > 0
// returns true from a nil buffer rather than panicking — so a guard written as
// `l.IsValid(i) && r.IsValid(i)`, which is the pattern used elsewhere in the
// codebase, judges rows that are null. combinedValidity has already resolved the
// broadcast, which is why the guard reads that instead.
func TestNullRowsAreNotJudgedForOverflow(t *testing.T) {
	far := time.Date(2260, 1, 1, 0, 0, 0, 0, time.UTC)

	// Every row is null because the literal is: the payload under it is arbitrary
	// and must never be read.
	//
	// SIXTEEN rows, not three, and that is the point. The literal's validity is one
	// bit in one byte, so a guard reading l.IsValid(i)/r.IsValid(i) finds zeros for
	// rows 1..7 and reads off the end of the buffer at row 8. A three-row fixture
	// never reaches the byte boundary and the naive guard looks correct.
	const rows = 16
	instants := make([]time.Time, rows)
	for i := range instants {
		instants[i] = far
	}
	df, err := ursus.Frame(ursus.Values("t", instants)).
		Select(ursus.Col("t").Add(ursus.Null(ursus.Duration(ursus.Nano))).Alias("shifted")).
		Collect(t.Context(), ursus.WithThreads(1))
	if err != nil {
		t.Fatalf("adding a null literal must not be refused: %v", err)
	}
	if df.Height() != rows {
		t.Fatalf("%d rows, want %d", df.Height(), rows)
	}

	// And a null row whose own payload would overflow, beside valid rows that do not.
	huge := 200 * 365 * 24 * time.Hour
	_, err = ursus.Frame(
		ursus.ValuesNullable("d", []time.Duration{huge, huge, time.Second},
			[]bool{false, true, true}),
	).Select(ursus.Col("d").Add(ursus.Col("d")).Alias("doubled")).
		Collect(t.Context(), ursus.WithThreads(1))
	if err == nil {
		t.Fatal("row 1 doubles 200 years of nanoseconds and must be refused")
	}
	if strings.Contains(err.Error(), "at row 0") {
		t.Errorf("the refusal names the NULL row: %v", err)
	}
}
