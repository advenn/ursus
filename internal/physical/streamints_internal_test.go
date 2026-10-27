package physical

// Step 398: the streaming build keys its one integer key by the integer (B4 of
// v0.8-scope.md), where it encoded every key; at its first split it turns the table
// into encoded keys, each id kept, as a spill routes by their bytes.

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
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

// keyedScan is n rows of a key k of type dt, distinct up to keys values, every
// tenth null, and a value v, in batches of 512.
func keyedScan(t *testing.T, rng *rand.Rand, k, v string, dt dtype.DataType, n, keys int) *plan.Scan {
	t.Helper()
	sch := dtype.MustSchema(dtype.Of(k, dt), dtype.Of(v, dtype.Int64))
	var batches []*data.Batch
	for lo := 0; lo < n; lo += 512 {
		m := min(512, n-lo)
		ks, vs := make([]int64, m), make([]int64, m)
		valid := bitmap.NewBuilder(m)
		for i := range m {
			ks[i], vs[i] = int64(rng.IntN(keys)), int64(lo+i)
			valid.Append((lo+i)%10 != 3)
		}
		key := kernel.IntKeyColumn(k, dt, ks).WithValidity(valid.Finish())
		b, err := data.NewBatch(sch, []*data.Column{key, data.NewFixed(v, dtype.Int64, vs, bitmap.AllSet(m))})
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

// TestTheStreamingBuildKeysItsInteger: under a limit the caller set, a join of one
// integer key of any width, a date's too, keys its streaming build by the integer,
// and answers as it does; past the limit, a kind that keeps its build rows spills,
// its keys encoded at the split, and answers the same rows. An Enum is keyed by its
// index, which it encodes as.
func TestTheStreamingBuildKeysItsInteger(t *testing.T) {
	cats := make([]string, 100)
	for i := range cats {
		cats[i] = fmt.Sprintf("c%d", i)
	}
	for _, dt := range []dtype.DataType{dtype.Int8, dtype.Int16, dtype.Int32, dtype.Int64,
		dtype.Uint8, dtype.Uint16, dtype.Uint32, dtype.Uint64, dtype.Date, dtype.Enum(cats...)} {
		for _, kind := range []plan.JoinKind{plan.JoinInner, plan.JoinLeft, plan.JoinFull, plan.JoinSemi} {
			t.Run(fmt.Sprintf("%s, %s", dt, kind), func(t *testing.T) {
				join := func() *plan.Join {
					rng := rand.New(rand.NewPCG(398, uint64(kind)))
					return &plan.Join{
						Left:    keyedScan(t, rng, "k", "l", dt, 3_000, 100),
						Right:   keyedScan(t, rng, "k2", "r", dt, 4_000, 100),
						LeftOn:  []expr.Node{&expr.Col{Name: "k"}},
						RightOn: []expr.Node{&expr.Col{Name: "k2"}},
						Kind:    kind,
					}
				}
				want, _, _, err := runJoin(t, join(), Options{Threads: 4, BatchSize: 512,
					Budget: execopt.NewBudget(0, "")})
				if err != nil {
					t.Fatal(err)
				}
				slices.Sort(want)

				got, sink, keys, err := runJoin(t, join(), streamingOpts())
				if err != nil {
					t.Fatal(err)
				}
				if _, ints := keys.(*kernel.IntKeyParts); sink.deferred || !ints {
					t.Fatalf("the streaming build read a %T, deferred %v", keys, sink.deferred)
				}
				slices.Sort(got)
				if !slices.Equal(got, want) {
					t.Fatalf("%d rows keyed by the integer, %d by the partitioned build", len(got), len(want))
				}

				small := Options{Threads: 1, BatchSize: 512, Budget: execopt.NewBudget(16<<10, t.TempDir())}
				got, sink, _, err = runJoin(t, join(), small)
				if err != nil {
					t.Fatal(err)
				}
				if kind != plan.JoinSemi && (sink.split == splitNone || sink.ints != nil) {
					t.Fatalf("under 16 KiB: split %v, still keyed by the integer %v", sink.split, sink.ints != nil)
				}
				slices.Sort(got)
				if !slices.Equal(got, want) {
					t.Fatalf("%d rows spilled, %d in memory", len(got), len(want))
				}
			})
		}
	}
}

// TestAOneThreadBuildDefers: on one thread, under the default budget or none, a keyed
// join defers its build and keys it as the partitioned build does, one partition
// (step 398); under a limit the caller set it streams, from its first batch.
func TestAOneThreadBuildDefers(t *testing.T) {
	rng := rand.New(rand.NewPCG(398, 10))
	join := &plan.Join{
		Left:    keyedScan(t, rng, "k", "l", dtype.Int64, 600, 50),
		Right:   keyedScan(t, rng, "k2", "r", dtype.Int64, 600, 50),
		LeftOn:  []expr.Node{&expr.Col{Name: "k"}},
		RightOn: []expr.Node{&expr.Col{Name: "k2"}},
		Kind:    plan.JoinInner,
	}
	def := execopt.NewBudget(1<<30, "")
	def.MarkDefault()
	for _, c := range []struct {
		name string
		opts Options
		want bool
	}{
		{"no budget", Options{Threads: 1, Budget: execopt.NewBudget(0, "")}, true},
		{"the default budget", Options{Threads: 1, Budget: def}, true},
		{"a limit of the caller's", streamingOpts(), false},
	} {
		if got := deferBuild(join, c.opts); got != c.want {
			t.Errorf("one thread, %s: deferred %v, want %v", c.name, got, c.want)
		}
	}
	_, sink, keys, err := runJoin(t, join, Options{Threads: 1, BatchSize: 512, Budget: execopt.NewBudget(0, "")})
	if err != nil {
		t.Fatal(err)
	}
	if _, ints := keys.(*kernel.IntKeyParts); !sink.deferred || !ints {
		t.Errorf("one thread's build: deferred %v, read a %T", sink.deferred, keys)
	}
}

// TestASemiJoinSpillsItsEncodedKeys: a Semi or Anti join keeps the table its split
// encoded, where a kind that keeps its rows builds another from them, so its answer
// past the limit is the encoding's: 5,000 keys of each width under 16 KiB.
func TestASemiJoinSpillsItsEncodedKeys(t *testing.T) {
	for _, dt := range []dtype.DataType{dtype.Int16, dtype.Int32, dtype.Uint32, dtype.Int64, dtype.Date} {
		for _, kind := range []plan.JoinKind{plan.JoinSemi, plan.JoinAnti} {
			join := func() *plan.Join {
				rng := rand.New(rand.NewPCG(398, 11))
				return &plan.Join{
					Left:    keyedScan(t, rng, "k", "l", dt, 3_000, 5_000),
					Right:   keyedScan(t, rng, "k2", "r", dt, 6_000, 5_000),
					LeftOn:  []expr.Node{&expr.Col{Name: "k"}},
					RightOn: []expr.Node{&expr.Col{Name: "k2"}},
					Kind:    kind,
				}
			}
			want, _, _, err := runJoin(t, join(), streamingOpts())
			if err != nil {
				t.Fatal(err)
			}
			small := Options{Threads: 1, BatchSize: 512, Budget: execopt.NewBudget(16<<10, t.TempDir())}
			got, sink, _, err := runJoin(t, join(), small)
			if err != nil {
				t.Fatal(err)
			}
			if sink.split == splitNone || sink.ints != nil {
				t.Fatalf("%s, %s under 16 KiB: split %v, keyed by the integer %v", dt, kind, sink.split, sink.ints != nil)
			}
			slices.Sort(want)
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("%s, %s: %d rows spilled, %d in memory", dt, kind, len(got), len(want))
			}
		}
	}
}

// TestAPlainProbeCutsItsBatches: a probe row of 2,000 matches is cut at the batch size
// and resumed, as the loop stepPlain stands in for cut it.
func TestAPlainProbeCutsItsBatches(t *testing.T) {
	rng := rand.New(rand.NewPCG(398, 12))
	j := &plan.Join{
		Left:    keyedScan(t, rng, "k", "l", dtype.Int64, 5, 1),
		Right:   keyedScan(t, rng, "k2", "r", dtype.Int64, 2_000, 1),
		LeftOn:  []expr.Node{&expr.Col{Name: "k"}},
		RightOn: []expr.Node{&expr.Col{Name: "k2"}},
		Kind:    plan.JoinInner,
	}
	op, err := planJoin(t.Context(), j, streamingOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	total := 0
	for {
		b, err := op.Next(t.Context())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if b.Rows() > 512 {
			t.Fatalf("a batch of %d rows, past the batch size of 512", b.Rows())
		}
		total += b.Rows()
	}
	// Every tenth row's key is null, the fourth of five on the left, and 200 of the
	// 2,000 on the right: four probe rows of 1,800 matches.
	if total != 4*1_800 {
		t.Errorf("%d rows, want %d", total, 4*1_800)
	}
}
