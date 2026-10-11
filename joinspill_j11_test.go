package ursus_test

// Step 180, audit row J11: a spilled join refused with "a single join key has more
// build rows than the limit" when no key had more than two rows. The cause, found:
// it refused at a fifth level on the depth alone, and under a limit smaller than its
// own spill writers, 8 KiB each, every sub-join is over the limit whatever its keys.

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// j11KeyA and j11KeyB share a spill bucket at each of the first five levels, and part
// at the sixth: internal/physical's TestJ11KeysShareFiveLevels keeps them so.
const j11KeyA, j11KeyB = 1, 248238

func j11Join(t *testing.T, keys []int64, rowBytes int, limit int64) (*ursus.DataFrame, error) {
	t.Helper()
	c := ursus.Col
	w := make([]string, len(keys))
	for i := range w {
		w[i] = strings.Repeat("w", rowBytes)
	}
	build := ursus.Frame(ursus.Values("k", keys), ursus.Values("w", w))
	probe := ursus.Frame(ursus.Values("k", []int64{j11KeyA, j11KeyB, 7}))
	// A left join builds its right side, the wide one, whatever the sides' sizes.
	return probe.Join(build, ursus.JoinOn(c("k")), ursus.JoinHow(ursus.JoinLeft)).Collect(t.Context(), ursus.WithMemoryLimit(limit),
		ursus.WithSpillDir(t.TempDir()), ursus.WithThreads(1), ursus.WithBatchSize(64))
}

// TestASpilledJoinDividesPastFourLevels: two keys of 24 KiB each, 48 together, under a
// 40 KiB limit, sharing every bucket to the fifth level. Each fits alone, so the
// sixth level divides them and the join answers; it was refused at the fifth.
func TestASpilledJoinDividesPastFourLevels(t *testing.T) {
	keys := make([]int64, 120)
	for i := range keys {
		keys[i] = j11KeyA
		if i%2 == 1 {
			keys[i] = j11KeyB
		}
	}
	df, err := j11Join(t, keys, 400, 40<<10)
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 121 { // 60 + 60 matches, and key 7 unmatched
		t.Fatalf("%d rows, want 121", df.Height())
	}
}

// TestASpilledJoinNamesWhatIsOverTheLimit: one key's rows over the limit is that key's
// refusal; a limit below the join's own spill writers is theirs, where it was the
// key's, about keys of two rows.
func TestASpilledJoinNamesWhatIsOverTheLimit(t *testing.T) {
	hot := make([]int64, 200)
	for i := range hot {
		hot[i] = j11KeyA
	}
	_, err := j11Join(t, hot, 400, 40<<10)
	if err == nil || !errors.Is(err, ursus.ErrResource) || !strings.Contains(err.Error(), "a single join key") {
		t.Errorf("one key of 80 KiB under 40: %v", err)
	}

	pairs := make([]int64, 20_000)
	for i := range pairs {
		pairs[i] = int64(i / 2)
	}
	_, err = j11Join(t, pairs, 400, 4<<10)
	if err == nil || !errors.Is(err, ursus.ErrResource) || !strings.Contains(err.Error(), "spill writers") ||
		strings.Contains(err.Error(), "single join key") {
		t.Errorf("keys of two rows under 4 KiB: %v", err)
	}
}
