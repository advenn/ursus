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
//
// # An account keeps alive what it holds
//
// A data.BufferID is a pointer to an allocation's first byte, and the ledger is
// keyed by it, so an allocation stays reachable while any account holds it. An
// operator that lets go of a buffer it will not read again, to free it, must
// release it from its account first: released afterwards, the account keeps it
// alive until then. A group-by assembling its answer and Collect assembling its
// result each held their input twice that way (step 131).
//
// # A result is held apart
//
// Collect holds its result in an account of its own, opened with Result, and only
// under the default budget. The ledger keeps the bytes only a result holds apart
// from what the operators hold, and the limit, Over and Peak see only the
// operators'. Charged with them, a result would put a spilled group-by's or join's
// replay over budget from its first partition, though it cannot be spilled: see
// Result.
package execopt

import (
	"strconv"
	"sync"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// Budget is one query's memory ceiling and spill location.
//
// A nil *Budget means unlimited and untracked; every method is nil-safe so that
// operators need no branch of their own.
type Budget struct {
	isDefault bool // the limit is DefaultLimit, not the caller's; see MarkDefault
	limit     int64
	spillDir  string

	mu     sync.Mutex
	held   map[data.BufferID]entry
	total  int64 // what the operators hold: what the limit, Over and Peak are about
	kept   int64 // what only a result holds, on top of total
	peak   int64
	spills int64
}

type entry struct {
	size int64
	refs int
	kept int // how many of refs are a result's
}

// counted splits the entry's size between the operators' total and the result's.
// An allocation an operator holds is the operators', whoever else holds it too.
func (e entry) counted() (ops, kept int64) {
	switch {
	case e.refs == 0:
		return 0, 0
	case e.refs > e.kept:
		return e.size, 0
	default:
		return 0, e.size
	}
}

// NewBudget returns a budget with the given limit in bytes; a limit <= 0 means
// unlimited, and the ledger still tracks the peak so a caller can measure what a
// query actually held.
func NewBudget(limit int64, spillDir string) *Budget {
	return &Budget{limit: limit, spillDir: spillDir, held: map[data.BufferID]entry{}}
}

// MarkDefault records that the limit is the default (DefaultLimit), not one the
// caller asked for. Only a parallel group-by reads it: under a limit the caller set,
// it stays serial, as it always was, so its row order is the same on every run
// (step 92).
func (b *Budget) MarkDefault() {
	if b != nil {
		b.isDefault = true
	}
}

// IsDefault reports whether the limit is the default rather than the caller's.
func (b *Budget) IsDefault() bool { return b != nil && b.isDefault }

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

// Used returns what the query's operators hold now, in bytes.
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

// Holding returns everything the query holds now: what its operators hold, and
// the result Collect is assembling, each allocation once.
func (b *Budget) Holding() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total + b.kept
}

// ResultLimit is what a query may hold, its result included, before Collect
// refuses it, or 0 when Collect is not checked.
//
// It is three quarters of the memory ceiling: the default budget, half the
// ceiling, and half as much again. Under the default budget ursus sets the Go soft
// limit at nine tenths of the ceiling, and a query holds up to about a fifth more
// than the ledger counts, measured by internal/memcheck; three quarters, and a
// fifth more, is that soft limit. Past it the collector cannot keep the heap under
// the soft limit, and a process under a container's limit is killed.
//
// Only under the default budget. A limit the caller gave bounds what the
// operators hold, as it always has: Collect then holds whatever it is asked to.
func (b *Budget) ResultLimit() int64 {
	if b == nil || !b.isDefault || b.limit <= 0 {
		return 0
	}
	return b.limit + b.limit/2
}

