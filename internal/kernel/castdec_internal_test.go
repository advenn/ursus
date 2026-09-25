package kernel

import (
	"math/big"
	"testing"
)

// TestPow10IsEveryPowerOfTen checks the table every Decimal bound and rescale reads,
// against math/big: pow10 is built by repeated mul10, so one wrong step would make
// every later entry wrong.
func TestPow10IsEveryPowerOfTen(t *testing.T) {
	want := big.NewInt(1)
	for k, got := range pow10 {
		if got.String() != want.String() {
			t.Errorf("pow10[%d] = %s, want %s", k, got, want)
		}
		want.Mul(want, big.NewInt(10))
	}
}
