package duck

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/advenn/ursus/qcheck/gen"
)

// recordRows are a record's values as Go reads them, a float as its bits.
func recordRows(recs []arrow.RecordBatch) [][]any {
	var out [][]any
	for _, r := range recs {
		for i := range int(r.NumRows()) {
			row := make([]any, r.NumCols())
			for j := range row {
				v := value(r.Column(j), i)
				if f, ok := v.(float64); ok {
					v = math.Float64bits(f) // bit-exact, a NaN's payload too
				}
				row[j] = v
			}
			out = append(out, row)
		}
	}
	return out
}

// diffRows is where two tables of rows first differ, or "".
func diffRows(want, got [][]any) string {
	if len(want) != len(got) {
		return fmt.Sprintf("%d rows read back of %d", len(got), len(want))
	}
	for i := range want {
		for j := range want[i] {
			if want[i][j] != got[i][j] {
				return fmt.Sprintf("row %d, column %d: wrote %v, read back %v", i, j, want[i][j], got[i][j])
			}
		}
	}
	return ""
}

// TestArrowRoundTrip registers generated records, as Load registers them, with
// __rid and each Date as its days, and reads each back bit-exact: every value,
// null, NaN with its payload, -0, the integers' ends, Unicode, NUL and long text,
// and days at int32's ends. A value changed in what was read back must show.
func TestArrowRoundTrip(t *testing.T) {
	d := open(t)
	seen := map[string]bool{}
	tables := 0
	for seed := uint64(1); seed <= 400 && (tables < 40 || len(seen) < 6); seed++ {
		tab := gen.Table(seed, "t", 8193)
		rec := WithRID(tab.Rec)
		if err := d.Register("u_rt", rec); err != nil {
			t.Fatal(err)
		}
		schema, recs, err := d.Query(context.Background(), `SELECT * FROM "u_rt" ORDER BY "__rid"`)
		if err != nil {
			t.Fatal(err)
		}
		if schema.NumFields() != int(rec.NumCols()) {
			t.Fatalf("seed %d: %d columns read back of %d", seed, schema.NumFields(), rec.NumCols())
		}
		for j, f := range rec.Schema().Fields() {
			if g := schema.Field(j); g.Name != f.Name {
				t.Errorf("seed %d: column %q read back as %q", seed, f.Name, g.Name)
			}
		}
		want, got := recordRows([]arrow.RecordBatch{rec}), recordRows(recs)
		for _, r := range recs {
			r.Release()
		}
		if diff := diffRows(want, got); diff != "" {
			t.Errorf("seed %d (%s): %s", seed, tab, diff)
		}
		// The comparison's teeth: one value changed must show.
		for i := range got {
			for j := range got[i] {
				if got[i][j] == nil {
					continue
				}
				old := got[i][j]
				got[i][j] = fmt.Sprintf("changed %v", old)
				if diffRows(want, got) == "" {
					t.Errorf("seed %d: a value changed at row %d, column %d did not show", seed, i, j)
				}
				got[i][j] = old
				break
			}
			break
		}
		for _, row := range want {
			for _, v := range row {
				switch v := v.(type) {
				case uint64:
					if math.IsNaN(math.Float64frombits(v)) {
						seen["NaN"] = true
					}
					if v == 1<<63 {
						seen["-0"] = true
					}
				case int64:
					if v == math.MinInt64 {
						seen["MinInt64"] = true
					}
					if v == math.MaxInt32 && rec.Schema().Field(0).Type.ID() == arrow.INT64 {
						seen["MaxInt32"] = true
					}
				case string:
					if len(v) >= 1000 {
						seen["long text"] = true
					}
					if strings.ContainsRune(v, 0) {
						seen["NUL"] = true
					}
				}
			}
		}
		rec.Release()
		tables++
	}
	for _, k := range []string{"NaN", "-0", "MinInt64", "MaxInt32", "long text", "NUL"} {
		if !seen[k] {
			t.Errorf("no table held %s", k)
		}
	}
	t.Logf("%d tables read back; held %v", tables, seen)
}
