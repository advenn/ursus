package shrink

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/ipc"

	"github.com/advenn/ursus/qcheck/model"
)

// recordBytes is t's record as an Arrow IPC stream, which compares two tables to
// the bit, a NaN's payload and a zero's sign among them.
func recordBytes(t *testing.T, m *model.Table) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(m.Rec.Schema()))
	if err := w.Write(m.Rec); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// roundTrip is c through its file and back.
func roundTrip(t *testing.T, c *Case) *Case {
	t.Helper()
	b, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	back := &Case{}
	if err := json.Unmarshal(b, back); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	return back
}

// sameCase fails unless two cases hold the same tables to the bit and the same
// query to the character.
func sameCase(t *testing.T, what string, got, want *Case) {
	t.Helper()
	if got.Query.Go() != want.Query.Go() || got.Query.Count != want.Query.Count {
		t.Fatalf("%s: the query came back\n%s\nnot\n%s", what, got.Query.Go(), want.Query.Go())
	}
	if got.Signature != want.Signature || got.Config != want.Config || got.Mode != want.Mode || got.Seed != want.Seed {
		t.Fatalf("%s: came back as %+v, not %+v", what, got, want)
	}
	gm, err := got.Models()
	if err != nil {
		t.Fatal(err)
	}
	wm, err := want.Models()
	if err != nil {
		t.Fatal(err)
	}
	for i := range wm {
		if !bytes.Equal(recordBytes(t, gm[i]), recordBytes(t, wm[i])) {
			t.Fatalf("%s: table %s came back other than it went", what, wm[i].Name)
		}
	}
}

// TestACaseIsItsFile: a case written to its file and read back is the case, its
// tables to the bit and its query to the character, whether its tables are
// generated or held as rows: over generated cases, whose extreme values hold NaN,
// -0, the infinities, each integer's ends, NUL and Unicode in text; and a value of
// text that is not UTF-8.
func TestACaseIsItsFile(t *testing.T) {
	var seen struct{ nan, negZero, inf, minInt, nul bool }
	for seed := uint64(1); seed <= 120; seed++ {
		c := FromSeed(SourceSelf, seed, ModeDifferential, model.Reference(), model.Pairwise()[seed%21])
		c.Signature = Signature{Kind: KindMismatch, What: "cell", Op: "Min", Type: "Float64"}
		sameCase(t, "generated", roundTrip(t, c), c)

		held := *c
		held.Tables = nil
		for _, tb := range c.Tables {
			h, err := tb.Held()
			if err != nil {
				t.Fatal(err)
			}
			held.Tables = append(held.Tables, h)
			for _, col := range h.Cols {
				for _, v := range col.Values {
					switch {
					case v.Null:
					case col.Type == model.Float64 && math.IsNaN(v.F):
						seen.nan = true
					case col.Type == model.Float64 && v.F == 0 && math.Signbit(v.F):
						seen.negZero = true
					case col.Type == model.Float64 && math.IsInf(v.F, 0):
						seen.inf = true
					case col.Type == model.Int64 && v.I == math.MinInt64:
						seen.minInt = true
					case col.Type == model.String && bytes.IndexByte([]byte(v.S), 0) >= 0:
						seen.nul = true
					}
				}
			}
		}
		sameCase(t, "held", roundTrip(t, &held), &held)
	}
	if !seen.nan || !seen.negZero || !seen.inf || !seen.minInt || !seen.nul {
		t.Fatalf("the cases held no value of some extreme: %+v", seen)
	}

	// Text that is not UTF-8 is held as its hex.
	c := FromSeed(SourceSelf, 7, ModeRun, model.Reference(), model.Reference())
	for i, tb := range c.Tables {
		h, err := tb.Held()
		if err != nil {
			t.Fatal(err)
		}
		h.Cols = append(h.Cols, Column{Name: "strz", Type: model.String, Values: make([]Value, h.Rows)})
		for r := range h.Rows {
			h.Cols[len(h.Cols)-1].Values[r] = Value{S: string([]byte{0xff, byte(r), 'x'})}
		}
		c.Tables[i] = h
	}
	sameCase(t, "not UTF-8", roundTrip(t, c), c)
}
