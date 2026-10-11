package physical

import (
	"context"
	"errors"
	"io"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
)

// asOfStreamOp is an as-of join that streams both sides: backward since step 183
// (scope item R1), forward and nearest since step 194 (v0.6.1 item O3).
//
// # Backward
//
// A backward match is the LATEST right row at or before the left key, in the left
// row's by-bucket. Both sides are sorted on the key, so walking the right side up to
// each left key in turn leaves, for every by-key, its latest row: the answer. That
// needs one right batch in hand and one row a by-key, however long the right side.
//
// # Forward and nearest
//
// A forward match is the FIRST right row at or after the left key, in its bucket,
// and a nearest one the closer of that and the backward one. The right side is read
// up to the left key as for backward: a row before the key becomes its by-key's
// latest, and a row AT the key waits in its by-key's queue, since with exact matches
// it is a candidate both ways. Then, when the left row's by-key holds no forward
// candidate, the right side is read on, every row read queued under its by-key,
// until one arrives or the side ends. A queued row leaves its queue for latest when
// the left side passes it; pend is every queued row in the order read, which is key
// order, so the rows the left side passes are always its front.
//
// So the join holds the rows read ahead. Where the by-keys interleave, as series of
// several symbols do, that is the gap to a by-key's next row. A left by-key the right
// side never holds reads it to its end, and holds what the buffered join held, the
// whole side, charged to the budget like the rest.
//
// # What it holds
//
// latest[id] is a by-key's latest right row, and fq[id] its queue, rowRefs into
// parts: the right batches a by-key, its queue, or the output being built, still
// points into, the current one last. As the right side moves on and parts reaches
// maxAsOfParts, compact gathers the rows still pointed at into one batch.
type asOfStreamOp struct {
	cfg    *asOfBuildSink // the join's keys, by-keys, tolerance and rules
	schema *dtype.Schema
	layout *plan.JoinLayout
	left   Operator
	right  Operator

	// The right side's current batch, its keys, each row's by-key id (-1 for a null
	// by-key, which no left row matches), and how far the left side has read it.
	rk    []int64
	rid   []int32
	rpos  int
	cur   int32 // the current batch's index in parts, or -1
	rLast int64 // the last key of the right batch before, for sortedness across batches
	rSeen bool
	rRows int // right rows read, to name a backward one
	rDone bool

	ids    map[string]int32 // by-key, encoded, to its id; "" when there are no by-keys
	latest []rowRef
	// Forward and nearest only: each by-key's rows read but not yet passed, from
	// fqHead on, and the by-key of every queued row in the order read, from pendHead.
	fq       [][]queued
	fqHead   []int
	pend     []int32
	pendHead int
	out      []rowRef // the right rows matched for the output being built
	lkey     []int64  // latest[id]'s key, for the tolerance
	parts    []*data.Batch

	lRows int
	lLast int64
	done  bool
}

// maxAsOfParts is how many right batches may be held before compact gathers the rows
// still pointed at into one.
const maxAsOfParts = 8

// queued is a right row read ahead of the left side: where it is, and its key.
type queued struct {
	ref rowRef
	key int64
}

func (p *asOfStreamOp) Schema() *dtype.Schema { return p.schema }

func (p *asOfStreamOp) Close() error {
	p.cfg.mem.Release()
	return errors.Join(p.left.Close(), p.right.Close())
}

func (p *asOfStreamOp) Next(ctx context.Context) (*data.Batch, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if p.done {
			return nil, io.EOF
		}
		in, err := p.left.Next(ctx)
		if errors.Is(err, io.EOF) {
			p.done = true
			p.fq, p.fqHead, p.pend, p.pendHead = nil, nil, nil, 0 // nothing more to match
			// The right side's rest can match nothing, but it is read for its order,
			// as the buffered join read it whole: an unsorted right side is refused
			// whatever the left side holds.
			for !p.rDone {
				p.rpos = len(p.rk)
				if err := p.fillRight(ctx); err != nil {
					return nil, err
				}
			}
			return nil, io.EOF
		}
		if err != nil {
			return nil, err
		}
		if in.Rows() == 0 {
			continue // an empty batch is not end-of-stream
		}
		return p.match(ctx, in)
	}
}

