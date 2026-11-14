package hive

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
)

func noFiles(t *testing.T) *dtype.Schema {
	t.Helper()
	s, err := dtype.NewSchema()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestInfer: the type a partition key's values infer is Polars 2.1.0's, recorded in
// the hive_infer group of testdata/oracle, each layout read through ScanParquet, and
// spelled in oracle_hive_infer_test.go since step 503, which took this test's table of
// twenty-five keys. What stays is ursus's own: a key of dates in five layouts, each
// value read by its own pattern, as CSV dates are (feature-gaps §9), where Polars 2.1
// reads every value by the first's and refuses the third (hive_infer/date_layouts_mixed
// asks Polars so since step 507; feature-gaps.md §9); a key of no values, which no
// directory can hold; and a date not asked for.
func TestInfer(t *testing.T) {
	if got := Infer([]string{"2024-01-02", "2024-1-2", "2024/01/02", "2024.01.02", "02-01-2024"}, true); got != dtype.Date {
		t.Errorf("five layouts of one date: %s, want Date", got)
	}
	if got := Infer(nil, true); got != dtype.String {
		t.Errorf("no values: %s, want String", got)
	}
	// Without dates, a date is text.
	if got := Infer([]string{"2024-01-02"}, false); got != dtype.String {
		t.Errorf("a date without dates: %s", got)
	}
}

// TestParse: each path's key=value directories below the root, percent-decoded,
// Hive's default partition null, and an empty value null but in a key of text, where
// it is the empty string, as Polars reads it (step 367); a directory of another form
// and the root's own name passed over; paths of other keys refused.
func TestParse(t *testing.T) {
	root := filepath.FromSlash("/data/k=0/t")
	paths := []string{
		filepath.FromSlash("/data/k=0/t/a=1/x/b=y%20z/p0.parquet"),
		filepath.FromSlash("/data/k=0/t/a=__HIVE_DEFAULT_PARTITION__/x/b=/p1.parquet"),
		filepath.FromSlash("/data/k=0/t/a=x%3Dy/x/b=w/p2.parquet"),
	}
	p, err := Parse(root, paths)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Keys, ","); got != "a,b" {
		t.Errorf("keys %s", got)
	}
	want := [][]string{{"1", "y z"}, {DefaultPartition, ""}, {"x=y", "w"}}
	nulls := [][]bool{{false, false}, {true, false}, {false, false}}
	for k := range paths {
		for j := range p.Keys {
			if p.Text[k][j] != want[k][j] || p.Null[k][j] != nulls[k][j] {
				t.Errorf("file %d key %s: %q null %v", k, p.Keys[j], p.Text[k][j], p.Null[k][j])
			}
		}
	}
	types := p.Types(nil, noFiles(t), true)
	if types[0] != dtype.String || types[1] != dtype.String {
		t.Errorf("types %v", types)
	}
	vals, err := p.Values([]dtype.DataType{dtype.String, dtype.String})
	if err != nil {
		t.Fatal(err)
	}
	if vals[1][0].IsValid(0) || !vals[2][0].IsValid(0) || vals[2][0].Strings().Get(0) != "x=y" {
		t.Errorf("values %v", vals)
	}
	if !vals[1][1].IsValid(0) || vals[1][1].Strings().Get(0) != "" {
		t.Errorf("an empty key of text: %v", vals[1][1])
	}
	// An empty value of an integer key is null, and leaves the type the others infer.
	q := &Partitions{Keys: []string{"n"}, Text: [][]string{{""}, {"1"}}, Null: [][]bool{{false}, {false}}}
	if types := q.Types(nil, noFiles(t), true); types[0] != dtype.Int64 {
		t.Errorf("an empty value beside an integer typed the key %s", types[0])
	} else if vals, err := q.Values(types); err != nil || vals[0][0].IsValid(0) {
		t.Errorf("an empty integer key: %v, %v", vals, err)
	}

	for name, bad := range map[string][]string{
		"another key":   {filepath.FromSlash("/r/a=1/p.parquet"), filepath.FromSlash("/r/b=2/p.parquet")},
		"another depth": {filepath.FromSlash("/r/a=1/b=2/p.parquet"), filepath.FromSlash("/r/a=3/p.parquet")},
	} {
		if _, err := Parse(filepath.FromSlash("/r"), bad); err == nil || !strings.Contains(err.Error(), "partitioned by") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestValues: a key's values cast to its type, a date and an instant in any layout
// inference takes, an instant with an offset in UTC; a value its type cannot take,
// a given one's or a file column's, refused.
func TestValues(t *testing.T) {
	p := &Partitions{Keys: []string{"d", "t", "z"},
		Text: [][]string{{"2024/1/2", "2024-01-02 3:04", "2024-01-02T03:04:05.5+0530"}},
		Null: [][]bool{{false, false, false}}}
	types := p.Types(nil, noFiles(t), true)
	if got := types[0].String() + " " + types[1].String() + " " + types[2].String(); got != "Date Datetime(us) Datetime(us, UTC)" {
		t.Fatalf("types %s", got)
	}
	vals, err := p.Values(types)
	if err != nil {
		t.Fatal(err)
	}
	if got := data.MustValues[int32](vals[0][0])[0]; got != int32(time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC).Unix()/86400) {
		t.Errorf("the date: %d", got)
	}
	if got := data.MustValues[int64](vals[0][1])[0]; got != time.Date(2024, 1, 2, 3, 4, 0, 0, time.UTC).UnixMicro() {
		t.Errorf("the instant: %d", got)
	}
	if got := data.MustValues[int64](vals[0][2])[0]; got != time.Date(2024, 1, 1, 21, 34, 5, 5e8, time.UTC).UnixMicro() {
		t.Errorf("the instant at +05:30: %d", got)
	}
	q := &Partitions{Keys: []string{"a"}, Text: [][]string{{"x"}}, Null: [][]bool{{false}}}
	if _, err := q.Values([]dtype.DataType{dtype.Int64}); err == nil {
		t.Error("x as an Int64")
	}
}
