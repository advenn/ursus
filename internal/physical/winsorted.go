package physical

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// A window over one huge partition, in an order of its own (v0.6.4 step 235).
//
// The one-partition file (winstream.go) is read back in its own order, the input's,
// so a function that walks the partition in another order refused: an order_by. Its
// answer needs only a bounded context in THAT order, so the rows are put in it, the
// function walked over them, and the answers put back:
//
//  1. the file is read, each row stamped with its position in it, and projected to
//     what the functions read: the position, the order keys and each function's
//     child and fill;
//  2. that is sorted by the order keys, then the position, which makes the order
//     total and equal to the in-memory stable sort, whatever order runs merge in;
//  3. the functions walk the sorted stream as they walk the file, with their history,
//     lookahead or carried answer;
//  4. the answers are sorted back by the position into an answer file;
//  5. the walk over the file splices the answers in beside its rows (winStreamOp).
//
// Only the narrow projection is sorted, twice, never the rows; the file is read once
// for each distinct order and once more for the answer. The specs that share an
// order share a pass.
//
// A reversed cumulative function and a backward fill with no limit (step 236) walk
// the stream backward: over the rows in an order reversed, cum_sum reversed is
// cum_sum, and a backward fill a forward one, both carrying their last answer. The
// stream goes through a reverse sink after the forward walk, so one sort serves both
// directions, and its reverse is exact: the same rows, ties and nulls, backward.

// passOrder is the order a spec's pass walks: rank's own, its child in its direction,
// nulls first, as window.go's segments sorts it; a rolling window by time's, its by
// and then its order_by, as rollingBy sorts the order_by's order by its by, stably
// (step 246); or the spec's order_by.
func passOrder(sp *winSpec) []plan.SortKey {
	if sp.fn != nil && sp.fn.Fn == expr.WinRank {
		return []plan.SortKey{{Expr: sp.fn.Child, Descending: sp.fn.Params.Descending}}
	}
	if by, ok := byOrderOf(sp); ok {
		return append([]plan.SortKey{{Expr: by, NullsLast: true}}, sp.order...)
	}
	return sp.order
}

// byOrderOf is the by whose order a function walks: a function by time's
// (timeByOf), or interpolate_by's (step 329).
func byOrderOf(sp *winSpec) (expr.Node, bool) {
	if by, ok := timeByOf(sp); ok {
		return by, true
	}
	if sp.fn == nil || sp.fn.Fn != expr.WinInterpolate || !sp.fn.Params.By {
		return nil, false
	}
	_, by, ok := expr.PairArgs(sp.fn.Child)
	return by, ok
}

// timeByOf is a function by time's by, a rolling window's or an ewm's (step 285), and
// false for any other spec.
func timeByOf(sp *winSpec) (expr.Node, bool) {
	if by, ok := rollingByOf(sp); ok {
		return by, true
	}
	if sp.fn == nil || !sp.fn.Fn.IsEwm() || !sp.fn.Params.By {
		return nil, false
	}
	_, by, ok := expr.PairArgs(sp.fn.Child)
	return by, ok
}

// rollingByOf is a rolling window by time's by, and false for any other spec.
func rollingByOf(sp *winSpec) (expr.Node, bool) {
	// A rank by time is not walked (step 338): the walk keeps a window's values as
	// floats, which would tie integers and instants a float cannot tell apart.
	if sp.fn == nil || !sp.fn.Fn.IsRolling() || !sp.fn.Params.By || sp.fn.Fn == expr.WinRollingRank {
		return nil, false
	}
	_, by, ok := expr.PairArgs(sp.fn.Child)
	return by, ok
}

// passStep is one walk of a pass: a spec rewritten to read the projection, its need,
// its direction, and the type of the column it makes.
type passStep struct {
	walk winSpec
	need winNeed
	back bool
	typ  dtype.DataType
	// childTyp makes the column the child's type, which sortedPass knows: a fill's.
	childTyp bool
	// typOf makes the column a type of the child's, which sortedPass knows: a field's,
	// interpolate_by's (step 329).
	typOf func(child dtype.DataType) dtype.DataType
}

// passPlan is how a spec is answered in a sorted pass: its steps, and how their
// columns make its answer, the one step's column when combine is nil.
type passPlan struct {
	steps   []passStep
	combine func(rows int64, cols []*data.Column) (*data.Column, error)
	// values: combine is given the count of the spec's child's values, its nulls
	// left out, and not of the rows: a rank's, which numbers values only.
	values bool
}

