package gen

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"

	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/model"
)

// Case is one generated case: tables, a query over them, and the k of the "1
// against k source records" factor.
type Case struct {
	Seed    uint64
	Tables  []*model.Table
	Query   *ast.Query
	Records int // 2 to 5: how many records a variant cuts each table into
}

// Tables are the case's tables by name.
func (c *Case) TableMap() map[string]*model.Table {
	m := make(map[string]*model.Table, len(c.Tables))
	for _, t := range c.Tables {
		m[t.Name] = t
	}
	return m
}

// String describes the case: its seed, tables and query.
func (c *Case) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "seed %d, %d records for k\n", c.Seed, c.Records)
	for _, t := range c.Tables {
		b.WriteString("  " + t.String() + "\n")
	}
	b.WriteString(indent(c.Query.Go(), "  "))
	return b.String()
}

func indent(s, by string) string {
	return by + strings.ReplaceAll(s, "\n", "\n"+by)
}

// MaxJoinRows bounds a join's answer, counted on the base tables before any filter:
// a key that repeats on both sides multiplies, and an 8193-row table joined to itself
// on a column of three values would answer 22 million rows. In batches of one row a
// row of a dozen columns costs about 37 KB of batch headers through four operators,
// so 10,000 rows keep a run well under fuzzcheck's gigabyte.
const MaxJoinRows = 10_000

// splitmix is SplitMix64's step, which derives the tables' seeds from the case's.
func splitmix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// NewCase draws the case of seed.
func NewCase(seed uint64) *Case {
	// Mixed first: PCGs seeded with neighbouring seeds draw alike for a while, and a
	// run's cases have neighbouring seeds.
	r := rand.New(rand.NewPCG(splitmix(seed), splitmix(seed^0xca5e)))
	c := &Case{Seed: seed, Records: 2 + r.IntN(4)}
	ntables := 1 + r.IntN(2)
	for i := range ntables {
		c.Tables = append(c.Tables, Table(splitmix(seed+uint64(i)), fmt.Sprintf("t%d", i), 8193))
	}
	g := &qgen{r: r, tables: c.TableMap(), order: c.Tables}
	c.Query = g.query()
	return c
}

// qgen draws one query.
type qgen struct {
	r      *rand.Rand
	tables map[string]*model.Table
	order  []*model.Table
	names  int // the next fresh name's number
	joins  int
	groups int
}

func (g *qgen) fresh(prefix string) string {
	g.names++
	return fmt.Sprintf("%s%d", prefix, g.names)
}

func (g *qgen) info(f ast.Frame) *ast.Info {
	in, err := ast.Schema(f, g.tables)
	if err != nil {
		panic(fmt.Sprintf("gen: an ill-formed frame: %v", err))
	}
	return in
}

func (g *qgen) query() *ast.Query {
	var f ast.Frame = &ast.Scan{Table: g.order[0].Name}
	steps := 1 + g.r.IntN(4)
	for range steps {
		f = g.step(f)
	}
	return &ast.Query{Root: f, Count: g.r.IntN(10) == 0}
}

// step applies one operation to f, drawn among those its Info allows.
func (g *qgen) step(f ast.Frame) ast.Frame {
	in := g.info(f)
	type choice struct {
		w  int
		fn func() ast.Frame
	}
	choices := []choice{
		{4, func() ast.Frame { return &ast.Filter{In: f, Pred: g.expr(in, model.Bool, 2)} }},
		{2, func() ast.Frame { return g.withColumns(f, in) }},
		{1, func() ast.Frame { return g.select_(f, in) }},
		{2, func() ast.Frame { return g.sort(f, in) }},
	}
	if in.Ordered {
		choices = append(choices, choice{1, func() ast.Frame { return g.limit(f) }})
	}
	if g.groups < 2 {
		choices = append(choices, choice{3, func() ast.Frame { return g.groupBy(f, in) }})
	}
	if g.joins < 1 {
		choices = append(choices, choice{3, func() ast.Frame { return g.join(f, in) }})
	}
	total := 0
	for _, c := range choices {
		total += c.w
	}
	n := g.r.IntN(total)
	for _, c := range choices {
		if n < c.w {
			return c.fn()
		}
		n -= c.w
	}
	panic("unreachable")
}

