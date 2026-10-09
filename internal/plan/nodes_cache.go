package plan

import (
	"strconv"

	"github.com/advenn/ursus/dtype"
)

// Cache marks a subtree the query uses at more than one place, so it runs once.
//
// # Why it is needed
//
// A LazyFrame shares nodes: `agg := scan.GroupBy(k).Agg(..)` used in a join and
// again under a max is ONE *Aggregate with two parents. Resolve is a TransformUp,
// which copies a shared subtree into each place it is reached, and the engine then
// ran it twice. PDS-H q15 does exactly that with its per-supplier revenue, which was
// most of why it ran at 10.5× Polars (step 141).
//
// # How it stays one
//
// MarkShared wraps each shared subtree in a Cache with an ID BEFORE Resolve. The
// copies Resolve makes are each under a Cache with the same ID, and the physical
// planner runs the first copy it plans and has every Cache of that ID replay it.
//
// That is only right if every copy computes the same rows, so the optimizer treats a
// Cache as a barrier: predicate pushdown and projection pushdown both stop at a node
// they do not know and optimize below it as a fresh root, and the build-side rule
// gives every copy the same answer to whether its rows must keep their order. The
// copies are rewritten by the same rules from the same tree, and come out the same.
//
// The price of the barrier: a filter above one site is not pushed into the shared
// subtree, and every site sees every column the subtree produces. MarkShared caches
// only subtrees with a join, an aggregate or a sort in them, where computing once
// is worth more than that (step 143).
type Cache struct {
	ID    int
	Input Node
}

func (c *Cache) planNode()        {}
func (c *Cache) Children() []Node { return []Node{c.Input} }

func (c *Cache) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: Cache takes exactly one child")
	}
	return &Cache{ID: c.ID, Input: kids[0]}
}

func (c *Cache) Schema() (*dtype.Schema, error) { return c.Input.Schema() }
func (c *Cache) Label() string                  { return "CACHE #" + strconv.Itoa(c.ID) }

// MarkShared wraps every subtree of root that has more than one parent, and holds
// a join, an aggregate or a sort, in a Cache. Sharing is by pointer: the nodes a
// LazyFrame reused. It returns root unchanged when nothing is shared.
//
// A node is shared when it has more than one PARENT, not when it is reached by more
// than one path: the nodes below a shared subtree are reached twice through it, and
// are not themselves shared unless something else also points at them.
func MarkShared(root Node) Node {
	parents := map[Node]int{}
	seen := map[Node]bool{}
	var count func(Node)
	count = func(n Node) {
		if seen[n] {
			return
		}
		seen[n] = true
		for _, c := range n.Children() {
			parents[c]++
			count(c)
		}
	}
	count(root)

	worth := map[Node]bool{}
	for n, p := range parents {
		if p > 1 && worthSharing(n) {
			worth[n] = true
		}
	}
	if len(worth) == 0 {
		return root
	}

	next := 1
	memo := map[Node]Node{}
	var rebuild func(Node) Node
	rebuild = func(n Node) Node {
		if r, ok := memo[n]; ok {
			return r
		}
		out := n
		if kids := n.Children(); len(kids) > 0 {
			nk := make([]Node, len(kids))
			changed := false
			for i, c := range kids {
				nk[i] = rebuild(c)
				changed = changed || nk[i] != c
			}
			if changed {
				out = n.WithChildren(nk)
			}
		}
		if worth[n] {
			out = &Cache{ID: next, Input: out}
			next++
		}
		memo[n] = out
		return out
	}
	return rebuild(root)
}

// worthSharing reports whether running n once is worth a barrier: whether n holds
// a join, an aggregate or a sort. A shared scan, or a filter over one, is cheaper
// read twice than held whole.
func worthSharing(n Node) bool {
	found := false
	Walk(n, func(x Node) bool {
		switch x.(type) {
		case *Join, *AsOfJoin, *Aggregate, *TemporalGroup, *Sort, *Distinct, *Window:
			found = true
		}
		return !found
	})
	return found
}
