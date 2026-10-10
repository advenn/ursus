package physical_test

// Step 150: a group-by of many groups folds its workers partitioned, each partition
// on its own goroutine. Its answer must be the serial group-by's, row for row once
// sorted, for every aggregate that runs in parallel, and over keys that are strings,
// numbers and nulls.

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/physical"
)

// rowsOf renders every row of df, one line each, in df's order, each value as its
// cast to String gives it.
func rowsOf(t *testing.T, df *ursus.DataFrame) []string {
	t.Helper()
	text, err := df.Select(t.Context(), ursus.All().Cast(ursus.String))
	if err != nil {
		t.Fatal(err)
	}
	cols := make([][]string, 0, len(text.Columns()))
	for _, c := range text.Columns() {
		col, err := text.Column[string](c)
		if err != nil {
			t.Fatal(err)
		}
		vals := make([]string, col.Len())
		for i := range vals {
			v, ok := col.Get(i)
			if !ok {
				v = "null"
			}
			vals[i] = v
		}
		cols = append(cols, vals)
	}
	out := make([]string, df.Height())
	for i := range out {
		var b strings.Builder
		for _, c := range cols {
			fmt.Fprintf(&b, "%s|", c[i])
		}
		out[i] = b.String()
	}
	return out
}

func TestAPartitionedFoldAnswersAsTheSerialOne(t *testing.T) {
	rng := rand.New(rand.NewPCG(150, 1))
	n := 300_000
	k1 := make([]string, n)
	k2 := make([]int64, n)
	k2ok := make([]bool, n)
	v := make([]float64, n)
	w := make([]int64, n)
	for i := range n {
		k1[i] = fmt.Sprintf("s%d", rng.IntN(400))
		k2[i], k2ok[i] = int64(rng.IntN(500)), rng.IntN(50) != 0
		v[i] = float64(rng.IntN(1000)) / 8
		w[i] = int64(rng.IntN(1 << 20))
	}
	lf := ursus.Frame(ursus.Values("k1", k1), ursus.ValuesNullable("k2", k2, k2ok),
		ursus.Values("v", v), ursus.Values("w", w)).
		GroupBy(ursus.Col("k1"), ursus.Col("k2")).
		Agg(ursus.Col("v").Sum().Alias("sum"), ursus.Col("v").Mean().Alias("mean"),
			ursus.Col("w").Min().Alias("min"), ursus.Col("w").Max().Alias("max"),
			ursus.Len().Alias("len"), ursus.Col("v").Count().Alias("count"),
			ursus.Col("v").Var(1).Alias("var"), ursus.Col("w").NUnique().Alias("nunique"),
			ursus.Col("v").Quantile(0.5, ursus.InterpLinear).Alias("median"),
			ursus.Col("k1").Max().Alias("maxs"))

	serial, err := lf.Collect(t.Context(), ursus.WithThreads(1))
	if err != nil {
		t.Fatal(err)
	}
	if serial.Height() < 1<<16 {
		t.Fatalf("only %d groups: too few to fold partitioned", serial.Height())
	}
	before := physical.PartitionedFolds()
	par, err := lf.Collect(t.Context(), ursus.WithThreads(4), ursus.WithBatchSize(4096))
	if err != nil {
		t.Fatal(err)
	}
	if physical.PartitionedFolds() == before {
		t.Fatal("the fold did not run partitioned")
	}
	want, got := rowsOf(t, serial), rowsOf(t, par)
	slices.Sort(want)
	sortedGot := slices.Clone(got)
	slices.Sort(sortedGot)
	if len(want) != len(sortedGot) {
		t.Fatalf("%d groups, the serial group-by has %d", len(sortedGot), len(want))
	}
	for i := range want {
		if !sameRow(want[i], sortedGot[i]) {
			t.Fatalf("a group differs:\nserial:      %s\npartitioned: %s", want[i], sortedGot[i])
		}
	}

	// The same input and thread count give the same order, run after run.
	again, err := lf.Collect(t.Context(), ursus.WithThreads(4), ursus.WithBatchSize(4096))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rowsOf(t, again), got) {
		t.Error("two runs gave two orders")
	}
}

// TestAPartitionedFoldOfIntegerKeys: one integer key is folded in its own tables
// since step 160, routed by IntHash and its null by a fixed hash. Its answer is the
// encoded keys': the same group-by, on one thread, of the key cast to String.
func TestAPartitionedFoldOfIntegerKeys(t *testing.T) {
	rng := rand.New(rand.NewPCG(160, 1))
	n := 200_000
	k := make([]int64, n)
	ok := make([]bool, n)
	w := make([]int64, n)
	for i := range n {
		k[i], ok[i], w[i] = int64(rng.IntN(150_000))-75_000, rng.IntN(50) != 0, int64(rng.IntN(1<<20))
	}
	frame := ursus.Frame(ursus.ValuesNullable("k", k, ok), ursus.Values("w", w))
	aggs := []ursus.Expr{ursus.Col("w").Sum().Alias("sum"), ursus.Col("w").Min().Alias("min"),
		ursus.Len().Alias("len")}
	encoded, err := frame.GroupBy(ursus.Col("k").Cast(ursus.String)).Agg(aggs...).
		Collect(t.Context(), ursus.WithThreads(1))
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Height() < 1<<16 {
		t.Fatalf("only %d groups: too few to fold partitioned", encoded.Height())
	}
	before := physical.PartitionedFolds()
	par, err := frame.GroupBy(ursus.Col("k")).Agg(aggs...).
		Collect(t.Context(), ursus.WithThreads(4), ursus.WithBatchSize(4096))
	if err != nil {
		t.Fatal(err)
	}
	if physical.PartitionedFolds() == before {
		t.Fatal("the fold did not run partitioned")
	}
	want, got := rowsOf(t, encoded), rowsOf(t, par)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("%d groups, the encoded keys give %d, or they differ", len(got), len(want))
	}
}

func TestAFewGroupsFoldAsBefore(t *testing.T) {
	k := make([]int64, 100_000)
	for i := range k {
		k[i] = int64(i % 1000)
	}
	before := physical.PartitionedFolds()
	if _, err := ursus.Frame(ursus.Values("k", k)).GroupBy(ursus.Col("k")).Agg(ursus.Len()).
		Collect(t.Context(), ursus.WithThreads(4), ursus.WithBatchSize(1024)); err != nil {
		t.Fatal(err)
	}
	if physical.PartitionedFolds() != before {
		t.Error("a thousand groups folded partitioned; the serial fold keeps their order")
	}
}

// sameRow compares two rendered rows field by field: a number to a relative 1e-12,
// since Chan's merge of two variances and Welford's running update round their
// last digit differently, as every parallel fold's var always has; anything else
// exactly. Keys come first and are text or integers, so the sort orders both alike.
func sameRow(a, b string) bool {
	fa, fb := strings.Split(a, "|"), strings.Split(b, "|")
	if len(fa) != len(fb) {
		return false
	}
	for i := range fa {
		if fa[i] == fb[i] {
			continue
		}
		x, errx := strconv.ParseFloat(fa[i], 64)
		y, erry := strconv.ParseFloat(fb[i], 64)
		if errx != nil || erry != nil || math.Abs(x-y) > 1e-12*math.Max(math.Abs(x), math.Abs(y)) {
			return false
		}
	}
	return true
}