// --- frame operations -----------------------------------------------------------

// withColumns adds or replaces one to three columns, each expression over the
// columns the ones before it left.
func (g *qgen) withColumns(f ast.Frame, in *ast.Info) ast.Frame {
	wc := &ast.WithColumns{In: f}
	for range 1 + g.r.IntN(3) {
		name := g.fresh("x")
		// Sometimes replace a column in place, a key or noisy column never.
		if cands := g.replaceable(in); len(cands) > 0 && g.r.IntN(3) == 0 {
			name = cands[g.r.IntN(len(cands))]
		}
		if slices.ContainsFunc(wc.Cols, func(n ast.Named) bool { return n.Name == name }) {
			continue
		}
		wc.Cols = append(wc.Cols, ast.Named{Name: name, E: g.expr(in, g.anyType(in), 2)})
		in = g.info(wc)
	}
	return wc
}

// replaceable are the columns WithColumns may replace: not noisy, not a key.
func (g *qgen) replaceable(in *ast.Info) []string {
	var out []string
	for _, c := range in.Cols {
		if !c.Noisy && !slices.ContainsFunc(in.Keys, func(k []string) bool { return slices.Contains(k, c.Name) }) {
			out = append(out, c.Name)
		}
	}
	return out
}

func (g *qgen) select_(f ast.Frame, in *ast.Info) ast.Frame {
	var cols []ast.Named
	for _, c := range in.Cols {
		if g.r.IntN(3) > 0 {
			cols = append(cols, ast.Named{Name: c.Name, E: &ast.Col{Name: c.Name, T: c.Type}})
		}
	}
	if len(cols) == 0 {
		c := in.Cols[g.r.IntN(len(in.Cols))]
		cols = append(cols, ast.Named{Name: c.Name, E: &ast.Col{Name: c.Name, T: c.Type}})
	}
	for range g.r.IntN(3) {
		cols = append(cols, ast.Named{Name: g.fresh("x"), E: g.expr(in, g.anyType(in), 2)})
	}
	g.r.Shuffle(len(cols), func(i, j int) { cols[i], cols[j] = cols[j], cols[i] })
	return &ast.Select{In: f, Cols: cols}
}

