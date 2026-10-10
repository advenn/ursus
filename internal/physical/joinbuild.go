package physical

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// The join's partitioned build (step 151): v0.5 item 3.
//
// # Why
//
// The build side's keys went into one KeyTable, row by row, on one goroutine, while
// the probe that follows runs on every core. With two million build keys against
// four million probe rows, the build was about 55% of the join's wall clock.
//
// # How
//
// Consume only retains the build batches (deferred). freeze then:
//
//  1. evaluates each batch's keys, and routes each row by the top half of its key's
//     hash, a batch per goroutine;
//  2. inserts each partition's keys into a KeyTable of its own, one goroutine a
//     partition, in global row order, so a key's local id is its first row's in the
//     partition and a duplicate is met at the row the serial build would meet it;
//  3. reads the tables as one, kernel.KeyParts, whose ids are each table's base plus
//     its local id, and rebases every row's key to that numbering.
//
// counts and rowKey then have the streaming build's shape, so freeze's CSR, the
// probe and a Right or Full join's flush are unchanged: a key's rows stay
// ascending, which a Left join's match order and the flush depend on.
//
// # Integer keys (step 159)
//
// A key of one integer column, or one stored as an integer, whose nulls match
// nothing, is never encoded: its values, widened to int64, are routed by IntHash and
// inserted into IntKeyTables, which the probe reads the same way (intKeyed). Encoding
// a key to bytes, hashing them and comparing them were most of the cost of a probe
// against a small build, and nearly every PDS-H join is on one integer column. The
// streaming build, the one that can spill, keeps its encoded keys: a spill routes
// by their bytes.
//
// # When it does not
//
// planJoin defers only where the streaming build would hold every row anyway and
// nothing could need to spill it: deferBuild. Past half the budget, catchUp turns a
// deferred build back into the streaming one, which can split and spill.

// noPart marks a row whose null key belongs to no partition, in buildPartitioned.
const noPart = 0xFF

// maxBuildParts bounds the partitions, which noPart's byte must stay above.
const maxBuildParts = 64

// deferBuild reports whether a join's build may wait for freeze and run partitioned:
// several threads, a key, and no limit the caller set, under which a spill must
// stay possible from the first batch.
//
// Semi and Anti joins defer too since step 161. They read their build rows for the
// keys alone, so they keep each batch's evaluated keys (keyParts), not the batch,
// and the streaming build's O(distinct keys) becomes O(build rows) of key columns
// until freeze: about what a kept build side costs every other kind, and bounded the
// same way, by catchUp at half the budget.
func deferBuild(j *plan.Join, opts Options) bool {
	if opts.Threads <= 1 || len(j.RightOn) == 0 {
		return false
	}
	return opts.Budget.Limit() == 0 || opts.Budget.IsDefault()
}

// keyBatch is a deferred Semi or Anti join's keys of in, evaluated as the streaming
// build evaluates them, in a batch of their own.
func (s *joinBuildSink) keyBatch(ctx context.Context, in *data.Batch) (*data.Batch, error) {
	cols, err := evalKeys(ctx, "join", s.keys, in, s.layout.KeyTypes)
	if err != nil {
		return nil, err
	}
	if s.keySchema == nil {
		fields := make([]dtype.Field, len(cols))
		for i, c := range cols {
			fields[i] = dtype.Of(fmt.Sprintf("__key%d", i), c.DType())
		}
		if s.keySchema, err = dtype.NewSchema(fields...); err != nil {
			return nil, err
		}
	}
	renamed := make([]*data.Column, len(cols))
	for i, c := range cols {
		renamed[i] = c.Rename(s.keySchema.Field(i).Name)
	}
	return data.NewBatch(s.keySchema, renamed)
}

// catchUp inserts the deferred batches' keys as the streaming build would have,
// batch by batch in order, and streams from here on.
func (s *joinBuildSink) catchUp(ctx context.Context) error {
	parts, keyParts := s.parts, s.keyParts
	s.parts, s.keyParts, s.nRows, s.nBuild, s.deferred = nil, nil, 0, 0, false
	for _, b := range parts {
		if err := s.admit(ctx, b, s.nRows); err != nil {
			return err
		}
		s.nRows += b.Rows()
	}
	for _, kb := range keyParts {
		if err := s.admitKeys(kb.Columns(), kb.Rows(), s.nRows); err != nil {
			return err
		}
		s.nRows += kb.Rows()
	}
	if len(keyParts) > 0 {
		// The keys are in the table, and a Semi or Anti join keeps nothing else, so
		// the key batches' charge goes with them; reaccount charges the table.
		s.mem.Release()
		s.stateBytes = 0
	}
	s.reaccount()
	return nil
}

