package shrink

import (
	"encoding/json"
	"fmt"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/model"
)

// A query is JSON: each frame an object naming its op, each expression an object
// naming its operator, every node read back through ast's constructors, so a file
// cannot hold an ill-typed tree.

type queryJSON struct {
	Count bool       `json:"count,omitempty"`
	Root  *frameJSON `json:"root"`
}

type frameJSON struct {
	Op            string        `json:"op"`
	Table         string        `json:"table,omitempty"`
	In            *frameJSON    `json:"in,omitempty"`
	Pred          *exprJSON     `json:"pred,omitempty"`
	Cols          []namedJSON   `json:"cols,omitempty"`
	Keys          []namedJSON   `json:"keys,omitempty"`
	Aggs          []namedJSON   `json:"aggs,omitempty"`
	SortKeys      []sortKeyJSON `json:"sort_keys,omitempty"`
	N             *int          `json:"n,omitempty"`
	Offset        *int          `json:"offset,omitempty"`
	Len           *int          `json:"len,omitempty"`
	Left          *frameJSON    `json:"left,omitempty"`
	Right         *frameJSON    `json:"right,omitempty"`
	How           string        `json:"how,omitempty"`
	LeftOn        string        `json:"left_on,omitempty"`
	RightOn       string        `json:"right_on,omitempty"`
	MaintainOrder bool          `json:"maintain_order,omitempty"`
}

type namedJSON struct {
	Name string    `json:"name"`
	E    *exprJSON `json:"e"`
}

type sortKeyJSON struct {
	Col       string `json:"col"`
	Desc      bool   `json:"desc,omitempty"`
	NullsLast bool   `json:"nulls_last,omitempty"`
}

type exprJSON struct {
	Type     string          `json:"type"`
	Col      *string         `json:"col,omitempty"`
	Lit      json.RawMessage `json:"lit,omitempty"`
	Null     bool            `json:"null,omitempty"`
	Bin      string          `json:"bin,omitempty"`
	Un       string          `json:"un,omitempty"`
	L        *exprJSON       `json:"l,omitempty"`
	R        *exprJSON       `json:"r,omitempty"`
	X        *exprJSON       `json:"x,omitempty"`
	When     *exprJSON       `json:"when,omitempty"`
	Then     *exprJSON       `json:"then,omitempty"`
	Else     *exprJSON       `json:"else,omitempty"`
	FillNull *exprJSON       `json:"fill_null,omitempty"`
	With     *exprJSON       `json:"with,omitempty"`
	Cast     *exprJSON       `json:"cast,omitempty"`
	Lossy    bool            `json:"lossy,omitempty"`
	Agg      string          `json:"agg,omitempty"`
}

func encodeQuery(q *ast.Query) (json.RawMessage, error) {
	if q == nil {
		return nil, fmt.Errorf("no query")
	}
	root, err := encodeFrame(q.Root)
	if err != nil {
		return nil, err
	}
	return json.Marshal(queryJSON{Count: q.Count, Root: root})
}

func encodeFrame(f ast.Frame) (*frameJSON, error) {
	j := &frameJSON{Op: opName(f)}
	var err error
	in := func(x ast.Frame) *frameJSON {
		if err != nil {
			return nil
		}
		var fj *frameJSON
		fj, err = encodeFrame(x)
		return fj
	}
	switch f := f.(type) {
	case *ast.Scan:
		j.Table = f.Table
	case *ast.Filter:
		j.In = in(f.In)
		j.Pred, err = encodeExpr(f.Pred, err)
	case *ast.Select:
		j.In = in(f.In)
		j.Cols, err = encodeNamed(f.Cols, err)
	case *ast.WithColumns:
		j.In = in(f.In)
		j.Cols, err = encodeNamed(f.Cols, err)
	case *ast.GroupBy:
		j.In = in(f.In)
		j.Keys, err = encodeNamed(f.Keys, err)
		j.Aggs, err = encodeNamed(f.Aggs, err)
		j.MaintainOrder = f.MaintainOrder
		if j.Keys == nil {
			j.Keys = []namedJSON{}
		}
	case *ast.Sort:
		j.In = in(f.In)
		for _, k := range f.Keys {
			j.SortKeys = append(j.SortKeys, sortKeyJSON(k))
		}
	case *ast.Head:
		j.In = in(f.In)
		n := f.N
		j.N = &n
	case *ast.Slice:
		j.In = in(f.In)
		off, l := f.Offset, f.Len
		j.Offset, j.Len = &off, &l
	case *ast.Join:
		j.Left, j.Right = in(f.L), in(f.R)
		j.How, j.LeftOn, j.RightOn, j.MaintainOrder = f.How.String(), f.LeftOn, f.RightOn, f.MaintainOrder
	default:
		return nil, fmt.Errorf("unknown frame %T", f)
	}
	return j, err
}

