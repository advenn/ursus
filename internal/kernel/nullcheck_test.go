package kernel_test

import "github.com/advenn/ursus/internal/data"

// See data.CheckNonNullable. One line per test binary, run before any test starts.
func init() { data.CheckNonNullable = true }
