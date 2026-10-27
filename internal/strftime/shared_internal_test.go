package strftime

import (
	"fmt"
	"testing"
)

// TestAFormatIsCompiledOnce (step 399): a format is compiled once and shared; one that
// does not compile is refused each time and not kept; and past maxShared formats a
// new one is compiled each time, not kept, so its users' formats cannot grow the
// cache without end.
func TestAFormatIsCompiledOnce(t *testing.T) {
	a, err := Shared("%Y-%m-%d %H")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := Shared("%Y-%m-%d %H"); a != b {
		t.Error("a format compiled twice")
	}
	for range 2 {
		if _, err := Shared("%Q"); err == nil {
			t.Fatal("a format of no directive compiled")
		}
	}
	shared.mu.Lock()
	_, kept := shared.m["%Q"]
	shared.mu.Unlock()
	if kept {
		t.Error("a format that does not compile was kept")
	}
	for i := range 2 * maxShared {
		f, err := Shared(fmt.Sprintf("%%Y %d", i))
		if err != nil || f.String() != fmt.Sprintf("%%Y %d", i) {
			t.Fatalf("format %d: %v, %v", i, f, err)
		}
	}
	shared.mu.Lock()
	n := len(shared.m)
	shared.mu.Unlock()
	if n > maxShared {
		t.Errorf("%d formats kept, past %d", n, maxShared)
	}
}
