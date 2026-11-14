package self

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strings"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/qcheck/ast"
)

// Answer is what one run of a query gave: an error, a count, or a frame read into
// canonical cells, column by column.
type Answer struct {
	Err   error
	Count int64 // a Count query's answer
	Names []string
	Types []dtype.DataType
	Rows  int
	// Cells are each column's cells: "" for a null, else "v" and the value's bytes,
	// a fixed-width value's as stored, text as it is, any NaN as one NaN. A float
	// compares by its bits, so −0 is not +0.
	Cells [][]string
}

const nanCell = "vNaN"

// read is df's answer.
func read(df *ursus.DataFrame) (*Answer, error) {
	a := &Answer{Names: df.Columns(), Rows: df.Height()}
	for j := range df.Width() {
		c := df.ColumnAt(j)
		dt := c.DType()
		a.Types = append(a.Types, dt)
		cells := make([]string, c.Len())
		switch {
		case dt.ID() == dtype.TypeBool:
			bits := c.Bools()
			for i := range cells {
				if !c.IsValid(i) {
					continue
				}
				cells[i] = "v0"
				if bits.Get(i) {
					cells[i] = "v1"
				}
			}
		case dt.ID() == dtype.TypeString:
			s := c.Strings()
			for i := range cells {
				if c.IsValid(i) {
					cells[i] = "v" + s.Get(i)
				}
			}
		case dt.IsFixedWidth():
			w := dt.Physical().BitWidth() / 8
			raw := c.RawFixed()
			float := dt.ID() == dtype.TypeFloat64
			for i := range cells {
				if !c.IsValid(i) {
					continue
				}
				b := raw[i*w : (i+1)*w]
				if float && math.IsNaN(math.Float64frombits(binary.LittleEndian.Uint64(b))) {
					cells[i] = nanCell
					continue
				}
				cells[i] = "v" + string(b)
			}
		default:
			return nil, fmt.Errorf("an answer column %q of %s, which qcheck does not read", a.Names[j], dt)
		}
		a.Cells = append(a.Cells, cells)
	}
	return a, nil
}

// float is a Float64 cell's value.
func float(cell string) float64 {
	if cell == nanCell {
		return math.NaN()
	}
	return math.Float64frombits(binary.LittleEndian.Uint64([]byte(cell[1:])))
}

// u64 is a Uint64 cell's value.
func u64(cell string) uint64 { return binary.LittleEndian.Uint64([]byte(cell[1:])) }

// show renders a cell for a report.
func show(cell string, dt dtype.DataType) string {
	switch {
	case cell == "":
		return "null"
	case cell == nanCell:
		return "NaN"
	case dt.ID() == dtype.TypeFloat64:
		return ast.GoFloat(float(cell))
	case dt.ID() == dtype.TypeString:
		s := cell[1:]
		if len(s) > 40 {
			s = s[:40] + fmt.Sprintf("…(%d bytes)", len(s))
		}
		return fmt.Sprintf("%q", s)
	case dt.ID() == dtype.TypeBool:
		return map[string]string{"v0": "false", "v1": "true"}[cell]
	case dt.IsFixedWidth():
		b := []byte(cell[1:])
		switch len(b) {
		case 1:
			return fmt.Sprint(int8(b[0]))
		case 2:
			return fmt.Sprint(int16(binary.LittleEndian.Uint16(b)))
		case 4:
			return fmt.Sprint(int32(binary.LittleEndian.Uint32(b)))
		case 8:
			if dt.ID() == dtype.TypeUint64 {
				return fmt.Sprint(binary.LittleEndian.Uint64(b))
			}
			return fmt.Sprint(int64(binary.LittleEndian.Uint64(b)))
		case 16:
			lo, hi := binary.LittleEndian.Uint64(b), int64(binary.LittleEndian.Uint64(b[8:]))
			v := new(big.Int).Lsh(big.NewInt(hi), 64)
			return v.Add(v, new(big.Int).SetUint64(lo)).String()
		}
		return fmt.Sprintf("%x", b)
	}
	return cell
}

// verdict is one comparison's outcome.
type verdict struct {
	diff  string // "" when the answers agree
	fired int    // noisy cells that differed within their bound
}

// comparison says how two answers compare.
type comparison struct {
	ordered bool
	bounds  map[string]ast.Bound
	// threads allows a noisy cell's bits to differ within its bound: the two runs
	// had different thread counts. Otherwise every cell compares exactly.
	threads bool
}

const eps = 0x1p-52

