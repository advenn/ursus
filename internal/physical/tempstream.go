package physical

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// A temporal group that streams (step 196, item O1 of v0.6.1-scope.md).
//
// Until step 196 temporalSink buffered its whole input and cut the windows at the
// end, so GroupByDynamic and Rolling over a series larger than the budget refused.
// They need not: the index is sorted, so every window is a run of a bucket's rows,
// and a row can be given to its windows as it arrives.
//
// # Dynamic: the open windows
//
// A bucket's grid starts at its first row (gridStart, then stepped back so that row
// is in a window). When a row arrives, every grid window starting at or before it is
// made, empty ones included, which is what the buffered grid made: the windows from
// the first row's to the last's. A window is made by the first of the bucket's rows
// that reaches its start, so no earlier row can be in it, and the row is given to
// every window still open that holds it. Nothing is held but the open windows.
//
// # Rolling: the rows in reach
//
// A row's window starts at its own instant plus the offset, minus the period by
// default, and is a period long, so it holds rows before it, after it, or both
// (rollRow). Both ends rise from one row's window to the next. Each bucket keeps the
// rows a window yet to come may reach, those not below the current window's lower
// end, about one period of them; the new window takes them, then its own row, and
// stays open for the rows after it that it reaches, ties and a forward offset.
//
// # What it holds, and the order
//
// The rows kept for rolling windows point into the batches they came in, which are
// gathered into one when eight are held, as the as-of join does; a dynamic group
// keeps none past the batch. The aggregates hold a state a window, a segment of
// windows at a time (tempsegment.go). A window is numbered as it is made, so across
// buckets they interleave; the output is per bucket, in the order the buckets
// appeared, and each bucket's windows in order, as the buffered sink gave it, by a
// sort after the group.
//
// The pairs of a row and a window are fed to the ordinary accumulators a chunk at a
// time, gathered once, as the buffered sink fed them: a window's rows in index order.

// tgBucket is one categorical key's share of a temporal group.
type tgBucket struct {
	id      int32 // its place among the buckets, in the order they appeared
	started bool
	next    time.Time // a dynamic group's next grid start, not yet a window
	active  []tgWin   // windows a later row may still fall in, oldest first
	recent  []tgRow   // a rolling group's rows that a window yet to come may reach
}

// tgWin is an open window: its number and its membership, [lo, hi] under closed.
// Only an open window needs its bounds, so they live here, not a window apiece.
type tgWin struct {
	id     int32
	lo, hi int64
}

// tgRow is a row kept for the rolling windows still to come.
type tgRow struct {
	ref  rowRef
	tick int64
}

// maxTempParts is how many batches a rolling group may hold before it gathers the
// rows it still keeps into one.
const maxTempParts = 8

func (s *temporalSink) Consume(ctx context.Context, in *data.Batch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n := in.Rows()
	if n == 0 || !s.sorted {
		return nil // past a row that went backwards nothing is read; Finish says so
	}
	idx, err := evalColumn(ctx, s.index, in)
	if err != nil {
		return err
	}
	ticks, err := temporalTicks(idx)
	if err != nil {
		return err
	}
	for i := range n {
		if !idx.IsValid(i) {
			// A null index belongs to no window. Dropping it would change the row
			// count with no error; assigning it one would invent an instant.
			return uerr.New(uerr.KindValue, s.op(),
				"the index column %q is null at row %d", idx.Name(), s.nRows+i).
				Hint("filter the nulls out, or fill them, before grouping by time")
		}
	}
	var enc *kernel.GroupKeyEncoder
	var keyCols []*data.Column
	if len(s.keys) > 0 {
		if keyCols, err = s.keyColumns(ctx, in); err != nil {
			return err
		}
		if enc, err = kernel.NewGroupKeyEncoder(s.op(), keyCols); err != nil {
			return err
		}
	}
	s.parts = append(s.parts, in)
	cur := int32(len(s.parts) - 1)
	for i := range n {
		t := ticks[i]
		if s.nRows+i > 0 && t < s.lastT {
			s.sorted, s.badRow = false, s.nRows+i
			break
		}
		s.lastT = t
		b, err := s.bucketOf(enc, keyCols, i)
		if err != nil {
			return err
		}
		ref := rowRef{src: cur, row: int32(i)}
		if s.rolling {
			err = s.rollRow(b, ref, t)
		} else {
			err = s.gridRow(b, ref, t)
		}
		if err != nil {
			return err
		}
		if len(s.pairs) >= s.chunk() {
			if err := s.feed(ctx); err != nil {
				return err
			}
		}
	}
	s.nRows += n
	if err := s.feed(ctx); err != nil {
		return err
	}
	if err := s.letGo(); err != nil {
		return err
	}
	return s.chargeState(ctx)
}