// sortedPlan is how a spec is answered in a sorted pass, and false when it is not:
//
//   - forward, a function under an order_by of its own whose need, over the rows in
//     that order, is bounded;
//   - backward, a reversed cumulative function or a backward fill with no limit,
//     with an order or none (step 236): over the rows in the order reversed it is the
//     forward function, cum_* with Reverse cleared and the fill a forward one, and
//     those carry their last answer;
//   - rank (step 237), in its own order: ordinal, dense and min forward. A peer
//     group's last position is not known until it ends, so max is min over the order
//     reversed, counted from the end, and average is min and max halved, as winRank
//     computes it;
//   - interpolation (step 238), with an order or none: forward, the last value and
//     its position, and each row's own; backward, the next value and its position.
//     kernel.InterpolateFrom answers from the five as the kernel does;
//   - a rolling window by time (step 246), in its by order: kernel.RollingByNext
//     walks it as rollingBy does, holding the rows in reach of a window, over batches
//     cut between runs of equal by values;
//   - an ewm by time (step 285), in its by order, carrying its running state;
//   - interpolate_by (step 329), in its by order: interpolation's five steps over the
//     pair's value, and the by of the last value, of the next and of each row, by a
//     fill of by where the value is. kernel.InterpolateByFrom answers from the eight.
func sortedPlan(sp *winSpec) (passPlan, bool) {
	if sp.acc != nil {
		return passPlan{}, false
	}
	walk := *sp
	walk.order = nil
	if _, ok := rollingByOf(sp); ok {
		return passPlan{steps: []passStep{{walk: walk, need: winNeed{rollingBy: true}, typ: sp.out}}}, true
	}
	if _, ok := timeByOf(sp); ok {
		// An ewm by time (step 285) carries its running state over the rows in by's
		// order, as an ewm does over the file's.
		return passPlan{steps: []passStep{{walk: walk, need: winNeed{ewm: true}, typ: sp.out}}}, true
	}
	fn := *sp.fn
	if fn.Fn == expr.WinInterpolate && fn.Params.By {
		return interpolateByPlan(walk, fn, sp.out), true
	}
	if fn.Fn == expr.WinRank {
		step := func(m expr.RankMethod, back bool) passStep {
			f := fn
			f.Params.Method = m
			w := walk
			w.fn, w.out = &f, dtype.Uint32
			return passStep{walk: w, need: winNeed{rank: true}, back: back, typ: dtype.Uint32}
		}
		switch m := fn.Params.Method; m {
		case expr.RankOrdinal, expr.RankDense, expr.RankMin:
			return passPlan{steps: []passStep{step(m, false)}}, true
		case expr.RankMax:
			return passPlan{steps: []passStep{step(expr.RankMin, true)}, combine: rankMax, values: true}, true
		case expr.RankAverage:
			return passPlan{steps: []passStep{step(expr.RankMin, false), step(expr.RankMin, true)},
				combine: rankAverage, values: true}, true
		}
		return passPlan{}, false
	}
	if fn.Fn == expr.WinInterpolate && !fn.Params.By {
		fill := func(back bool) passStep {
			f := fn
			f.Fn, f.Params = expr.WinForwardFill, expr.WinParams{}
			w := walk
			w.fn = &f
			need, _ := streamNeed(&w)
			return passStep{walk: w, need: need, back: back, childTyp: true}
		}
		held := func(back bool) passStep {
			return passStep{walk: walk, need: winNeed{lastPos: true}, back: back, typ: dtype.Int64}
		}
		f := fn
		f.Fn, f.Params = expr.WinCumCount, expr.WinParams{}
		own := walk
		own.fn = &f
		nearest, out := fn.Params.Nearest, sp.out
		return passPlan{
			steps: []passStep{fill(false), held(false),
				{walk: own, need: winNeed{count: true}, typ: dtype.Uint32},
				fill(true), held(true)},
			combine: func(rows int64, c []*data.Column) (*data.Column, error) {
				return kernel.InterpolateFrom(nearest, "interpolate", out, rows, c[0], c[1], c[2], c[3], c[4])
			},
		}, true
	}
	back := false
	switch {
	case fn.Fn.IsCumulative() && fn.Params.Reverse:
		fn.Params.Reverse = false
		back = true
	case fn.Fn == expr.WinBackwardFill && fn.Params.N == 0:
		fn.Fn = expr.WinForwardFill
		back = true
	case len(sp.order) == 0:
		return passPlan{}, false // the file's order: streamNeed's
	}
	walk.fn = &fn
	need, ok := streamNeed(&walk)
	return passPlan{steps: []passStep{{walk: walk, need: need, back: back, typ: sp.out}}}, ok
}

