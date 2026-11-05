package physical

import (
	"context"
	"errors"
	"os"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// --- Sink ---------------------------------------------------------------------

func (s *hashAggSink) Schema() *dtype.Schema { return s.schema }

func (s *hashAggSink) Close() error {
	errs := []error{s.closeParts(), closeSpills(s.spills)}
	s.mem.Release()
	if s.dirOwner && s.dir != "" {
		dir := s.dir
		s.dir, s.files = "", nil
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, uerr.Wrap(err, uerr.KindIO, "group_by",
				"removing spill directory %s", dir))
		}
	}
	return errors.Join(errs...)
}

func (s *hashAggSink) Consume(ctx context.Context, in *data.Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n := in.Rows()
	if n == 0 {
		return nil
	}
	if len(s.keys) == 0 && s.wholeOK() {
		if err := s.consumeWhole(ctx, in, n); err != nil {
			return err
		}
		return s.consumed(ctx, n)
	}

	// Evaluate the key expressions for this batch.
	keyCols := make([]*data.Column, len(s.keys))
	for i, k := range s.keys {
		c, err := evalColumn(ctx, k, in)
		if err != nil {
			return err
		}
		keyCols[i] = c
	}

	// Assign group ids, remembering which rows introduced new ones.
	//
	// s.groups is DENSE OVER RESIDENT ROWS, not indexed by row. That matters once
	// the sink can freeze: AddBatch reads every slot it is given, and a routed row
	// left unwritten would inherit the PREVIOUS batch's id at that index — always in
	// range, because ids is monotone — and fold into an arbitrary resident group
	// with no panic, no length mismatch and no error. Keeping it dense makes that
	// unreachable rather than merely unlikely.
	//
	// keep is the resident rows of a frozen sink's batch; an open sink's are all its
	// rows, which it does not list. Each grown to the batch at once (step 400):
	// appended from empty, a query's sink grew them a dozen times each, and listed
	// every row in keep, 250 kB of the 358 a 10,000-row group-by allocated.
	if cap(s.groups) < n {
		s.groups = make([]int32, 0, n)
	}
	if s.frozen && cap(s.keep) < n {
		s.keep = make([]int32, 0, n)
	}
	s.groups = s.groups[:0]
	keep := s.keep[:0]
	var newRows []int32
	routed := 0

	if len(s.keys) == 0 {
		// A global aggregate: every row belongs to group 0, and group 0 exists even
		// if no row ever arrives (see Finish). No new key can ever appear, so this
		// path never routes.
		for range n {
			s.groups = append(s.groups, 0)
		}
		if s.ids.Len() == 0 {
			s.ids.bytes.GetOrInsert(nil)
			if s.ordered {
				s.firstSeen = append(s.firstSeen, s.ordinalOf(in, 0))
			}
		}
	} else if s.ids.ints != nil {
		var err error
		if keep, newRows, routed, err = s.consumeInts(in, keyCols[0], keep); err != nil {
			return err
		}
	} else if s.ids.pairs != nil {
		var err error
		if keep, newRows, routed, err = s.consumePairs(in, keyCols, keep); err != nil {
			return err
		}
	} else if s.ids.raw {
		var err error
		if keep, newRows, routed, err = s.consumeStrings(in, keyCols[0], keep); err != nil {
			return err
		}
	} else {
		enc, err := kernel.NewGroupKeyEncoder("group_by", keyCols)
		if err != nil {
			return err
		}
		// The freeze is tested at a batch boundary (see below), so `frozen` cannot
		// change while this loop runs and the two cases are separate loops rather
		// than a branch on every row.
		// Keys go to the table a chunk at a time, in row order, so ids, newRows and
		// firstSeen come out exactly as one key at a time would give them.
		ch := &s.keyCh
		ch.reset()
		if s.frozen {
			// Residency is closed: a key already resident behaves exactly as before,
			// and a NEW key is routed. Get is the lookup-only form precisely so a miss
			// here cannot admit the key — partitionOf is a pure function of the
			// encoded key, so every later row of that key lands in the same file.
			flush := func() {
				ids, seen := ch.lookup(s.ids.bytes)
				for j, id := range ids {
					i := ch.rows[j]
					if !seen[j] {
						p := partitionOf(ch.keys[j], s.level)
						s.pend[p] = append(s.pend[p], i)
						routed++
						continue
					}
					keep = append(keep, i)
					s.groups = append(s.groups, id)
				}
				ch.reset()
			}
			for i := range n {
				ch.add(enc, i)
				if ch.full() {
					flush()
				}
			}
			flush()
		} else {
			// One hash and one probe per key, whether or not it is new: the probe that
			// misses is the one that fills the slot.
			flush := func() {
				ids, inserted := ch.insert(s.ids.bytes)
				for j, id := range ids {
					i := ch.rows[j]
					if inserted[j] {
						newRows = append(newRows, i)
						if s.ordered {
							s.firstSeen = append(s.firstSeen, s.ordinalOf(in, int(i)))
						}
					}
					s.groups = append(s.groups, id)
				}
				ch.reset()
			}
			for i := range n {
				ch.add(enc, i)
				if ch.full() {
					flush()
				}
			}
			flush()
		}
	}
	s.keep = keep

	if routed > 0 {
		if err := s.route(in); err != nil {
			return err
		}
	}

	// Materialise the key values of newly-seen groups only.
	if len(newRows) > 0 {
		cols := make([]*data.Column, len(keyCols))
		for i, c := range keyCols {
			taken, err := kernel.Take(c, newRows)
			if err != nil {
				return err
			}
			cols[i] = taken.Rename(s.keySchema.Field(i).Name)
		}
		part, err := data.NewBatch(s.keySchema, cols)
		if err != nil {
			return err
		}
		s.keyParts = append(s.keyParts, part)
		s.mem.Retain(part)
	}

	// Feed the accumulators, over the RESIDENT rows only.
	//
	// Restricting the batch first is sound because an aggregate's input is a
	// row-local expression — the same argument the sort's re-keying makes for
	// evaluating a key over a gathered batch. A window inside Agg() is refused at
	// plan time, so there is no non-row-local case to worry about.
	kept := n
	if s.frozen {
		kept = len(keep)
	}
	if kept > 0 {
		src := in
		if kept != n {
			var err error
			if src, err = takeBatch(s.inSchema, in, keep); err != nil {
				return err
			}
		}
		nGroups := s.ids.Len()
		for i, spec := range s.specs {
			// evalColumn broadcasts a literal input so it lines up with `groups`. That
			// is what makes Len() — which aggregates over a literal precisely so it
			// needs no column — work on a frame that has only key columns.
			col, gids, err := aggInput(ctx, spec.input, src, s.groups)
			if err != nil {
				return err
			}
			s.accs[i].Reserve(nGroups)
			if err := s.accs[i].AddBatch(gids, col); err != nil {
				return err
			}
		}
	}
	return s.consumed(ctx, n)
}

