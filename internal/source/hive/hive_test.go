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

// TestInfer: a partition key's values infer the type Polars 1.44.1 infers for them.
func TestInfer(t *testing.T) {
	for _, tc := range []struct {
		vals []string
		want string
	}{
		{[]string{"1", "-2", "007", "-0", "00"}, "Int64"},
		{[]string{"9223372036854775808", "1"}, "Int128"},
		{[]string{"-170141183460469231731687303715884105728"}, "Int128"},
		{[]string{"1.5", "2"}, "Float64"},
		{[]string{"2.5e3", "1e3", "inf", "-inf", "NaN", "1.", ".5", "-1.5E-3"}, "Float64"},
		{[]string{"true", "FALSE", "True"}, "Bool"},
		{[]string{"2024-01-02", "2024-1-2", "2024/01/02", "2024.01.02", "02-01-2024"}, "Date"},
		{[]string{"2024-01-02 03:04:05", "2024-01-02T03:04", "2024-01-02 3:04", "2024/1/2 03:04:05",
			"2024-01-02T03:04:05.123456789"}, "Datetime(us)"},
		{[]string{"2024-01-02T03:04:05Z", "2024-01-02T03:04:05.5+0530", "2024-01-02 03:04:05+01:00"}, "Datetime(us, UTC)"},
		// Text, each of these, where Polars too reads text.
		{[]string{"+5"}, "String"},
		{[]string{"nan"}, "String"},
		{[]string{"0x10"}, "String"},
		{[]string{"1_000"}, "String"},
		{[]string{"1,5"}, "String"},
		{[]string{"yes"}, "String"},
		{[]string{"1.5e"}, "String"},
		{[]string{"2024-01-02T03"}, "String"},
		{[]string{"2024-13-01"}, "String"},
		{[]string{"2024-02-30"}, "String"},
		{[]string{"1", "x"}, "String"},
		{[]string{"2024-01-02", "x"}, "String"},
		{[]string{"true", "1"}, "String"},
		{nil, "String"},
		// 20240102 is a number, as Polars reads it; 24-01-02 is text, where Polars
		// reads the year 2.
		{[]string{"20240102"}, "Int64"},
		{[]string{"24-01-02"}, "String"},
	} {
		if got := Infer(tc.vals, true).String(); got != tc.want {
			t.Errorf("%q: %s, want %s", tc.vals, got, tc.want)
		}
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