// interpolateByPlan is interpolate_by's sorted pass (step 329): its steps read the
// pair's fields, which the pass puts in place of the pair (onto).
func interpolateByPlan(walk winSpec, fn expr.WinFn, out dtype.DataType) passPlan {
	field := func(i string) expr.Node {
		return &expr.Call{Fn: expr.FnStructField, Args: []expr.Node{fn.Child, &expr.Lit{Value: i, DT: dtype.String}}}
	}
	value, by := field(expr.PairFirst), field(expr.PairSecond)
	valueTyp := func(pair dtype.DataType) dtype.DataType { return pair.Fields()[0].Type }
	byTyp := func(pair dtype.DataType) dtype.DataType { return pair.Fields()[1].Type }
	// by where the value is: filled forward, the last value's by, and backward the
	// next one's.
	held := &expr.Cond{Pred: &expr.Unary{Op: expr.OpIsNotNull, Child: value}, Then: by, Else: &expr.Lit{DT: dtype.Null}}
	fill := func(of expr.Node, typ func(dtype.DataType) dtype.DataType, back bool) passStep {
		f := fn
		f.Fn, f.Params, f.Child = expr.WinForwardFill, expr.WinParams{}, of
		w := walk
		w.fn = &f
		need, _ := streamNeed(&w)
		return passStep{walk: w, need: need, back: back, typOf: typ}
	}
	at := func(back bool) passStep {
		f := fn
		f.Child = value
		w := walk
		w.fn = &f
		return passStep{walk: w, need: winNeed{lastPos: true}, back: back, typ: dtype.Int64}
	}
	f := fn
	f.Fn, f.Params = expr.WinCumCount, expr.WinParams{}
	own := walk
	own.fn = &f
	return passPlan{
		steps: []passStep{fill(value, valueTyp, false), at(false),
			{walk: own, need: winNeed{count: true}, typ: dtype.Uint32},
			fill(value, valueTyp, true), at(true),
			fill(held, byTyp, false), fill(held, byTyp, true), fill(by, byTyp, false)},
		combine: func(rows int64, c []*data.Column) (*data.Column, error) {
			return kernel.InterpolateByFrom("interpolate_by", out, rows, c[0], c[1], c[2], c[3], c[4], c[5], c[6], c[7])
		},
	}
}

// onto is e reading the projected column c wherever it reads child, the spec's own
// child: a step reads the child, or an expression of it.
func onto(e, child expr.Node, c string) expr.Node {
	if e == child {
		return &expr.Col{Name: c}
	}
	kids := e.Children()
	out := make([]expr.Node, len(kids))
	changed := false
	for i, k := range kids {
		out[i] = onto(k, child, c)
		changed = changed || out[i] != k
	}
	if !changed {
		return e
	}
	return expr.Rebuild(e, out)
}

// rankMax is a rank's max from min over its order reversed: the group's last
// position is its first counted from the end, values the count of the values
// ranked. A null's rank is null.
func rankMax(values int64, cols []*data.Column) (*data.Column, error) {
	rev, valid := data.MustValues[uint32](cols[0]), cols[0].Validity()
	out := make([]uint32, len(rev))
	for i, r := range rev {
		if valid.Get(i) {
			out[i] = uint32(values) + 1 - r
		}
	}
	return data.NewFixed("rank", dtype.Uint32, out, valid), nil
}

// rankAverage is a rank's average from its min and min over the order reversed, as
// winRank computes it: the sum of the two, then halved as a float.
func rankAverage(values int64, cols []*data.Column) (*data.Column, error) {
	first, rev, valid := data.MustValues[uint32](cols[0]), data.MustValues[uint32](cols[1]), cols[0].Validity()
	out := make([]float64, len(first))
	for i := range out {
		if valid.Get(i) {
			last := uint32(values) + 1 - rev[i]
			out[i] = float64(first[i]+last) / 2
		}
	}
	return data.NewFixed("rank", dtype.Float64, out, valid), nil
}

