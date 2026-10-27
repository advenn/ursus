package ursus_test

// Step 394: a cancelled query answers its cancellation, or, where it finished first,
// its whole answer; never the rows it read before the cancel. A parallel operator's
// lanes, closed by a cancel, read as the end of its input, and a group-by above it
// answered on what it had: a cancelled count of a CSV file returned an empty frame
// and no error.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestACancelledQueryNeverAnswersInPart: 300,000 rows read from CSV and Parquet bytes
// and from a frame, counted and summed, grouped, sorted and joined, each cancelled at
// moments spread over its own running time: each answer is the cancellation or the
// query's whole answer.
func TestACancelledQueryNeverAnswersInPart(t *testing.T) {
	const n = 300_000
	k, v := make([]int64, n), make([]int64, n)
	var csvText strings.Builder
	csvText.WriteString("k,v\n")
	for i := range n {
		r := uint64(i) * 0x9E3779B97F4A7C15
		k[i], v[i] = int64(r>>40%100), int64(r>>20%10_000)
		fmt.Fprintf(&csvText, "%d,%d\n", k[i], v[i])
	}
	frame, err := ursus.Frame(ursus.Values("k", k), ursus.Values("v", v)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var pq bytes.Buffer
	if err := frame.Lazy().WriteParquet(t.Context(), &pq); err != nil {
		t.Fatal(err)
	}
	ks := make([]int64, 100)
	for i := range ks {
		ks[i] = int64(i)
	}
	dim := ursus.Frame(ursus.Values("k", ks), ursus.Values("w", ks))
	c := ursus.Col
	count := func(lf *ursus.LazyFrame) *ursus.LazyFrame {
		return lf.Select(c("v").Count().Alias("n"), c("v").Sum().Alias("sum"))
	}
	queries := map[string]*ursus.LazyFrame{
		"a CSV file counted":     count(ursus.ScanCSVReader([]byte(csvText.String()), "t.csv")),
		"a Parquet file counted": count(ursus.ScanParquetBytes(pq.Bytes(), "t.parquet")),
		"a frame grouped":        frame.Lazy().GroupBy(c("k")).Agg(c("v").Sum()).Sort(ursus.Asc(c("k"))),
		"a frame sorted":         frame.Lazy().Sort(ursus.Asc(c("v")), ursus.Asc(c("k"))).Head(5),
		"a frame joined":         count(frame.Lazy().Filter(c("v").Gt(int64(100))).Join(dim, ursus.JoinOn(c("k")))),
	}
	rng := rand.New(rand.NewPCG(394, 2))
	for name, lf := range queries {
		t0 := time.Now()
		want, err := lf.Collect(t.Context(), ursus.WithThreads(4))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		took := time.Since(t0)
		cancelled := 0
		for trial := range 24 {
			ctx, cancel := context.WithCancel(t.Context())
			at := time.Duration(rng.Int64N(int64(took)*5/4 + 1))
			if trial == 0 {
				at = 0
			}
			timer := time.AfterFunc(at, cancel)
			got, err := lf.Collect(ctx, ursus.WithThreads(4))
			timer.Stop()
			cancel()
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("%s, cancelled at %v: %v", name, at, err)
				}
				cancelled++
				continue
			}
			ursustest.AssertFrameEqual(t, got, want)
		}
		if cancelled == 0 {
			t.Errorf("%s: no trial was cancelled before it finished", name)
		}
	}
}
