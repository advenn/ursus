//go:build amd64.v3

package ursus_test

// recordedBits says the recorded runs of testdata/canonicalnan hold here to the bit:
// they were written on amd64 at GOAMD64=v3, as the Makefile and CI build, where a
// float's a*b+c may fuse into one rounding. Elsewhere a skew's or an ewm's last bits
// may differ, and a run is held to its NaNs and its nulls.
const recordedBits = true