// match emits one output batch: every left row, with its latest right row at or
// before it, or nulls.
func (p *asOfStreamOp) match(ctx context.Context, in *data.Batch) (*data.Batch, error) {
	s := p.cfg
	lk, err := asOfKeys(ctx, s.leftOn, in, s.keyType)
	if err != nil {
		return nil, err
	}
	for i, k := range lk {
		prev := p.lLast
		if i > 0 {
			prev = lk[i-1]
		}
		if (p.lRows > 0 || i > 0) && k < prev {
			return nil, unsortedErr("left", p.lRows+i)
		}
	}
	var byEnc *kernel.GroupKeyEncoder
	var byValid bitmap.View
	if len(s.leftBy) > 0 {
		cols, err := evalKeys(ctx, "join_asof", s.leftBy, in, s.layout.KeyTypes[1:])
		if err != nil {
			return nil, err
		}
		if byEnc, err = kernel.NewGroupKeyEncoder("join_asof", cols); err != nil {
			return nil, err
		}
		byValid = keyValidity(cols)
	}

	n := in.Rows()
	lsel, rsel := make([]int32, n), make([]int32, n)
	p.out = p.out[:0] // the matched right rows, gathered once below
	if s.strategy != plan.AsOfBackward {
		if err := p.matchAhead(ctx, lk, byEnc, byValid, rsel); err != nil {
			return nil, err
		}
		for i := range n {
			lsel[i] = int32(i)
		}
		p.lRows += n
		p.lLast = lk[n-1]
		right, err := gatherRows(s.right, p.parts, p.out)
		if err != nil {
			return nil, err
		}
		return gatherOut(p.schema, p.layout, in, right, lsel, rsel, keyFromLeft)
	}
	for i := range n {
		lsel[i], rsel[i] = int32(i), kernel.NullIndex
		k := lk[i]
		if err := p.advance(ctx, k); err != nil {
			return nil, err
		}
		key := ""
		if byEnc != nil {
			if !byValid.Get(i) {
				continue // a null by-key matches nothing
			}
			key = string(byEnc.Encode(i))
		}
		id, ok := p.ids[key]
		if !ok || p.latest[id].src < 0 {
			continue
		}
		within, err := s.withinTolerance(k, p.lkey[id])
		if err != nil {
			return nil, err
		}
		if within {
			rsel[i] = int32(len(p.out))
			p.out = append(p.out, p.latest[id])
		}
	}
	p.lRows += n
	p.lLast = lk[n-1]
	right, err := gatherRows(s.right, p.parts, p.out)
	if err != nil {
		return nil, err
	}
	return gatherOut(p.schema, p.layout, in, right, lsel, rsel, keyFromLeft)
}

// matchAhead fills rsel for a forward or nearest join, each left row's right row as
// an index into p.out, or NullIndex.
func (p *asOfStreamOp) matchAhead(ctx context.Context, lk []int64, byEnc *kernel.GroupKeyEncoder,
	byValid bitmap.View, rsel []int32) error {
	s := p.cfg
	for i, k := range lk {
		rsel[i] = kernel.NullIndex
		if err := p.passTo(ctx, k); err != nil {
			return err
		}
		key := ""
		if byEnc != nil {
			if !byValid.Get(i) {
				continue // a null by-key matches nothing
			}
			key = string(byEnc.Encode(i))
		}
		id := p.id(key)
		// The forward candidate first: finding it may read on, and compact, which
		// moves every rowRef; the backward one is read after.
		fwd, fkey, hasFwd, err := p.forwardOf(ctx, id, k)
		if err != nil {
			return err
		}
		pick, pkey, has := fwd, fkey, hasFwd
		if s.strategy == plan.AsOfNearest {
			back, bkey, hasBack := p.backOf(id, k)
			switch {
			case hasBack && (!hasFwd || s.preferBack(k, bkey, fkey)):
				pick, pkey, has = back, bkey, true
			}
		}
		if !has {
			continue
		}
		within, err := s.withinTolerance(k, pkey)
		if err != nil {
			return err
		}
		if within {
			rsel[i] = int32(len(p.out))
			p.out = append(p.out, pick)
		}
	}
	return nil
}

