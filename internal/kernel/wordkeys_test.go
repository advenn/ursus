package kernel

// Steps 381 and 384: WordKeyTable against a map, of two words and of three, through
// growth and lookups that miss; KeyPacker's words equal exactly when their keys are;
// and n_unique of an integer column, keyed by two words, counting as a map does
// through merges and spills.

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// wordPool is n keys of words words that repeat, differ in one word alone, swap
// their words, and reach the words' extremes.
func wordPool(rng *rand.Rand, n, words int) [][3]uint64 {
	edges := []uint64{0, 1, math.MaxUint64, 1 << 63, math.MaxUint32, 1 << 32}
	out := make([][3]uint64, n)
	for i := range out {
		a, b, c := uint64(rng.IntN(n/4+1)), uint64(rng.IntN(8)), uint64(rng.IntN(3))
		switch rng.IntN(6) {
		case 0:
			a, b, c = edges[rng.IntN(len(edges))], edges[rng.IntN(len(edges))], edges[rng.IntN(len(edges))]
		case 1:
			a, b, c = b, c, a
		}
		if words == 2 {
			c = 0
		}
		out[i] = [3]uint64{a, b, c}
	}
	return out
}

func TestWordKeyTableAgainstAMap(t *testing.T) {
	rng := rand.New(rand.NewPCG(381, 1))
	for _, words := range []int{2, 3} {
		for _, reserve := range []int{0, 5000} {
			wordKeyTableAgainstAMap(t, rng, words, reserve)
		}
	}
}

func wordKeyTableAgainstAMap(t *testing.T, rng *rand.Rand, words, reserve int) {
	t.Helper()
	{
		tab := NewWordKeyTable(words)
		tab.Reserve(reserve)
		want := map[[3]uint64]int32{}
		keys := wordPool(rng, 20_000, words)
		// A key of two words is given no third, as the group-by's of two words is not.
		third := func(c []uint64) []uint64 {
			if words == 2 && rng.IntN(2) == 0 {
				return nil
			}
			return c
		}
		for lo := 0; lo < len(keys); {
			m := min(1+rng.IntN(ManyChunk), len(keys)-lo)
			var a, b, c [ManyChunk]uint64
			for j := range m {
				a[j], b[j], c[j] = keys[lo+j][0], keys[lo+j][1], keys[lo+j][2]
			}
			var ids [ManyChunk]int32
			var ins [ManyChunk]bool
			tab.GetOrInsertMany(a[:m], b[:m], third(c[:m]), ids[:m], ins[:m])
			for j := range m {
				k := keys[lo+j]
				id, had := want[k]
				if !had {
					id = int32(len(want))
					want[k] = id
				}
				if ids[j] != id || ins[j] == had {
					t.Fatalf("%d words, reserved %d: key %v given id %d inserted %v, want %d inserted %v",
						words, reserve, k, ids[j], ins[j], id, !had)
				}
			}
			lo += m
		}
		if tab.Len() != len(want) || len(want) < 1000 {
			t.Fatalf("%d words, reserved %d: %d keys, the map %d", words, reserve, tab.Len(), len(want))
		}
		for k, id := range want {
			if a, b, c := tab.KeyAt(id); a != k[0] || b != k[1] || c != k[2] {
				t.Fatalf("id %d holds (%d, %d, %d), want %v", id, a, b, c, k)
			}
			if tab.HashAt(id) != WordHash(k[0], k[1], k[2]) {
				t.Fatalf("id %d's hash is not its key's", id)
			}
		}
		// Lookups, half of them of keys never inserted, in the first word or the last.
		var a, b, c [ManyChunk]uint64
		var ids [ManyChunk]int32
		for range 200 {
			var ks [ManyChunk][3]uint64
			for j := range ManyChunk {
				ks[j] = keys[rng.IntN(len(keys))]
				if rng.IntN(2) == 0 {
					ks[j][words-1] += 1 << 40
				}
				a[j], b[j], c[j] = ks[j][0], ks[j][1], ks[j][2]
			}
			tab.GetMany(a[:], b[:], third(c[:]), ids[:])
			for j, k := range ks {
				id, ok := want[k]
				if !ok {
					id = -1
				}
				if ids[j] != id {
					t.Fatalf("GetMany of %v is %d, want %d", k, ids[j], id)
				}
			}
		}
		if tab.Len() != len(want) {
			t.Fatal("GetMany inserted")
		}
	}
}

