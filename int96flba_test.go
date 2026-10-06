package ursus_test

// Parquet's INT96 timestamps and unannotated fixed-length byte arrays read
// (step 112).
//
// INT96 is the deprecated nanosecond timestamp Spark writes by default, and ursus
// refused the column outright. An unannotated FIXED_LEN_BYTE_ARRAY was typed Binary
// by the schema and then refused by the reader (audit.md I16): a column the plan
// promised that no Collect could produce. PyArrow wrote the fixture.

import (
	"os"
	"slices"
	"testing"

	"github.com/advenn/ursus"
)

func TestInt96AndFixedLenByteArrayRead(t *testing.T) {
	b, err := os.ReadFile("testdata/parquet/pyarrow_int96_flba.parquet")
	if err != nil {
		t.Fatal(err)
	}
	df, err := ursus.ScanParquetBytes(b, "pyarrow_int96_flba.parquet").Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	typ, ts := textOf(t, df, "ts")
	if typ != "Datetime(ns)" {
		t.Errorf("ts is %s, want Datetime(ns), as PyArrow reads INT96", typ)
	}
	// The instants PyArrow wrote, as ursus renders a Datetime(ns) cast to text.
	want := []string{"2024-01-02T03:04:05.123456000Z", "∅", "1969-12-31T23:59:59.999999000Z", "1900-01-01T00:00:00.000000000Z"}
	if !slices.Equal(ts, want) {
		t.Errorf("ts %v, want %v", ts, want)
	}

	fixed, err := df.Column[string]("fixed")
	if err != nil {
		t.Fatal(err)
	}
	if ft := df.Schema().Field(1).Type.String(); ft != "Binary" {
		t.Errorf("fixed is %s, want Binary", ft)
	}
	wantFixed := []*string{ptr("abcd"), ptr("\x00\x01\x02\x03"), nil, ptr("zzzz")}
	for i, w := range wantFixed {
		v, ok := fixed.Get(i)
		switch {
		case w == nil && ok:
			t.Errorf("fixed row %d is %q, want null", i, v)
		case w != nil && (!ok || v != *w):
			t.Errorf("fixed row %d is %q (valid %v), want %q", i, v, ok, *w)
		}
	}
}

func ptr[T any](v T) *T { return &v }
