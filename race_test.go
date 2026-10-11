//go:build race

package ursus_test

// raceDetector is whether the race detector is built in. It slows a test about
// tenfold, so a test that measures memory over a large input skips that part under
// it: the budget's bound is not a race, and the root package's run reached its ten
// minutes (step 210).
const raceDetector = true