// sortedNeed reports whether a spec is answered in a sorted pass.
func sortedNeed(sp *winSpec) bool {
	_, ok := sortedPlan(sp)
	return ok
}

// streamsAlone reports whether a spec answers over one huge partition: in the file's
// order, or in a sorted pass.
func streamsAlone(sp *winSpec) bool {
	if _, ok := streamNeed(sp); ok {
		return true
	}
	return sortedNeed(sp)
}

// orderKey identifies an order: its keys, each with its direction and null place.
// No order is the empty key: the file's order, walked backward.
func orderKey(order []plan.SortKey) string {
	var b strings.Builder
	for _, k := range order {
		b.WriteString(expr.Identity(k.Expr))
		if k.Descending {
			b.WriteString(" desc")
		}
		if k.NullsLast {
			b.WriteString(" nulls last")
		}
		b.WriteByte(';')
	}
	return b.String()
}

const posName = "__pos" // a row's position in the one-partition file

// sortedPass answers group, the specs that share one order, over the one-partition
// file of rows rows, and returns the answer file: the position, then each spec's
// answer, in position order.
//
// The rows go to the order: sorted by its keys and the position, or, with no keys,
// the file as it is. The forward steps walk that stream; the backward ones walk it
// reversed, through a reverse sink, after the forward ones. Then each spec's answer
// is made from its steps' columns, and everything sorted back by position.
func (s *windowSink) sortedPass(ctx context.Context, group []*winSpec, rows int64) (path string, err error) {
	order := passOrder(group[0])

	// The projection: the position, the order keys, each function's child and fill.
	fields := []dtype.Field{dtype.NotNull(posName, dtype.Int64)}
	keys := make([]plan.SortKey, 0, len(order)+1)
	for i, k := range order {
		f, err := k.Expr.Field(s.inSchema)
		if err != nil {
			return "", err
		}
		name := "__k" + itoa(i)
		fields = append(fields, dtype.Of(name, f.Type))
		keys = append(keys, plan.SortKey{Expr: &expr.Col{Name: name},
			Descending: k.Descending, NullsLast: k.NullsLast})
	}
	keys = append(keys, plan.SortKey{Expr: &expr.Col{Name: posName}})
	plans := make([]passPlan, len(group))
	var steps []passStep
	stepsOf := make([][]int, len(group)) // each spec's steps, by index into steps
	for j, sp := range group {
		pl, _ := sortedPlan(sp)
		f, err := sp.fn.Child.Field(s.inSchema)
		if err != nil {
			return "", err
		}
		ct := f.Type
		if sp.fillOf != nil {
			ct = sp.out // the column and its fill meet there, as finishOrdered casts them
		}
		c, fl := "__c"+itoa(j), "__f"+itoa(j)
		fields = append(fields, dtype.Of(c, ct))
		if sp.fillOf != nil {
			fields = append(fields, dtype.Of(fl, sp.out))
		}
		for _, st := range pl.steps {
			fn := *st.walk.fn
			fn.Child = onto(fn.Child, sp.fn.Child, c)
			st.walk.fn = &fn
			if sp.fillOf != nil {
				st.walk.fillOf = &expr.Col{Name: fl}
			}
			st.walk.name = "__s" + itoa(len(steps))
			if st.childTyp {
				st.typ, st.walk.out = ct, ct
			}
			if st.typOf != nil {
				st.typ = st.typOf(ct)
				st.walk.out = st.typ
			}
			stepsOf[j] = append(stepsOf[j], len(steps))
			steps = append(steps, st)
		}
		plans[j] = pl
	}
	narrow, err := dtype.NewSchema(fields...)
	if err != nil {
		return "", err
	}

	// The stream in the order.
	var stream Operator
	proj := &projOp{s: s, group: group, order: order, narrow: narrow, nulls: make([]int64, len(group))}
	if len(order) == 0 {
		stream = proj
	} else {
		kSch, err := keySchema(keys, narrow)
		if err != nil {
			return "", err
		}
		fwd := &sortSink{schema: narrow, keys: keys, kSch: kSch, budget: s.budget,
			mem: s.budget.Account("over"), batch: s.batch, minRun: s.minRun()}
		defer func() { err = errors.Join(err, fwd.Close()) }()
		if err := feedEach(ctx, proj, fwd.Consume); err != nil {
			return "", err
		}
		if stream, err = fwd.Finish(ctx); err != nil {
			return "", err
		}
	}

	// The forward walk, then the backward one. at[i] is step i's column in the last
	// walk's batches. A walk by time takes batches cut between runs of equal by
	// values, the first order key's.
	at := make([]int, len(steps))
	for _, st := range steps {
		if st.need.rollingBy {
			stream = &tieAligned{in: stream, col: narrow.IndexOf("__k0")}
			break
		}
	}
	if stream, err = walkPass(ctx, stream, steps, false, rows, at, s.budget); err != nil {
		return "", err
	}
	if hasBack(steps) {
		rev := &reverseSink{schema: stream.Schema(), mem: s.budget.Account("over"), budget: s.budget}
		defer func() { err = errors.Join(err, rev.Close()) }()
		if err := feedEach(ctx, stream, rev.Consume); err != nil {
			return "", err
		}
		reversed, err := rev.Finish(ctx)
		if err != nil {
			return "", err
		}
		if stream, err = walkPass(ctx, reversed, steps, true, rows, at, s.budget); err != nil {
			return "", err
		}
	}

	// The answers, made from the steps' columns, sorted back by position.
	ans := []dtype.Field{dtype.NotNull(posName, dtype.Int64)}
	for _, sp := range group {
		i := s.schema.IndexOf(sp.name)
		if i < 0 {
			stream.Close()
			return "", uerr.Internalf("physical: window column %s is not in the window's schema", sp.name)
		}
		ans = append(ans, s.schema.Field(i))
	}
	ansSch, err := dtype.NewSchema(ans...)
	if err != nil {
		stream.Close()
		return "", err
	}
	posKey := []plan.SortKey{{Expr: &expr.Col{Name: posName}}}
	posSch, err := keySchema(posKey, ansSch)
	if err != nil {
		stream.Close()
		return "", err
	}
	back := &sortSink{schema: ansSch, keys: posKey, kSch: posSch, budget: s.budget,
		mem: s.budget.Account("over"), batch: s.batch, minRun: s.minRun()}
	defer func() { err = errors.Join(err, back.Close()) }()
	err = feedEach(ctx, stream, func(ctx context.Context, b *data.Batch) error {
		cols := make([]*data.Column, 0, len(ans))
		cols = append(cols, b.Column(0))
		for j, pl := range plans {
			in := make([]*data.Column, len(stepsOf[j]))
			for k, i := range stepsOf[j] {
				in[k] = b.Column(at[i])
			}
			c := in[0]
			if pl.combine != nil {
				n := rows
				if pl.values {
					n -= proj.nulls[j]
				}
				var err error
				if c, err = pl.combine(n, in); err != nil {
					return err
				}
			}
			cols = append(cols, c)
		}
		ab, err := data.NewBatch(ansSch, renamed(ansSch, cols))
		if err != nil {
			return err
		}
		return back.Consume(ctx, ab)
	})
	if err != nil {
		return "", err
	}
	answers, err := back.Finish(ctx)
	if err != nil {
		return "", err
	}
	return s.writeAnswers(ctx, answers, ansSch)
}

