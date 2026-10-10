package plan

import (
	"strconv"
	"sync/atomic"

	"github.com/advenn/ursus/dtype"
)

// RuntimeSlot is where a join publishes its build side's keys once its build is
// complete, for a RuntimeFilter on its probe side to read (step 167). What it holds
// is the physical layer's; the plan only carries it from the join to the filter.
type RuntimeSlot struct {
	ID int
	v  atomic.Value
}

// Publish makes v the slot's keys. v must not be nil.
func (s *RuntimeSlot) Publish(v any) { s.v.Store(v) }

// Published is the slot's keys, or nil before they are published, and for good when
// the join never publishes them.
func (s *RuntimeSlot) Published() any { return s.v.Load() }

// RuntimeFilter keeps the rows of Input whose Key may be among the keys the join
// that publishes Slot builds: every row until the join publishes, and after that the
// rows whose key it has. The join drops the rest anyway, so dropping them here, below
// the operators between, is a saving and never a change of answer (rule
// runtimeFilters, step 167).
type RuntimeFilter struct {
	Input Node
	Key   string
	Slot  *RuntimeSlot
}

func (r *RuntimeFilter) planNode()        {}
func (r *RuntimeFilter) Children() []Node { return []Node{r.Input} }

func (r *RuntimeFilter) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: RuntimeFilter takes exactly one child")
	}
	return &RuntimeFilter{Input: kids[0], Key: r.Key, Slot: r.Slot}
}

func (r *RuntimeFilter) Schema() (*dtype.Schema, error) { return r.Input.Schema() }

func (r *RuntimeFilter) Label() string {
	return "RUNTIME FILTER [col(" + strconv.Quote(r.Key) + ")] by join #" + strconv.Itoa(r.Slot.ID)
}
