package ursus

import (
	"math"
	"os"
	"runtime/debug"
	"sync"
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

// TestTheDefaultBudgetSetsTheProcessMemoryLimit: the first query under the default
// budget sets the Go soft limit to nine tenths of the ceiling, once (step 128). A
// limit the caller gave, LeaveProcessMemoryLimit, GOMEMLIMIT and a limit already
// chosen all keep it from doing so.
func TestTheDefaultBudgetSetsTheProcessMemoryLimit(t *testing.T) {
	if _, set := os.LookupEnv("GOMEMLIMIT"); set {
		t.Skip("GOMEMLIMIT is set in this environment")
	}
	restoreLimit := debug.SetMemoryLimit(-1)
	restoreCeiling, restoreDefault := processCeiling, defaultMemoryLimit
	t.Cleanup(func() {
		debug.SetMemoryLimit(restoreLimit)
		processCeiling, defaultMemoryLimit = restoreCeiling, restoreDefault
		autoLimitOnce, _ = sync.Once{}, autoLimitOff.Swap(false)
	})
	processCeiling = func() int64 { return 8 << 30 }
	defaultMemoryLimit = func() int64 { return 4 << 30 }
	fresh := func() {
		debug.SetMemoryLimit(math.MaxInt64)
		autoLimitOnce = sync.Once{}
		autoLimitOff.Store(false)
	}
	query := func(t *testing.T, opts ...CollectOption) {
		t.Helper()
		if _, err := Frame(Values("x", []int64{1, 2})).Collect(t.Context(), opts...); err != nil {
			t.Fatal(err)
		}
	}
	want := int64(8<<30) / 10 * 9

	t.Run("the default budget sets it", func(t *testing.T) {
		fresh()
		query(t)
		if got := debug.SetMemoryLimit(-1); got != want {
			t.Errorf("the runtime's limit is %d, want %d", got, want)
		}
	})
	t.Run("once: a limit changed afterwards is not set back", func(t *testing.T) {
		fresh()
		query(t)
		debug.SetMemoryLimit(2 << 30)
		query(t)
		if got := debug.SetMemoryLimit(-1); got != 2<<30 {
			t.Errorf("the runtime's limit is %d, want the later %d", got, int64(2<<30))
		}
	})
	t.Run("a limit the caller gave leaves it alone", func(t *testing.T) {
		fresh()
		query(t, WithMemoryLimit(1<<30))
		if got := debug.SetMemoryLimit(-1); got != math.MaxInt64 {
			t.Errorf("the runtime's limit is %d, want none", got)
		}
	})
	t.Run("LeaveProcessMemoryLimit leaves it alone", func(t *testing.T) {
		fresh()
		LeaveProcessMemoryLimit()
		query(t)
		if got := debug.SetMemoryLimit(-1); got != math.MaxInt64 {
			t.Errorf("the runtime's limit is %d, want none", got)
		}
	})
	t.Run("GOMEMLIMIT=off leaves it alone", func(t *testing.T) {
		fresh()
		t.Setenv("GOMEMLIMIT", "off")
		query(t)
		if got := debug.SetMemoryLimit(-1); got != math.MaxInt64 {
			t.Errorf("the runtime's limit is %d, want none", got)
		}
	})
	t.Run("a limit already chosen is kept", func(t *testing.T) {
		fresh()
		debug.SetMemoryLimit(3 << 30)
		query(t)
		if got := debug.SetMemoryLimit(-1); got != 3<<30 {
			t.Errorf("the runtime's limit is %d, want the existing %d", got, int64(3<<30))
		}
	})
}