// bucketOf is row i's bucket, made as its key first appears.
func (s *temporalSink) bucketOf(enc *kernel.GroupKeyEncoder, keyCols []*data.Column, i int) (*tgBucket, error) {
	if enc == nil {
		if len(s.bkts) == 0 {
			s.bkts = append(s.bkts, &tgBucket{})
		}
		return s.bkts[0], nil
	}
	k := string(enc.Encode(i))
	if id, ok := s.bIDs[k]; ok {
		return s.bkts[id], nil
	}
	cols := make([]*data.Column, len(keyCols))
	for j, c := range keyCols {
		one, err := kernel.Take(c, []int32{int32(i)})
		if err != nil {
			return nil, err
		}
		cols[j] = one.Rename(s.keyOnly.Field(j).Name)
	}
	key, err := data.NewBatch(s.keyOnly, cols)
	if err != nil {
		return nil, err
	}
	if s.bIDs == nil {
		s.bIDs = map[string]int32{}
	}
	id := int32(len(s.bkts))
	s.bIDs[k] = id
	b := &tgBucket{id: id}
	s.bkts = append(s.bkts, b)
	// Its key, kept for its windows' rows: a batch's worth of buckets at a time
	// goes into one batch.
	s.keyNew = append(s.keyNew, key)
	s.keyBytes += key.NBytes()
	if len(s.keyNew) == s.chunk() {
		all, err := kernel.Concat(s.keyOnly, s.keyNew)
		if err != nil {
			return nil, err
		}
		for _, k := range s.keyNew {
			s.keyBytes -= k.NBytes()
		}
		s.keyRows, s.keyNew = append(s.keyRows, all), nil
		s.keyBytes += all.NBytes()
	}
	return b, nil
}

// gridRow makes the bucket's grid windows that start at or before t, then gives the
// row to the open windows that hold it.
func (s *temporalSink) gridRow(b *tgBucket, ref rowRef, t int64) error {
	if !b.started {
		start, err := s.firstGrid(t)
		if err != nil {
			return err
		}
		b.next, b.started = start, true
	}
	// How many windows reaching t asks for, before making any of them: only for a
	// fixed interval, where it is a division.
	if lo, err := s.fromTime(b.next); err == nil {
		if want, ok := s.gridPointsBetween(lo, t); ok && want > int64(s.maxWindows()) {
			return s.gridTooLarge("walking from the first row to the last")
		}
	}
	for {
		lo, err := s.fromTime(b.next)
		if err != nil {
			return err
		}
		if lo > t {
			break
		}
		if s.maxWindows() <= 0 {
			return s.gridTooLarge("walking from the first row to the last")
		}
		hi, err := s.fromTime(s.period.AddTo(b.next))
		if err != nil {
			return err
		}
		w, err := s.newWindow(b, lo, hi, lo, hi)
		if err != nil {
			return err
		}
		s.gridPoints++
		b.active = append(b.active, w)
		next := s.every.AddTo(b.next)
		if !next.After(b.next) {
			return s.gridAtTheEdge(b.next)
		}
		b.next = next
	}
	s.place(b, ref, t)
	return nil
}