// wholeOK reports whether every accumulator takes a whole column, so a frame's
// aggregate of no keys feeds them so (consumeWhole).
func (s *hashAggSink) wholeOK() bool {
	for _, a := range s.accs {
		if w, ok := a.(kernel.WholeAdder); !ok || !w.Whole() {
			return false
		}
	}
	return true
}

// consumeWhole is Consume for an aggregate of no keys whose accumulators take whole
// columns (step 372): each input evaluated once and handed over whole, no group id
// made a row, and a count of a literal told the rows, evaluating nothing.
func (s *hashAggSink) consumeWhole(ctx context.Context, in *data.Batch, n int) error {
	if s.ids.Len() == 0 {
		s.ids.bytes.GetOrInsert(nil)
		if s.ordered {
			s.firstSeen = append(s.firstSeen, s.ordinalOf(in, 0))
		}
	}
	for i, spec := range s.specs {
		inner, preds := expr.FilterChain(spec.input)
		if _, lit := inner.(*expr.Lit); lit && len(preds) == 0 {
			if r, ok := s.accs[i].(kernel.RowAdder); ok && r.AddRows(n) {
				continue
			}
		}
		col, err := evalColumn(ctx, inner, in)
		if err != nil {
			return err
		}
		if len(preds) > 0 {
			sel, err := passingRows(ctx, preds, in)
			if err != nil {
				return err
			}
			if len(sel) != n {
				if col, err = kernel.Take(col, sel); err != nil {
					return err
				}
			}
		}
		if err := s.accs[i].(kernel.WholeAdder).AddAll(col); err != nil {
			return err
		}
	}
	return nil
}

