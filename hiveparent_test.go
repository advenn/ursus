package ursus_test

// Step 420: a partition path with a parent directory component is refused, as Polars
// 1.44.1 refuses it, where `..\..\x` escaped the dataset's directory on Windows.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestAPartitionPathHoldsNoParentDirectory: hive.Escape leaves `\` as it is, so a key
// value of `..\..\..\x`, or a key so named, made a segment Windows splits into `..`s,
// and the files were written outside the dataset. Each is refused, an ErrValue
// naming the '..', on every system, as Polars refuses it; `a\b` and `..` alone, which
// make one segment, are written, as Polars writes them.
func TestAPartitionPathHoldsNoParentDirectory(t *testing.T) {
	for _, c := range []struct {
		name, key, value string
	}{
		{"a value", "k", `..\..\..\x`},
		{"a key's name", `..\..\x`, "v"},
		{"a value of a second key", "k", `y\..\..`},
	} {
		dir := filepath.Join(t.TempDir(), "out")
		lf := ursus.Frame(ursus.Values(c.key, []string{c.value}), ursus.Values("x", []int64{1}))
		err := lf.SinkParquet(t.Context(), dir, ursus.WithPartitionBy(c.key))
		if err == nil || !errors.Is(err, ursus.ErrValue) || !strings.Contains(err.Error(), "'..'") {
			t.Errorf("%s: got %v, want the path refused for its '..'", c.name, err)
		}
	}
	for _, v := range []string{`a\b`, ".."} {
		dir := filepath.Join(t.TempDir(), "out")
		lf := ursus.Frame(ursus.Values("k", []string{v}), ursus.Values("x", []int64{1}))
		if err := lf.SinkParquet(t.Context(), dir, ursus.WithPartitionBy("k")); err != nil {
			t.Errorf("%q: %v", v, err)
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "k="+v, "00000000.parquet")); err != nil {
			t.Errorf("%q: %v", v, err)
		}
	}
}