// firstGrid is the start of a bucket's grid whose first row is at t: gridStart,
// stepped back while it lies past the row, and one step further when the lower end
// is open, so the row is in a window.
func (s *temporalSink) firstGrid(t int64) (time.Time, error) {
	first, err := s.toTime(t)
	if err != nil {
		return time.Time{}, err
	}
	start, err := s.gridStart(first)
	if err != nil {
		return time.Time{}, err
	}
	back := s.every.Neg()
	// A positive offset can push the first window past the first row. The bound is
	// how many grid points this query is asking for; exhausting it is a refusal.
	for steps := 0; start.After(first); steps++ {
		if steps >= s.maxWindows() {
			return time.Time{}, s.gridTooLarge("stepping back from the offset to the first row")
		}
		prev := start
		if start = back.AddTo(start); !start.Before(prev) {
			return time.Time{}, s.gridAtTheEdge(prev)
		}
	}
	// And when the LOWER end is open, a row sitting exactly on the first boundary is
	// in no window either: the grid reaches one step further back to catch it.
	if s.closed.LowerOpen() {
		start = back.AddTo(start)
	}
	return start, nil
}

// rollRow makes the row's own window, gives it the bucket's earlier rows it reaches
// and the row itself, gives the row to the bucket's open windows, and keeps it for
// the windows still to come.
func (s *temporalSink) rollRow(b *tgBucket, ref rowRef, t int64) error {
	at, err := s.toTime(t)
	if err != nil {
		return err
	}
	// Polars' window (step 313): it starts at the row's instant plus the offset,
	// minus the period unless one is given, and ends a period later, or at the row
	// itself when the offset is as long as the period behind it.
	off := s.offset
	if !off.Given() {
		off = s.period.Neg()
	}
	start := off.AddTo(at)
	lo, err := s.fromTime(start)
	if err != nil {
		return err
	}
	hi := t
	if !s.behind {
		if hi, err = s.fromTime(s.period.AddTo(start)); err != nil {
			return err
		}
	}
	// A later row's window starts no earlier than this one's, so the rows below
	// this one's lower end are in none of them.
	drop := 0
	for drop < len(b.recent) && b.recent[drop].tick < lo {
		drop++
	}
	if drop > 0 {
		b.recent = append(b.recent[:0], b.recent[drop:]...)
	}
	s.place(b, ref, t)
	w, err := s.newWindow(b, lo, hi, t, hi)
	if err != nil {
		return err
	}
	for _, r := range b.recent {
		if s.holds(w, r.tick) {
			s.addPair(r.ref, r.tick, w.id)
		}
	}
	if s.holds(w, t) {
		s.addPair(ref, t, w.id)
	}
	if !s.passed(w, t) {
		b.active = append(b.active, w)
	}
	b.recent = append(b.recent, tgRow{ref: ref, tick: t})
	return nil
}

// place gives the row at t to every open window of the bucket that holds it, and
// closes those it has passed. Both ends rise from one window to the next, so the
// passed ones are at the front, and none after a window it is below holds it.
func (s *temporalSink) place(b *tgBucket, ref rowRef, t int64) {
	shut := 0
	for shut < len(b.active) && s.passed(b.active[shut], t) {
		shut++
	}
	if shut > 0 {
		b.active = append(b.active[:0], b.active[shut:]...)
	}
	for _, w := range b.active {
		if s.below(w, t) {
			break
		}
		if s.holds(w, t) {
			s.addPair(ref, t, w.id)
		}
	}
}

// holds, below and passed are a window's membership: the one place the four closed
// conventions are read.
func (s *temporalSink) holds(w tgWin, t int64) bool { return !s.below(w, t) && !s.passed(w, t) }

func (s *temporalSink) below(w tgWin, t int64) bool {
	if s.closed.LowerOpen() {
		return t <= w.lo
	}
	return t < w.lo
}

func (s *temporalSink) passed(w tgWin, t int64) bool {
	if s.closed.UpperClosed() {
		return t > w.hi
	}
	return t >= w.hi
}

// newWindow numbers a window of bucket b with membership [lo, hi] under closed, and
// start and end as it is reported, which are kept only for IncludeBoundaries.
func (s *temporalSink) newWindow(b *tgBucket, lo, hi, start, end int64) (tgWin, error) {
	w := s.wBase + int32(len(s.wLabel))
	if err := s.openSeg(w, hi); err != nil {
		return tgWin{}, err
	}
	if s.boundaries {
		s.wStart, s.wEnd = append(s.wStart, start), append(s.wEnd, end)
	}
	label := start
	if s.label == "right" {
		label = end
	}
	s.wLabel, s.wFed = append(s.wLabel, label), append(s.wFed, false)
	if len(s.keys) > 0 {
		s.wBkt = append(s.wBkt, b.id)
	}
	return tgWin{id: w, lo: lo, hi: hi}, nil
}

