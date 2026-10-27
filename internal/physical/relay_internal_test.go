package physical

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// TestAPhysicalPiecesPanicReachesTheCaller (step 393): a panic in one of a sort's
// pieces, a unique's ranges or its parts, each on a goroutine of its own, is raised
// again in the caller as an internal error, where it ended the process.
func TestAPhysicalPiecesPanicReachesTheCaller(t *testing.T) {
	d := newUniqueKeys("unique", dtype.MustSchema(dtype.Of("k", dtype.Int64)), []int{0}, 4)
	for name, run := range map[string]func(){
		"eachPiece": func() {
			eachPiece(1<<17, 4, func(lo, _ int) {
				if lo > 0 {
					panic("piece boom")
				}
			})
		},
		"eachRange": func() {
			eachRange(100, 4, func(w, _, _ int) {
				if w == 2 {
					panic("piece boom")
				}
			})
		},
		"eachPart": func() {
			d.eachPart(4, func(p int) {
				if p == 1 {
					panic("piece boom")
				}
			})
		},
	} {
		err := uerr.GuardErr("unique", func() error { run(); return nil })
		if !errors.Is(err, uerr.ErrInternal) || !strings.Contains(err.Error(), "piece boom") {
			t.Fatalf("%s: want the piece's panic as an internal error, got %v", name, err)
		}
	}
}