// passTo moves the join up to the left key k: every queued row before k leaves its
// queue for its by-key's latest, then the right side is read through k, a row before
// it becoming its by-key's latest and a row at it queued.
func (p *asOfStreamOp) passTo(ctx context.Context, k int64) error {
	for p.pendHead < len(p.pend) {
		id := p.pend[p.pendHead]
		e := p.fq[id][p.fqHead[id]]
		if e.key >= k {
			break
		}
		p.latest[id], p.lkey[id] = e.ref, e.key
		p.fqHead[id]++
		p.pendHead++
	}
	p.trimQueues()
	for {
		if p.rpos >= len(p.rk) {
			if p.rDone {
				return nil
			}
			if err := p.fillRight(ctx); err != nil {
				return err
			}
			continue
		}
		rk := p.rk[p.rpos]
		if rk > k {
			return nil
		}
		if id := p.rid[p.rpos]; id >= 0 {
			ref := rowRef{src: p.cur, row: int32(p.rpos)}
			// Nothing is queued before a row read here: the queue's rows were read
			// earlier and are at k or after, so this one is too, and is queued.
			if rk < k && p.pendHead == len(p.pend) {
				p.latest[id], p.lkey[id] = ref, rk
			} else {
				p.enqueue(id, ref, rk)
			}
		}
		p.rpos++
	}
}

// enqueue puts a right row read ahead in its by-key's queue.
func (p *asOfStreamOp) enqueue(id int32, ref rowRef, key int64) {
	for int(id) >= len(p.fq) {
		p.fq = append(p.fq, nil)
		p.fqHead = append(p.fqHead, 0)
	}
	p.fq[id] = append(p.fq[id], queued{ref: ref, key: key})
	p.pend = append(p.pend, id)
}

// trimQueues lets go of the passed fronts of pend and of the queues, once they are
// most of what is held.
func (p *asOfStreamOp) trimQueues() {
	if p.pendHead > 1024 && p.pendHead*2 > len(p.pend) {
		p.pend = append(p.pend[:0], p.pend[p.pendHead:]...)
		p.pendHead = 0
	}
	for id, h := range p.fqHead {
		if h > 64 && h*2 > len(p.fq[id]) {
			p.fq[id] = append(p.fq[id][:0], p.fq[id][h:]...)
			p.fqHead[id] = 0
		}
	}
}

// forwardOf is by-key id's forward candidate for the left key k: its first row at or
// after k, or after it when exact matches are off. When its queue holds none, the
// right side is read on, every row queued, until one arrives or the side ends.
func (p *asOfStreamOp) forwardOf(ctx context.Context, id int32, k int64) (rowRef, int64, bool, error) {
	for {
		if int(id) < len(p.fq) {
			for _, e := range p.fq[id][p.fqHead[id]:] {
				if e.key > k || (p.cfg.allowExact && e.key == k) {
					return e.ref, e.key, true, nil
				}
			}
		}
		if p.rpos >= len(p.rk) {
			if p.rDone {
				return rowRef{}, 0, false, nil
			}
			if err := p.fillRight(ctx); err != nil {
				return rowRef{}, 0, false, err
			}
			continue
		}
		// Read on to the next row of this by-key, queueing every row on the way.
		for p.rpos < len(p.rk) {
			rid, rk := p.rid[p.rpos], p.rk[p.rpos]
			ref := rowRef{src: p.cur, row: int32(p.rpos)}
			p.rpos++
			if rid < 0 {
				continue
			}
			p.enqueue(rid, ref, rk)
			if rid == id {
				break
			}
		}
	}
}

// backOf is by-key id's backward candidate for the left key k: its last row at or
// before k, or before it when exact matches are off. passTo has left every row before
// k in latest and none in the queue, so with exact matches it is the last of the
// queue's rows AT k, if any, and latest otherwise.
func (p *asOfStreamOp) backOf(id int32, k int64) (rowRef, int64, bool) {
	ref, key := p.latest[id], p.lkey[id]
	if p.cfg.allowExact && int(id) < len(p.fq) {
		for _, e := range p.fq[id][p.fqHead[id]:] {
			if e.key != k {
				break
			}
			ref, key = e.ref, e.key
		}
	}
	return ref, key, ref.src >= 0
}

// advance reads the right side up to the left key k: every row at or before it, or
// strictly before it without exact matches, becomes its by-key's latest.
func (p *asOfStreamOp) advance(ctx context.Context, k int64) error {
	for {
		if p.rpos >= len(p.rk) {
			if p.rDone {
				return nil
			}
			if err := p.fillRight(ctx); err != nil {
				return err
			}
			continue
		}
		rk := p.rk[p.rpos]
		if rk > k || (!p.cfg.allowExact && rk == k) {
			return nil
		}
		if id := p.rid[p.rpos]; id >= 0 {
			p.latest[id] = rowRef{src: p.cur, row: int32(p.rpos)}
			p.lkey[id] = rk
		}
		p.rpos++
	}
}

