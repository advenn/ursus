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
//
// A query on an engine's budget has one opened from the engine's Pool, and is over
// when the engine's queries together are, as room says; one with a limit of its own
// has one apart (NewBudget).
type Budget struct {
	isDefault   bool // the limit is not one the caller gave this query; see MarkDefault
	checkResult bool // Collect checks its result: see ResultLimit
	limit       int64
	spillDir    string
	share       *share // its place in an engine's pool, or nil

	mu     sync.Mutex
	held   map[data.BufferID]entry
	total  int64 // what the operators hold: what the limit, Over and Peak are about
	kept   int64 // what only a result holds, on top of total
	peak   int64
	spills int64

	// opened, where set, is told of each account opened: a profile attributes an
	// account to the plan node being planned when it opens (OnAccount).
	opened func(*Account)
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
// caller asked for: a parallel group-by and a join's partitioned build read it, as
// under a limit the caller set they stay serial, so a group-by's row order is the
// same on every run (step 92); and Collect checks its result (ResultLimit). A budget
// opened from a pool is marked so, as its spills depend on the other queries anyway,
// and checks its result where the pool's limit is DefaultLimit's.
func (b *Budget) MarkDefault() {
	if b != nil {
		b.isDefault, b.checkResult = true, true
	}
}

// IsDefault reports whether the limit is not one the caller gave this query alone:
// the default, or an engine's.
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
	if b.share != nil {
		b.share.pool.spills.Add(1)
	}
}

// Over reports whether the query is above its ceiling: on an engine's budget, past
// the room the engine's other queries leave it. An operator that can spill asks it.
// Always false when unlimited.
func (b *Budget) Over() bool {
	if b == nil || b.limit <= 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total > b.room()
}

// OverAlone reports whether the query alone is above its limit, whatever an engine's
// other queries hold. A refusal asks it: another query's holding makes this one
// spill sooner, and never makes it fail. On no engine's budget it is Over.
func (b *Budget) OverAlone() bool {
	if b == nil || b.limit <= 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total > b.limit
}

