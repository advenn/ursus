package execopt_test

import (
	"runtime"
	"testing"
	"time"

	"github.com/advenn/ursus/internal/execopt"
)

// TestAPoolsQueriesShareItsLimit: two queries on a pool of 64 MiB. Each is over once
// the two hold more than the limit and it more than what the other leaves it, never
// less than half; alone, at the limit, as a budget of its own. Neither is refused
// unless it alone holds more than the limit.
func TestAPoolsQueriesShareItsLimit(t *testing.T) {
	const mib = 1 << 20
	p := execopt.NewPool(64*mib, "", false)
	a, b := p.Budget(""), p.Budget("")
	a.Join()
	b.Join()
	defer a.Leave()
	defer b.Leave()
	ha, hb := a.Account("sort"), b.Account("group_by")

	ha.RetainBytes(48 * mib)
	if a.Over() || ha.Check() != nil {
		t.Fatal("48 MiB of 64, the other query holding nothing, is over")
	}
	if got := a.Room(); got != 64*mib {
		t.Errorf("room alone %d, want the limit", got)
	}
	hb.RetainBytes(24 * mib)
	// 72 MiB held together. a's room is what b leaves, 40; b's its even share, 32.
	if got := a.Room(); got != 40*mib {
		t.Errorf("a's room %d MiB, want 40", got/mib)
	}
	if got := b.Room(); got != 32*mib {
		t.Errorf("b's room %d MiB, want its even share, 32", got/mib)
	}
	if !a.Over() || !ha.Over() {
		t.Error("a, holding 48 MiB where b leaves it 40, is not over")
	}
	if b.Over() {
		t.Error("b, holding 24 MiB of its even share of 32, is over")
	}
	if ha.Check() != nil || hb.Check() != nil || a.OverAlone() {
		t.Error("a query under the limit alone was refused")
	}
	if s := p.Stats(); s.Held != 72*mib || s.Peak != 72*mib || s.Running != 2 {
		t.Errorf("stats %+v, want 72 MiB held at a peak of 72, two running", s)
	}

	ha.Release()
	hb.RetainBytes(16 * mib)
	if b.Over() {
		t.Error("b, holding 40 MiB alone on the pool, is over")
	}
	hb.RetainBytes(32 * mib)
	if !b.OverAlone() || hb.Check() == nil {
		t.Error("b, holding 72 MiB of a 64 MiB limit alone, was not refused")
	}
}

// TestALeftQueryLeavesNothing: what a query holds as it leaves, and what its
// operators release afterwards, leave the pool's figures where the others hold them.
// Joining twice counts once; leaving a query that never joined, nothing.
func TestALeftQueryLeavesNothing(t *testing.T) {
	const mib = 1 << 20
	p := execopt.NewPool(64*mib, "", false)
	a, b := p.Budget(""), p.Budget("")
	a.Join()
	a.Join()
	b.Join()
	ha := a.Account("sort")
	ha.RetainBytes(8 * mib)
	b.Account("join").RetainBytes(4 * mib)
	a.NoteSpill()
	a.Leave()
	if s := p.Stats(); s.Held != 4*mib || s.Running != 1 || s.Spills != 1 {
		t.Errorf("after a left: %+v, want b's 4 MiB, one running, one spill", s)
	}
	ha.Release()
	a.Leave()
	if s := p.Stats(); s.Held != 4*mib || s.Running != 1 {
		t.Errorf("a's release after it left reached the pool: %+v", s)
	}
	b.Leave()
	never := p.Budget("")
	never.Leave()
	if s := p.Stats(); s.Held != 0 || s.Running != 0 || s.Peak != 12*mib {
		t.Errorf("all left: %+v, want nothing held or running, a peak of 12 MiB", s)
	}
	// What a query holds is its own still, after it left: its MemoryStats.
	if a.Peak() != 8*mib || a.Spills() != 1 {
		t.Errorf("a's own peak %d and spills %d", a.Peak(), a.Spills())
	}
}

// TestAPoolIsToldAGrainAtATime: a query's total reaches its pool once it has moved a
// sixty-fourth of the limit, at most a mebibyte, from what the pool was told.
func TestAPoolIsToldAGrainAtATime(t *testing.T) {
	p := execopt.NewPool(64<<10, "", false) // a grain of 1 KiB
	b := p.Budget("")
	b.Join()
	defer b.Leave()
	h := b.Account("sort")
	h.RetainBytes(1000)
	if got := p.Stats().Held; got != 0 {
		t.Errorf("1000 bytes, under a grain, told as %d", got)
	}
	h.RetainBytes(100)
	if got := p.Stats().Held; got != 1100 {
		t.Errorf("1100 bytes told as %d", got)
	}
	big := execopt.NewPool(1<<40, "", false)
	q := big.Budget("")
	q.Join()
	defer q.Leave()
	q.Account("sort").RetainBytes(1<<20 - 1)
	if got := big.Stats().Held; got != 0 {
		t.Errorf("a byte under a mebibyte told as %d", got)
	}
}

// TestAQueryNoOneLeftIsTakenOutOfItsPool: a budget joined and dropped without Leave
// leaves its pool once it is collected, the backstop for an entry point that
// forgot.
func TestAQueryNoOneLeftIsTakenOutOfItsPool(t *testing.T) {
	p := execopt.NewPool(64<<20, "", false)
	func() {
		b := p.Budget("")
		b.Join()
		b.Account("sort").RetainBytes(4 << 20)
	}()
	if p.Stats().Running != 1 {
		t.Fatal("the query never joined")
	}
	for range 200 {
		runtime.GC()
		if s := p.Stats(); s.Running == 0 && s.Held == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("a dropped query still in its pool: %+v", p.Stats())
}

// TestAPoolsBudgetIsTheDefaultsKind: a query on a pool may run its group-by in
// parallel, as under the default budget, and checks its result where the pool's limit
// is the default's; a spill directory of its own is kept over the pool's.
func TestAPoolsBudgetIsTheDefaultsKind(t *testing.T) {
	derived := execopt.NewPool(1000, "/pool", true).Budget("")
	given := execopt.NewPool(1000, "/pool", false).Budget("/own")
	if !derived.IsDefault() || !given.IsDefault() {
		t.Error("a budget on a pool is not marked as the default's kind")
	}
	if derived.ResultLimit() != 1500 || given.ResultLimit() != 0 {
		t.Errorf("result limits %d and %d, want 1500 under the default's and none under a given one",
			derived.ResultLimit(), given.ResultLimit())
	}
	if derived.SpillDir() != "/pool" || given.SpillDir() != "/own" {
		t.Errorf("spill directories %q and %q", derived.SpillDir(), given.SpillDir())
	}
}
