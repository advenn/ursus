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

// asOfStreamOp is a backward as-of join that streams both sides (step 183, scope
// item R1).
//
// # Why only backward
//
// A backward match is the LATEST right row at or before the left key, in the left
// row's by-bucket. Both sides are sorted on the key, so walking the right side up to
// each left key in turn leaves, for every by-key, its latest row: the answer. That
// needs one right batch in hand and one row a by-key, however long the right side.
//
// A forward or nearest match is the FIRST right row at or after the left key, and in
// a by-bucket that row can be anywhere ahead, or nowhere: finding it can mean reading
// the whole right side. Those keep asOfBuildSink, which buffers it, and say so.
//
// # What it holds
//
// latest[id] is a by-key's latest right row, a rowRef into parts: the right batches
// a by-key, or the output being built, still points into, the current one last. As
// the right side moves on and parts reaches maxAsOfParts, compact gathers the rows
// still pointed at into one batch. So it holds a row a by-key, the rows matched for
// one output batch, and a few right batches, rather than the right side.
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
	out    []rowRef // the right rows matched for the output being built
	lkey   []int64  // latest[id]'s key, for the tolerance
	parts  []*data.Batch

	lRows int
	lLast int64
	done  bool
}

// maxAsOfParts is how many right batches may be held before compact gathers the rows
// still pointed at into one.
const maxAsOfParts = 8

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
	p.parts, p.cur = parts, newCur
	p.cfg.mem.Release()
	for _, b := range p.parts {
		p.cfg.mem.Retain(b)
	}
	return p.cfg.mem.Check()
}
