package readfuzz_test

import (
	"bytes"
	"testing"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/fuzzcheck"
)

// csvSeeds are the CSV target's seed corpus: the shapes the reader's own tests name,
// and one of them compressed each way the reader knows a stream by its first bytes.
func csvSeeds(f *testing.F) [][]byte {
	plain := []string{
		"",
		"a,b,c\n1,2,3\n4,5,6\n",
		"a,b\n1,x\n,\n3,\n",
		"id,price,when,day,flag\n1,1.5,2024-01-02 03:04:05,2024-01-02,true\n2,-0.25,2024-02-29 23:59:59,2024-02-29,false\n",
		"a,b\r\n1,2\r\n3,4\r\n",
		"\xef\xbb\xbfa,b\n1,2\n",
		"name,text\n1,\"quoted, with a comma\"\n2,\"a \"\"quote\"\" inside\"\n3,\"two\nlines\"\n",
		"a,b,c\n1,2\n3,4,5,6\n",
		"a\n\n\n1\n",
		"a,a,b\n1,2,3\n",
		"x\n9223372036854775807\n-9223372036854775808\n9223372036854775808\n",
		"f\n1e308\n1e309\nNaN\ninf\n-0.0\n",
		"t\n12:34:56\n24:00:00\n",
		"s\n\xff\xfe\n\xc3\x28\n",
		"a,b\n\"unterminated,1\n",
		"x^2,b\n1,2\n",
	}
	seeds := make([][]byte, 0, len(plain)+3)
	for _, s := range plain {
		seeds = append(seeds, []byte(s))
	}
	text := []byte(plain[3])
	var gz, zl bytes.Buffer
	gw := gzip.NewWriter(&gz)
	zw := zlib.NewWriter(&zl)
	for _, w := range []interface {
		Write([]byte) (int, error)
		Close() error
	}{gw, zw} {
		if _, err := w.Write(text); err != nil {
			f.Fatal(err)
		}
		if err := w.Close(); err != nil {
			f.Fatal(err)
		}
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		f.Fatal(err)
	}
	zs := enc.EncodeAll(text, nil)
	enc.Close()
	return append(seeds, gz.Bytes(), zl.Bytes(), zs)
}

// FuzzScanCSV: any bytes, read as a CSV file through the public API with its
// defaults, are a frame or an error, inside fuzzcheck's limits. The bytes are read
// twice, for the schema and for the frame, as a query reads them.
func FuzzScanCSV(f *testing.F) {
	for _, b := range csvSeeds(f) {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		n, ok := inflated(b)
		if !ok {
			t.Skip("decompresses past maxInflated")
		}
		fuzzcheck.Run(t, limits(n), func() error {
			lf := ursus.ScanCSVReader(b, "fuzz.csv")
			if _, err := lf.CollectSchema(t.Context()); err != nil {
				return err
			}
			_, err := lf.Collect(t.Context())
			return err
		})
	})
}
