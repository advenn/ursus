// Package execopt carries the execution-time knobs that are not planning
// decisions: how much memory one query may hold, and where it is allowed to
// spill.
//
// # Why a ledger rather than a counter
//
// Seven operators in ursus buffer, and every one of them retains the SOURCE's
// batches rather than copies of them. Columns share payloads on top of that —
// Rename shares one, so does a sliced String column's character buffer. So a
// counter that adds up what each operator was handed double-counts, and a budget
// that double-counts fires early and unpredictably, which is worse for a user
// than having no budget at all.
//
// The ledger below therefore counts ALLOCATIONS, keyed by data.BufferID, with one
// reference count per allocation across every account. An allocation two sinks
// hold is one entry, and it is released when the last account lets go.
//
// # What it deliberately does not do
//
// It does not hook the allocator. arrowx pins memory.NewGoAllocator, whose Free
// is a no-op and which reports nothing, so there is no allocation-site seam to
// hook — and design/physical.md's proposed kernel.Ctx, which would have carried a
// reservation into every kernel, was never built. Accounting therefore happens
// where memory is RETAINED, not where it is allocated. That is exactly right for
// the buffering operators, which are the only things that hold memory across
// batches, and it is silent about a single kernel's transient output.
package execopt

import (
	"strconv"
	"sync"

	"ursus/internal/data"
	"ursus/internal/uerr"
)

// Budget is one query's memory ceiling and spill location.
//
// A nil *Budget means unlimited and untracked; every method is nil-safe so that
// operators need no branch of their own.
type Budget struct {
	limit    int64
	spillDir string

	mu     sync.Mutex
	held   map[data.BufferID]entry
	total  int64
	peak   int64
	spills int64
}

type entry struct {
	size int64
	refs int
}

// NewBudget returns a budget with the given limit in bytes; a limit <= 0 means
// unlimited, and the ledger still tracks the peak so a caller can measure what a
// query actually held.
func NewBudget(limit int64, spillDir string) *Budget {
	return &Budget{limit: limit, spillDir: spillDir, held: map[data.BufferID]entry{}}
}

// Limit returns the ceiling in bytes, or 0 when unlimited.
func (b *Budget) Limit() int64 {
	if b == nil {
		return 0
	}
	return b.limit
}

// SpillDir returns the directory spill files are created under, or "" for the
// system temporary directory.
func (b *Budget) SpillDir() string {
	if b == nil {
		return ""
	}
	return b.spillDir
}

// Used returns the current retained total in bytes.
func (b *Budget) Used() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// Peak returns the highest retained total the query reached.
//
// This is the number that demonstrates bounded memory. A test that asserts a
// query completed proves only that it did not run out; asserting the peak proves
// it stayed under the ceiling.
func (b *Budget) Peak() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.peak
}

// Spills returns how many runs were written to disk.
//
// It exists so a test can assert that a small budget ACTUALLY spilled. Without
// it, "external sort matches in-memory sort" passes trivially when the spill
// never triggered.
func (b *Budget) Spills() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spills
}

// NoteSpill records that one run was written.
func (b *Budget) NoteSpill() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.spills++
	b.mu.Unlock()
}

// Over reports whether the query is above its ceiling. Always false when
// unlimited.
func (b *Budget) Over() bool {
	if b == nil || b.limit <= 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total > b.limit
}

// Account opens an operator's view of the budget.
//
// op is the user-facing operator name — "sort", "group_by", "join" — because the
// only useful thing a memory error can say is which operator could not fit.
func (b *Budget) Account(op string) *Account {
	if b == nil {
		return nil
	}
	return &Account{b: b, op: op, ids: map[data.BufferID]int64{}}
}

func (b *Budget) retain(id data.BufferID, size int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.held[id]
	if e.refs == 0 {
		e.size = size
		b.total += size
	}
	e.refs++
	b.held[id] = e
	if b.total > b.peak {
		b.peak = b.total
	}
}

func (b *Budget) release(id data.BufferID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.held[id]
	if !ok {
		return
	}
	e.refs--
	if e.refs <= 0 {
		b.total -= e.size
		delete(b.held, id)
		return
	}
	b.held[id] = e
}

func (b *Budget) addExtra(n int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += n
	if b.total > b.peak {
		b.peak = b.total
	}
}

// Account is one operator's share of a budget.
//
// It holds each allocation at most once — retaining the same buffer twice does
// not hold more memory — so Retain is idempotent per allocation and Release is
// exact.
//
// A nil *Account is a working no-op, which is what lets an operator written
// against it work unchanged when no budget was configured.
type Account struct {
	b   *Budget
	op  string
	ids map[data.BufferID]int64
	raw int64
}