// minRun is the least a pass's sort holds before it spills (step 239): a sixteenth of
// the limit. The pass's walks and merges keep the query over its limit at times, and
// a sort that spilled at every batch then would leave hundreds of runs for one merge.
func (s *windowSink) minRun() int64 {
	return s.budget.Limit() / 16
}

// hasBack reports whether a pass walks backward.
func hasBack(steps []passStep) bool {
	for _, st := range steps {
		if st.back {
			return true
		}
	}
	return false
}

// walkPass walks in with the steps of the direction back, appending their columns to
// its rows, and records each one's place in at. With no such step, in is the walk.
func walkPass(ctx context.Context, in Operator, steps []passStep, back bool,
	rows int64, at []int, budget *execopt.Budget) (Operator, error) {

	inSch := in.Schema()
	fields := append([]dtype.Field(nil), inSch.FieldSlice()...)
	walk := &winStreamOp{inSchema: inSch, rows: rows,
		open: func() (runSource, error) { return &opRun{ctx: ctx, op: in}, nil }}
	for i := range steps {
		st := &steps[i]
		if st.back != back {
			continue
		}
		if st.need.rollingBy && walk.mem == nil {
			walk.mem, walk.limit = budget.Account("over"), budget.Limit()
		}
		at[i] = len(fields)
		fields = append(fields, dtype.Of(st.walk.name, st.typ))
		walk.specs = append(walk.specs, &winStreamSpec{sp: &st.walk, need: st.need, held: -1})
		walk.maxL = max(walk.maxL, st.need.l)
	}
	if len(walk.specs) == 0 {
		return in, nil
	}
	sch, err := dtype.NewSchema(fields...)
	if err != nil {
		in.Close()
		return nil, err
	}
	walk.schema = sch
	return walk, nil
}