// addPair queues the row for window w. A datapoint label is the window's first row.
func (s *temporalSink) addPair(ref rowRef, t int64, w int32) {
	if at := w - s.wBase; !s.wFed[at] {
		s.wFed[at] = true
		if s.label == "datapoint" {
			s.wLabel[at] = t
		}
	}
	s.pairs = append(s.pairs, ref)
	s.pgroups = append(s.pgroups, w)
}

// feed gathers the queued pairs' rows once and gives them to the aggregates of
// their windows' segments: almost always one, and then the pairs go as they are.
func (s *temporalSink) feed(ctx context.Context) error {
	nGroups := len(s.wLabel)
	if len(s.pairs) == 0 {
		return nil
	}
	src, err := gatherRows(s.inSchema, s.parts, s.pairs)
	if err != nil {
		return err
	}
	// Each aggregate's input, and under Expr.Filter which pairs it reads (step 251).
	cols := make([]*data.Column, len(s.specs))
	reads := make([][]bool, len(s.specs))
	for i, spec := range s.specs {
		inner, preds := expr.FilterChain(spec.input)
		if cols[i], err = evalColumn(ctx, inner, src); err != nil {
			return err
		}
		if len(preds) > 0 {
			sel, err := passingRows(ctx, preds, src)
			if err != nil {
				return err
			}
			reads[i] = make([]bool, src.Rows())
			for _, r := range sel {
				reads[i][r] = true
			}
		}
	}
	lo, hi := len(s.segs), -1
	for _, w := range s.pgroups {
		j := s.segIndex(w)
		lo, hi = min(lo, j), max(hi, j)
	}
	for j := lo; j <= hi; j++ {
		g := s.segs[j]
		var sel []int32 // the pairs in g, when not all are
		local := s.local[:0]
		for k, w := range s.pgroups {
			if lo == hi || s.segIndex(w) == j {
				local = append(local, w-g.base)
				if lo != hi {
					sel = append(sel, int32(k))
				}
			}
		}
		s.local = local
		for i, col := range cols {
			isel, ilocal := sel, local
			if reads[i] != nil {
				isel, ilocal = readPairs(reads[i], sel, local)
			}
			if isel != nil {
				if col, err = kernel.Take(col, isel); err != nil {
					return err
				}
			}
			g.accs[i].Reserve(int(g.n))
			if err := g.accs[i].AddBatch(ilocal, col); err != nil {
				return err
			}
		}
	}
	s.unseen += len(s.pairs)
	s.pairs, s.pgroups = s.pairs[:0], s.pgroups[:0]
	if err := s.handOut(ctx, false); err != nil {
		return err
	}
	// What the aggregates keep is charged as it grows, not once a batch: a median or
	// an implode keeps a value a pair, and one batch of a long rolling window is
	// millions of pairs. NBytes is O(windows), and the charge walks the buckets, so
	// it is made once per that many pairs, as the buffered sink made it: a keyed
	// group that hands its windows out holds few of them, and may have many buckets.
	if s.unseen >= max(s.chunk(), nGroups, len(s.bkts)) {
		s.unseen = 0
		return s.chargeState(ctx)
	}
	return nil
}

// letGo drops the batches nothing points into, after a batch is fed: every one, for a
// dynamic group; for a rolling one, the rows still kept are gathered into one batch
// once eight are held, or whenever they are under an eighth of a batch, which is
// cheap. The second is step 207's: a rolling group that hands its answer out holds
// little else, and eight batches for a few rows in reach were most of it.
func (s *temporalSink) letGo() error {
	if !s.rolling {
		s.parts = s.parts[:0]
		return nil
	}
	nKept := 0
	for _, b := range s.bkts {
		nKept += len(b.recent)
	}
	if len(s.parts) < 2 || (len(s.parts) < maxTempParts && nKept > s.chunk()/8) {
		return nil
	}
	refs := make([]rowRef, 0, nKept)
	for _, b := range s.bkts {
		for _, r := range b.recent {
			refs = append(refs, r.ref)
		}
	}
	if len(refs) == 0 {
		s.parts = s.parts[:0]
		return nil
	}
	kept, err := gatherRows(s.inSchema, s.parts, refs)
	if err != nil {
		return err
	}
	s.parts = append(s.parts[:0], kept)
	next := int32(0)
	for _, b := range s.bkts {
		for j := range b.recent {
			b.recent[j].ref = rowRef{src: 0, row: next}
			next++
		}
	}
	return nil
}

