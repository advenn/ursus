package ursus_test

// Step 389: a CSV file's float, read in one pass since step 373 only where it was
// Clinger's exact case, a whole number below 2^53 over a power of ten of at most 22, is
// read in one pass however it is written, its fraction eight digits at a time and its
// value by Eisel-Lemire, as strconv finds it; and Collect copies each column's rows on
// the query's threads, a piece each, where it copied them on one.

import (
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestACSVsFloatsReadAsStrconvReadsThem: 120,000 rows, more than a piece of Collect's
// copy, of floats written in full, as Go and Polars write them, with exponents and
// subnormals, as h2o writes them, to six places, and nulls; beside text with nulls and
// empty strings and an integer. Every float is strconv.ParseFloat's to the bit, and
// the file reads the same on one thread and four.
func TestACSVsFloatsReadAsStrconvReadsThem(t *testing.T) {
	const n = 120_000
	rng := rand.New(rand.NewPCG(389, 2))
	var b strings.Builder
	b.WriteString("i,f,s\n")
	ftext, stext, snull := make([]string, n), make([]string, n), make([]bool, n)
	for i := range n {
		switch rng.IntN(5) {
		case 0:
			ftext[i] = strconv.FormatFloat(float64(i)/3, 'f', -1, 64)
		case 1:
			f := math.Float64frombits(rng.Uint64())
			if math.IsNaN(f) || math.IsInf(f, 0) {
				f = math.Copysign(0, -1)
			}
			ftext[i] = strconv.FormatFloat(f, 'g', -1, 64)
		case 2:
			ftext[i] = strconv.FormatFloat(rng.Float64()*1000, 'f', 6, 64)
		case 3:
			ftext[i] = strconv.FormatFloat(rng.NormFloat64(), 'e', rng.IntN(20), 64)
		default:
			ftext[i] = "" // a null
		}
		switch rng.IntN(6) {
		case 0:
			snull[i] = true
			b.WriteString(strconv.Itoa(i) + "," + ftext[i] + ",\n") // a null
		case 1:
			b.WriteString(strconv.Itoa(i) + "," + ftext[i] + ",\"\"\n") // the empty string
		default:
			stext[i] = "s" + strconv.Itoa(rng.IntN(1000))
			b.WriteString(strconv.Itoa(i) + "," + ftext[i] + "," + stext[i] + "\n")
		}
	}
	lf := ursus.ScanCSVReader([]byte(b.String()), "floats.csv",
		ursus.WithSchemaOverrides(map[string]ursus.DataType{"f": ursus.Float64, "s": ursus.String}))
	var frames [2]*ursus.DataFrame
	for k, threads := range []int{1, 4} {
		df, err := lf.Collect(t.Context(), ursus.WithThreads(threads))
		if err != nil {
			t.Fatal(err)
		}
		if df.Height() != n {
			t.Fatalf("%d threads: %d rows, want %d", threads, df.Height(), n)
		}
		f, err := df.Column[float64]("f")
		if err != nil {
			t.Fatal(err)
		}
		for i := range n {
			got, ok := f.Get(i)
			if ftext[i] == "" {
				if ok {
					t.Fatalf("%d threads, row %d: %v, want a null", threads, i, got)
				}
				continue
			}
			want, _ := strconv.ParseFloat(ftext[i], 64)
			if !ok || math.Float64bits(got) != math.Float64bits(want) {
				t.Fatalf("%d threads, row %d, %q: %v (%v), want %v", threads, i, ftext[i], got, ok, want)
			}
		}
		frames[k] = df
	}
	for k, df := range frames {
		iv, _ := df.Column[int64]("i")
		sv, _ := df.Column[string]("s")
		for i := range n {
			if v, ok := iv.Get(i); !ok || v != int64(i) {
				t.Fatalf("frame %d, row %d: i is %d, want %d", k, i, v, i)
			}
			if v, ok := sv.Get(i); ok == snull[i] || v != stext[i] {
				t.Fatalf("frame %d, row %d: s is %q (%v), want %q", k, i, v, ok, stext[i])
			}
		}
	}
}
