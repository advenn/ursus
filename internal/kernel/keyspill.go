package kernel

import (
	"bytes"
	"slices"

	"github.com/advenn/ursus/internal/uerr"
)

// n_unique's distinct values past its sink's budget (v0.6.4 step 241).
//
// n_unique keeps every distinct value of every group, a key a value in its table, so
// over a few huge groups it refused, as a quantile did until step 220. Its answer is
// a count a group, which keys sorted by (group, key) give in one pass: the distinct
// keys of a group are its runs of equal keys. So when its sink would refuse, the
// accumulator sorts its table's entries by group and key, hands them to a KeySpill
// as one run, and starts its table afresh. At Finish it puts the rest, and counts
// each group's distinct keys as the runs merge.
//
// A key is the group-key encoder's bytes, so equal bytes are equal values under
// grouping equality: NaN is NaN, and −0 is +0. The merge orders bytes as bytes,
// which is all a count needs.

// KeySpill holds keys past a sink's budget, in runs, and gives them back ordered by
// group and then by their bytes. The kernel touches no file.
type KeySpill interface {
	// Put adds keys to the open run, opening one if none is. Within a run they come
	// in order of group, then of their bytes.
	Put(gids []int32, keys [][]byte) error
	// EndRun closes the open run, if there is one.
	EndRun() error
	// Ordered gives back every key put, merged across the runs.
	Ordered(fn func(gid int32, key []byte) error) error
}

// KeySpiller is an accumulator whose keys can go to a KeySpill.
type KeySpiller interface {
	// SpillKeys hands every key held to sp and lets them go, and reports whether it
	// held any.
	SpillKeys(sp KeySpill) (bool, error)
}

// SpillKeys hands every key held to sp as one run and lets them go: see KeySpiller.
func (a *nuniqueAcc) SpillKeys(sp KeySpill) (bool, error) {
	if a.sp != nil && a.sp != sp {
		return false, uerr.Internalf("kernel: an n_unique's keys spilled to two places")
	}
	n := a.tab.Len()
	if n == 0 {
		return false, nil
	}
	ids := make([]int32, n)
	for i := range ids {
		ids[i] = int32(i)
	}
	slices.SortFunc(ids, func(x, y int32) int {
		kx, ky := a.tab.KeyAt(x), a.tab.KeyAt(y)
		if gx, gy := takeGroup(kx), takeGroup(ky); gx != gy {
			if gx < gy {
				return -1
			}
			return 1
		}
		return bytes.Compare(kx[groupPrefixLen:], ky[groupPrefixLen:])
	})
	gids := make([]int32, 0, spillChunk)
	keys := make([][]byte, 0, spillChunk)
	for _, id := range ids {
		k := a.tab.KeyAt(id)
		gids, keys = append(gids, takeGroup(k)), append(keys, k[groupPrefixLen:])
		if len(gids) == spillChunk {
			if err := sp.Put(gids, keys); err != nil {
				return false, err
			}
			gids, keys = gids[:0], keys[:0]
		}
	}
	if len(gids) > 0 {
		if err := sp.Put(gids, keys); err != nil {
			return false, err
		}
	}
	a.tab.Reset()
	a.sp = sp
	return true, sp.EndRun()
}

// finishSpilled is Finish for an n_unique whose keys spilled: the rest are put too,
// and each group counted as the keys come back in order.
func (a *nuniqueAcc) finishSpilled(out []uint64) error {
	if _, err := a.SpillKeys(a.sp); err != nil {
		return err
	}
	cur := int32(-1)
	var prev []byte
	return a.sp.Ordered(func(g int32, k []byte) error {
		if int(g) >= len(out) {
			return uerr.Internalf("kernel: a spilled n_unique's group %d of %d", g, len(out))
		}
		if g != cur || !bytes.Equal(k, prev) {
			out[g]++
		}
		cur, prev = g, append(prev[:0], k...)
		return nil
	})
}
