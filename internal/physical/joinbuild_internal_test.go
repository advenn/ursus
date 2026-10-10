package physical

// Step 151: a join's build side inserted partitioned, at freeze. Every join that
// defers must answer as the streaming build does — the same rows in the same order,
// since a key's build rows stay ascending — and refuse a duplicate right key at the
// same row; one that reaches half the default budget catches up and streams.

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source/memsrc"
)

// sideScan is a scan of n rows of (k, name) in batches of 512: k an Int64 with
// one null in nulls, distinct up to keys values; name text.
func sideScan(t *testing.T, rng *rand.Rand, k, name string, n, keys, nulls int) *plan.Scan {
	t.Helper()
	sch := dtype.MustSchema(dtype.Of(k, dtype.Int64), dtype.Of(name, dtype.String))
	var batches []*data.Batch
	for lo := 0; lo < n; lo += 512 {
		m := min(512, n-lo)
		ks := make([]int64, m)
		valid := bitmap.NewBuilder(m)
		names := make([]string, m)
		for i := range m {
			ks[i] = int64(rng.IntN(keys))
			valid.Append(rng.IntN(nulls) != 0)
			names[i] = fmt.Sprintf("%s%d", name, lo+i)
		}
		b, err := data.NewBatch(sch, []*data.Column{
			data.NewFixed(k, dtype.Int64, ks, valid.Finish()),
			data.NewString(name, names, bitmap.AllSet(m)),
		})
		if err != nil {
			t.Fatal(err)
		}
		batches = append(batches, b)
	}
	src, err := memsrc.New(sch, batches...)
	if err != nil {
		t.Fatal(err)
	}
	return &plan.Scan{Src: src, Full: sch}
}

// runJoin plans and drains j, and returns its rows, rendered, with the build sink
// and the table its probe read: a *kernel.KeyTable, *kernel.KeyParts or
// *kernel.IntKeyParts.
func runJoin(t *testing.T, j *plan.Join, opts Options) ([]string, *joinBuildSink, any, error) {
	t.Helper()
	ctx := t.Context()
	op, err := planJoin(ctx, j, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	var rows []string
	for {
		b, err := op.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, nil, err
		}
		for i := range b.Rows() {
			var line strings.Builder
			for _, c := range b.Columns() {
				if !c.IsValid(i) {
					line.WriteString("∅|")
					continue
				}
				switch c.DType().ID() {
				case dtype.TypeString:
					line.WriteString(c.Strings().Get(i) + "|")
				default:
					fmt.Fprintf(&line, "%v|", reflect.ValueOf(c.FixedSlice()).Index(i))
				}
			}
			rows = append(rows, line.String())
		}
	}
	br := op.(*joinBreaker)
	sink := br.builder.(*joinBuildSink)
	var table *joinTable
	switch out := br.out.(type) {
	case *parProbeOp:
		table = out.workers[0].t
	case *joinProbeOp:
		table = out.t
	}
	if table.ints != nil {
		return rows, sink, table.ints, nil
	}
	return rows, sink, table.ids, nil
}

func TestThePartitionedBuildAnswersAsTheStreamingOne(t *testing.T) {
	serial := Options{Threads: 1, BatchSize: 512, Budget: execopt.NewBudget(0, "")}
	parallel := Options{Threads: 4, BatchSize: 512, Budget: execopt.NewBudget(0, "")}
	for _, kind := range []plan.JoinKind{plan.JoinInner, plan.JoinLeft, plan.JoinRight, plan.JoinFull} {
		for _, nullsEqual := range []bool{false, true} {
			name := fmt.Sprintf("%s, nulls equal %v", kind, nullsEqual)
			t.Run(name, func(t *testing.T) {
				join := func() *plan.Join {
					rng := rand.New(rand.NewPCG(151, uint64(kind)))
					return &plan.Join{
						Left:  sideScan(t, rng, "k", "l", 9_000, 6_000, 40),
						Right: sideScan(t, rng, "k2", "r", 12_000, 6_000, 40),
						// Several build rows a key, so a key's rows must stay in order.
						LeftOn:     []expr.Node{&expr.Col{Name: "k"}},
						RightOn:    []expr.Node{&expr.Col{Name: "k2"}},
						Kind:       kind,
						NullsEqual: nullsEqual,
					}
				}
				want, _, streamed, err := runJoin(t, join(), serial)
				if err != nil {
					t.Fatal(err)
				}
				got, sink, keys, err := runJoin(t, join(), parallel)
				if err != nil {
					t.Fatal(err)
				}
				if !sink.deferred {
					t.Fatal("the build did not defer")
				}
				// An Int64 key whose nulls match nothing is an integer (step 159).
				var n int
				switch parts := keys.(type) {
				case *kernel.KeyParts:
					if !nullsEqual {
						t.Fatal("the probe read encoded keys, not integers")
					}
					n = parts.Len()
				case *kernel.IntKeyParts:
					if nullsEqual {
						t.Fatal("the probe read integers where a null is a key")
					}
					n = parts.Len()
				default:
					t.Fatalf("the probe read a %T, not the partitioned tables", keys)
				}
				// A null key is no key unless nulls are equal, in both builds.
				if want := streamed.(*kernel.KeyTable).Len(); n != want {
					t.Errorf("%d keys, the streaming build has %d", n, want)
				}
				if len(got) != len(want) {
					t.Fatalf("%d rows, the streaming build gives %d", len(got), len(want))
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("row %d: %s, the streaming build gives %s", i, got[i], want[i])
					}
				}
			})
		}
	}
}