// TestKeyPackerKeepsKeysApart: two rows' words are equal exactly when each column
// of theirs is null in both or present in both and equal, over every integer width,
// signed and not, at their extremes, with nulls whose slots hold a value, a column
// of no payload, sliced columns, and rows packed from an offset.
func TestKeyPackerKeepsKeysApart(t *testing.T) {
	rng := rand.New(rand.NewPCG(381, 2))
	const n = 600
	raw := func() []int64 {
		edges := []int64{0, -1, 1, math.MinInt64, math.MaxInt64, math.MinInt32, math.MaxInt32,
			math.MaxUint32, -128, 127, 255, -32768, 65535}
		v := make([]int64, n)
		for i := range v {
			v[i] = int64(rng.IntN(5)) - 2
			if rng.IntN(4) == 0 {
				v[i] = edges[rng.IntN(len(edges))]
			}
		}
		return v
	}
	col := func(dt dtype.DataType, free bool) *data.Column {
		if free {
			return data.NewNull("k", dt, n+7).Slice(7, n)
		}
		v := raw()
		b := bitmap.NewBuilder(n + 7)
		for range 7 {
			b.Append(true)
		}
		for range n {
			b.Append(rng.IntN(6) != 0)
		}
		valid := b.Finish()
		pad := func(x []int64) []int64 { return append(make([]int64, 7), x...) }
		w := pad(v)
		var c *data.Column
		switch dt {
		case dtype.Int8:
			c = data.NewFixed("k", dt, narrowTo[int8](w), valid)
		case dtype.Int16:
			c = data.NewFixed("k", dt, narrowTo[int16](w), valid)
		case dtype.Int32:
			c = data.NewFixed("k", dt, narrowTo[int32](w), valid)
		case dtype.Int64:
			c = data.NewFixed("k", dt, w, valid)
		case dtype.Uint8:
			c = data.NewFixed("k", dt, narrowTo[uint8](w), valid)
		case dtype.Uint16:
			c = data.NewFixed("k", dt, narrowTo[uint16](w), valid)
		case dtype.Uint32:
			c = data.NewFixed("k", dt, narrowTo[uint32](w), valid)
		case dtype.Uint64:
			c = data.NewFixed("k", dt, narrowTo[uint64](w), valid)
		case dtype.Date:
			c = data.NewFixed("k", dt, narrowTo[int32](w), valid)
		}
		return c.Slice(7, n)
	}
	type tc struct {
		types []dtype.DataType
		free  int // the column of no payload, or -1
	}
	cases := []tc{
		{[]dtype.DataType{dtype.Int32, dtype.Int32}, -1},
		{[]dtype.DataType{dtype.Int8, dtype.Uint16, dtype.Int64}, -1},
		{[]dtype.DataType{dtype.Uint64, dtype.Int32}, -1},
		{[]dtype.DataType{dtype.Int64, dtype.Int32, dtype.Int16, dtype.Int8}, -1},
		{[]dtype.DataType{dtype.Date, dtype.Uint8, dtype.Uint32}, 1},
		{[]dtype.DataType{dtype.Int16, dtype.Int16}, 0},
		// Three words: PDS-H q20's two Int64s, q3's Int64, Date and Int32, and more.
		{[]dtype.DataType{dtype.Int64, dtype.Int64}, -1},
		{[]dtype.DataType{dtype.Int64, dtype.Date, dtype.Int32}, -1},
		{[]dtype.DataType{dtype.Uint64, dtype.Int64, dtype.Int16, dtype.Uint8}, 2},
		{[]dtype.DataType{dtype.Int32, dtype.Int32, dtype.Int32, dtype.Int32}, -1},
	}
	for _, c := range cases {
		name := fmt.Sprint(c.types)
		p, ok := NewKeyPacker(c.types)
		if !ok {
			t.Fatalf("%s: refused", name)
		}
		cols := make([]*data.Column, len(c.types))
		for i, dt := range c.types {
			cols[i] = col(dt, i == c.free)
		}
		a, b, w3 := make([]uint64, n), make([]uint64, n), make([]uint64, n)
		for lo := 0; lo < n; lo += 64 {
			m := min(64, n-lo)
			p.Pack(cols, lo, a[lo:lo+m], b[lo:lo+m], w3[lo:lo+m])
		}
		var bits int
		for _, dt := range c.types {
			bits += dt.Physical().BitWidth() + 1
		}
		if want := 2 + bits/129; p.Words() != want {
			t.Fatalf("%s: %d words, want %d", name, p.Words(), want)
		}
		wide := make([][]int64, len(cols))
		for i, c := range cols {
			var scratch []int64
			wide[i] = slices.Clone(IntKeys(c, &scratch))
		}
		same := func(i, j int) bool {
			for k, c := range cols {
				vi, vj := c.IsValid(i), c.IsValid(j)
				if vi != vj {
					return false
				}
				if vi && wide[k][i] != wide[k][j] {
					return false
				}
			}
			return true
		}
		equal := 0
		for i := range n {
			for j := range n {
				eq := a[i] == a[j] && b[i] == b[j] && w3[i] == w3[j]
				if eq != same(i, j) {
					t.Fatalf("%s: rows %d and %d pack equal %v, keys equal %v", name, i, j, eq, same(i, j))
				}
				if i != j && same(i, j) {
					equal++
				}
			}
		}
		if equal == 0 {
			t.Fatalf("%s: no two rows' keys are equal, so equality is not tested", name)
		}
	}
	for _, types := range [][]dtype.DataType{
		{dtype.Int64, dtype.Int64, dtype.Int64},                            // 195 bits
		{dtype.Int64, dtype.Int64, dtype.Int32, dtype.Int32},               // 196
		{dtype.Uint64, dtype.Int32, dtype.Int32, dtype.Int32, dtype.Int32}, // 197
		{dtype.Float64, dtype.Int32}, {dtype.String, dtype.Int8}, {dtype.Bool, dtype.Int8},
	} {
		if _, ok := NewKeyPacker(types); ok {
			t.Errorf("%v packed", types)
		}
	}
}

