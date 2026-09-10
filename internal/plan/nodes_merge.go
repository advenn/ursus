package plan

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// MergeSorted interleaves two frames that are already sorted on a key, keeping the
// result sorted.
//
// # It is not a join and not a concat
//
// A Union concatenates: every row of the first frame, then every row of the second,
// and the result is sorted only if the second's first row follows the first's last.
// A merge interleaves, so the result is sorted whenever both inputs were. Nothing is
// matched and no row is dropped — the output height is always the sum.
//
// # Both inputs must be sorted on Key, and it is CHECKED
//
// Same discipline as GroupByDynamic and the as-of join: a sortedness claim that is
// trusted but wrong produces a result that is merely in the wrong order, with no
// error — which is worse here than elsewhere, because the whole point of the operator
// is the order.
type MergeSorted struct {
	Left, Right Node

	// Key names the sorted column. One column, by name, because both schemas must
	// already be identical — there is no side to disambiguate.
	Key string
}

func (m *MergeSorted) planNode()        {}
func (m *MergeSorted) Children() []Node { return []Node{m.Left, m.Right} }

func (m *MergeSorted) WithChildren(kids []Node) Node {
	if len(kids) != 2 {
		panic("plan: MergeSorted takes exactly two children")
	}
	c := *m
	c.Left, c.Right = kids[0], kids[1]
	return &c
}

// Schema is the shared schema, and the two sides must agree exactly.
//
// Exactly, not compatibly: a merge produces rows from both frames interleaved, so a
// column that is Int32 on one side and Int64 on the other would need a promotion
// whose result belongs to neither input — which Union already has a mode for, and
// which would make this operator a second, quieter Union.
func (m *MergeSorted) Schema() (*dtype.Schema, error) {
	ls, err := m.Left.Schema()
	if err != nil {
		return nil, err
	}
	rs, err := m.Right.Schema()
	if err != nil {
		return nil, err
	}
	if !ls.Equal(rs) {
		return nil, uerr.New(uerr.KindSchema, "merge_sorted",
			"the two frames have different schemas: %s and %s", ls, rs).
			Hint("merge_sorted interleaves rows, so both sides must match exactly").
			Hint("use Concat for frames that only need to be compatible")
	}
	if ls.IndexOf(m.Key) < 0 {
		return nil, uerr.UnknownColumn("merge_sorted", m.Key, ls.Names())
	}
	return ls, nil
}

func (m *MergeSorted) Label() string { return "MERGE SORTED [" + m.Key + "]" }

func (m *MergeSorted) childLabels() []string { return []string{"left", "right"} }

// resolveMergeSorted checks the key exists and the schemas agree.
// mergeKeyExpr is the key as an expression, for the liveness walk.
func (m *MergeSorted) mergeKeyExpr() expr.Node { return &expr.Col{Name: m.Key} }
