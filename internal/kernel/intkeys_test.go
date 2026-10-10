package kernel

// Step 159: IntKeyTable against a map, through growth, partitioning and lookups that
// miss; and IntKeys's widening, which must keep every width's distinct values
// distinct.

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// intKeyPool is n keys drawn from a small range, so they repeat, and from every
// width's extremes, so a widening that confused two would be met.
func intKeyPool(rng *rand.Rand, n int) []int64 {
	edges := []int64{0, -1, 1, math.MinInt64, math.MaxInt64, math.MinInt32, math.MaxInt32,
		math.MaxUint32, -128, 127, 255, 1 << 32, -1 << 32}
	out := make([]int64, n)
	for i := range out {
		if rng.IntN(4) == 0 {
			out[i] = edges[rng.IntN(len(edges))]
		} else {
			out[i] = int64(rng.IntN(n/3+1)) - int64(n/6)
		}
	}
	return out
}

func TestIntKeyTableNumbersAsAMap(t *testing.T) {
	for _, n := range []int{0, 1, 63, 64, 65, 1_000, 50_000} {
		rng := rand.New(rand.NewPCG(159, uint64(n)))
		keys := intKeyPool(rng, n)
		tbl := NewIntKeyTable()
		want := map[int64]int32{}
		var ids [ManyChunk]int32
		var inserted [ManyChunk]bool
		for lo := 0; lo < n; lo += ManyChunk {
			hi := min(lo+ManyChunk, n)
			tbl.GetOrInsertMany(keys[lo:hi], ids[:], inserted[:])
			for j, k := range keys[lo:hi] {
				id, ok := want[k]
				if !ok {
					id = int32(len(want))
					want[k] = id
				}
				if ids[j] != id || inserted[j] == ok {
					t.Fatalf("n=%d: key %d got id %d, inserted %v; want %d, %v",
						n, k, ids[j], inserted[j], id, !ok)
				}
			}
		}
		if tbl.Len() != len(want) {
			t.Fatalf("n=%d: %d keys, want %d", n, tbl.Len(), len(want))
		}

		// Every key found at its id, and keys never inserted not found.
		probe := append(intKeyPool(rand.New(rand.NewPCG(160, uint64(n))), n+100), keys...)
		for lo := 0; lo < len(probe); lo += ManyChunk {
			hi := min(lo+ManyChunk, len(probe))
			tbl.GetMany(probe[lo:hi], ids[:])
			for j, k := range probe[lo:hi] {
				id, ok := want[k]
				if !ok {
					id = -1
				}
				if ids[j] != id {
					t.Fatalf("n=%d: GetMany(%d) = %d, want %d", n, k, ids[j], id)
				}
			}
		}
	}
}

// TestIntKeyPartsNumbersAcrossTables: keys routed by PartitionOf into tables of their
// own, read back as one with each table's base, as the join's partitioned build does.
func TestIntKeyPartsNumbersAcrossTables(t *testing.T) {
	const n, parts = 20_000, 5
	rng := rand.New(rand.NewPCG(159, 7))
	keys := intKeyPool(rng, n)
	tables := make([]*IntKeyTable, parts)
	local := map[int64][2]int32{} // key -> partition, local id
	var ids [ManyChunk]int32
	var inserted [ManyChunk]bool
	for p := range tables {
		tables[p] = NewIntKeyTable()
		tables[p].Reserve(n / parts / 2)
	}
	for _, k := range keys {
		p := PartitionOf(IntHash(k), parts)
		tables[p].GetOrInsertMany([]int64{k}, ids[:1], inserted[:1])
		local[k] = [2]int32{int32(p), ids[0]}
	}
	all := NewIntKeyParts(tables)
	if all.Len() != len(local) {
		t.Fatalf("%d keys, want %d", all.Len(), len(local))
	}
	probe := append(intKeyPool(rand.New(rand.NewPCG(159, 8)), n), keys...)
	for lo := 0; lo < len(probe); lo += ManyChunk {
		hi := min(lo+ManyChunk, len(probe))
		all.GetMany(probe[lo:hi], ids[:])
		for j, k := range probe[lo:hi] {
			want := int32(-1)
			if pl, ok := local[k]; ok {
				want = all.Base(int(pl[0])) + pl[1]
			}
			if ids[j] != want {
				t.Fatalf("GetMany(%d) = %d, want %d", k, ids[j], want)
			}
		}
	}
}

