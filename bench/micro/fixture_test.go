package micro

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

const (
	// rows is the fixture size. Large enough that a worker has real work and
	// the group tables outgrow L2, small enough that the whole suite runs in
	// under a minute per -count on an idle laptop.
	rows = 1 << 20 // ~1.05M

	// groups gives the high-cardinality key ~16k distinct values, which is past
	// the point where a group-by is a handful of accumulators in cache.
	groups = 1 << 14
)

var regions = []string{"eu", "us", "apac", "latam"}

// fixture is built once per `go test` process and shared by every benchmark.
// Writing a million rows to Parquet is not what any of these measure.
type fixture struct {
	dir     string
	parquet string
	csv     string
	wide    string // 32 columns, of which most queries read two
}

var (
	fixtureOnce sync.Once
	shared      *fixture
	fixtureErr  error
)

func load(b *testing.B) *fixture {
	b.Helper()
	fixtureOnce.Do(func() { shared, fixtureErr = build() })
	if fixtureErr != nil {
		b.Fatalf("building fixture: %v", fixtureErr)
	}
	return shared
}

func build() (*fixture, error) {
	dir, err := os.MkdirTemp("", "ursus-micro-*")
	if err != nil {
		return nil, err
	}
	f := &fixture{
		dir:     dir,
		parquet: filepath.Join(dir, "events.parquet"),
		csv:     filepath.Join(dir, "events.csv"),
		wide:    filepath.Join(dir, "wide.parquet"),
	}

	ctx := context.Background()
	if err := frame().SinkParquet(ctx, f.parquet); err != nil {
		return nil, err
	}
	if err := frame().SinkCSV(ctx, f.csv); err != nil {
		return nil, err
	}
	if err := wideFrame().SinkParquet(ctx, f.wide); err != nil {
		return nil, err
	}
	return f, nil
}

// frame is the shared shape: an id, two numerics, a low-cardinality string, a
// high-cardinality key, a timestamp and a text column with real variation in it.
//
// Deliberately unlike the root bench_test.go fixture, whose values are all
// `index % constant`. Modular data has no skew, no repeated hash collisions and
// perfectly uniform group sizes, which flatters a hash table. A seeded PRNG
// costs nothing here — the fixture is built once — and gives group sizes that
// vary the way real ones do.
func frame() *ursus.LazyFrame {
	rng := rand.New(rand.NewPCG(1, 2))

	id := make([]int64, rows)
	qty := make([]int64, rows)
	price := make([]float64, rows)
	region := make([]string, rows)
	key := make([]int64, rows)
	ts := make([]time.Time, rows)
	label := make([]string, rows)

	base := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	for i := range id {
		id[i] = int64(i)
		qty[i] = int64(rng.IntN(100))
		price[i] = rng.Float64() * 1000
		region[i] = regions[rng.IntN(len(regions))]
		// Zipf-ish: squaring a uniform draw concentrates most rows in the low
		// keys, so the group sizes are lopsided rather than uniform.
		u := rng.Float64()
		key[i] = int64(u * u * float64(groups))
		ts[i] = base.Add(time.Duration(rng.IntN(365*24)) * time.Hour)
		label[i] = "sku-" + regions[rng.IntN(len(regions))] + "-" + itoa(rng.IntN(10000))
	}

	return ursus.Frame(
		ursus.Values("id", id),
		ursus.Values("qty", qty),
		ursus.Values("price", price),
		ursus.Values("region", region),
		ursus.Values("key", key),
		ursus.Values("ts", ts),
		ursus.Values("label", label),
	)
}

// wideFrame is 32 numeric columns. Queries that read two of them measure
// whether projection actually reaches the Parquet reader.
func wideFrame() *ursus.LazyFrame {
	const width = 32
	rng := rand.New(rand.NewPCG(3, 4))

	columns := make([]*ursus.Column, 0, width)
	for c := range width {
		values := make([]float64, rows/4)
		for i := range values {
			values[i] = rng.Float64()
		}
		columns = append(columns, ursus.Values("c"+itoa(c), values))
	}
	return ursus.Frame(columns...)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// consume drains a query without keeping the rows, so a benchmark measures the
// engine rather than the allocator handing back a result nobody reads.
func consume(b *testing.B, lf *ursus.LazyFrame, opts ...ursus.CollectOption) {
	b.Helper()
	n, err := lf.Count(b.Context(), opts...)
	if err != nil {
		b.Fatal(err)
	}
	if n < 0 {
		b.Fatalf("negative row count %d", n)
	}
}

func collect(b *testing.B, lf *ursus.LazyFrame, opts ...ursus.CollectOption) {
	b.Helper()
	df, err := lf.Collect(b.Context(), opts...)
	if err != nil {
		b.Fatal(err)
	}
	if df.Height() < 0 {
		b.Fatalf("negative height %d", df.Height())
	}
}
