package physical

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

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
// several threads, a key, every build row retained, and no limit the caller set.
// Semi and Anti keep their keys only, never their rows, so there would be nothing
// to read them back from; and under a caller's limit, a spill must stay possible
// from the first batch.
func deferBuild(j *plan.Join, opts Options) bool {
	if opts.Threads <= 1 || len(j.RightOn) == 0 || !newJoinSpec(j, 0).needBuildRows {
		return false
	}
	return opts.Budget.Limit() == 0 || opts.Budget.IsDefault()
}

// catchUp inserts the deferred batches' keys as the streaming build would have,
// batch by batch in order, and streams from here on.
func (s *joinBuildSink) catchUp(ctx context.Context) error {
	parts := s.parts
	s.parts, s.nRows, s.nBuild, s.deferred = nil, 0, 0, false
	for _, b := range parts {
		if err := s.admit(ctx, b, s.nRows); err != nil {
			return err
		}
		s.nRows += b.Rows()
	}
	s.reaccount()
	return nil
}

// buildPartitioned inserts the deferred build side's keys, partitioned, and leaves
// counts and rowKey in the streaming build's shape, numbered by the tables it
// returns read as one.
func (s *joinBuildSink) buildPartitioned(ctx context.Context) (*kernel.KeyParts, error) {
	nParts := min(max(s.threads, 1), maxBuildParts)
	starts := make([]int, len(s.parts)+1)
	for i, b := range s.parts {
		starts[i+1] = starts[i] + b.Rows()
	}
	nRows := starts[len(s.parts)]
	part := make([]uint8, nRows)
	keyCols := make([][]*data.Column, len(s.parts))
	inPart := make([][]int, len(s.parts)) // rows of each batch in each partition

	// 1. Each batch's keys, evaluated, hashed and routed.
	err := s.eachConcurrently(len(s.parts), nParts, func(bi int) error {
		b := s.parts[bi]
		cols, err := evalKeys(ctx, "join", s.keys, b, s.layout.KeyTypes)
		if err != nil {
			return err
		}
		ok := keyValidity(cols)
		enc, err := kernel.NewGroupKeyEncoder("join", cols)
		if err != nil {
			return err
		}
		counts := make([]int, nParts)
		var key []byte
		for i := range b.Rows() {
			g := starts[bi] + i
			if !s.spec.nullsEqual && !ok.Get(i) {
				part[g] = noPart
				continue
			}
			key = enc.AppendKey(key[:0], i)
			p := kernel.PartitionOf(kernel.KeyHash(key), nParts)
			part[g] = uint8(p)
			counts[p]++
		}
		keyCols[bi], inPart[bi] = cols, counts
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 2. Each partition's keys, in row order, into its own table. Every row is
	// written by its partition's goroutine alone.
	s.rowKey = kernel.Extend(s.rowKey[:0], nRows, noKey)
	tables := make([]*kernel.KeyTable, nParts)
	counts := make([][]int32, nParts)
	dup := make([]int, nParts)
	err = s.eachConcurrently(nParts, nParts, func(p int) error {
		t := kernel.NewKeyTable()
		rows := 0
		for _, c := range inPart {
			rows += c[p]
		}
		// Half the rows: a build of distinct keys grows once more, and one of a few
		// keys over many rows does not hold slots it will never use.
		t.Reserve(rows / 2)
		var (
			cnt      []int32
			buf      []byte
			keys     [kernel.ManyChunk][]byte
			hs       [kernel.ManyChunk]uint64
			at       [kernel.ManyChunk]int
			ids      [kernel.ManyChunk]int32
			inserted [kernel.ManyChunk]bool
		)
		dup[p] = -1
		n := 0
		flush := func() {
			t.GetOrInsertHashed(keys[:n], hs[:n], ids[:], inserted[:])
			for j := range n {
				id := ids[j]
				if inserted[j] {
					cnt = append(cnt, 1)
				} else {
					cnt[id]++
					if dup[p] < 0 {
						dup[p] = at[j]
					}
				}
				s.rowKey[at[j]] = id
			}
			n, buf = 0, buf[:0]
		}
		for bi, b := range s.parts {
			if inPart[bi][p] == 0 {
				continue
			}
			enc, err := kernel.NewGroupKeyEncoder("join", keyCols[bi])
			if err != nil {
				return err
			}
			for i := range b.Rows() {
				g := starts[bi] + i
				if part[g] != uint8(p) {
					continue
				}
				start := len(buf)
				buf = enc.AppendKey(buf, i)
				keys[n] = buf[start:len(buf):len(buf)]
				hs[n], at[n] = kernel.KeyHash(keys[n]), g
				if n++; n == kernel.ManyChunk {
					flush()
				}
			}
		}
		flush()
		tables[p], counts[p] = t, cnt
		return nil
	})
	if err != nil {
		return nil, err
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
			return nil, duplicateKeyErr(s.spec.validate, "right", s.right, s.keys, first)
		}
	}

	// 3. One numbering: each table's keys after the tables before it.
	keys := kernel.NewKeyParts(tables)
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
// their errors joined. A panic in one is its error.
func (s *joinBuildSink) eachConcurrently(n, workers int, fn func(i int) error) error {
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