// fillRight pulls the right side's next batch with rows, reading its keys, checking
// its order against the batch before, and giving each row its by-key's id.
func (p *asOfStreamOp) fillRight(ctx context.Context) error {
	s := p.cfg
	if len(p.rk) > 0 {
		p.rLast, p.rSeen = p.rk[len(p.rk)-1], true
	}
	if len(p.parts) >= maxAsOfParts {
		// Moving off the current batch: it is no longer read, so it is gathered
		// with the rest.
		p.cur = -1
		if err := p.compact(); err != nil {
			return err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := p.right.Next(ctx)
		if errors.Is(err, io.EOF) {
			p.rDone, p.rk, p.rid, p.rpos = true, nil, nil, 0
			return nil
		}
		if err != nil {
			return err
		}
		if b.Rows() == 0 {
			continue
		}
		ks, err := asOfKeys(ctx, s.rightOn, b, s.keyType)
		if err != nil {
			return err
		}
		for i, k := range ks {
			prev, has := p.rLast, p.rSeen
			if i > 0 {
				prev, has = ks[i-1], true
			}
			if has && k < prev {
				return unsortedErr("right", p.rRows+i)
			}
		}
		ids, err := p.byIDs(ctx, b)
		if err != nil {
			return err
		}
		p.rRows += b.Rows()
		p.parts = append(p.parts, b)
		s.mem.Retain(b)
		p.cur = int32(len(p.parts) - 1)
		p.rk, p.rid, p.rpos = ks, ids, 0
		return s.mem.Check()
	}
}

// byIDs gives each right row its by-key's id, numbering new ones as they appear.
func (p *asOfStreamOp) byIDs(ctx context.Context, b *data.Batch) ([]int32, error) {
	s := p.cfg
	ids := make([]int32, b.Rows())
	if len(s.rightBy) == 0 {
		p.id("") // every row is one bucket, id 0
		return ids, nil
	}
	cols, err := evalKeys(ctx, "join_asof", s.rightBy, b, s.layout.KeyTypes[1:])
	if err != nil {
		return nil, err
	}
	enc, err := kernel.NewGroupKeyEncoder("join_asof", cols)
	if err != nil {
		return nil, err
	}
	valid := keyValidity(cols)
	for i := range ids {
		if !valid.Get(i) {
			ids[i] = -1 // in no bucket, as the buffered join's bucket map leaves it
			continue
		}
		ids[i] = p.id(string(enc.Encode(i)))
	}
	return ids, nil
}

// id is a by-key's id, numbering it if it is new: with no latest row yet.
func (p *asOfStreamOp) id(key string) int32 {
	if id, ok := p.ids[key]; ok {
		return id
	}
	id := int32(len(p.latest))
	p.ids[key] = id
	p.latest = append(p.latest, rowRef{src: -1})
	p.lkey = append(p.lkey, 0)
	return id
}

// compact gathers every row a by-key or the output being built points at, outside the
// current right batch, into one batch, which with that batch is then all parts holds.
// Both sets of rowRefs are moved to it.
func (p *asOfStreamOp) compact() error {
	var refs []rowRef
	moved := func(r rowRef) bool { return r.src >= 0 && r.src != p.cur }
	for _, r := range p.latest {
		if moved(r) {
			refs = append(refs, r)
		}
	}
	for _, r := range p.out {
		if moved(r) {
			refs = append(refs, r)
		}
	}
	for id, q := range p.fq {
		for _, e := range q[p.fqHead[id]:] {
			if moved(e.ref) {
				refs = append(refs, e.ref)
			}
		}
	}
	kept, err := gatherRows(p.cfg.right, p.parts, refs)
	if err != nil {
		return err
	}
	parts := []*data.Batch{kept}
	newCur := int32(-1)
	if p.cur >= 0 {
		parts = append(parts, p.parts[p.cur])
		newCur = 1
	}
	next := int32(0)
	remap := func(r *rowRef) {
		switch {
		case r.src < 0:
		case r.src == p.cur:
			r.src = newCur
		default:
			*r = rowRef{src: 0, row: next}
			next++
		}
	}
	for i := range p.latest {
		remap(&p.latest[i])
	}
	for i := range p.out {
		remap(&p.out[i])
	}
	for id, q := range p.fq {
		for j := p.fqHead[id]; j < len(q); j++ {
			remap(&q[j].ref)
		}
	}
	p.parts, p.cur = parts, newCur
	p.cfg.mem.Release()
	for _, b := range p.parts {
		p.cfg.mem.Retain(b)
	}
	return p.cfg.mem.Check()
}
