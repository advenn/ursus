package physical

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// TestOneKeyOverNamesTheOtherOperators (step 426): a join's sub-sink at the deepest
// split, holding one key, refuses naming what is over the query's limit. With the
// key's rows over it, the key; with its own spill writers, them; with neither, the
// other operators, whose bytes it names. That third case blamed "a single join key"
// about a key of two rows.
func TestOneKeyOverNamesTheOtherOperators(t *testing.T) {
	const kib = 1 << 10
	for _, c := range []struct {
		name         string
		others, rows int64
		writers      int
		want, not    string
	}{
		{"the key's rows over the limit", 0, 80 * kib, 0, "a single join key", "other operators"},
		{"its spill writers over it", 0, 4 * kib, 16, "spill writers", "single join key"},
		{"the other operators over it", 60 * kib, 8 * kib, 0, "60.0KiB of it in its other operators", "single join key"},
		{"the other operators over it, writers open", 60 * kib, 2 * kib, 1, "other operators", "single join key"},
	} {
		bud := execopt.NewBudget(64*kib, t.TempDir())
		bud.Account("sort").RetainBytes(c.others)
		s := &joinBuildSink{level: maxSpillDepth, ids: kernel.NewKeyTable(), mem: bud.Account("join"),
			budget: bud, bparts: make([]*partWriter, nBuckets)}
		s.ids.GetOrInsert([]byte("k"))
		for i := range c.writers {
			s.bparts[i] = &partWriter{}
		}
		s.mem.RetainBytes(c.rows + int64(c.writers)*partBufBytes)
		err := s.oneKeyOver()
		if err == nil || !errors.Is(err, uerr.ErrResource) || !strings.Contains(err.Error(), c.want) ||
			strings.Contains(err.Error(), c.not) {
			t.Errorf("%s: %v, want a resource error naming %q and not %q", c.name, err, c.want, c.not)
		}
	}
}