// opName is a frame's op in a case's file: its type's name.
func opName(f ast.Frame) string {
	switch f.(type) {
	case *ast.Scan:
		return "Scan"
	case *ast.Filter:
		return "Filter"
	case *ast.Select:
		return "Select"
	case *ast.WithColumns:
		return "WithColumns"
	case *ast.GroupBy:
		return "GroupBy"
	case *ast.Sort:
		return "Sort"
	case *ast.Head:
		return "Head"
	case *ast.Slice:
		return "Slice"
	case *ast.Join:
		return "Join"
	}
	return fmt.Sprintf("%T", f)
}

func encodeNamed(ns []ast.Named, err error) ([]namedJSON, error) {
	if err != nil {
		return nil, err
	}
	var out []namedJSON
	for _, n := range ns {
		e, err := encodeExpr(n.E, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, namedJSON{Name: n.Name, E: e})
	}
	return out, nil
}

func encodeExpr(e ast.Expr, err error) (*exprJSON, error) {
	if err != nil {
		return nil, err
	}
	j := &exprJSON{Type: e.Type().String()}
	sub := func(x ast.Expr) *exprJSON {
		if err != nil {
			return nil
		}
		var xj *exprJSON
		xj, err = encodeExpr(x, nil)
		return xj
	}
	switch e := e.(type) {
	case *ast.Col:
		name := e.Name
		j.Col = &name
	case *ast.Lit:
		if e.Null {
			j.Null = true
			break
		}
		j.Lit, err = encodeValue(e.T, litValue(e))
	case *ast.Bin:
		j.Bin, j.L, j.R = e.Op.String(), sub(e.L), sub(e.R)
	case *ast.Un:
		j.Un, j.X = e.Op.String(), sub(e.X)
	case *ast.When:
		j.When, j.Then, j.Else = sub(e.Cond), sub(e.Then), sub(e.Else)
	case *ast.FillNull:
		j.FillNull, j.With = sub(e.X), sub(e.With)
	case *ast.Cast:
		j.Cast, j.Lossy = sub(e.X), e.Lossy
	case *ast.Agg:
		j.Agg = e.Op.String()
		if e.X != nil {
			j.X = sub(e.X)
		}
	default:
		return nil, fmt.Errorf("unknown expression %T", e)
	}
	return j, err
}

func decodeQuery(raw json.RawMessage) (*ast.Query, error) {
	var j queryJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, err
	}
	if j.Root == nil {
		return nil, fmt.Errorf("no root frame")
	}
	root, err := decodeFrame(j.Root)
	if err != nil {
		return nil, err
	}
	return &ast.Query{Root: root, Count: j.Count}, nil
}

func decodeFrame(j *frameJSON) (ast.Frame, error) {
	if j == nil {
		return nil, fmt.Errorf("a missing frame")
	}
	in := func() (ast.Frame, error) { return decodeFrame(j.In) }
	switch j.Op {
	case "Scan":
		return &ast.Scan{Table: j.Table}, nil
	case "Filter":
		f, err := in()
		if err != nil {
			return nil, err
		}
		p, err := decodeExpr(j.Pred)
		if err != nil {
			return nil, err
		}
		return &ast.Filter{In: f, Pred: p}, nil
	case "Select", "WithColumns":
		f, err := in()
		if err != nil {
			return nil, err
		}
		cols, err := decodeNamed(j.Cols)
		if err != nil {
			return nil, err
		}
		if j.Op == "Select" {
			return &ast.Select{In: f, Cols: cols}, nil
		}
		return &ast.WithColumns{In: f, Cols: cols}, nil
	case "GroupBy":
		f, err := in()
		if err != nil {
			return nil, err
		}
		keys, err := decodeNamed(j.Keys)
		if err != nil {
			return nil, err
		}
		aggs, err := decodeNamed(j.Aggs)
		if err != nil {
			return nil, err
		}
		return &ast.GroupBy{In: f, Keys: keys, Aggs: aggs, MaintainOrder: j.MaintainOrder}, nil
	case "Sort":
		f, err := in()
		if err != nil {
			return nil, err
		}
		s := &ast.Sort{In: f}
		for _, k := range j.SortKeys {
			s.Keys = append(s.Keys, ast.SortKey(k))
		}
		return s, nil
	case "Head":
		f, err := in()
		if err != nil {
			return nil, err
		}
		if j.N == nil {
			return nil, fmt.Errorf("a Head without n")
		}
		return &ast.Head{In: f, N: *j.N}, nil
	case "Slice":
		f, err := in()
		if err != nil {
			return nil, err
		}
		if j.Offset == nil || j.Len == nil {
			return nil, fmt.Errorf("a Slice without offset and len")
		}
		return &ast.Slice{In: f, Offset: *j.Offset, Len: *j.Len}, nil
	case "Join":
		l, err := decodeFrame(j.Left)
		if err != nil {
			return nil, err
		}
		r, err := decodeFrame(j.Right)
		if err != nil {
			return nil, err
		}
		how := -1
		for h := ast.Inner; h <= ast.Anti; h++ {
			if h.String() == j.How {
				how = int(h)
			}
		}
		if how < 0 {
			return nil, fmt.Errorf("a join %q", j.How)
		}
		return &ast.Join{L: l, R: r, How: ast.JoinHow(how), LeftOn: j.LeftOn, RightOn: j.RightOn,
			MaintainOrder: j.MaintainOrder}, nil
	}
	return nil, fmt.Errorf("an op %q", j.Op)
}

