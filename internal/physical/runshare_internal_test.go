package physical

// Step 170: a join's output shares the probe batch's columns when the rows it takes
// from it are a run, as when every probe row matched once, in order, and gathers
// them otherwise.

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source/memsrc"
)

func TestRunOf(t *testing.T) {
	b := data.NewBatchRows(dtype.MustSchema(), nil, 10)
	for _, c := range []struct {
		sel   []int32
		start int
		ok    bool
	}{
		{[]int32{3, 4, 5}, 3, true},
		{[]int32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, 0, true},
		{[]int32{9}, 9, true},
		{[]int32{3, 5}, 0, false},
		{[]int32{4, 3}, 0, false},
		{[]int32{8, 9, 10}, 0, false}, // past the batch
		{[]int32{kernel.NullIndex}, 0, false},
		{nil, 0, false},
	} {
		start, ok := runOf(c.sel, b)
		if ok != c.ok || (ok && start != c.start) {
			t.Errorf("runOf(%v) = %d, %v; want %d, %v", c.sel, start, ok, c.start, c.ok)
		}
	}
	if _, ok := runOf([]int32{0}, nil); ok {
		t.Error("a run over no batch")
	}
}

// TestAJoinSharesARunOfItsProbeRows: gatherOut over a run of the left batch aliases
// its column, and over anything else copies it; the values are the rows' either way.
func TestAJoinSharesARunOfItsProbeRows(t *testing.T) {
	ls := dtype.MustSchema(dtype.Of("k", dtype.Int64), dtype.Of("l", dtype.Int64))
	rs := dtype.MustSchema(dtype.Of("k2", dtype.Int64), dtype.Of("r", dtype.Int64))
	lk, lv := []int64{1, 2, 3, 4, 5, 6}, []int64{10, 20, 30, 40, 50, 60}
	left, err := data.NewBatch(ls, []*data.Column{
		data.NewFixed("k", dtype.Int64, lk, bitmap.AllSet(6)), data.NewFixed("l", dtype.Int64, lv, bitmap.AllSet(6))})
	if err != nil {
		t.Fatal(err)
	}
	right, err := data.NewBatch(rs, []*data.Column{
		data.NewFixed("k2", dtype.Int64, []int64{3, 4, 5}, bitmap.AllSet(3)),
		data.NewFixed("r", dtype.Int64, []int64{7, 8, 9}, bitmap.AllSet(3))})
	if err != nil {
		t.Fatal(err)
	}
	lsrc, _ := memsrc.New(ls, left)
	rsrc, _ := memsrc.New(rs, right)
	j := &plan.Join{Left: &plan.Scan{Src: lsrc, Full: ls}, Right: &plan.Scan{Src: rsrc, Full: rs},
		LeftOn: []expr.Node{&expr.Col{Name: "k"}}, RightOn: []expr.Node{&expr.Col{Name: "k2"}}, Kind: plan.JoinInner}
	layout, err := j.Layout()
	if err != nil {
		t.Fatal(err)
	}
	src := data.MustValues[int64](left.Column(1))
	for _, c := range []struct {
		lsel, rsel []int32
		shared     bool
	}{
		{[]int32{2, 3, 4}, []int32{0, 1, 2}, true},
		{[]int32{2, 4}, []int32{0, 2}, false},
	} {
		out, err := gatherOut(layout.Schema, layout, left, right, c.lsel, c.rsel, keyFromLeft)
		if err != nil {
			t.Fatal(err)
		}
		l, ok := out.ByName("l")
		if !ok {
			t.Fatal("no column l")
		}
		got := data.MustValues[int64](l)
		if shared := &got[0] == &src[c.lsel[0]]; shared != c.shared {
			t.Errorf("lsel %v: shared %v, want %v", c.lsel, shared, c.shared)
		}
		for i, r := range c.lsel {
			if got[i] != lv[r] {
				t.Errorf("lsel %v, row %d: %d, want %d", c.lsel, i, got[i], lv[r])
			}
		}
	}
}
