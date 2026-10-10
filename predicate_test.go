package ursus_test

// Step 164: a filter of Ands, split into its conjuncts and combined a word at a
// time, keeps the rows Kleene logic keeps: those where the whole predicate is true.

import (
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus"
)

func TestAFilterOfAndsKeepsWhatKleeneKeeps(t *testing.T) {
	c := ursus.Col
	rng := rand.New(rand.NewPCG(164, 4))
	const n = 5_000
	x := make([]int64, n)
	xok := make([]bool, n)
	b := make([]bool, n)
	bok := make([]bool, n)
	seq := make([]int64, n)
	for i := range n {
		x[i], xok[i] = int64(rng.IntN(100)), rng.IntN(9) != 0
		b[i], bok[i] = rng.IntN(2) == 0, rng.IntN(7) != 0
		seq[i] = int64(i)
	}
	frame := ursus.Frame(ursus.ValuesNullable("x", x, xok), ursus.ValuesNullable("b", b, bok),
		ursus.Values("seq", seq))
	// (x in [20, 60) AND b) AND (x != 33 OR b): nested Ands, an IsBetween, and an Or
	// that must stay whole.
	pred := c("x").IsBetween(int64(20), int64(60), ursus.ClosedLeft).And(c("b")).
		And(c("x").Ne(int64(33)).Or(c("b")))
	got, err := frame.Filter(pred).Select(c("seq")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Kept where the predicate is TRUE: a null anywhere it decides drops the row.
	var want []int64
	for i := range n {
		between := xok[i] && x[i] >= 20 && x[i] < 60
		neq := xok[i] && x[i] != 33
		left := between && bok[i] && b[i]
		right := neq || (bok[i] && b[i])
		if left && right {
			want = append(want, int64(i))
		}
	}
	vals := int64Col(t, got, "seq")
	if len(vals) != len(want) {
		t.Fatalf("kept %d rows, want %d", len(vals), len(want))
	}
	for i, w := range want {
		if vals[i] != w {
			t.Fatalf("row %d is %d, want %d", i, vals[i], w)
		}
	}
}