// Result opens the account Collect holds its result in, or nil, a working no-op,
// when ResultLimit is 0.
//
// What only a result holds is kept off the operators' total, so it moves neither
// Over nor Peak. An operator that can spill decides to by Over, and a result
// cannot be spilled: a spilled group-by or join replays its partitions while the
// answers of the earlier ones sit in Collect, and charged with them each
// partition's sub-sink would start over budget, partition again and again, and
// reach maxSpillDepth on data that is not skewed at all. That is the rebasing
// hashAggSink.releaseState does, for the same reason.
//
// An allocation an operator holds too, such as a group-by's answer the result
// holds slices of, stays the operator's until the operator lets go.
func (b *Budget) Result() *Account {
	if b.ResultLimit() == 0 {
		return nil
	}
	return &Account{b: b, op: "collect", ids: map[data.BufferID]int64{}, result: true}
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

func (b *Budget) retain(id data.BufferID, size int64, result bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.held[id]
	if e.refs == 0 {
		e.size = size
	}
	ops, kept := e.counted()
	e.refs++
	if result {
		e.kept++
	}
	b.move(e, ops, kept)
	b.held[id] = e
}

func (b *Budget) release(id data.BufferID, result bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.held[id]
	if !ok {
		return
	}
	ops, kept := e.counted()
	e.refs--
	if result {
		e.kept--
	}
	b.move(e, ops, kept)
	if e.refs <= 0 {
		delete(b.held, id)
		return
	}
	b.held[id] = e
}

// move applies an entry's change, from what it counted before, to both totals.
// An allocation an operator lets go of and a result still holds moves from the
// operators' total to the result's.
func (b *Budget) move(e entry, wasOps, wasKept int64) {
	ops, kept := e.counted()
	b.total += ops - wasOps
	b.kept += kept - wasKept
	if b.total > b.peak {
		b.peak = b.total
	}
}

func (b *Budget) addExtra(n int64, result bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if result {
		b.kept += n
		return
	}
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
	b      *Budget
	op     string
	ids    map[data.BufferID]int64
	raw    int64
	result bool // opened by Result: what only it holds is kept off the operators' total
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
		a.b.retain(id, size, a.result)
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
	a.b.addExtra(n, a.result)
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
		Hint("sort, group_by, join, unique and a partitioned over spill to disk; reverse, " +
			"hstack, tail, join_asof, merge_sorted, rolling, group_by_dynamic and an " +
			"unpartitioned over fail rather than exceed the limit")
}

// Release drops everything this account holds.
func (a *Account) Release() {
	if a == nil {
		return
	}
	for id := range a.ids {
		a.b.release(id, a.result)
	}
	clear(a.ids)
	if a.raw != 0 {
		a.b.addExtra(-a.raw, a.result)
		a.raw = 0
	}
}

// CheckResult returns a resource error naming collect when the query holds more,
// its result included, than ResultLimit. It is Check for the account Result opens.
func (a *Account) CheckResult() error {
	if a == nil {
		return nil
	}
	limit, held := a.b.ResultLimit(), a.b.Holding()
	if limit <= 0 || held <= limit {
		return nil
	}
	return uerr.New(uerr.KindResource, a.op,
		"the result does not fit in memory: the query holds %s, %s of it the result "+
			"so far, and may hold %s, three quarters of the %s this process may use",
		Bytes(held), Bytes(a.Used()), Bytes(limit), Bytes(2*a.b.Limit())).
		Hint("CollectBatches streams the result a batch at a time, and SinkParquet " +
			"and SinkCSV write it to a file; neither holds it whole").
		Hint("this check comes with the default memory budget: WithMemoryLimit, with " +
			"a limit of your own or 0, turns it off, and Collect then holds whatever " +
			"it is asked to")
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
		// unique spills since step 82, so reaching Check means the freeze could not
		// help: the limit was gone before the first key, or four levels of
		// partitioning still left one file's distinct rows too many to hold.
		return "unique holds one entry per distinct row, and partitions the rows it " +
			"cannot hold to disk; reaching this means the limit is too small for one " +
			"partition's distinct rows — raise it with WithMemoryLimit, or reduce the " +
			"subset of columns unique is called on"
	case "over":
		return "over holds its whole input, and partitions it to disk by its " +
			"partition_by when it does not fit; raise the limit with WithMemoryLimit"
	case "join_asof":
		return "join_asof holds its whole right side, and a second copy while it " +
			"builds the buckets it probes; raise the limit with WithMemoryLimit"
	case "merge_sorted":
		return "merge_sorted holds each side whole, to check it is sorted before " +
			"interleaving; raise the limit with WithMemoryLimit"
	case "group_by_dynamic", "rolling":
		// The default sentence is actively wrong here. What these hold that grows is
		// the window GRID, which is derived from the index range and the interval
		// rather than from the input — so a two-row frame can ask for sixteen
		// million windows, and telling its author to reduce the input would send
		// them nowhere. This is the same falsity holdsHint was written for when the
		// generic line said tail "buffers its whole input".
		return op + " holds one window per every= between the first and last row, " +
			"plus the rows each window covers; a coarser every=, a smaller offset= " +
			"or a narrower index range is what makes it smaller"
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