func narrowTo[T int8 | int16 | int32 | uint8 | uint16 | uint32 | uint64](v []int64) []T {
	out := make([]T, len(v))
	for i, x := range v {
		out[i] = T(x)
	}
	return out
}

// keyBits is row i's value as IntKeys widens it.
func keyBits(c *data.Column, i int) int64 {
	var scratch []int64
	return IntKeys(c, &scratch)[i]
}

// TestAnNUniqueOfIntegersCountsAsAMap: of an integer column, n_unique's keys are two
// words since step 381. Over every width, with nulls and a column of no payload, in
// batches, merged through a dense remap, a sparse one and none, and spilled at random
// points, each group's count is a map's of its present values.
func TestAnNUniqueOfIntegersCountsAsAMap(t *testing.T) {
	rng := rand.New(rand.NewPCG(381, 3))
	const nGroups = 40
	type key struct {
		g int32
		v int64
	}
	batch := func(dt dtype.DataType) ([]int32, *data.Column) {
		n := 1 + rng.IntN(300)
		groups := make([]int32, n)
		vals := make([]int64, n)
		ok := bitmap.NewBuilder(n)
		for i := range n {
			groups[i] = int32(rng.IntN(nGroups - 5)) // the last five see no value
			vals[i] = int64(rng.IntN(60)) - 30
			if rng.IntN(20) == 0 {
				vals[i] = math.MinInt64 + int64(rng.IntN(3))
			}
			ok.Append(rng.IntN(7) != 0)
		}
		valid := ok.Finish()
		switch dt {
		case dtype.Int8:
			return groups, data.NewFixed("v", dt, narrowTo[int8](vals), valid)
		case dtype.Uint32:
			return groups, data.NewFixed("v", dt, narrowTo[uint32](vals), valid)
		case dtype.Date:
			return groups, data.NewFixed("v", dt, narrowTo[int32](vals), valid)
		}
		return groups, data.NewFixed("v", dt, vals, valid)
	}
	counts := func(seen map[key]bool) []uint64 {
		out := make([]uint64, nGroups)
		for k := range seen {
			out[k.g]++
		}
		return out
	}
	record := func(seen map[key]bool, groups []int32, c *data.Column) {
		for i, g := range groups {
			if c.IsValid(i) {
				seen[key{g, keyBits(c, i)}] = true
			}
		}
	}
	finish := func(a *nuniqueAcc) []uint64 {
		t.Helper()
		got, err := a.Finish("n", nGroups)
		if err != nil {
			t.Fatal(err)
		}
		return data.MustValues[uint64](got)
	}
	for _, dt := range []dtype.DataType{dtype.Int8, dtype.Int64, dtype.Uint32, dtype.Date} {
		t.Run(dt.String(), func(t *testing.T) {
			// Batches, some spilled after.
			seen := map[key]bool{}
			a := newNUnique(t, dt)
			sp := &fakeKeySpill{t: t}
			a.Reserve(nGroups)
			// An all-null column of no payload first: no key.
			if err := a.AddBatch([]int32{0, 1}, data.NewNull("v", dt, 2)); err != nil {
				t.Fatal(err)
			}
			for range 30 {
				groups, c := batch(dt)
				if err := a.AddBatch(groups, c); err != nil {
					t.Fatal(err)
				}
				record(seen, groups, c)
				if a.ints == nil {
					t.Fatal("an integer column's keys are not two words")
				}
				if rng.IntN(4) == 0 {
					if _, err := a.SpillKeys(sp); err != nil {
						t.Fatal(err)
					}
				}
			}
			if len(sp.runs) < 2 {
				t.Fatal("fewer than two runs, so the merge of runs is not tested")
			}
			if got, want := finish(a), counts(seen); !slices.Equal(got, want) {
				t.Fatalf("spilled: %v, want %v", got, want)
			}

			// Merged: into an accumulator that has seen nothing, through no remap, a
			// dense one and a sparse one.
			for _, form := range []string{"none", "dense", "sparse"} {
				dst := newNUnique(t, dt)
				seen := map[key]bool{}
				for w := range 3 {
					src := newNUnique(t, dt)
					src.Reserve(nGroups)
					for range 5 {
						groups, c := batch(dt)
						if err := src.AddBatch(groups, c); err != nil {
							t.Fatal(err)
						}
						var remapped []int32
						for _, g := range groups {
							remapped = append(remapped, remapOf(form, w, g))
						}
						record(seen, remapped, c)
					}
					var remap []int32
					switch form {
					case "dense":
						remap = make([]int32, nGroups)
						for g := range remap {
							remap[g] = remapOf(form, w, int32(g))
						}
					case "sparse":
						var from, to []int32
						for g := range int32(nGroups) {
							from, to = append(from, g), append(to, remapOf(form, w, g))
						}
						remap = SparseRemap(from, to)
					}
					if err := dst.Merge(src, remap); err != nil {
						t.Fatal(err)
					}
				}
				if got, want := finish(dst), counts(seen); !slices.Equal(got, want) {
					t.Fatalf("merged through %s: %v, want %v", form, got, want)
				}
			}
		})
	}

	// One of encoded keys does not take one of integers' keys.
	ints, enc := newNUnique(t, dtype.Int64), newNUnique(t, dtype.Float64)
	ints.Reserve(2)
	enc.Reserve(2)
	if err := ints.AddBatch([]int32{0, 1}, data.NewFixed("v", dtype.Int64, []int64{3, 4}, bitmap.AllSet(2))); err != nil {
		t.Fatal(err)
	}
	if err := enc.AddBatch([]int32{0, 1}, data.NewFixed("v", dtype.Float64, []float64{3, 4}, bitmap.AllSet(2))); err != nil {
		t.Fatal(err)
	}
	if err := enc.Merge(ints, nil); err == nil {
		t.Error("an n_unique of encoded keys merged one of integers")
	}
}

// remapOf is worker w's group g's group after the merge, in one form: none keeps it.
func remapOf(form string, w int, g int32) int32 {
	if form == "none" {
		return g
	}
	return (g + int32(7*w)) % 35
}