// Room returns what the query may hold now before it is Over: its limit, alone or
// on no engine's budget; on one, what the engine's other queries leave of it, never
// less than an even share. A threshold an operator sizes by its budget takes it, so
// that under load its runs and its read-ahead shrink with what it may hold. 0 when
// unlimited.
func (b *Budget) Room() int64 {
	if b == nil || b.limit <= 0 {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.room()
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
	if b == nil || !b.checkResult || b.limit <= 0 {
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
	a := &Account{b: b, op: op, ids: map[data.BufferID]int64{}}
	if b.opened != nil {
		b.opened(a)
	}
	return a
}

// OnAccount has fn told of each account opened from now on, on the goroutine that
// opens it: LazyFrame.Profile's, which attributes an account to the plan node being
// planned (step 369). Set before the query runs.
func (b *Budget) OnAccount(fn func(*Account)) {
	if b != nil {
		b.opened = fn
	}
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
	b.tell()
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
	b.tell()
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

	// used is Used kept as it changes, and peak the most it has been: Peak.
	used, peak int64
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
		a.grow(size)
	}
}

// grow notes n more bytes held, or fewer where n is negative, and the peak.
func (a *Account) grow(n int64) {
	a.used += n
	if a.used > a.peak {
		a.peak = a.used
	}
}

// Peak returns the most this account has held at once, as Used counts it: the
// memory a profile reports for its operator (step 369).
func (a *Account) Peak() int64 {
	if a == nil {
		return 0
	}
	return a.peak
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
	a.grow(n)
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
// when its own share happens to be the large one. On an engine's budget, the
// engine's queries' pressure too: Budget.Over.
func (a *Account) Over() bool {
	if a == nil {
		return false
	}
	return a.b.Over()
}

// OverAlone reports whether the query alone is over its limit: Budget.OverAlone.
func (a *Account) OverAlone() bool {
	if a == nil {
		return false
	}
	return a.b.OverAlone()
}

// Check returns a resource error naming this operator when the query alone is over
// its limit. An operator that can spill calls Over instead and spills. Not Over: an
// engine's other queries holding its budget make a query spill, not fail.
func (a *Account) Check() error {
	if a == nil || !a.b.OverAlone() {
		return nil
	}
	return uerr.New(uerr.KindResource, a.op,
		"memory limit of %s exceeded: the query is holding %s, %s of it in this operator",
		Bytes(a.b.Limit()), Bytes(a.b.Used()), Bytes(a.Used())).
		Hint("%s", holdsHint(a.op)).
		Hint("sort, group_by, join, unique, over, reverse and tail spill to disk; " +
			"join_asof, merge_sorted, hstack, rolling and group_by_dynamic stream " +
			"their input; an over computing a rolling window by time one of whose " +
			"periods holds more than the limit fails rather than exceed it, and so " +
			"does implode over one group larger than it")
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
	a.used = 0
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
		// tail spills since v0.6.3 step 217; what it holds in memory past that is
		// what it has not yet written, so reaching Check means the limit is under a
		// batch.
		return "tail holds the last N rows, and writes them to disk past the limit; " +
			"reaching this means the limit is under one batch, so raise it with " +
			"WithMemoryLimit or lower the batch size"
	case "join":
		// join spills since step 13, so reaching Check at all means partitioning
		// could not help — and there are exactly two ways for that to be true, both
		// of them "there is no key space left to divide".
		return "join holds its whole build side, and partitions it to disk when it " +
			"does not fit; reaching this means one key with more build rows than the " +
			"limit (a CROSS join, and a WhereExists of a predicate alone, spill their " +
			"build side whole)"
	case "group_by":
		// group_by spills since step 12, so reaching Check at all means the freeze
		// could not help: either the limit is too small to admit one batch's keys, or
		// the state that is growing is INSIDE one key. Radix partitioning divides the
		// key space, so the second kind cannot be partitioned away at any depth.
		return "group_by holds one entry per distinct key, and implode holds every " +
			"VALUE as its answer — which partitioning to disk cannot divide, because " +
			"it divides across keys (quantile, median, n_unique, mode and " +
			"corr_spearman spill their values); raise the limit with WithMemoryLimit"
	case "pivot":
		// pivot holds its pairs since step 303; the group-by beneath it, which
		// streams the input and spills, is where the input went.
		return "pivot holds a row for each index and On values that meet, from the " +
			"group_by beneath it, which spills; it does not, so raise the limit with " +
			"WithMemoryLimit, or pivot fewer index values at a time"
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
			"partition_by when it does not fit; a window with none, or one partition " +
			"larger than the limit, goes to one file and is read back, its functions " +
			"in an order of their own sorted on disk and its quantiles, n_unique and " +
			"corr_spearman spilling their values; a rolling window by time holds the rows of its " +
			"period, and an implode every value of the partition, its answer; raise " +
			"the limit with WithMemoryLimit"
	case "join_asof":
		// Every strategy streams since step 194.
		return "join_asof streams both sides, holding a right row a by-key and the " +
			"right rows a forward or nearest join reads ahead to find a by-key's next " +
			"one: a left by-key the right side lacks reads that side to its end; " +
			"raise the limit with WithMemoryLimit"
	case "merge_sorted":
		return "merge_sorted holds a batch of each side; raise the limit with " +
			"WithMemoryLimit, or lower the batch size"
	case "group_by_dynamic", "rolling":
		// The default sentence is actively wrong here. What these hold that grows is
		// the window GRID, which is derived from the index range and the interval
		// rather than from the input — so a two-row frame can ask for sixteen
		// million windows, and telling its author to reduce the input would send
		// them nowhere. This is the same falsity holdsHint was written for when the
		// generic line said tail "buffers its whole input".
		return op + " holds its open windows and the rows they cover, handing each " +
			"out as the index passes its end; a gap in the index makes one window per " +
			"every= across it at once, and an implode keeps every value of its " +
			"windows (a median, a quantile, n_unique, a mode and corr_spearman spill theirs); a coarser " +
			"every=, a smaller offset= or a narrower index range is what makes it smaller"
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
