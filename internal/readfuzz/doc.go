// Package readfuzz holds fuzz targets of ursus's public readers, and nothing else
// (step 508). Each hands the bytes the fuzzer makes to a public scan inside
// fuzzcheck.Run, which fails an input that hangs, allocates far past its size, leaves
// goroutines behind, answers ErrInternal or panics in ursus's code, with
// internal/data's boundary checks on.
//
// The targets run here, apart from the root package's tests, because Run's
// allocation and goroutine counts are the process's, and the root package runs its
// tests in parallel. Their seed corpora run in every CI leg as ordinary tests;
// .github/workflows/fuzz.yml fuzzes each for 30 seconds, and `make fuzz FUZZ=<target>
// PKG=./internal/readfuzz FUZZTIME=<d>` locally.
package readfuzz
