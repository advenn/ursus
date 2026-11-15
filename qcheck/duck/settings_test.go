//go:build duckdb

package duck

import (
	"slices"
	"testing"

	"github.com/advenn/ursus/qcheck/shrink"
)

// TestEmittedSettingsAreDuckDBs: the settings an emitted script sets are the ones a
// DB sets, which only a build with DuckDB has.
func TestEmittedSettingsAreDuckDBs(t *testing.T) {
	if !slices.Equal(shrink.SQLSettings, Settings) {
		t.Fatalf("shrink.SQLSettings %q, duck.Settings %q", shrink.SQLSettings, Settings)
	}
}