// intKeyed reports whether the join's key is one integer column whose nulls match
// nothing, which the partitioned build keys in IntKeyTables (step 159).
func (s *joinBuildSink) intKeyed() bool {
	return len(s.keys) == 1 && !s.spec.nullsEqual && kernel.IsIntKey(s.layout.KeyTypes[0])
}

// builtKeys is what buildPartitioned leaves the probe: the tables read as one, of
// encoded keys or, for an intKeyed join, of integers. One of the two is set.
type builtKeys struct {
	bytes *kernel.KeyParts
	ints  *kernel.IntKeyParts
}

func (k builtKeys) Len() int {
	if k.ints != nil {
		return k.ints.Len()
	}
	return k.bytes.Len()
}

func (k builtKeys) Base(p int) int32 {
	if k.ints != nil {
		return k.ints.Base(p)
	}
	return k.bytes.Base(p)
}

func (k builtKeys) NBytes() int64 {
	if k.ints != nil {
		return k.ints.NBytes()
	}
	return k.bytes.NBytes()
}

// buildPartitioned inserts the deferred build side's keys, partitioned, and leaves
// counts and rowKey in the streaming build's shape, numbered by the tables it
// returns read as one.
//
// An intKeyed join's keys are the column's integers, hashed with IntHash, and never
// encoded: the two forms differ only in how a key is made and which table holds it.
func (s *joinBuildSink) buildPartitioned(ctx context.Context) (builtKeys, error) {
	nParts := min(max(s.threads, 1), maxBuildParts)
	ints := s.intKeyed()
	// The batches: the build rows, or a Semi or Anti join's keys alone.
	batches := s.parts
	if !s.spec.needBuildRows {
		batches = s.keyParts
	}
	track := s.spec.tracksRows()
	starts := make([]int, len(batches)+1)
	for i, b := range batches {
		starts[i+1] = starts[i] + b.Rows()
	}
	nRows := starts[len(batches)]
	part := make([]uint8, nRows)
	keyCols := make([][]*data.Column, len(batches))
	keyInts := make([][]int64, len(batches)) // an intKeyed join's keys, by batch
	// inPart[bi][p] are batch bi's rows in partition p, ascending, so a partition's
	// goroutine visits its own rows alone: scanning every row's partition, once a
	// partition, was a ninth of q4's CPU at eight (step 161).
	inPart := make([][][]int32, len(batches))
	byPart := func(bi int, counts []int) [][]int32 {
		total := 0
		for _, c := range counts {
			total += c
		}
		flat := make([]int32, total)
		out := make([][]int32, nParts)
		at := 0
		for p, c := range counts {
			out[p] = flat[at : at : at+c]
			at += c
		}
		for i, p := range part[starts[bi]:starts[bi+1]] {
			if p != noPart {
				out[p] = append(out[p], int32(i))
			}
		}
		return out
	}

	// 1. Each batch's keys, evaluated, hashed and routed.
	err := eachConcurrently(len(batches), nParts, func(bi int) error {
		b := batches[bi]
		cols := b.Columns()
		if s.spec.needBuildRows {
			var err error
			if cols, err = evalKeys(ctx, "join", s.keys, b, s.layout.KeyTypes); err != nil {
				return err
			}
		}
		ok := keyValidity(cols)
		counts := make([]int, nParts)
		route := func(i int, h uint64) {
			p := kernel.PartitionOf(h, nParts)
			part[starts[bi]+i] = uint8(p)
			counts[p]++
		}
		if ints {
			var scratch []int64
			v := kernel.IntKeys(cols[0], &scratch)
			for i := range b.Rows() {
				if !ok.Get(i) {
					part[starts[bi]+i] = noPart
					continue
				}
				route(i, kernel.IntHash(v[i]))
			}
			keyInts[bi], inPart[bi] = v, byPart(bi, counts)
			return nil
		}
		enc, err := kernel.NewGroupKeyEncoder("join", cols)
		if err != nil {
			return err
		}
		var key []byte
		for i := range b.Rows() {
			if !s.spec.nullsEqual && !ok.Get(i) {
				part[starts[bi]+i] = noPart
				continue
			}
			key = enc.AppendKey(key[:0], i)
			route(i, kernel.KeyHash(key))
		}
		keyCols[bi], inPart[bi] = cols, byPart(bi, counts)
		return nil
	})
	if err != nil {
		return builtKeys{}, err
	}

	// 2. Each partition's keys, in row order, into its own table. Every row is
	// written by its partition's goroutine alone.
	if track {
		s.rowKey = kernel.Extend(s.rowKey[:0], nRows, noKey)
	}
	tables := make([]*kernel.KeyTable, nParts)
	intTables := make([]*kernel.IntKeyTable, nParts)
	counts := make([][]int32, nParts)
	dup := make([]int, nParts)
	err = eachConcurrently(nParts, nParts, func(p int) error {
		rows := 0
		for _, c := range inPart {
			rows += len(c[p])
		}
		// Half the rows: a build of distinct keys grows once more, and one of a few
		// keys over many rows does not hold slots it will never use.
		var (
			t  *kernel.KeyTable
			it *kernel.IntKeyTable
		)
		if ints {
			it = kernel.NewIntKeyTable()
			it.Reserve(rows / 2)
		} else {
			t = kernel.NewKeyTable()
			t.Reserve(rows / 2)
		}
		var (
			cnt      []int32
			buf      []byte
			keys     [kernel.ManyChunk][]byte
			hs       [kernel.ManyChunk]uint64
			intKeys  [kernel.ManyChunk]int64
			at       [kernel.ManyChunk]int
			ids      [kernel.ManyChunk]int32
			inserted [kernel.ManyChunk]bool
		)
		dup[p] = -1
		n := 0
		flush := func() {
			if ints {
				it.GetOrInsertMany(intKeys[:n], ids[:n], inserted[:n])
			} else {
				t.GetOrInsertHashed(keys[:n], hs[:n], ids[:], inserted[:])
			}
			for j := range n {
				id := ids[j]
				if !inserted[j] && dup[p] < 0 {
					dup[p] = at[j]
				}
				if !track {
					continue
				}
				if inserted[j] {
					cnt = append(cnt, 1)
				} else {
					cnt[id]++
				}
				s.rowKey[at[j]] = id
			}
			n, buf = 0, buf[:0]
		}
		for bi := range batches {
			mine := inPart[bi][p]
			if len(mine) == 0 {
				continue
			}
			var enc *kernel.GroupKeyEncoder
			if !ints {
				var err error
				if enc, err = kernel.NewGroupKeyEncoder("join", keyCols[bi]); err != nil {
					return err
				}
			}
			for _, r := range mine {
				i := int(r)
				g := starts[bi] + i
				if ints {
					intKeys[n] = keyInts[bi][i]
				} else {
					start := len(buf)
					buf = enc.AppendKey(buf, i)
					keys[n] = buf[start:len(buf):len(buf)]
					hs[n] = kernel.KeyHash(keys[n])
				}
				at[n] = g
				if n++; n == kernel.ManyChunk {
					flush()
				}
			}
		}
		flush()
		tables[p], intTables[p], counts[p] = t, it, cnt
		return nil
	})
	if err != nil {
		return builtKeys{}, err
	}

	// The serial build met its first duplicate at the first such row of all.
	if s.spec.validate.RequiresRightUnique() {
		first := -1
		for _, d := range dup {
			if d >= 0 && (first < 0 || d < first) {
				first = d
			}
		}
		if first >= 0 {
			return builtKeys{}, duplicateKeyErr(s.spec.validate, "right", s.right, s.keys, first)
		}
	}

	// 3. One numbering: each table's keys after the tables before it.
	var keys builtKeys
	if ints {
		keys.ints = kernel.NewIntKeyParts(intTables)
	} else {
		keys.bytes = kernel.NewKeyParts(tables)
	}
	if !track {
		return keys, nil
	}
	s.counts = s.counts[:0]
	for _, c := range counts {
		s.counts = append(s.counts, c...)
	}
	for g, p := range part {
		if p != noPart {
			s.rowKey[g] += keys.Base(int(p))
		}
	}
	return keys, nil
}

// eachConcurrently runs fn(0) to fn(n-1) on up to workers goroutines, and returns
// their errors joined. A panic in one is its error. A join's partitioned build and a
// group-by's partitioned fold both fan out through it (step 166), so neither can
// lose the process to a panic the other would have returned.
func eachConcurrently(n, workers int, fn func(i int) error) error {
	var (
		next atomic.Int64
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for range min(n, workers) {
		wg.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				if err := uerr.GuardErr("", func() error { return fn(i) }); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}
