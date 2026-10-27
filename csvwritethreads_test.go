package ursus_test

// Step 390: the CSV writer formats its batches at once, a goroutine each, as many as
// the query's threads, and writes their texts in order, where one goroutine made and
// wrote every record; and a Date, a naive Datetime and a Time are written digit by
// digit where the time package interpreted a layout a field at a time.

import (
	"bytes"
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// TestCSVWritesTheSameBytesOnFourThreadsAsOnOne: 30,000 rows in batches of 997, of
// integers, floats with NaN, infinities and nulls, text that must be quoted and nulls,
// dates and instants of years either side of 0 to 9999, naive at three units and
// zoned, times of day and booleans; written on one thread and four, under the default
// options, every field quoted with a null's text, another separator, a datetime
// format and a byte-order mark, and gzip without a header: the same bytes each time.
// And a Date's, a naive Datetime's and a Time's text is the time package's layout's.
func TestCSVWritesTheSameBytesOnFourThreadsAsOnOne(t *testing.T) {
	const n = 30_000
	rng := rand.New(rand.NewPCG(390, 2))
	texts := []string{"plain", "with,comma", `with "quote"`, "line\nbreak", "", "trailing ", "ñandú"}
	iv, fv, fOK := make([]int64, n), make([]float64, n), make([]bool, n)
	sv, sOK, bv := make([]string, n), make([]bool, n), make([]bool, n)
	days, ticks, tod := make([]int32, n), make([]int64, n), make([]int64, n)
	for k := range n {
		iv[k], bv[k] = rng.Int64()>>rng.IntN(64), rng.IntN(2) == 0
		fv[k], fOK[k] = rng.NormFloat64()*math.Pow(10, float64(rng.IntN(40)-20)), rng.IntN(9) > 0
		switch rng.IntN(20) {
		case 0:
			fv[k] = math.NaN()
		case 1:
			fv[k] = math.Inf(1 - 2*rng.IntN(2))
		}
		sv[k], sOK[k] = texts[rng.IntN(len(texts))]+strconv.Itoa(k), rng.IntN(7) > 0
		days[k] = int32(rng.Int64N(8_000_000) - 4_000_000) // the years -8,982 to 12,921
		ticks[k] = rng.Int64N(1<<59) - rng.Int64N(1<<59)   // microseconds: ±18,000 years
		tod[k] = rng.Int64N(86_400_000_000_000)
	}
	c := ursus.Col
	src := ursus.Frame(ursus.Values("i", iv), ursus.ValuesNullable("f", fv, fOK), ursus.ValuesNullable("s", sv, sOK),
		ursus.Values("d", days), ursus.Values("t", ticks), ursus.Values("tod", tod), ursus.Values("b", bv)).
		WithColumns(
			c("d").Cast(ursus.Date),
			c("t").Cast(ursus.Datetime(ursus.Micro, "")).Alias("us"),
			c("t").Mul(int64(7)).Cast(ursus.Datetime(ursus.Nano, "")).Alias("ns"),
			c("t").FloorDiv(int64(1000)).Cast(ursus.Datetime(ursus.Milli, "Europe/Dublin")).Alias("zoned"),
			c("tod").Cast(ursus.TimeOf(ursus.Nano)),
		)
	for _, opts := range [][]ursus.CSVSinkOption{
		nil,
		{ursus.WithQuoteStyle(ursus.QuoteAlways), ursus.WithNullValue("NA")},
		{ursus.WithWriteSeparator(';'), ursus.WithDatetimeFormat("%Y/%m/%d %H:%M:%S"), ursus.WithIncludeBOM(true)},
		{ursus.WithWriteHeader(false), ursus.WithCSVCompression(ursus.CSVGzip)},
	} {
		var one, four bytes.Buffer
		if err := src.WriteCSV(t.Context(), &one, append(opts, ursus.WithThreads(1), ursus.WithBatchSize(997))...); err != nil {
			t.Fatal(err)
		}
		if err := src.WriteCSV(t.Context(), &four, append(opts, ursus.WithThreads(4), ursus.WithBatchSize(997))...); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(one.Bytes(), four.Bytes()) {
			at := 0
			for at < min(one.Len(), four.Len()) && one.Bytes()[at] == four.Bytes()[at] {
				at++
			}
			t.Fatalf("%d options: one thread wrote %d bytes and four %d, first apart at %d",
				len(opts), one.Len(), four.Len(), at)
		}
		if one.Len() < n*20 {
			t.Fatalf("%d options: %d bytes for %d rows", len(opts), one.Len(), n)
		}
	}

	// The temporal text against the time package's layouts, a row a line.
	var out bytes.Buffer
	err := src.Select(c("d"), c("us"), c("tod")).WriteCSV(t.Context(), &out, ursus.WithThreads(4), ursus.WithBatchSize(97))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	if len(lines) != n+2 || lines[0] != "d,us,tod" {
		t.Fatalf("%d lines, the first %q", len(lines), lines[0])
	}
	for k := range n {
		want := time.Unix(int64(days[k])*86_400, 0).UTC().Format("2006-01-02") + "," +
			time.UnixMicro(ticks[k]).UTC().Format("2006-01-02T15:04:05.000000") + "," +
			time.Unix(0, tod[k]).UTC().Format("15:04:05.000000000")
		if lines[k+1] != want {
			t.Fatalf("row %d: %q, want %q", k, lines[k+1], want)
		}
	}
}

// failAfter is a writer that fails once it has taken n bytes.
type failAfter struct{ n int }

func (f *failAfter) Write(p []byte) (int, error) {
	if f.n -= len(p); f.n < 0 {
		return 0, errors.New("disk full")
	}
	return len(p), nil
}

// TestCSVWriteStopsAtItsFirstError: with batches formatted ahead on four threads, a
// destination that fails partway and a batch whose query fails each end the write
// with their error, the batches still being formatted waited for.
func TestCSVWriteStopsAtItsFirstError(t *testing.T) {
	const n = 50_000
	vals := make([]string, n)
	for k := range vals {
		vals[k] = strconv.Itoa(k)
	}
	vals[n-10] = "not a number" // in a late batch
	src := ursus.Frame(ursus.Values("s", vals))
	err := src.WriteCSV(t.Context(), &failAfter{n: 100_000}, ursus.WithThreads(4), ursus.WithBatchSize(500))
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("a failing destination: %v", err)
	}
	var sb strings.Builder
	err = src.Select(ursus.Col("s").Cast(ursus.Int64)).WriteCSV(t.Context(), &sb, ursus.WithThreads(4), ursus.WithBatchSize(500))
	if err == nil || !strings.Contains(err.Error(), "not a number") {
		t.Fatalf("a failing batch: %v", err)
	}
}
