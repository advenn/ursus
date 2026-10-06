package ursus

import (
	"math"
	"os"
	"runtime/debug"
	"testing"
)

// TestSetProcessMemoryLimit: nine tenths of the ceiling when nothing has chosen a
// limit, and whatever was chosen otherwise.
func TestSetProcessMemoryLimit(t *testing.T) {
	if _, set := os.LookupEnv("GOMEMLIMIT"); set {
		t.Skip("GOMEMLIMIT is set in this environment")
	}
	restoreLimit := debug.SetMemoryLimit(-1)
	restoreCeiling := processCeiling
	t.Cleanup(func() {
		debug.SetMemoryLimit(restoreLimit)
		processCeiling = restoreCeiling
	})
	ceiling := func(n int64) { processCeiling = func() int64 { return n } }

	t.Run("nothing chosen: nine tenths of the ceiling", func(t *testing.T) {
		debug.SetMemoryLimit(math.MaxInt64)
		ceiling(8 << 30)
		want := int64(8<<30) / 10 * 9
		if got := SetProcessMemoryLimit(); got != want {
			t.Errorf("returned %d, want %d", got, want)
		}
		if got := debug.SetMemoryLimit(-1); got != want {
			t.Errorf("the runtime's limit is %d, want %d", got, want)
		}
	})
	t.Run("a limit already set is kept", func(t *testing.T) {
		debug.SetMemoryLimit(3 << 30)
		ceiling(8 << 30)
		if got := SetProcessMemoryLimit(); got != 3<<30 {
			t.Errorf("returned %d, want the existing %d", got, int64(3<<30))
		}
		if got := debug.SetMemoryLimit(-1); got != 3<<30 {
			t.Errorf("the runtime's limit is %d, want the existing %d", got, int64(3<<30))
		}
	})
	t.Run("GOMEMLIMIT in the environment is kept, off included", func(t *testing.T) {
		t.Setenv("GOMEMLIMIT", "off")
		debug.SetMemoryLimit(math.MaxInt64)
		ceiling(8 << 30)
		if got := SetProcessMemoryLimit(); got != math.MaxInt64 {
			t.Errorf("returned %d, want the environment's none", got)
		}
		if got := debug.SetMemoryLimit(-1); got != math.MaxInt64 {
			t.Errorf("the runtime's limit is %d, want the environment's none", got)
		}
	})
	t.Run("no ceiling readable: nothing set", func(t *testing.T) {
		debug.SetMemoryLimit(math.MaxInt64)
		ceiling(0)
		if got := SetProcessMemoryLimit(); got != math.MaxInt64 {
			t.Errorf("returned %d, want none", got)
		}
		if got := debug.SetMemoryLimit(-1); got != math.MaxInt64 {
			t.Errorf("the runtime's limit is %d, want none", got)
		}
	})
}
