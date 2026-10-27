package kernel

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/uerr"
)

// TestAPiecesPanicReachesTheCaller (step 393): a panic in one of ranges' or a
// spreader's pieces, on a goroutine of its own, is raised again in the caller, an
// internal error at the caller's recovery naming the piece's site, where it ended the
// process.
func TestAPiecesPanicReachesTheCaller(t *testing.T) {
	pieceBoom := func(lo int) {
		if lo > 0 {
			panic("piece boom")
		}
	}
	for name, run := range map[string]func(){
		"ranges":   func() { ranges(1000, 4, func(_, lo, _ int) { pieceBoom(lo) }) },
		"spreader": func() { spreader(4)(1<<17, func(lo, _ int) { pieceBoom(lo) }) },
	} {
		err := uerr.GuardErr("sort", func() error { run(); return nil })
		if !errors.Is(err, uerr.ErrInternal) || !strings.Contains(err.Error(), "piece boom") {
			t.Fatalf("%s: want the piece's panic as an internal error, got %v", name, err)
		}
		var p *uerr.PanicError
		if !errors.As(err, &p) || !strings.Contains(p.Site, "TestAPiecesPanicReachesTheCaller") {
			t.Fatalf("%s: the site is not the piece's: %+v", name, p)
		}
	}
}