// Retain records that the operator now holds the batch's allocations.
func (a *Account) Retain(b *data.Batch) {
	if a == nil || b == nil {
		return
	}
	for _, c := range b.Columns() {
		a.retainColumn(c)
	}
}

// retainColumn records that the operator now holds the column's allocations.
func (a *Account) retainColumn(c *data.Column) {
	if a == nil || c == nil {
		return
	}
	for id, size := range c.Buffers() {
		if _, dup := a.ids[id]; dup {
			continue
		}
		a.ids[id] = size
		a.b.retain(id, size)
	}
}

// RetainBytes records plain-Go state that has no BufferID.
//
// This is not a rounding detail. Three of the largest retentions in the engine
// are not batches at all — windowSink's one int32 per row PER DISTINCT
// PARTITIONING, joinBuildSink's one int32 per build row, and quantileAcc's
// []float64 per group — so a walker over batches alone would report a window
// sink at half its true size.
func (a *Account) RetainBytes(n int64) {
	if a == nil || n == 0 {
		return
	}
	a.raw += n
	a.b.addExtra(n)
}

// Used returns what this account is holding.
func (a *Account) Used() int64 {
	if a == nil {
		return 0
	}
	var n int64
	for _, size := range a.ids {
		n += size
	}
	return n + a.raw
}

// Over reports whether the QUERY is over its ceiling.
//
// Deliberately query-wide rather than per-account: the budget is shared, so an
// operator that can spill should do so when the query is under pressure, not only
// when its own share happens to be the large one.
func (a *Account) Over() bool {
	if a == nil {
		return false
	}
	return a.b.Over()
}

// Check returns a resource error naming this operator when the query is over
// budget. An operator that can spill calls Over instead and spills.
func (a *Account) Check() error {
	if a == nil || !a.b.Over() {
		return nil
	}
	return uerr.New(uerr.KindResource, a.op,
		"memory limit of %s exceeded: the query is holding %s, %s of it in this operator",
		Bytes(a.b.Limit()), Bytes(a.b.Used()), Bytes(a.Used())).
		Hint("%s", holdsHint(a.op)).
		Hint("sort, group_by and join spill to disk; the other buffering operators — " +
			"over, reverse, hstack, tail and unique — fail rather than exceed the limit")
}

// Release drops everything this account holds.
func (a *Account) Release() {
	if a == nil {
		return
	}
	for id := range a.ids {
		a.b.release(id)
	}
	clear(a.ids)
	if a.raw != 0 {
		a.b.addExtra(-a.raw)
		a.raw = 0
	}
}

// holdsHint says what the named operator actually holds.
//
// Per-operator rather than one interpolated sentence, because the sentence that
// was there — "%s buffers its whole input" — is FALSE for tail, whose own plan
// node doc says it is "the only operator here that buffers" a bounded amount, and
// imprecise for join, which buffers one side.
func holdsHint(op string) string {
	switch op {
	case "tail":
		return "tail holds the last N rows; reduce N or raise the limit with WithMemoryLimit"
	case "join":
		// join spills since step 13, so reaching Check at all means partitioning
		// could not help — and there are exactly two ways for that to be true, both
		// of them "there is no key space left to divide".
		return "join holds its whole build side, and partitions it to disk when it " +
			"does not fit; reaching this means either a CROSS join, which has no key " +
			"to partition on, or one key with more build rows than the limit"
	case "group_by":
		// group_by spills since step 12, so reaching Check at all means the freeze
		// could not help: either the limit is too small to admit one batch's keys, or
		// the state that is growing is INSIDE one key. Radix partitioning divides the
		// key space, so the second kind cannot be partitioned away at any depth.
		return "group_by holds one entry per distinct key, and quantile, median and " +
			"n_unique hold state per VALUE — which partitioning to disk cannot divide, " +
			"because it divides across keys; raise the limit with WithMemoryLimit"
	case "unique":
		return "unique holds one entry per distinct row; raise the limit with " +
			"WithMemoryLimit, or reduce the subset of columns it is called on"
	default:
		return op + " buffers its whole input; raise the limit with WithMemoryLimit"
	}
}

// Bytes renders a byte count the way a person reads one.
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + "B"
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	q := float64(n) / float64(div)
	return strconv.FormatFloat(q, 'f', 1, 64) + string("KMGTP"[exp]) + "iB"
}