// feedEach hands every batch of op to consume, and closes op.
func feedEach(ctx context.Context, op Operator, consume func(context.Context, *data.Batch) error) (err error) {
	defer func() { err = errors.Join(err, op.Close()) }()
	for {
		b, err := op.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := consume(ctx, b); err != nil {
			return err
		}
	}
}

// projOp reads the one-partition file, stamping each row's position, as the
// projection a pass's specs read: the position, the order keys, each child and fill.
type projOp struct {
	s      *windowSink
	group  []*winSpec
	order  []plan.SortKey
	narrow *dtype.Schema

	r     *spill.Reader
	pos   int64
	eof   bool
	nulls []int64 // by spec, its child's nulls read so far
}

func (p *projOp) Schema() *dtype.Schema { return p.narrow }

func (p *projOp) Close() error {
	if p.r == nil {
		return nil
	}
	err := p.r.Close()
	p.r = nil
	return err
}

func (p *projOp) Next(ctx context.Context) (*data.Batch, error) {
	if p.eof {
		return nil, io.EOF
	}
	if p.r == nil {
		r, err := spill.Open(p.s.oneFile)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "over", "reading a window's rows back")
		}
		p.r = r
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b, err := p.r.Next()
		if errors.Is(err, io.EOF) {
			p.eof = true
			return nil, io.EOF
		}
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "over", "reading a window's rows back")
		}
		if b.Rows() > 0 {
			return p.project(ctx, b)
		}
	}
}