// consumed is Consume's end, once a batch of n rows is in the accumulators: its
// memory, and the budget's freeze or a parallel worker's turn to serial.
func (s *hashAggSink) consumed(ctx context.Context, n int) error {
	acc := s.recharge()
	s.nRows += int64(n)

	// The freeze is tested at a BATCH BOUNDARY, so no batch is ever half-frozen.
	// The cost is that the resident set overshoots by up to one batch's distinct
	// keys; the benefit is that the invariant is visible rather than argued.
	if s.parallel {
		// A parallel worker neither freezes, since a frozen sink cannot be merged,
		// nor raises the budget's error: the budget is shared, and another worker
		// can push it over between this sink's look and its Check, which made a
		// spillable group-by fail under -race. Over the budget, it asks for serial
		// instead. This batch is already aggregated, so nothing is lost: the driver
		// merges the workers and finishes on one sink, where the freeze, the spill
		// and the per-value refusal all happen as they do serially.
		//
		// At HALF the budget, because the fold that follows builds the merged table
		// while the workers' still exist: switching at the budget itself doubled it
		// there, and h2o gb10 — ten million groups — was OOM-killed under the very
		// cap its serial run spills within. Of the room an engine's other queries
		// leave it (step 395).
		if room := s.budget.Room(); room > 0 && s.budget.Used() > room/2 {
			return errWantSerial
		}
		return nil
	}
	// At the last depth, only when the query alone is over: its files' sub-sinks
	// would refuse, and pressure from an engine's other queries makes a query spill,
	// never fail (step 395).
	if !s.frozen && s.budget.Limit() > 0 && s.mem.Over() && len(s.keys) > 0 &&
		(s.level < maxSpillDepth || s.mem.OverAlone()) {
		s.frozen, s.frozenAcc = true, acc
	}
	return s.overBudget(ctx)
}

// consumeInts is Consume's key assignment for a key of one integer column (step 160):
// the column's values, a chunk at a time, never encoded except to route a frozen
// sink's new key. It appends to s.groups, and a frozen sink's resident rows to keep,
// as the encoded path does, and returns keep, the rows that introduced groups, and
// how many rows it routed.
func (s *hashAggSink) consumeInts(in *data.Batch, col *data.Column, keep []int32,
) (_ []int32, newRows []int32, routed int, _ error) {
	if !kernel.IsIntKey(col.DType()) {
		return nil, nil, 0, uerr.Internalf("physical: an integer group key evaluated to %s", col.DType())
	}
	t := s.ids.ints
	n := col.Len()
	keys := kernel.IntKeys(col, &s.intKeys)
	valid := col.Validity()
	var (
		ids      [kernel.ManyChunk]int32
		inserted [kernel.ManyChunk]bool
		enc      *kernel.GroupKeyEncoder
		base     int
	)
	null := func(j int) bool { return !valid.Get(base + j) }
	for base = 0; base < n; base += kernel.ManyChunk {
		m := min(kernel.ManyChunk, n-base)
		chunk := keys[base : base+m]
		if s.frozen {
			// Residency is closed, as on the encoded path: a resident key, a null among
			// them, behaves as before, and a new one is routed by partitionOf of its
			// encoded key, a pure function of the key, so every later row of it lands
			// in the same file. Only routed keys are encoded, and only after a freeze.
			t.GetMany(chunk, ids[:m])
			for j := range m {
				i := base + j
				id := ids[j]
				if !valid.IsAllSet() && !valid.Get(i) {
					id = t.NullID()
				}
				if id < 0 {
					if enc == nil {
						var err error
						if enc, err = kernel.NewGroupKeyEncoder("group_by", []*data.Column{col}); err != nil {
							return nil, nil, 0, err
						}
					}
					p := partitionOf(enc.Encode(i), s.level)
					s.pend[p] = append(s.pend[p], int32(i))
					routed++
					continue
				}
				keep = append(keep, int32(i))
				s.groups = append(s.groups, id)
			}
			continue
		}
		if valid.IsAllSet() {
			t.GetOrInsertMany(chunk, ids[:m], inserted[:m])
		} else {
			t.GetOrInsertNullable(chunk, null, ids[:m], inserted[:m])
		}
		for j := range m {
			i := int32(base + j)
			if inserted[j] {
				newRows = append(newRows, i)
				if s.ordered {
					s.firstSeen = append(s.firstSeen, s.ordinalOf(in, int(i)))
				}
			}
			s.groups = append(s.groups, ids[j])
		}
	}
	return keep, newRows, routed, nil
}

