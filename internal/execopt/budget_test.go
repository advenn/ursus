package execopt_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/uerr"
)

func batch(t *testing.T, name string, vals []int64) *data.Batch {
	t.Helper()
	s, err := dtype.NewSchema(dtype.NotNull(name, dtype.Int64))
	if err != nil {
		t.Fatal(err)
	}
	b, err := data.NewBatch(s, []*data.Column{
		data.NewFixed(name, dtype.Int64, vals, bitmap.View{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestBudgetCountsSharedAllocationsOnce: two operators retaining the SAME batch
// is not two allocations, and it is the ordinary case — every pipeline breaker
// retains the source's own batches rather than copies.
func TestBudgetCountsSharedAllocationsOnce(t *testing.T) {
	b := batch(t, "v", []int64{1, 2, 3, 4})
	bud := execopt.NewBudget(0, "")
	one, two := bud.Account("sort"), bud.Account("join")

	one.Retain(b)
	after := bud.Used()
	if after == 0 {
		t.Fatal("retaining a batch reported nothing")
	}
	two.Retain(b)
	if bud.Used() != after {
		t.Errorf("a shared batch counted twice: %d then %d", after, bud.Used())
	}

	// It is still held while ANY account holds it, and only then released.
	one.Release()
	if bud.Used() != after {
		t.Errorf("released while another account still holds it: %d", bud.Used())
	}
	two.Release()
	if bud.Used() != 0 {
		t.Errorf("used = %d after every account released", bud.Used())
	}
	if bud.Peak() != after {
		t.Errorf("peak = %d, want %d", bud.Peak(), after)
	}
}

// TestBudgetRetainIsIdempotent: an account that is handed the same batch twice
// holds no more memory than one that was handed it once.
func TestBudgetRetainIsIdempotent(t *testing.T) {
	b := batch(t, "v", []int64{1, 2, 3})
	bud := execopt.NewBudget(0, "")
	a := bud.Account("sort")

	a.Retain(b)
	one := a.Used()
	a.Retain(b)
	if a.Used() != one || bud.Used() != one {
		t.Errorf("second retain changed the total: account %d, budget %d, want %d",
			a.Used(), bud.Used(), one)
	}
	a.Release()
	if bud.Used() != 0 {
		t.Errorf("used = %d after release, want 0", bud.Used())
	}
}

// TestBudgetTracksPlainState: three of the largest retentions in the engine are
// not batches — windowSink.perRow, joinBuildSink.rowKey and quantileAcc.vals — so
// RetainBytes has to move the same needle Retain does.
func TestBudgetTracksPlainState(t *testing.T) {
	bud := execopt.NewBudget(100, "")
	a := bud.Account("over")

	a.RetainBytes(40)
	if err := a.Check(); err != nil {
		t.Fatalf("40 bytes against a 100 byte limit: %v", err)
	}
	a.RetainBytes(80)
	if bud.Used() != 120 {
		t.Errorf("used = %d, want 120", bud.Used())
	}
	err := a.Check()
	if err == nil {
		t.Fatal("120 bytes against a 100 byte limit was accepted")
	}
	if !errors.Is(err, uerr.ErrResource) {
		t.Errorf("want a resource error, got %v", err)
	}
	if !strings.Contains(err.Error(), "over") {
		t.Errorf("the error must name the operator: %v", err)
	}

	// A negative delta is how an operator reports state that shrank.
	a.RetainBytes(-80)
	if bud.Used() != 40 {
		t.Errorf("used = %d after shrinking, want 40", bud.Used())
	}
	if err := a.Check(); err != nil {
		t.Errorf("back under the limit but still failing: %v", err)
	}
}

// TestNilBudgetIsAWorkingNoOp: Options built by hand leave Budget nil, and every
// operator is written against an Account without a branch of its own.
func TestNilBudgetIsAWorkingNoOp(t *testing.T) {
	var bud *execopt.Budget
	a := bud.Account("sort")

	a.Retain(batch(t, "v", []int64{1, 2, 3}))
	a.RetainBytes(1 << 20)
	if a.Over() {
		t.Error("a nil budget reported being over")
	}
	if err := a.Check(); err != nil {
		t.Errorf("a nil budget refused: %v", err)
	}
	a.Release()

	if bud.Limit() != 0 || bud.Peak() != 0 || bud.Spills() != 0 || bud.SpillDir() != "" {
		t.Error("a nil budget reported non-zero figures")
	}
	bud.NoteSpill()
}

// TestUnlimitedBudgetStillTracksPeak: a limit of zero means unlimited, not
// untracked. Measuring a query's peak without capping it is how the memory story
// gets tested at all.
func TestUnlimitedBudgetStillTracksPeak(t *testing.T) {
	bud := execopt.NewBudget(0, "")
	a := bud.Account("sort")
	a.RetainBytes(1 << 20)
	if bud.Over() {
		t.Error("an unlimited budget reported being over")
	}
	if bud.Peak() != 1<<20 {
		t.Errorf("peak = %d, want %d", bud.Peak(), 1<<20)
	}
}

func TestBytesRendersReadably(t *testing.T) {
	cases := map[int64]string{
		0:         "0B",
		512:       "512B",
		1 << 10:   "1.0KiB",
		1536:      "1.5KiB",
		1 << 20:   "1.0MiB",
		512 << 20: "512.0MiB",
		3 << 30:   "3.0GiB",
	}
	for n, want := range cases {
		if got := execopt.Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestAResultIsHeldApartFromTheOperators: what only Collect's result holds moves
// neither Used, nor Over, nor Peak, so an operator spills exactly as it would if the
// result were streamed. An allocation an operator holds too is counted once, as the
// operator's, and becomes the result's when the operator lets go.
func TestAResultIsHeldApartFromTheOperators(t *testing.T) {
	shared, fresh := batch(t, "a", []int64{1, 2, 3, 4}), batch(t, "b", []int64{5, 6, 7, 8, 9, 10, 11, 12})
	probe := execopt.NewBudget(0, "")
	probe.Account("x").Retain(shared)
	one := probe.Used()
	probe.Account("x").Retain(fresh)
	two := probe.Used() - one

	bud := execopt.NewBudget(one+1, "")
	bud.MarkDefault()
	op, res := bud.Account("group_by"), bud.Result()
	if res == nil {
		t.Fatal("no result account under the default budget")
	}

	op.Retain(shared)
	res.Retain(shared)
	if bud.Used() != one || bud.Holding() != one {
		t.Errorf("a result sharing an operator's batch: used %d, holding %d, want %d and %d",
			bud.Used(), bud.Holding(), one, one)
	}

	res.Retain(fresh)
	if bud.Used() != one || bud.Holding() != one+two {
		t.Errorf("a batch only the result holds: used %d, holding %d, want %d and %d",
			bud.Used(), bud.Holding(), one, one+two)
	}
	if bud.Over() || op.Over() {
		t.Error("the result's own batch put the operators over their limit")
	}
	if bud.Peak() != one {
		t.Errorf("peak = %d, want the operators' %d", bud.Peak(), one)
	}

	op.Release()
	if bud.Used() != 0 || bud.Holding() != one+two {
		t.Errorf("after the operator let go: used %d, holding %d, want 0 and %d",
			bud.Used(), bud.Holding(), one+two)
	}

	// And back: an operator taking hold of what only the result held is the
	// operators' again.
	op.Retain(fresh)
	if bud.Used() != two || bud.Holding() != one+two {
		t.Errorf("an operator retaining the result's batch: used %d, holding %d, want %d and %d",
			bud.Used(), bud.Holding(), two, one+two)
	}
	op.Release()
	res.Release()
	if bud.Used() != 0 || bud.Holding() != 0 {
		t.Errorf("after both let go: used %d, holding %d", bud.Used(), bud.Holding())
	}
}

// TestOnlyTheDefaultBudgetChecksAResult: a limit the caller gave bounds the
// operators, as it always has, and Collect holds whatever it is asked to.
func TestOnlyTheDefaultBudgetChecksAResult(t *testing.T) {
	marked := func(limit int64) *execopt.Budget {
		b := execopt.NewBudget(limit, "")
		b.MarkDefault()
		return b
	}
	cases := []struct {
		name string
		bud  *execopt.Budget
		want int64
	}{
		{"the default budget", marked(1000), 1500},
		{"a limit the caller gave", execopt.NewBudget(1000, ""), 0},
		{"no budget", nil, 0},
		{"the default, where no ceiling is known", marked(0), 0},
	}
	for _, c := range cases {
		if got := c.bud.ResultLimit(); got != c.want {
			t.Errorf("%s: result limit %d, want %d", c.name, got, c.want)
		}
		if res := c.bud.Result(); (res != nil) != (c.want > 0) {
			t.Errorf("%s: result account %v, want one only with a result limit", c.name, res)
		}
	}

	var none *execopt.Account
	none.Retain(batch(t, "v", []int64{1}))
	if err := none.CheckResult(); err != nil {
		t.Errorf("no result account refused: %v", err)
	}
	none.Release()
}

// TestCheckResultRefusesPastTheResultLimit: the query's holdings, operators' and
// result's together, against three quarters of the ceiling, with an error that
// names collect and says what streams instead.
func TestCheckResultRefusesPastTheResultLimit(t *testing.T) {
	b := batch(t, "v", make([]int64, 64))
	probe := execopt.NewBudget(0, "")
	probe.Account("x").Retain(b)
	size := probe.Used()

	// The limit lets one batch through and not two: 1.5 × limit is 1.5 batches.
	bud := execopt.NewBudget(size, "")
	bud.MarkDefault()
	res := bud.Result()
	res.Retain(b)
	if err := res.CheckResult(); err != nil {
		t.Fatalf("a result of one batch, under a limit of one and a half: %v", err)
	}

	// An operator's holding counts toward it too.
	other := batch(t, "w", make([]int64, 64))
	op := bud.Account("join")
	op.Retain(other)
	err := res.CheckResult()
	if err == nil {
		t.Fatal("two batches held, past a limit of one and a half, were accepted")
	}
	if !errors.Is(err, uerr.ErrResource) {
		t.Errorf("want a resource error, got %v", err)
	}
	for _, want := range []string{"collect", "CollectBatches", "SinkParquet", "WithMemoryLimit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}
	op.Release()
	if err := res.CheckResult(); err != nil {
		t.Errorf("back under the limit but still refused: %v", err)
	}
}