// chargeState charges the batches held, the per-window arrays and what the
// aggregates hold, and checks the budget.
//
// What it holds is its open windows' state and the rows they reach; what it has
// handed out, a keyed group's sort holds, and spills. So it refuses only once its
// own state is past the limit, as a frozen group-by does (step 243): checking the
// query, it refused while the sort it fed held the query over, a batch before the
// sort would have spilled.
//
// Past the limit, the windows of an aggregate that keeps every value, a median's,
// a quantile's or n_unique's, hand their values to disk first, as a group-by's do
// (step 244), and it refuses only if it is still past. What is left to refuse is
// implode, whose answer is every value, and the windows themselves.
func (s *temporalSink) chargeState(ctx context.Context) error {
	s.charge()
	if s.limit <= 0 || s.mem.Used() <= s.limit {
		return nil
	}
	for _, g := range s.segs {
		err := spillState(ctx, g.accs, &g.spills, s.budget, s.batch, func(i int) (string, error) {
			if err := s.ensureDir(); err != nil {
				return "", err
			}
			return filepath.Join(s.dir, "w"+itoa(int(g.base))+"-"+itoa(i)+"-"), nil
		})
		if err != nil {
			return err
		}
	}
	s.charge()
	if s.mem.Used() <= s.limit {
		return nil
	}
	return s.mem.Check()
}

// charge charges what the sink holds.
func (s *temporalSink) charge() {
	s.mem.Release()
	for _, p := range s.parts {
		s.mem.Retain(p)
	}
	st := int64(cap(s.wLabel))*9 + int64(cap(s.wStart)+cap(s.wEnd))*8 + int64(cap(s.pairs))*12 +
		int64(cap(s.wBkt))*4
	for _, b := range s.bkts {
		st += int64(cap(b.recent))*16 + int64(cap(b.active))*24
	}
	st += s.keyBytes
	for _, g := range s.segs {
		for _, a := range g.accs {
			st += a.NBytes()
		}
	}
	s.mem.RetainBytes(st)
}

// ensureDir creates the sink's spill directory, the first time.
func (s *temporalSink) ensureDir() error {
	if s.dir != "" {
		return nil
	}
	dir, err := os.MkdirTemp(s.budget.SpillDir(), "ursus-"+s.op()+"-")
	if err != nil {
		return uerr.Wrap(err, uerr.KindResource, s.op(), "creating a spill directory").
			Hint("choose a writable location with WithSpillDir")
	}
	s.dir = dir
	return nil
}

// Finish hands out every segment left. A temporal group runs as a tempStreamOp,
// which feeds the sink and gives its segments as they close; Finish is the sink's
// whole answer at once.
func (s *temporalSink) Finish(ctx context.Context) (Operator, error) {
	out, err := s.finishStream(ctx)
	if err != nil {
		return nil, err
	}
	return newBatchOperator(s.schema, out...), nil
}

// keyOnlySchema is the key columns' part of keySchema, under their output names.
func keyOnlySchema(keySchema *dtype.Schema) (*dtype.Schema, error) {
	fields := make([]dtype.Field, 0, keySchema.Len()-1)
	for i := 1; i < keySchema.Len(); i++ {
		fields = append(fields, keySchema.Field(i))
	}
	return dtype.NewSchema(fields...)
}

// readPairs narrows a segment's pairs, sel into the gathered rows (nil for all of
// them, in order) with their windows local, to those a filtered aggregate reads.
func readPairs(reads []bool, sel, local []int32) (isel, ilocal []int32) {
	isel, ilocal = []int32{}, []int32{}
	for m, w := range local {
		k := int32(m)
		if sel != nil {
			k = sel[m]
		}
		if reads[k] {
			isel, ilocal = append(isel, k), append(ilocal, w)
		}
	}
	return isel, ilocal
}