// TestThePartitionedBuildRefusesADuplicateAtTheSameRow: the refusal names the
// first duplicate of all, which is the first of each partition's firsts. Over eight
// fixtures, it is not always partition 0's.
func TestThePartitionedBuildRefusesADuplicateAtTheSameRow(t *testing.T) {
	for seed := range uint64(8) {
		join := func() *plan.Join {
			rng := rand.New(rand.NewPCG(151, 100+seed))
			return &plan.Join{
				Left:     sideScan(t, rng, "k", "l", 2_000, 50_000, 1_000_000),
				Right:    sideScan(t, rng, "k2", "r", 4_000, 50_000, 1_000_000),
				LeftOn:   []expr.Node{&expr.Col{Name: "k"}},
				RightOn:  []expr.Node{&expr.Col{Name: "k2"}},
				Kind:     plan.JoinInner,
				Validate: plan.ValidateManyToOne,
			}
		}
		opts := Options{Threads: 4, BatchSize: 512, Budget: execopt.NewBudget(0, "")}
		if !deferBuild(join(), opts) {
			t.Fatal("a validated join does not defer")
		}
		_, _, _, want := runJoin(t, join(), Options{Threads: 1, BatchSize: 512, Budget: execopt.NewBudget(0, "")})
		_, _, _, got := runJoin(t, join(), opts)
		if want == nil {
			t.Fatalf("seed %d: the fixture's right side has no duplicate key", seed)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("seed %d: the partitioned build refused with\n%v\nwhere the streaming one refuses with\n%v",
				seed, got, want)
		}
	}
}