// compare compares got against want.
func (c comparison) compare(want, got *Answer) verdict {
	switch {
	case want.Err != nil && got.Err != nil:
		return verdict{}
	case want.Err != nil:
		return verdict{diff: fmt.Sprintf("the reference refused, %v; this answered", firstLine(want.Err))}
	case got.Err != nil:
		return verdict{diff: fmt.Sprintf("the reference answered; this refused, %v", firstLine(got.Err))}
	}
	if want.Count != got.Count {
		return verdict{diff: fmt.Sprintf("Count %d, the reference's %d", got.Count, want.Count)}
	}
	if !slices.Equal(want.Names, got.Names) {
		return verdict{diff: fmt.Sprintf("columns %v, the reference's %v", got.Names, want.Names)}
	}
	for j := range want.Types {
		if !want.Types[j].Equal(got.Types[j]) {
			return verdict{diff: fmt.Sprintf("column %q is %s, the reference's %s", want.Names[j], got.Types[j], want.Types[j])}
		}
	}
	if want.Rows != got.Rows {
		return verdict{diff: fmt.Sprintf("%d rows, the reference's %d", got.Rows, want.Rows)}
	}
	// The columns compared exactly, and the noisy ones with their bounds.
	var exact, noisy []int
	skip := map[string]bool{}
	for _, b := range c.bounds {
		skip[b.Abs] = true // a bound's own sum is noisy, and only bounds
	}
	for j, name := range want.Names {
		switch {
		case skip[name]:
		case c.bounds[name] != (ast.Bound{}):
			noisy = append(noisy, j)
		default:
			exact = append(exact, j)
		}
	}
	wi, gi := identity(want.Rows), identity(got.Rows)
	if !c.ordered {
		wi, gi = canonicalOrder(want, exact, noisy), canonicalOrder(got, exact, noisy)
	}
	var v verdict
	for r := range want.Rows {
		for _, j := range exact {
			if w, g := want.Cells[j][wi[r]], got.Cells[j][gi[r]]; w != g {
				return verdict{diff: c.rowDiff(want, got, wi[r], gi[r], j)}
			}
		}
		for _, j := range noisy {
			ok, fired := c.within(want, got, wi[r], gi[r], j)
			if !ok {
				return verdict{diff: c.rowDiff(want, got, wi[r], gi[r], j)}
			}
			if fired {
				v.fired++
			}
		}
	}
	return v
}

func identity(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// canonicalOrder is a's rows sorted by their exact cells, then their noisy ones as
// floats: two answers equal as multisets line up row for row.
func canonicalOrder(a *Answer, exact, noisy []int) []int {
	idx := identity(a.Rows)
	slices.SortStableFunc(idx, func(x, y int) int {
		for _, j := range exact {
			if c := strings.Compare(a.Cells[j][x], a.Cells[j][y]); c != 0 {
				return c
			}
		}
		for _, j := range noisy {
			fx, fy := a.Cells[j][x], a.Cells[j][y]
			if fx == "" || fy == "" {
				if c := strings.Compare(fx, fy); c != 0 {
					return c
				}
				continue
			}
			if c := cmpFloat(float(fx), float(fy)); c != 0 {
				return c
			}
		}
		return 0
	})
	return idx
}

func cmpFloat(a, b float64) int {
	switch {
	case math.IsNaN(a) && math.IsNaN(b):
		return 0
	case math.IsNaN(a):
		return 1
	case math.IsNaN(b):
		return -1
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// within is whether a noisy cell agrees: exactly, or, between runs on different
// thread counts, within the error model, which then fired.
func (c comparison) within(want, got *Answer, wr, gr, j int) (ok, fired bool) {
	w, g := want.Cells[j][wr], got.Cells[j][gr]
	if w == g {
		return true, false
	}
	if w == "" || g == "" || !c.threads {
		return false, false
	}
	b := c.bounds[want.Names[j]]
	absW, absG := cell(want, b.Abs, wr), cell(got, b.Abs, gr)
	nW := cell(want, b.N, wr)
	if absW == "" || absG == "" || nW == "" {
		return false, false
	}
	s := math.Max(float(absW), float(absG))
	n := float64(u64(nW))
	fw, fg := float(w), float(g)
	switch {
	case math.IsNaN(s):
		// A NaN among the values: the answer is NaN in any order.
		return math.IsNaN(fw) && math.IsNaN(fg), false
	case math.IsInf(s, 1) || s > math.MaxFloat64/4:
		// An infinity among the values, or partial sums that can overflow: which
		// infinity, or NaN, depends on the order of the additions.
		return true, true
	case math.IsNaN(fw) || math.IsNaN(fg) || math.IsInf(fw, 0) || math.IsInf(fg, 0):
		return false, false
	}
	var tol float64
	if b.Op == ast.Sum {
		tol = 2 * n * eps * s
	} else {
		tol = 2*eps*s + 2*ulp(math.Max(math.Abs(fw), math.Abs(fg)))
	}
	if math.Abs(fw-fg) <= tol {
		return true, true
	}
	return false, false
}

func ulp(x float64) float64 { return math.Nextafter(x, math.Inf(1)) - x }

func cell(a *Answer, name string, r int) string {
	j := slices.Index(a.Names, name)
	if j < 0 {
		return ""
	}
	return a.Cells[j][r]
}

// rowDiff describes a differing row: the column, and both rows' cells.
func (c comparison) rowDiff(want, got *Answer, wr, gr, j int) string {
	var b strings.Builder
	how := "row"
	if !c.ordered {
		how = "row, in the rows' canonical order,"
	}
	fmt.Fprintf(&b, "%s %d differs in column %q: %s, the reference's %s\n", how, wr, want.Names[j],
		show(got.Cells[j][gr], got.Types[j]), show(want.Cells[j][wr], want.Types[j]))
	row := func(a *Answer, r int) string {
		parts := make([]string, len(a.Names))
		for k := range a.Names {
			parts[k] = a.Names[k] + "=" + show(a.Cells[k][r], a.Types[k])
		}
		return strings.Join(parts, " ")
	}
	fmt.Fprintf(&b, "  reference: %s\n  this:      %s", row(want, wr), row(got, gr))
	return b.String()
}

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}

// identical is whether two answers are the same to the bit, noisy cells too: the
// same configuration run twice.
func identical(a, b *Answer, ordered bool) string {
	return comparison{ordered: ordered}.compare(a, b).diff
}