// consumePairs is Consume's key assignment for a key of several integer columns
// packed into words (step 381), as consumeInts's is for one: a chunk of rows
// packed a column at a time, never encoded except to route a frozen sink's new key.
func (s *hashAggSink) consumePairs(in *data.Batch, cols []*data.Column, keep []int32,
) (_ []int32, newRows []int32, routed int, _ error) {
	for _, c := range cols {
		if !kernel.IsIntKey(c.DType()) {
			return nil, nil, 0, uerr.Internalf("physical: a packed group key evaluated to %s", c.DType())
		}
	}
	t, pack := s.ids.pairs, s.ids.pack
	n := cols[0].Len()
	var (
		a, b, c  [kernel.ManyChunk]uint64
		ids      [kernel.ManyChunk]int32
		inserted [kernel.ManyChunk]bool
		enc      *kernel.GroupKeyEncoder
	)
	for base := 0; base < n; base += kernel.ManyChunk {
		m := min(kernel.ManyChunk, n-base)
		pack.Pack(cols, base, a[:m], b[:m], c[:m])
		if s.frozen {
			// Residency is closed, as on the other paths: a new key is routed by
			// partitionOf of its encoded key, so every later row of it lands in the
			// same file.
			t.GetMany(a[:m], b[:m], c[:m], ids[:m])
			for j := range m {
				i := base + j
				if ids[j] < 0 {
					if enc == nil {
						var err error
						if enc, err = kernel.NewGroupKeyEncoder("group_by", cols); err != nil {
							return nil, nil, 0, err
						}
					}
					p := partitionOf(enc.Encode(i), s.level)
					s.pend[p] = append(s.pend[p], int32(i))
					routed++
					continue
				}
				keep = append(keep, int32(i))
				s.groups = append(s.groups, ids[j])
			}
			continue
		}
		t.GetOrInsertMany(a[:m], b[:m], c[:m], ids[:m], inserted[:m])
		for j := range m {
			i := int32(base + j)
			if inserted[j] {
				newRows = append(newRows, i)
				if s.ordered {
					s.firstSeen = append(s.firstSeen, s.ordinalOf(in, int(i)))
				}
			}
			s.groups = append(s.groups, ids[j])
		}
	}
	return keep, newRows, routed, nil
}

// consumeStrings is Consume's key assignment for a key of one String or Binary column
// (step 205), as consumeInts's is for an integer one: each value's own bytes, read
// in place a chunk at a time, never encoded except to route a frozen sink's new key.
// A null is the table's placeholder, never the empty string.
func (s *hashAggSink) consumeStrings(in *data.Batch, col *data.Column, keep []int32,
) (_ []int32, newRows []int32, routed int, _ error) {
	if !isRawKey(col.DType()) {
		return nil, nil, 0, uerr.Internalf("physical: a string group key evaluated to %s", col.DType())
	}
	t := s.ids.bytes
	n := col.Len()
	valid := col.Validity()
	// A payload-free column is all null, and has no characters to read.
	free := col.IsPayloadFree()
	strs := col.Strings()
	var (
		keys     [kernel.ManyChunk][]byte
		ids      [kernel.ManyChunk]int32
		inserted [kernel.ManyChunk]bool
		enc      *kernel.GroupKeyEncoder
		base     int
	)
	null := func(j int) bool { return free || !valid.Get(base+j) }
	for base = 0; base < n; base += kernel.ManyChunk {
		m := min(kernel.ManyChunk, n-base)
		for j := range m {
			keys[j] = nil
			if !null(j) {
				keys[j] = strs.Bytes(base + j)
			}
		}
		if s.frozen {
			// Residency is closed, as on the other paths: a resident key, a null among
			// them, behaves as before, and a new one is routed by partitionOf of its
			// encoded key.
			t.GetMany(keys[:m], ids[:m], inserted[:m])
			for j := range m {
				i := base + j
				id := ids[j]
				if null(j) {
					id = t.NullID()
				}
				if id < 0 {
					if enc == nil {
						var err error
						if enc, err = kernel.NewGroupKeyEncoder("group_by", []*data.Column{col}); err != nil {
							return nil, nil, 0, err
						}
					}
					p := partitionOf(enc.Encode(i), s.level)
					s.pend[p] = append(s.pend[p], int32(i))
					routed++
					continue
				}
				keep = append(keep, int32(i))
				s.groups = append(s.groups, id)
			}
			continue
		}
		if !free && valid.IsAllSet() {
			t.GetOrInsertMany(keys[:m], ids[:m], inserted[:m])
		} else {
			t.GetOrInsertNullable(keys[:m], nil, null, ids[:m], inserted[:m])
		}
		for j := range m {
			i := int32(base + j)
			if inserted[j] {
				newRows = append(newRows, i)
				if s.ordered {
					s.firstSeen = append(s.firstSeen, s.ordinalOf(in, int(i)))
				}
			}
			s.groups = append(s.groups, ids[j])
		}
	}
	return keep, newRows, routed, nil
}
