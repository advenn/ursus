package physical

// The window ceiling is query-wide.
//
// Finish generates a grid per categorical bucket, so a ceiling that reset per call
// would let N buckets build N times it — a limit that scales with cardinality is
// not a limit. Proving that end to end would mean building sixteen million windows
// to reach the unlimited ceiling, so the arithmetic is asserted here instead and
// the public test covers the same property through the memory limit, cheaply.

import "testing"

func TestMaxWindowsIsQueryWide(t *testing.T) {
	var s temporalSink

	if got := s.maxWindows(); got != defaultMaxWindows {
		t.Errorf("a fresh sink allows %d windows, want %d", got, defaultMaxWindows)
	}

	// What earlier buckets built comes off the ceiling.
	s.gridPoints = defaultMaxWindows / 4
	if got, want := s.maxWindows(), defaultMaxWindows-defaultMaxWindows/4; got != want {
		t.Errorf("after %d points the ceiling is %d, want %d", s.gridPoints, got, want)
	}

	// And it floors at zero rather than going negative, which would read as an
	// enormous ceiling to a caller comparing against it.
	s.gridPoints = defaultMaxWindows * 2
	if got := s.maxWindows(); got != 0 {
		t.Errorf("an exhausted ceiling is %d, want 0", got)
	}
}
