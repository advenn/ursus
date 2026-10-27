package execopt

import (
	"runtime"
	"sync/atomic"
)

// Pool is one memory budget the queries of an engine share (step 395, A3 of
// v0.8-scope.md). Each query made its own budget before, half the machine's memory
// by default, so sixty-four queries at once each assumed half the machine.
//
// A query on a pool keeps a ledger of its own, a Budget, as before: what it holds,
// each allocation once, its peak and its spills. It tells the pool its total, a grain
// at a time, so the queries take no lock in common. What the pool changes is when a
// query is over: when the pool's queries together hold more than its limit, and this
// one more than what the others leave it, never less than an even share (room). An
// operator that can spill spills then. A refusal still asks whether the query alone
// holds more than the whole limit (OverAlone): another query's holding makes a query
// spill sooner, and never makes it fail.
type Pool struct {
	limit    int64
	spillDir string
	derived  bool  // the limit is DefaultLimit's, not one the caller gave
	grain    int64 // how far a query's total moves before it tells the pool

	held    atomic.Int64 // the queries' totals, as each last told them, and their pages
	peak    atomic.Int64
	running atomic.Int64
	spills  atomic.Int64
}

// NewPool returns a pool with the given limit in bytes, <= 0 for none, and spill
// directory, "" for the system's. derived says the limit is DefaultLimit's: its
// queries' Collect checks its result, as the default budget's did (ResultLimit).
func NewPool(limit int64, spillDir string, derived bool) *Pool {
	grain := int64(1 << 20)
	if limit > 0 {
		grain = min(grain, max(limit/64, 1))
	}
	return &Pool{limit: limit, spillDir: spillDir, derived: derived, grain: grain}
}

// Limit returns the pool's limit in bytes, or 0 for none.
func (p *Pool) Limit() int64 { return p.limit }

// Derived reports whether the limit is DefaultLimit's rather than the caller's.
func (p *Pool) Derived() bool { return p.derived }

// Budget opens a query's budget on the pool, its spill directory spillDir where it
// is not "", else the pool's. The query counts among the pool's from Join to Leave.
func (p *Pool) Budget(spillDir string) *Budget {
	if spillDir == "" {
		spillDir = p.spillDir
	}
	b := NewBudget(p.limit, spillDir)
	b.isDefault, b.checkResult = true, p.derived
	b.share = &share{pool: p}
	return b
}

// PoolStats is what a pool's queries hold now and have held.
type PoolStats struct {
	Held    int64 // what its running queries' operators hold, to a grain each, and their scans' pages
	Peak    int64 // the most Held has been
	Limit   int64
	Running int   // queries between Join and Leave
	Spills  int64 // files its queries wrote
}

// Stats returns the pool's figures now.
func (p *Pool) Stats() PoolStats {
	return PoolStats{
		Held:    p.held.Load(),
		Peak:    p.peak.Load(),
		Limit:   p.limit,
		Running: int(p.running.Load()),
		Spills:  p.spills.Load(),
	}
}

func (p *Pool) add(d int64) {
	h := p.held.Add(d)
	for {
		pk := p.peak.Load()
		if h <= pk || p.peak.CompareAndSwap(pk, h) {
			return
		}
	}
}

// share is a query's place in its pool: what it last told it, and whether it is
// running. A budget's cleanup holds it, not the budget, so a budget no one left is
// taken out of its pool once it is collected.
type share struct {
	pool  *Pool
	told  atomic.Int64
	pages atomic.Int64 // what its scans hold for a moment outside its ledger (Pressure)
	state atomic.Int32 // 0 not joined, 1 running, 2 left
}

func (s *share) leave() {
	if s.state.CompareAndSwap(1, 2) {
		s.pool.add(-s.told.Swap(0) - s.pages.Load())
		s.pool.running.Add(-1)
	}
}

// Join counts the query among its pool's running ones. Every entry point that runs
// a query joins it as it plans it and leaves it as it ends. A budget on no pool, and
// one joined already, does nothing.
func (b *Budget) Join() {
	if b == nil || b.share == nil || !b.share.state.CompareAndSwap(0, 1) {
		return
	}
	b.share.pool.running.Add(1)
	// The backstop: a query some entry point forgot to leave would hold a pool's
	// even shares down for as long as the process runs.
	runtime.AddCleanup(b, (*share).leave, b.share)
	b.mu.Lock()
	b.share.pool.add(b.share.pages.Load())
	b.tell()
	b.mu.Unlock()
}

// Pressure tells the query's engine of d bytes more, or fewer, that the query holds
// for a moment outside its ledger: a Parquet scan's pages, decompressed into and
// read at once, up to 105 MiB for all sixteen of lineitem's columns at SF=1 (step
// 400, B3 of v0.8-scope.md). They count against the room of the engine's other
// queries, never against this one's: like a scan's read-ahead (step 214), they are
// bytes about to be consumed, which its own sinks should not spill for. A budget on
// no engine's pool does nothing with them.
func (b *Budget) Pressure(d int64) {
	if b == nil || b.share == nil || d == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.share.pages.Add(d)
	if b.share.state.Load() == 1 {
		b.share.pool.add(d)
	}
}

// Leave takes the query out of its pool: its total, and its count among those
// running. What its operators release afterwards no longer reaches the pool.
func (b *Budget) Leave() {
	if b == nil || b.share == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.share.leave()
}

// tell gives the pool the query's total once it has moved a grain from what the pool
// was last told. b.mu is held.
func (b *Budget) tell() {
	s := b.share
	if s == nil || s.state.Load() != 1 {
		return
	}
	if d := b.total - s.told.Load(); d >= s.pool.grain || d <= -s.pool.grain {
		s.told.Store(b.total)
		s.pool.add(d)
	}
}

// room is what the query may hold before it is over: its limit, on no pool or alone
// on one; on a pool, what its other queries leave of the limit, and never less than
// an even share of it, so that whenever the pool is over, a query holding more than
// its share is too, and spills. b.mu is held.
func (b *Budget) room() int64 {
	s := b.share
	if s == nil || s.state.Load() != 1 {
		return b.limit
	}
	others := s.pool.held.Load() - s.told.Load() - s.pages.Load()
	n := max(s.pool.running.Load(), 1)
	return max(b.limit-others, b.limit/n)
}
