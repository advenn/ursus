package ursus_test

// audit.md I20: a String column can hold any bytes, and a Parquet STRING must be
// UTF-8, which Polars and DuckDB refuse to read otherwise. SinkParquet refuses such
// a value, flat or nested, and a Binary column writes the same bytes as they are.

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

func TestParquetRefusesAStringThatIsNotUTF8(t *testing.T) {
	c := ursus.Col
	bad := ursus.Frame(ursus.Values("s", []string{"ok", "a\xffb"}), ursus.Values("g", []int64{1, 1}))
	for name, lf := range map[string]*ursus.LazyFrame{
		"flat":           bad,
		"a struct field": bad.Select(ursus.Struct(c("s"), c("g")).Alias("p")),
		"a list element": bad.GroupBy(c("g")).Agg(c("s").Implode()),
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			err := lf.WriteParquet(t.Context(), &buf)
			if err == nil || !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), "not UTF-8") {
				t.Fatalf("want a value refusal naming UTF-8, got %v", err)
			}
		})
	}

	t.Run("as Binary, the bytes as they are", func(t *testing.T) {
		var buf bytes.Buffer
		if err := bad.Select(c("s").Cast(ursus.Binary)).WriteParquet(t.Context(), &buf); err != nil {
			t.Fatal(err)
		}
		df, err := ursus.ScanParquetFrom([]ursus.ParquetFile{parquetFile("b.parquet", buf.Bytes(), &tally{}, 0, 0)}).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		v, _, err := df.At[string](1, "s")
		if err != nil {
			t.Fatal(err)
		}
		if string(v) != "a\xffb" {
			t.Errorf("read back %q", v)
		}
	})
}