// sort orders f, and cuts it with Head or Slice where the order is total or
// promised. A frame in no promised order is sorted by a key set too, when it has
// one, so that its order becomes total.
func (g *qgen) sort(f ast.Frame, in *ast.Info) ast.Frame {
	var names []string
	for _, c := range in.Cols {
		if !c.Noisy {
			names = append(names, c.Name)
		}
	}
	if len(names) == 0 {
		return f
	}
	g.r.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
	keys := names[:1+g.r.IntN(min(3, len(names)))]
	if !in.Ordered && !in.OneRow && len(in.Keys) > 0 {
		for _, k := range in.Keys[0] {
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	s := &ast.Sort{In: f}
	for _, k := range keys {
		s.Keys = append(s.Keys, ast.SortKey{Col: k, Desc: g.r.IntN(2) == 0, NullsLast: g.r.IntN(2) == 0})
	}
	if g.info(s).Ordered && g.r.IntN(3) > 0 {
		return g.limit(s)
	}
	return s
}

// limit is f's first rows, or a slice of them.
func (g *qgen) limit(f ast.Frame) ast.Frame {
	ns := []int{0, 1, 2, 3, 5, 10, 100, 1000, 8191, 8192, 8193}
	if g.r.IntN(2) == 0 {
		return &ast.Head{In: f, N: ns[g.r.IntN(len(ns))]}
	}
	offs := []int{0, 1, 7, 100, 4000, -1, -5, -100, -9000}
	lens := []int{-1, 0, 1, 5, 100, 5000}
	return &ast.Slice{In: f, Offset: offs[g.r.IntN(len(offs))], Len: lens[g.r.IntN(len(lens))]}
}

// groupBy aggregates f by one to three integer keys of mixed widths, or none.
func (g *qgen) groupBy(f ast.Frame, in *ast.Info) ast.Frame {
	g.groups++
	gb := &ast.GroupBy{In: f, MaintainOrder: g.r.IntN(10) < 3}
	var ints []ast.Column
	for _, c := range in.Cols {
		if !c.Noisy && c.Type.Arith() && c.Type.IsSignedInt() {
			ints = append(ints, c)
		}
	}
	nkeys := 1 + g.r.IntN(3)
	if len(ints) == 0 || g.r.IntN(10) == 0 {
		nkeys = 0
	}
	for range nkeys {
		c := ints[g.r.IntN(len(ints))]
		col := &ast.Col{Name: c.Name, T: c.Type}
		var key ast.Named
		switch g.r.IntN(6) {
		case 0, 1:
			key = ast.Named{Name: c.Name, E: col}
		case 2:
			to := []model.Type{model.Int8, model.Int16, model.Int32, model.Int64}[g.r.IntN(4)]
			e, err := ast.NewCast(col, to, true)
			must(err)
			key = ast.Named{Name: g.fresh("k"), E: e}
		case 3:
			e, err := ast.NewBin(ast.Mod, col, intLit(c.Type, int64(2+g.r.IntN(9))))
			must(err)
			key = ast.Named{Name: g.fresh("k"), E: e}
		case 4:
			e, err := ast.NewBin(ast.FloorDiv, col, intLit(c.Type, int64(1+g.r.IntN(1000))))
			must(err)
			key = ast.Named{Name: g.fresh("k"), E: e}
		default:
			e := g.expr(in, c.Type, 1)
			key = ast.Named{Name: g.fresh("k"), E: e}
		}
		if slices.ContainsFunc(gb.Keys, func(n ast.Named) bool { return n.Name == key.Name }) {
			continue
		}
		gb.Keys = append(gb.Keys, key)
	}
	for range 1 + g.r.IntN(4) {
		gb.Aggs = append(gb.Aggs, ast.Named{Name: g.fresh("a"), E: g.agg(in)})
	}
	return gb
}

// agg draws an aggregate over in's rows. A float Sum, Mean, Min or Max is drawn
// only where the rows come in a promised order: over rows in none, a float sum's
// last bits and the sign of a zero Min would move with the order, which the self
// check's one allowance, the float-threads rule, does not cover.
func (g *qgen) agg(in *ast.Info) ast.Expr {
	for {
		op := ast.AggOp(g.r.IntN(7))
		if op == ast.Len {
			e, err := ast.NewAgg(op, nil)
			must(err)
			return e
		}
		var t model.Type
		switch op {
		case ast.Sum:
			t = []model.Type{model.Int64, model.Int32, model.Float64, model.Float64, model.Bool, model.Int16}[g.r.IntN(6)]
		case ast.Mean:
			t = []model.Type{model.Int64, model.Int32, model.Float64, model.Float64}[g.r.IntN(4)]
		default:
			t = g.anyType(in)
		}
		if t == model.Float64 && op != ast.Count && op != ast.NUnique && !in.Ordered {
			continue
		}
		depth := 0
		if g.r.IntN(3) == 0 {
			depth = 1
		}
		e, err := ast.NewAgg(op, g.expr(in, t, depth))
		must(err)
		return e
	}
}

// join joins f to a scan of a table on a key of the base tables, an integer, text
// or a float with its NaN and ±0, the kind drawn first so that each is joined on
// often; an inner or left join only where the tables bound its matches under
// MaxJoinRows.
func (g *qgen) join(f ast.Frame, in *ast.Info) ast.Frame {
	g.joins++
	how := ast.JoinHow(g.r.IntN(4))
	type pair struct {
		lc ast.Column
		rt *model.Table
		rc model.Column
	}
	kinds := map[string][]pair{}
	for _, lc := range in.Cols {
		if lc.Origin == "" || lc.Noisy {
			continue
		}
		for _, rt := range g.order {
			for _, rc := range rt.Cols {
				if _, ok := ast.JoinKeyType(lc.Type, rc.Type); !ok {
					continue
				}
				kind := "int"
				switch lc.Type {
				case model.String:
					kind = "string"
				case model.Float64:
					kind = "float"
				}
				kinds[kind] = append(kinds[kind], pair{lc, rt, rc})
			}
		}
	}
	var names []string
	for k := range kinds {
		names = append(names, k)
	}
	if len(names) == 0 {
		return f
	}
	slices.Sort(names)
	cands := kinds[names[g.r.IntN(len(names))]]
	for range 8 {
		p := cands[g.r.IntN(len(cands))]
		if how == ast.Inner || how == ast.Left {
			if g.matches(p.lc.Origin, p.rt.Name+"."+p.rc.Name) > MaxJoinRows {
				continue
			}
		}
		var right ast.Frame = &ast.Scan{Table: p.rt.Name}
		if g.r.IntN(3) == 0 {
			right = &ast.Filter{In: right, Pred: g.expr(g.info(right), model.Bool, 1)}
		}
		j := &ast.Join{L: f, R: right, How: how, LeftOn: p.lc.Name, RightOn: p.rc.Name,
			MaintainOrder: (how == ast.Inner || how == ast.Left) && g.r.IntN(2) == 0}
		if _, err := ast.Schema(j, g.tables); err != nil {
			// A name the suffix cannot make unique; another key may do.
			continue
		}
		return j
	}
	return f
}

// matches is how many pairs of rows of two base columns hold the same key, a null
// matching none, NaN matching NaN and −0 matching +0, as ursus's join matches them
// (an inner join's answer, before any filter).
func (g *qgen) matches(lorigin, rorigin string) int {
	l, r := g.counts(lorigin), g.counts(rorigin)
	n := 0
	for k, c := range l {
		n += c * r[k]
	}
	return n
}

func (g *qgen) counts(origin string) map[string]int {
	tname, cname, _ := strings.Cut(origin, ".")
	t := g.tables[tname]
	idx := slices.IndexFunc(t.Cols, func(c model.Column) bool { return c.Name == cname })
	col := t.Rec.Column(idx)
	out := map[string]int{}
	for i := range col.Len() {
		if col.IsNull(i) {
			continue
		}
		var k string
		switch a := col.(type) {
		case *array.Int64:
			k = fmt.Sprint(a.Value(i))
		case *array.Int32:
			k = fmt.Sprint(a.Value(i))
		case *array.Float64:
			f := a.Value(i)
			switch {
			case math.IsNaN(f):
				k = "NaN"
			case f == 0:
				k = "0"
			default:
				k = fmt.Sprint(math.Float64bits(f))
			}
		case *array.String:
			k = a.Value(i)
		default:
			k = col.ValueStr(i)
		}
		out[k]++
	}
	return out
}

// --- expressions ------------------------------------------------------------------

// anyType is a type to compute in: one of in's column types mostly, so that columns
// are read.
func (g *qgen) anyType(in *ast.Info) model.Type {
	if g.r.IntN(5) > 0 {
		var ts []model.Type
		for _, c := range in.Cols {
			if !c.Noisy && c.Type != model.Uint64 && c.Type != model.Int128 {
				ts = append(ts, c.Type)
			}
		}
		if len(ts) > 0 {
			return ts[g.r.IntN(len(ts))]
		}
	}
	return []model.Type{model.Int64, model.Int32, model.Float64, model.String, model.Bool, model.Date,
		model.Int8, model.Int16}[g.r.IntN(8)]
}

// cols are in's columns of type t that an expression may read.
func cols(in *ast.Info, t model.Type) []ast.Column {
	var out []ast.Column
	for _, c := range in.Cols {
		if c.Type == t && !c.Noisy {
			out = append(out, c)
		}
	}
	return out
}

// expr draws an expression of type t over in, at most depth deep.
func (g *qgen) expr(in *ast.Info, t model.Type, depth int) ast.Expr {
	if depth <= 0 || g.r.IntN(4) == 0 {
		return g.leaf(in, t)
	}
	for range 10 {
		var e ast.Expr
		var err error
		switch t {
		case model.Bool:
			e, err = g.boolExpr(in, depth)
		case model.Int8, model.Int16, model.Int32, model.Int64:
			e, err = g.intExpr(in, t, depth)
		case model.Float64:
			e, err = g.floatExpr(in, depth)
		case model.String, model.Date:
			e, err = g.otherExpr(in, t, depth)
		default:
			return g.leaf(in, t)
		}
		if err == nil && e != nil && e.Type() == t {
			return e
		}
	}
	return g.leaf(in, t)
}

// leaf is a column of type t, or a literal: a value the column holds, where it
// came from a base table, so that a comparison with it keeps some rows.
func (g *qgen) leaf(in *ast.Info, t model.Type) ast.Expr {
	cs := cols(in, t)
	if len(cs) > 0 && g.r.IntN(5) > 0 {
		c := cs[g.r.IntN(len(cs))]
		return &ast.Col{Name: c.Name, T: c.Type}
	}
	if len(cs) > 0 && g.r.IntN(2) == 0 {
		if l := g.sample(cs[g.r.IntN(len(cs))]); l != nil {
			return l
		}
	}
	return g.lit(t)
}

// sample is a value of c's base column, or nil.
func (g *qgen) sample(c ast.Column) *ast.Lit {
	if c.Origin == "" {
		return nil
	}
	tname, cname, _ := strings.Cut(c.Origin, ".")
	t := g.tables[tname]
	if t.Rows == 0 {
		return nil
	}
	idx := slices.IndexFunc(t.Cols, func(mc model.Column) bool { return mc.Name == cname })
	col := t.Rec.Column(idx)
	i := g.r.IntN(t.Rows)
	if col.IsNull(i) {
		return nil
	}
	switch a := col.(type) {
	case *array.Int64:
		return &ast.Lit{T: model.Int64, V: a.Value(i)}
	case *array.Int32:
		return &ast.Lit{T: model.Int32, V: int64(a.Value(i))}
	case *array.Float64:
		return &ast.Lit{T: model.Float64, V: a.Value(i)}
	case *array.String:
		return &ast.Lit{T: model.String, V: a.Value(i)}
	case *array.Boolean:
		return &ast.Lit{T: model.Bool, V: a.Value(i)}
	case *array.Date32:
		return &ast.Lit{T: model.Date, V: int32(a.Value(i))}
	}
	return nil
}

func intLit(t model.Type, v int64) *ast.Lit { return &ast.Lit{T: t, V: v} }

// lit draws a literal of type t: tame mostly, an extreme sometimes, a typed null
// rarely.
func (g *qgen) lit(t model.Type) *ast.Lit {
	if g.r.IntN(20) == 0 {
		return &ast.Lit{T: t, Null: true}
	}
	v := values{r: g.r, c: model.Column{Type: t, Profile: model.Profile(g.r.IntN(3))}}
	v.pool()
	switch t {
	case model.Int64:
		return &ast.Lit{T: t, V: v.int64()}
	case model.Int32:
		return &ast.Lit{T: t, V: v.int32()}
	case model.Int8, model.Int16:
		bits := t.Bits()
		x := v.int32()
		if g.r.IntN(4) == 0 {
			x = []int64{-(1 << (bits - 1)), 1<<(bits-1) - 1}[g.r.IntN(2)]
		}
		return &ast.Lit{T: t, V: int64(wrap(x, bits))}
	case model.Float64:
		return &ast.Lit{T: t, V: v.float64()}
	case model.String:
		return &ast.Lit{T: t, V: v.string()}
	case model.Bool:
		return &ast.Lit{T: t, V: g.r.IntN(2) == 0}
	case model.Date:
		return &ast.Lit{T: t, V: int32(v.date())}
	}
	panic("gen: no literal of " + t.String())
}

// wrap is x reduced to a signed integer of bits, two's complement.
func wrap(x int64, bits int) int64 {
	shift := 64 - bits
	return x << shift >> shift
}

// narrower are the integer types no wider than t.
func narrower(t model.Type) []model.Type {
	var out []model.Type
	for _, u := range []model.Type{model.Int8, model.Int16, model.Int32, model.Int64} {
		if u.Bits() <= t.Bits() {
			out = append(out, u)
		}
	}
	return out
}

func (g *qgen) boolExpr(in *ast.Info, depth int) (ast.Expr, error) {
	switch g.r.IntN(9) {
	case 0, 1, 2:
		return g.compare(in, depth)
	case 3:
		return ast.NewBin([]ast.BinOp{ast.And, ast.Or}[g.r.IntN(2)],
			g.expr(in, model.Bool, depth-1), g.expr(in, model.Bool, depth-1))
	case 4:
		return ast.NewUn(ast.Not, g.expr(in, model.Bool, depth-1))
	case 5:
		return ast.NewUn([]ast.UnOp{ast.IsNull, ast.IsNotNull}[g.r.IntN(2)], g.expr(in, g.anyType(in), depth-1))
	case 6:
		return ast.NewWhen(g.expr(in, model.Bool, depth-1), g.expr(in, model.Bool, depth-1),
			g.expr(in, model.Bool, depth-1))
	case 7:
		return ast.NewFillNull(g.expr(in, model.Bool, depth-1), g.lit(model.Bool))
	default:
		return ast.NewCast(g.expr(in, model.String, depth-1), model.Bool, true)
	}
}

// compare compares two operands of comparable types, a column with a value it holds
// mostly. An Int128 or Uint64, an aggregate's answer, is compared with an Int64.
func (g *qgen) compare(in *ast.Info, depth int) (ast.Expr, error) {
	op := ast.BinOp(int(ast.Eq) + g.r.IntN(6))
	var lc []ast.Column
	for _, c := range in.Cols {
		if !c.Noisy {
			lc = append(lc, c)
		}
	}
	if len(lc) == 0 {
		return nil, fmt.Errorf("no column")
	}
	c := lc[g.r.IntN(len(lc))]
	l := ast.Expr(&ast.Col{Name: c.Name, T: c.Type})
	if c.Type == model.Int128 || c.Type == model.Uint64 {
		return ast.NewBin(op, l, &ast.Lit{T: model.Int64, V: int64(g.r.IntN(200)) - 20})
	}
	if depth > 1 && g.r.IntN(3) == 0 {
		l = g.expr(in, c.Type, depth-1)
	}
	var r ast.Expr
	switch {
	case g.r.IntN(2) == 0:
		if s := g.sample(c); s != nil {
			r = s
			break
		}
		fallthrough
	default:
		rt := c.Type
		if c.Type.Arith() && g.r.IntN(3) == 0 {
			rt = []model.Type{model.Int64, model.Int32, model.Float64, model.Int8}[g.r.IntN(4)]
		}
		r = g.expr(in, rt, depth-1)
	}
	return ast.NewBin(op, l, r)
}

func (g *qgen) intExpr(in *ast.Info, t model.Type, depth int) (ast.Expr, error) {
	nar := narrower(t)
	sub := func() ast.Expr { return g.expr(in, nar[g.r.IntN(len(nar))], depth-1) }
	switch g.r.IntN(10) {
	case 0, 1:
		op := []ast.BinOp{ast.Add, ast.Sub, ast.Mul}[g.r.IntN(3)]
		return ast.NewBin(op, g.expr(in, t, depth-1), sub())
	case 2:
		op := []ast.BinOp{ast.FloorDiv, ast.Mod}[g.r.IntN(2)]
		var r ast.Expr = sub()
		if g.r.IntN(2) == 0 {
			r = intLit(nar[0], int64([]int{-7, -2, -1, 0, 1, 2, 3, 7, 10}[g.r.IntN(9)]))
		}
		return ast.NewBin(op, g.expr(in, t, depth-1), r)
	case 3:
		return ast.NewUn([]ast.UnOp{ast.Neg, ast.Abs}[g.r.IntN(2)], g.expr(in, t, depth-1))
	case 4:
		var els ast.Expr = sub()
		if g.r.IntN(4) == 0 {
			els = &ast.Lit{T: t, Null: true}
		}
		return ast.NewWhen(g.expr(in, model.Bool, depth-1), g.expr(in, t, depth-1), els)
	case 5:
		var with ast.Expr
		switch g.r.IntN(4) {
		case 0:
			with = g.lit(t)
		case 1:
			// A weak literal: an Int64 that fits keeps t, one that does not widens.
			with = &ast.Lit{T: model.Int64, V: int64(g.r.IntN(400)) - 200}
		case 2:
			with = &ast.Lit{T: model.Float64, V: float64(g.r.IntN(21) - 10)}
		default:
			with = sub()
		}
		return ast.NewFillNull(g.expr(in, t, depth-1), with)
	case 6:
		// A strict cast that cannot refuse: from a narrower integer, a Boolean, a Date.
		froms := []model.Type{model.Bool}
		for _, u := range nar {
			if u != t {
				froms = append(froms, u)
			}
		}
		if t == model.Int32 || t == model.Int64 {
			froms = append(froms, model.Date)
		}
		return ast.NewCast(g.expr(in, froms[g.r.IntN(len(froms))], depth-1), t, false)
	case 7:
		// A lossy cast: from text, a float, a wider integer.
		froms := []model.Type{model.String, model.Float64, model.Int64, model.Int32}
		return ast.NewCast(g.expr(in, froms[g.r.IntN(len(froms))], depth-1), t, true)
	default:
		return g.leaf(in, t), nil
	}
}

func (g *qgen) floatExpr(in *ast.Info, depth int) (ast.Expr, error) {
	arith := []model.Type{model.Float64, model.Float64, model.Int64, model.Int32}
	sub := func() ast.Expr { return g.expr(in, arith[g.r.IntN(len(arith))], depth-1) }
	switch g.r.IntN(9) {
	case 0, 1:
		op := []ast.BinOp{ast.Add, ast.Sub, ast.Mul, ast.FloorDiv, ast.Mod}[g.r.IntN(5)]
		l, r := g.expr(in, model.Float64, depth-1), sub()
		if g.r.IntN(2) == 0 {
			l, r = r, l
		}
		return ast.NewBin(op, l, r)
	case 2:
		return ast.NewBin(ast.Div, sub(), sub())
	case 3:
		return ast.NewUn([]ast.UnOp{ast.Neg, ast.Abs}[g.r.IntN(2)], g.expr(in, model.Float64, depth-1))
	case 4:
		return ast.NewWhen(g.expr(in, model.Bool, depth-1), g.expr(in, model.Float64, depth-1), sub())
	case 5:
		var with ast.Expr = g.lit(model.Float64)
		if g.r.IntN(2) == 0 {
			with = sub()
		}
		return ast.NewFillNull(g.expr(in, model.Float64, depth-1), with)
	case 6:
		from := []model.Type{model.Int64, model.Int32, model.Bool, model.Int8}[g.r.IntN(4)]
		return ast.NewCast(g.expr(in, from, depth-1), model.Float64, false)
	case 7:
		return ast.NewCast(g.expr(in, model.String, depth-1), model.Float64, true)
	default:
		return g.leaf(in, model.Float64), nil
	}
}

// otherExpr is a String or a Date: a conditional, a fill, or a cast to it.
func (g *qgen) otherExpr(in *ast.Info, t model.Type, depth int) (ast.Expr, error) {
	switch g.r.IntN(5) {
	case 0:
		return ast.NewWhen(g.expr(in, model.Bool, depth-1), g.expr(in, t, depth-1), g.expr(in, t, depth-1))
	case 1:
		var with ast.Expr = g.lit(t)
		if g.r.IntN(2) == 0 {
			with = g.expr(in, t, depth-1)
		}
		return ast.NewFillNull(g.expr(in, t, depth-1), with)
	case 2:
		if t == model.String {
			from := []model.Type{model.Int64, model.Int32, model.Float64, model.Bool, model.Date}[g.r.IntN(5)]
			return ast.NewCast(g.expr(in, from, depth-1), t, false)
		}
		if g.r.IntN(2) == 0 {
			return ast.NewCast(g.expr(in, model.Int32, depth-1), t, false)
		}
		from := []model.Type{model.String, model.Int64}[g.r.IntN(2)]
		return ast.NewCast(g.expr(in, from, depth-1), t, true)
	default:
		return g.leaf(in, t), nil
	}
}

func must(err error) {
	if err != nil {
		panic("gen: " + err.Error())
	}
}
