package plan

import (
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Slice keeps Len rows starting at Offset.
//
// Streaming, not a breaker: it counts rows past and stops, so it never holds more
// than one batch. Head is the Offset == 0 special case and stays its own node
// because limit pushdown recognises it structurally.
//
// A negative Len means "to the end", which is how a caller says "drop the first n
// rows" without knowing the height.
type Slice struct {
	Input  Node
	Offset int
	Len    int
}

func (s *Slice) planNode()        {}
func (s *Slice) Children() []Node { return []Node{s.Input} }

func (s *Slice) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: Slice takes exactly one child")
	}
	c := *s
	c.Input = kids[0]
	return &c
}

// Schema is the input's: slicing chooses rows, never columns.
func (s *Slice) Schema() (*dtype.Schema, error) { return s.Input.Schema() }

func (s *Slice) Label() string {
	l := "SLICE offset " + strconv.Itoa(s.Offset)
	if s.Len >= 0 {
		l += " len " + strconv.Itoa(s.Len)
	}
	return l
}

// Tail keeps the last N rows.
//
// # Why it is not a Slice with a negative offset
//
// Slice can stream because it knows where to start. Tail cannot: which rows are
// the last N is unknown until the input ends. It holds a ring of the most recent N
// rows — O(N) memory, not O(input), so it is bounded but not free, and it is the
// only operator here that buffers.
type Tail struct {
	Input Node
	N     int
}

func (t *Tail) planNode()        {}
func (t *Tail) Children() []Node { return []Node{t.Input} }

func (t *Tail) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: Tail takes exactly one child")
	}
	c := *t
	c.Input = kids[0]
	return &c
}

func (t *Tail) Schema() (*dtype.Schema, error) { return t.Input.Schema() }
func (t *Tail) Label() string                  { return "TAIL " + strconv.Itoa(t.N) }

// Reverse emits rows in the opposite order.
//
// A full pipeline breaker: the last input row is the first output row, so nothing
// can be emitted until the input ends. Memory is O(rows), like Sort's — and unlike
// Sort it does no comparison, so it is the cheapest possible breaker and a useful
// shape to have written when external buffering arrives.
type Reverse struct{ Input Node }

func (r *Reverse) planNode()        {}
func (r *Reverse) Children() []Node { return []Node{r.Input} }

func (r *Reverse) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: Reverse takes exactly one child")
	}
	return &Reverse{Input: kids[0]}
}

func (r *Reverse) Schema() (*dtype.Schema, error) { return r.Input.Schema() }
func (r *Reverse) Label() string                  { return "REVERSE" }

// RowIndex prepends a column numbering the rows from Offset.
//
// # It is a node rather than an expression
//
// `CumCount(false).Over()` computes the same numbers, and does it through the
// window sink — a pipeline breaker holding every row. A row index needs none of
// that: it is a counter that crosses batch boundaries, which is the third operator
// shape (stateful and streaming, like limitOp and distinctOp) rather than the
// second.
//
// It also joins the list of things that depend on the driver delivering batches in
// input order. Numbering rows is meaningless otherwise.
type RowIndex struct {
	Input  Node
	Name   string
	Offset uint32
}

func (r *RowIndex) planNode()        {}
func (r *RowIndex) Children() []Node { return []Node{r.Input} }

func (r *RowIndex) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: RowIndex takes exactly one child")
	}
	c := *r
	c.Input = kids[0]
	return &c
}

func (r *RowIndex) Schema() (*dtype.Schema, error) {
	in, err := r.Input.Schema()
	if err != nil {
		return nil, err
	}
	if in.Has(r.Name) {
		return nil, uerr.New(uerr.KindSchema, "with_row_index",
			"the frame already has a column named %q", r.Name).
			Hint("choose another name for the index")
	}
	// Prepended, matching Polars: an index is an identifier, and identifiers read
	// better on the left.
	fields := make([]dtype.Field, 0, in.Len()+1)
	fields = append(fields, dtype.NotNull(r.Name, dtype.Uint32))
	fields = append(fields, in.FieldSlice()...)
	return dtype.NewSchema(fields...)
}

func (r *RowIndex) Label() string {
	return "ROW INDEX " + strconv.Quote(r.Name) + " from " + strconv.FormatUint(uint64(r.Offset), 10)
}