// project is b's projection, its rows numbered on from the last batch's.
func (p *projOp) project(ctx context.Context, b *data.Batch) (*data.Batch, error) {
	n := b.Rows()
	at := make([]int64, n)
	for i := range at {
		at[i] = p.pos + int64(i)
	}
	p.pos += int64(n)
	cols := []*data.Column{data.NewFixed(posName, dtype.Int64, at, bitmap.AllSet(n))}
	for _, k := range p.order {
		c, err := evalColumn(ctx, k.Expr, b)
		if err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	for j, sp := range p.group {
		c, err := evalColumn(ctx, sp.fn.Child, b)
		if err != nil {
			return nil, err
		}
		p.nulls[j] += int64(c.Validity().CountUnset())
		if sp.fillOf != nil {
			f, err := evalColumn(ctx, sp.fillOf, b)
			if err != nil {
				return nil, err
			}
			if c.DType() != sp.out {
				if c, err = kernel.Cast(c.Name(), sp.out, true, c); err != nil {
					return nil, err
				}
			}
			if f.DType() != sp.out {
				if f, err = kernel.Cast(f.Name(), sp.out, true, f); err != nil {
					return nil, err
				}
			}
			cols = append(cols, c, f)
			continue
		}
		cols = append(cols, c)
	}
	return data.NewBatch(p.narrow, renamed(p.narrow, cols))
}

// renamed names each column as the schema's field.
func renamed(sch *dtype.Schema, cols []*data.Column) []*data.Column {
	out := make([]*data.Column, len(cols))
	for i, c := range cols {
		out[i] = c.Rename(sch.Field(i).Name)
	}
	return out
}

// writeAnswers drains op into a new file of the window's.
func (s *windowSink) writeAnswers(ctx context.Context, op Operator, sch *dtype.Schema) (path string, err error) {
	defer func() { err = errors.Join(err, op.Close()) }()
	path, err = s.newFile()
	if err != nil {
		return "", err
	}
	w, err := spill.Create(path, sch)
	if err != nil {
		return "", err
	}
	for {
		b, err := op.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			w.Close()
			return "", err
		}
		if err := w.Write(b); err != nil {
			w.Close()
			return "", uerr.Wrap(err, uerr.KindIO, "over", "writing a window's answers")
		}
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// answerReader reads a sorted pass's answer file in lockstep with the one-partition
// file: the answers for the next n rows, checked to be those rows' by position.
type answerReader struct {
	r   *spill.Reader
	cur *data.Batch
	off int
	pos int64 // the position the next answer must have
}

// take is the answers to the next n rows, a column a spec.
func (a *answerReader) take(n int) ([]*data.Column, error) {
	var parts [][]*data.Column
	for need := n; need > 0; {
		if a.cur == nil || a.off >= a.cur.Rows() {
			b, err := a.r.Next()
			if errors.Is(err, io.EOF) {
				return nil, uerr.Internalf("physical: a window's answers ended at position %d", a.pos)
			}
			if err != nil {
				return nil, uerr.Wrap(err, uerr.KindIO, "over", "reading a window's answers")
			}
			a.cur, a.off = b, 0
			continue
		}
		k := min(need, a.cur.Rows()-a.off)
		at := data.MustValues[int64](a.cur.Column(0))
		// Positions are sorted and distinct, so the two ends being right is all of
		// them being right.
		if at[a.off] != a.pos || at[a.off+k-1] != a.pos+int64(k-1) {
			return nil, uerr.Internalf("physical: a window's answers are out of step: "+
				"position %d where %d belongs", at[a.off], a.pos)
		}
		cols := make([]*data.Column, 0, a.cur.NumCols()-1)
		for _, c := range a.cur.Columns()[1:] {
			cols = append(cols, c.Slice(a.off, k))
		}
		parts = append(parts, cols)
		a.off += k
		a.pos += int64(k)
		need -= k
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	out := make([]*data.Column, len(parts[0]))
	for j := range out {
		col := make([]*data.Column, len(parts))
		for i, p := range parts {
			col[i] = p[j]
		}
		c, err := concatCols(col)
		if err != nil {
			return nil, err
		}
		out[j] = c
	}
	return out, nil
}

func (a *answerReader) Close() error {
	if a.r == nil {
		return nil
	}
	err := a.r.Close()
	a.r = nil
	return err
}

// fileOpener opens a window's spill file as a run, naming the window on an error.
func fileOpener(path string) func() (runSource, error) {
	return func() (runSource, error) {
		r, err := spill.Open(path)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, "over", "reading a window's rows back")
		}
		return &fileRun{r: r}, nil
	}
}

// tieAligned passes a sorted stream on in batches cut between runs of equal values
// of column col, as a walk by time needs: a run is held back to the next batch, and
// a batch that is all one run is joined to the next.
type tieAligned struct {
	in    Operator
	col   int
	carry *data.Batch
}

func (t *tieAligned) Schema() *dtype.Schema { return t.in.Schema() }
func (t *tieAligned) Close() error          { return t.in.Close() }

func (t *tieAligned) Next(ctx context.Context) (*data.Batch, error) {
	for {
		b, err := t.in.Next(ctx)
		if errors.Is(err, io.EOF) {
			if t.carry == nil {
				return nil, io.EOF
			}
			out := t.carry
			t.carry = nil
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if b.Rows() == 0 {
			continue
		}
		if t.carry != nil {
			if b, err = kernel.Concat(b.Schema(), []*data.Batch{t.carry, b}); err != nil {
				return nil, err
			}
			t.carry = nil
		}
		cut, err := lastRun(b.Column(t.col))
		if err != nil {
			return nil, err
		}
		if cut == 0 {
			t.carry = b
			continue
		}
		t.carry = b.Slice(cut, b.Rows()-cut)
		return b.Slice(0, cut), nil
	}
}

// lastRun is where c's last run of equal values begins: nulls are one run.
func lastRun(c *data.Column) (int, error) {
	ticks, err := temporalTicks(c)
	if err != nil {
		return 0, err
	}
	v := c.Validity()
	n := c.Len()
	i := n - 1
	for i > 0 && v.Get(i-1) == v.Get(n-1) && (!v.Get(n-1) || ticks[i-1] == ticks[n-1]) {
		i--
	}
	return i, nil
}
