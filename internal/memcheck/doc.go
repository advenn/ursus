// Package memcheck holds tests, and nothing else, that measure a query's live heap
// against what its memory budget counts.
//
// They are their own package so they run alone in their own process: the heap is
// the process's, and the root package runs its tests in parallel.
package memcheck
