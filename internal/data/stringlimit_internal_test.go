package data

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/uerr"
)

// raisedBy runs build and returns the error it raised, recovered as every operator
// boundary recovers one.
func raisedBy(build func()) (err error) {
	defer uerr.Catch(&err, "test")
	build()
	return nil
}

// TestStringColumnsRefusePastTheirOffsets: a String column whose characters would
// pass what its 32-bit offsets hold is refused with a resource error, where it
// wrapped (audit.md S24). The limit is lowered to 1 KiB so the test can reach it.
func TestStringColumnsRefusePastTheirOffsets(t *testing.T) {
	defer func(old int64) { MaxStringBytes = old }(MaxStringBytes)
	MaxStringBytes = 1 << 10

	at := []string{strings.Repeat("x", 1000), strings.Repeat("y", 24)}
	past := append(at, "z")
	cases := []struct {
		name  string
		build func()
		fits  bool
	}{
		{"NewString at the limit builds", func() { NewString("s", at, bitmap.View{}) }, true},
		{"NewString past it is refused", func() { NewString("s", past, bitmap.View{}) }, false},
		{"NewStringParts at the limit builds", func() {
			NewStringParts("s", []int32{0, 1024}, make([]byte, 1024), bitmap.View{})
		}, true},
		{"NewStringParts past it is refused", func() {
			NewStringParts("s", []int32{0, 1025}, make([]byte, 1025), bitmap.View{})
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := raisedBy(tc.build)
			switch {
			case tc.fits && err != nil:
				t.Fatalf("refused a column that fits: %v", err)
			case !tc.fits && err == nil:
				t.Fatal("built a column past its offsets")
			case !tc.fits && !errors.Is(err, uerr.ErrResource):
				t.Fatalf("want a resource error, got %v", err)
			case !tc.fits && !strings.Contains(err.Error(), "CollectBatches"):
				t.Fatalf("the refusal does not say what to do: %v", err)
			}
		})
	}
}
