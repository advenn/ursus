//go:build amd64.v3 && !race

package ursus_test

// recordedBits says the recorded runs of testdata/canonicalnan hold here to the bit:
// they were written on amd64 at GOAMD64=v3 without the race detector, as the Makefile
// and CI's test legs build, where a float's a*b+c may fuse into one rounding. At
// another level, or under -race, whose instrumentation leaves some of those products
// apart (CI's race job: a Var's, a Std's and a correlation's last bit), a run is held
// to its NaNs and its nulls.
const recordedBits = true
