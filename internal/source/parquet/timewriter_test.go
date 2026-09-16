package parquet

// The TIME(MILLIS) narrowing guard, aimed.
//
// Step 57 wraps a Time at both of its producers, so after it the value written here
// always fits and the guard in writeInt32 is a PROOF rather than a repair. That makes
// it unreachable from any ordinary query — and unreachable code with no test is how a
// guard rots.
//
// So this reaches it the only way left: by turning data.CheckTimeRange off and
// building the column directly, which is exactly the shape a future source could
// produce. The bare `int32(v)` it replaced is the construct step 49 removed from
// rescaleTemporal, where it was "a silently wrapped date"; here it also wrote an
// out-of-spec TIME(MILLIS), whose Parquet range is 0..86_399_999.

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

func TestTimeMillisWriterRefusesAnOutOfRangeTick(t *testing.T) {
	// The batch constructor would reject this first, which is the point of the
	// invariant — so the check is stood down for exactly as long as it takes to build
	// a column no producer in the engine can make any more.
	data.CheckTimeRange = false
	defer func() { data.CheckTimeRange = true }()

	dt := dtype.Time(dtype.Milli)
	schema := dtype.MustSchema(dtype.Of("t", dt))

	for _, c := range []struct {
		name  string
		ticks []int64
	}{
		{"exactly midnight tomorrow", []int64{0, 86_400_000}},
		{"negative", []int64{0, -1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			col := data.NewFixed("t", dt, c.ticks, bitmap.AllSet(len(c.ticks)))
			b, err := data.NewBatch(schema, []*data.Column{col})
			if err != nil {
				t.Fatal(err)
			}

			var buf bytes.Buffer
			w, err := NewWriter(&buf, schema, DefaultWriteOptions())
			if err != nil {
				t.Fatal(err)
			}
			err = w.WriteBatch(b)
			if err == nil {
				t.Fatal("the writer accepted a tick outside [0, 24h) and would have " +
					"emitted an out-of-spec TIME(MILLIS)")
			}
			if !strings.Contains(err.Error(), "outside [0, 24h)") {
				t.Errorf("the error should say what is wrong: %v", err)
			}
			if errors.Is(err, uerr.ErrInternal) {
				t.Errorf("an unrepresentable value is a user error, not an ursus "+
					"bug: %v", err)
			}
		})
	}
}

// TestTimeMillisWriterAcceptsTheBoundary is the other side, so the guard cannot be
// satisfied by refusing everything: 86_399_999 is the last millisecond of the day and
// must be written.
func TestTimeMillisWriterAcceptsTheBoundary(t *testing.T) {
	dt := dtype.Time(dtype.Milli)
	schema := dtype.MustSchema(dtype.Of("t", dt))
	col := data.NewFixed("t", dt, []int64{0, 86_399_999}, bitmap.AllSet(2))
	b, err := data.NewBatch(schema, []*data.Column{col})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	w, err := NewWriter(&buf, schema, DefaultWriteOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteBatch(b); err != nil {
		t.Fatalf("midnight and the last millisecond of the day are both times of "+
			"day: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Error("nothing was written")
	}
}