func TestADeferredBuildPastHalfItsBudgetStreams(t *testing.T) {
	join := func() *plan.Join {
		rng := rand.New(rand.NewPCG(151, 11))
		return &plan.Join{
			Left:    sideScan(t, rng, "k", "l", 6_000, 4_000, 40),
			Right:   sideScan(t, rng, "k2", "r", 8_000, 4_000, 40),
			LeftOn:  []expr.Node{&expr.Col{Name: "k"}},
			RightOn: []expr.Node{&expr.Col{Name: "k2"}},
			Kind:    plan.JoinInner,
		}
	}
	want, _, _, err := runJoin(t, join(), Options{Threads: 1, BatchSize: 512, Budget: execopt.NewBudget(0, "")})
	if err != nil {
		t.Fatal(err)
	}
	b := execopt.NewBudget(96<<10, t.TempDir())
	b.MarkDefault()
	opts := Options{Threads: 4, BatchSize: 512, Budget: b}
	if !deferBuild(join(), opts) {
		t.Fatal("a default budget does not defer")
	}
	got, sink, keys, err := runJoin(t, join(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if sink.deferred {
		t.Fatal("the build never caught up")
	}
	switch keys.(type) {
	case *kernel.KeyParts, *kernel.IntKeyParts:
		t.Fatalf("the probe read the partitioned tables, %T, after a catch-up", keys)
	}
	// A spilled join's rows come out bucket by bucket, so compare them sorted.
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("%d rows, the streaming build alone gives %d, or they differ", len(got), len(want))
	}
}

// typedScan is a scan of n rows of (k, name) in batches of 512, k of type dt from
// raw values converted to T, so they wrap at T's width; a null in nulls.
func typedScan[T int8 | int16 | int32 | int64 | uint8 | uint16 | uint32 | uint64](t *testing.T, rng *rand.Rand, dt dtype.DataType, k, name string,
	n int, pool []int64, nulls int,
) *plan.Scan {
	t.Helper()
	sch := dtype.MustSchema(dtype.Of(k, dt), dtype.Of(name, dtype.String))
	var batches []*data.Batch
	for lo := 0; lo < n; lo += 512 {
		m := min(512, n-lo)
		ks := make([]T, m)
		valid := bitmap.NewBuilder(m)
		names := make([]string, m)
		for i := range m {
			ks[i] = T(pool[rng.IntN(len(pool))])
			valid.Append(rng.IntN(nulls) != 0)
			names[i] = fmt.Sprintf("%s%d", name, lo+i)
		}
		b, err := data.NewBatch(sch, []*data.Column{
			data.NewFixed(k, dt, ks, valid.Finish()),
			data.NewString(name, names, bitmap.AllSet(m)),
		})
		if err != nil {
			t.Fatal(err)
		}
		batches = append(batches, b)
	}
	src, err := memsrc.New(sch, batches...)
	if err != nil {
		t.Fatal(err)
	}
	return &plan.Scan{Src: src, Full: sch}
}

// TestTheIntegerKeysAnswerAsTheEncodedOnes: step 159's integer tables against the
// streaming build's encoded keys, for every integer width, the temporal types stored
// as integers, and two sides of different widths, which meet at the wider. The keys
// repeat, and include every width's extremes, which wrap where a width is narrower.
func TestTheIntegerKeysAnswerAsTheEncodedOnes(t *testing.T) {
	rng := rand.New(rand.NewPCG(159, 1))
	pool := []int64{0, -1, 1, math.MinInt64, math.MaxInt64, math.MinInt32, math.MaxInt32,
		math.MaxUint32, -128, 127, 255, 1 << 32}
	for range 300 {
		pool = append(pool, int64(rng.IntN(2_000))-1_000)
	}
	type side func(t *testing.T, rng *rand.Rand, k, name string, n int) *plan.Scan
	of := func(mk func(*testing.T, *rand.Rand, dtype.DataType, string, string, int, []int64, int) *plan.Scan,
		dt dtype.DataType) side {
		return func(t *testing.T, rng *rand.Rand, k, name string, n int) *plan.Scan {
			return mk(t, rng, dt, k, name, n, pool, 30)
		}
	}
	cases := []struct {
		name        string
		left, right side
	}{
		{"Int8", of(typedScan[int8], dtype.Int8), of(typedScan[int8], dtype.Int8)},
		{"Int16", of(typedScan[int16], dtype.Int16), of(typedScan[int16], dtype.Int16)},
		{"Int32", of(typedScan[int32], dtype.Int32), of(typedScan[int32], dtype.Int32)},
		{"Int64", of(typedScan[int64], dtype.Int64), of(typedScan[int64], dtype.Int64)},
		{"Uint8", of(typedScan[uint8], dtype.Uint8), of(typedScan[uint8], dtype.Uint8)},
		{"Uint16", of(typedScan[uint16], dtype.Uint16), of(typedScan[uint16], dtype.Uint16)},
		{"Uint32", of(typedScan[uint32], dtype.Uint32), of(typedScan[uint32], dtype.Uint32)},
		{"Uint64", of(typedScan[uint64], dtype.Uint64), of(typedScan[uint64], dtype.Uint64)},
		{"Date", of(typedScan[int32], dtype.Date), of(typedScan[int32], dtype.Date)},
		{"Datetime", of(typedScan[int64], dtype.Datetime(dtype.Micro, "")),
			of(typedScan[int64], dtype.Datetime(dtype.Micro, ""))},
		{"Int32 to Int64", of(typedScan[int32], dtype.Int32), of(typedScan[int64], dtype.Int64)},
		{"Uint32 to Int64", of(typedScan[uint32], dtype.Uint32), of(typedScan[int64], dtype.Int64)},
	}
	serial := Options{Threads: 1, BatchSize: 512, Budget: execopt.NewBudget(0, "")}
	parallel := Options{Threads: 4, BatchSize: 512, Budget: execopt.NewBudget(0, "")}
	for _, c := range cases {
		for _, kind := range []plan.JoinKind{plan.JoinInner, plan.JoinLeft, plan.JoinRight, plan.JoinFull} {
			t.Run(fmt.Sprintf("%s, %s", c.name, kind), func(t *testing.T) {
				join := func() *plan.Join {
					rng := rand.New(rand.NewPCG(159, uint64(kind)))
					return &plan.Join{
						Left:    c.left(t, rng, "k", "l", 3_000),
						Right:   c.right(t, rng, "k2", "r", 4_000),
						LeftOn:  []expr.Node{&expr.Col{Name: "k"}},
						RightOn: []expr.Node{&expr.Col{Name: "k2"}},
						Kind:    kind,
					}
				}
				want, _, _, err := runJoin(t, join(), serial)
				if err != nil {
					t.Fatal(err)
				}
				got, _, keys, err := runJoin(t, join(), parallel)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := keys.(*kernel.IntKeyParts); !ok {
					t.Fatalf("the probe read a %T, not integer tables", keys)
				}
				if len(want) == 0 {
					t.Fatal("the fixture joins nothing")
				}
				if !slices.Equal(got, want) {
					t.Fatalf("%d rows, the encoded keys give %d, or they differ", len(got), len(want))
				}
			})
		}
	}
}
