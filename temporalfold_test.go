package ursus_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/source/parquet"
)

// Step 142: a temporal literal written as a cast folds to a literal of its type, so
// it is not cast again on every batch, and the Parquet pruner can compare it with a
// row group's statistics. Before, `Lit(t).Cast(Date)` stayed a cast: the folder
// refused temporal values, and the pruner matches only a column against a literal.

// sortedDates writes 500 days from 1996-01-01, one a row, in ten row groups of 50,
// as d (a Date), and ts (a Datetime in milliseconds) at noon of each day.
func sortedDates(t *testing.T) string {
	t.Helper()
	days := make([]time.Time, 500)
	n := make([]int64, 500)
	for i := range days {
		days[i] = time.Date(1996, time.January, 1+i, 12, 0, 0, 0, time.UTC)
		n[i] = int64(i)
	}
	path := filepath.Join(t.TempDir(), "dates.parquet")
	err := ursus.Frame(ursus.Values("t", days), ursus.Values("n", n)).
		Select(
			ursus.Col("t").Cast(ursus.Date).Alias("d"),
			ursus.Col("t").Cast(ursus.Datetime(ursus.Milli, "UTC")).Alias("ts"),
			ursus.Col("n"),
		).
		SinkParquet(t.Context(), path, ursus.WithRowGroupRows(50))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// TestADateLiteralPrunesRowGroups: rows 450 to 499 are the last row group, from
// 1997-03-26. A filter from that date reads one group of ten.
func TestADateLiteralPrunesRowGroups(t *testing.T) {
	path := sortedDates(t)
	cut := ursus.Lit(day(1997, time.March, 26)).Cast(ursus.Date)

	src := parquet.New([]parquet.Opener{fileOpener(t, path)}, path, parquet.DefaultOptions())
	df, err := ursus.Scan(src).Filter(ursus.Col("d").Ge(cut)).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 50 {
		t.Fatalf("%d rows, want 50", df.Height())
	}
	if read, skipped := src.RowGroupStats(); read != 1 || skipped != 9 {
		t.Errorf("read %d row groups and skipped %d, want 1 and 9: only the last can hold "+
			"a date from 1997-03-26", read, skipped)
	}

	p, err := ursus.ScanParquet(path).Filter(ursus.Col("d").Ge(cut)).Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "lit(1997-03-26)") || strings.Contains(p, "cast(") {
		t.Errorf("the date was not folded to a literal that reads as a date:\n%s", p)
	}
}

// TestATemporalLiteralOfAnotherUnitIsNotPruned: a literal in microseconds against a
// column in milliseconds. Its ticks are a thousand times the column's, and compared
// raw with the column's statistics they would skip every group. The evaluator casts
// one side to the other; the pruner cannot, so it leaves the conjunct alone.
func TestATemporalLiteralOfAnotherUnitIsNotPruned(t *testing.T) {
	path := sortedDates(t)
	cut := ursus.Lit(day(1997, time.March, 26)).Cast(ursus.Datetime(ursus.Micro, "UTC"))

	src := parquet.New([]parquet.Opener{fileOpener(t, path)}, path, parquet.DefaultOptions())
	df, err := ursus.Scan(src).Filter(ursus.Col("ts").Ge(cut)).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 50 {
		t.Fatalf("%d rows, want 50", df.Height())
	}
	if _, skipped := src.RowGroupStats(); skipped != 0 {
		t.Errorf("skipped %d row groups on a literal of another unit", skipped)
	}

	// The same unit is pruned.
	same := ursus.Lit(day(1997, time.March, 26)).Cast(ursus.Datetime(ursus.Milli, "UTC"))
	src = parquet.New([]parquet.Opener{fileOpener(t, path)}, path, parquet.DefaultOptions())
	df, err = ursus.Scan(src).Filter(ursus.Col("ts").Ge(same)).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 50 {
		t.Fatalf("%d rows, want 50", df.Height())
	}
	if read, skipped := src.RowGroupStats(); read != 1 || skipped != 9 {
		t.Errorf("read %d row groups and skipped %d, want 1 and 9 with a literal of the "+
			"column's own unit", read, skipped)
	}
}