func decodeNamed(js []namedJSON) ([]ast.Named, error) {
	var out []ast.Named
	for _, n := range js {
		e, err := decodeExpr(n.E)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n.Name, err)
		}
		out = append(out, ast.Named{Name: n.Name, E: e})
	}
	return out, nil
}

func decodeExpr(j *exprJSON) (ast.Expr, error) {
	if j == nil {
		return nil, fmt.Errorf("a missing expression")
	}
	t, ok := typeByName[j.Type]
	if !ok {
		return nil, fmt.Errorf("a type %q", j.Type)
	}
	var e ast.Expr
	var err error
	switch {
	case j.Col != nil:
		e = &ast.Col{Name: *j.Col, T: t}
	case j.Null:
		e = &ast.Lit{T: t, Null: true}
	case len(j.Lit) > 0:
		var v Value
		if v, err = decodeValue(t, j.Lit); err != nil {
			return nil, err
		}
		if !hasLit(t) {
			return nil, fmt.Errorf("a literal of %s", t)
		}
		e = newLit(t, v)
	case j.Bin != "":
		op, ok := binOps[j.Bin]
		if !ok {
			return nil, fmt.Errorf("an operator %q", j.Bin)
		}
		l, r, err2 := decode2(j.L, j.R)
		if err2 != nil {
			return nil, err2
		}
		e, err = ast.NewBin(op, l, r)
	case j.Un != "":
		op, ok := unOps[j.Un]
		if !ok {
			return nil, fmt.Errorf("an operator %q", j.Un)
		}
		x, err2 := decodeExpr(j.X)
		if err2 != nil {
			return nil, err2
		}
		e, err = ast.NewUn(op, x)
	case j.When != nil:
		cond, then, err2 := decode2(j.When, j.Then)
		if err2 != nil {
			return nil, err2
		}
		els, err2 := decodeExpr(j.Else)
		if err2 != nil {
			return nil, err2
		}
		e, err = ast.NewWhen(cond, then, els)
	case j.FillNull != nil:
		x, with, err2 := decode2(j.FillNull, j.With)
		if err2 != nil {
			return nil, err2
		}
		e, err = ast.NewFillNull(x, with)
	case j.Cast != nil:
		x, err2 := decodeExpr(j.Cast)
		if err2 != nil {
			return nil, err2
		}
		e, err = ast.NewCast(x, t, j.Lossy)
	case j.Agg != "":
		op, ok := aggOps[j.Agg]
		if !ok {
			return nil, fmt.Errorf("an aggregate %q", j.Agg)
		}
		var x ast.Expr
		if j.X != nil {
			if x, err = decodeExpr(j.X); err != nil {
				return nil, err
			}
		}
		e, err = ast.NewAgg(op, x)
	default:
		return nil, fmt.Errorf("an expression of no kind")
	}
	if err != nil {
		return nil, err
	}
	if e.Type() != t {
		return nil, fmt.Errorf("%s is %s, its file says %s", e.Go(), e.Type(), t)
	}
	return e, nil
}

func decode2(a, b *exprJSON) (ast.Expr, ast.Expr, error) {
	x, err := decodeExpr(a)
	if err != nil {
		return nil, nil, err
	}
	y, err := decodeExpr(b)
	return x, y, err
}

// hasLit is whether a literal of t is a thing qcheck makes: every type a table
// holds, and the integers casts make.
func hasLit(t model.Type) bool {
	switch t {
	case model.Int8, model.Int16, model.Int32, model.Int64, model.Float64, model.String, model.Bool, model.Date:
		return true
	}
	return false
}

var (
	binOps = map[string]ast.BinOp{}
	unOps  = map[string]ast.UnOp{}
	aggOps = map[string]ast.AggOp{}
)

func init() {
	for op := ast.Add; op <= ast.Or; op++ {
		binOps[op.String()] = op
	}
	for op := ast.Not; op <= ast.IsNotNull; op++ {
		unOps[op.String()] = op
	}
	for op := ast.Sum; op <= ast.NUnique; op++ {
		aggOps[op.String()] = op
	}
}
