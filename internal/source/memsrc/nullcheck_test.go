package memsrc_test

import "github.com/advenn/ursus/internal/data"

// See data.CheckNonNullable. One line per test binary, run before any test starts.
//
// memsrc was the one batch-constructing package that omitted this. It matters more
// here than almost anywhere: memsrc.FromBatch is what step 49 named as producing one
// of the two legal shapes — a NULLABLE column holding no nulls, which the whole
// design goes out of its way to preserve — so this package's tests are exactly where
// a regression in that distinction would show.
func init() { data.CheckNonNullable = true }
