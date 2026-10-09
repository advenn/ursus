package physical

// Step 151: a join's build side inserted partitioned, at freeze. Every join that
// defers must answer as the streaming build does — the same rows in the same order,
// since a key's build rows stay ascending — and refuse a duplicate right key at the
// same row; one that reaches half the default budget catches up and streams.

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
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
// and the table its probe read.
func runJoin(t *testing.T, j *plan.Join, opts Options) ([]string, *joinBuildSink, joinKeys, error) {
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
					v, _ := data.Values[int64](c)
					fmt.Fprintf(&line, "%d|", v[i])
				}
			}
			rows = append(rows, line.String())
		}
	}
	br := op.(*joinBreaker)
	sink := br.builder.(*joinBuildSink)
	var keys joinKeys
	switch out := br.out.(type) {
	case *parProbeOp:
		keys = out.workers[0].t.ids
	case *joinProbeOp:
		keys = out.t.ids
	}
	return rows, sink, keys, nil
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
				parts, ok := keys.(*kernel.KeyParts)
				if !ok {
					t.Fatalf("the probe read a %T, not the partitioned tables", keys)
				}
				// A null key is no key unless nulls are equal, in both builds.
				if n, want := parts.Len(), streamed.(*kernel.KeyTable).Len(); n != want {
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
	if _, ok := keys.(*kernel.KeyParts); ok {
		t.Fatal("the probe read the partitioned tables after a catch-up")
	}
	// A spilled join's rows come out bucket by bucket, so compare them sorted.
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("%d rows, the streaming build alone gives %d, or they differ", len(got), len(want))
	}
}