// TestIntKeysWidensByBits: each width's values come back as the int64 of their bits,
// sign-extended if signed, so two distinct values never widen alike; an Int64
// column's are its own; and a payload-free column reads as zeros.
func TestIntKeysWidensByBits(t *testing.T) {
	valid := bitmap.AllSet(4)
	cases := []struct {
		col  *data.Column
		want []int64
	}{
		{data.NewFixed("k", dtype.Int8, []int8{-128, -1, 0, 127}, valid), []int64{-128, -1, 0, 127}},
		{data.NewFixed("k", dtype.Uint8, []uint8{0, 1, 128, 255}, valid), []int64{0, 1, 128, 255}},
		{data.NewFixed("k", dtype.Int16, []int16{math.MinInt16, -1, 1, math.MaxInt16}, valid),
			[]int64{math.MinInt16, -1, 1, math.MaxInt16}},
		{data.NewFixed("k", dtype.Uint16, []uint16{0, 1, 1 << 15, math.MaxUint16}, valid),
			[]int64{0, 1, 1 << 15, math.MaxUint16}},
		{data.NewFixed("k", dtype.Int32, []int32{math.MinInt32, -1, 1, math.MaxInt32}, valid),
			[]int64{math.MinInt32, -1, 1, math.MaxInt32}},
		{data.NewFixed("k", dtype.Date, []int32{-719162, -1, 0, 20000}, valid), []int64{-719162, -1, 0, 20000}},
		{data.NewFixed("k", dtype.Uint32, []uint32{0, 1, 1 << 31, math.MaxUint32}, valid),
			[]int64{0, 1, 1 << 31, math.MaxUint32}},
		{data.NewFixed("k", dtype.Uint64, []uint64{0, 1, 1 << 63, math.MaxUint64}, valid),
			[]int64{0, 1, math.MinInt64, -1}},
		{data.NewFixed("k", dtype.Datetime(dtype.Micro, "UTC"), []int64{math.MinInt64, -1, 0, math.MaxInt64}, valid),
			[]int64{math.MinInt64, -1, 0, math.MaxInt64}},
	}
	for _, c := range cases {
		if !IsIntKey(c.col.DType()) {
			t.Fatalf("%s is not an integer key", c.col.DType())
		}
		var scratch []int64
		got := IntKeys(c.col, &scratch)
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("%s row %d: %d, want %d", c.col.DType(), i, got[i], c.want[i])
			}
		}
	}

	own := data.NewFixed("k", dtype.Int64, []int64{5, 6}, bitmap.AllSet(2))
	var scratch []int64
	if got := IntKeys(own, &scratch); &got[0] != &data.MustValues[int64](own)[0] || scratch != nil {
		t.Error("an Int64 column's keys were copied")
	}

	// A payload-free column is all null: its keys are read, as zeros, and skipped.
	payloadFree := data.NewNull("k", dtype.Int32, 3)
	if !payloadFree.IsPayloadFree() {
		t.Fatal("the fixture has a payload")
	}
	scratch = []int64{9, 9, 9, 9}
	if got := IntKeys(payloadFree, &scratch); len(got) != 3 || got[0]|got[1]|got[2] != 0 {
		t.Errorf("a payload-free column read as %v", got)
	}

	for _, dt := range []dtype.DataType{dtype.Float64, dtype.String, dtype.Bool, dtype.Int128} {
		if IsIntKey(dt) {
			t.Errorf("%s is an integer key", dt)
		}
	}
}

// TestIntKeyTableComparesTheKeyNotItsTag: 107450 and 189447 hash alike in their top
// 32 bits, the tag a slot keeps, and in the low six that place them in a new table of
// 64 slots, so one's search meets the other's slot first. Found by search, since
// random keys almost never do both.
func TestIntKeyTableComparesTheKeyNotItsTag(t *testing.T) {
	const a, b = 107450, 189447
	ha, hb := IntHash(a), IntHash(b)
	if ha>>32 != hb>>32 || ha&63 != hb&63 {
		t.Fatalf("the fixture's hashes %#x and %#x no longer collide", ha, hb)
	}
	tbl := NewIntKeyTable()
	var ids [2]int32
	var inserted [2]bool
	tbl.GetOrInsertMany([]int64{a}, ids[:1], inserted[:1])
	tbl.GetMany([]int64{b, a}, ids[:])
	if ids != [2]int32{-1, 0} {
		t.Fatalf("GetMany(%d, %d) = %v, want [-1 0]", b, a, ids)
	}
	tbl.GetOrInsertMany([]int64{b, a}, ids[:], inserted[:])
	if ids != [2]int32{1, 0} || inserted != [2]bool{true, false} {
		t.Fatalf("GetOrInsertMany(%d, %d) = %v, %v; want [1 0], [true false]", b, a, ids, inserted)
	}
}
