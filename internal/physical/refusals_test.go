package physical

// The three Sink.Merge implementations that exist only to say no.
//
// They live here rather than in mergeinventory_test.go because that file is
// EXCLUDED from the inventory's own scan: it names every sink type, so leaving these
// there would let the inventory certify a type by merely having listed it. It did
// exactly that until the tooth for the check came back green.

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/uerr"
)

// TestRefusingSinksRefuse covers the three Merges that exist only to say no.
//
// windowSink, temporalSink and asOfBuildSink all refuse for one reason: two partial
// sinks assigned their own partition or group numbering independently, and folding
// them would attribute one partition's value to another. None of the three had any
// test at all — the refusals were as unexercised as reverseSink's implementation
// was, and an untested refusal is one statement away from becoming an untested
// implementation.
//
// Zero values are enough: none of these methods reads a field before refusing, and
// that is itself worth pinning — a refusal that starts dereferencing state is no
// longer a refusal.
func TestRefusingSinksRefuse(t *testing.T) {
	for _, c := range []struct {
		name string
		sink Sink
		want string
	}{
		{"windowSink", &windowSink{}, "remap"},
		{"temporalSink", &temporalSink{}, "cannot be merged"},
		{"asOfBuildSink", &asOfBuildSink{}, "cannot be merged"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Same type: still refused. This is the arm that matters — a merge of
			// two genuine partials is the case the operator would hit in
			// production, and it must fail loudly rather than silently fold.
			err := c.sink.Merge(c.sink)
			if err == nil {
				t.Fatalf("%s merged two partials; it must refuse", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the refusal should explain itself, got: %v", err)
			}
			if !errors.Is(err, uerr.ErrInternal) {
				t.Errorf("a merge nothing should have attempted is an ursus bug, "+
					"not a user error: %v", err)
			}

			// A different sink type: also refused, and not by accident.
			if err := c.sink.Merge(&reverseSink{}); err == nil {
				t.Errorf("%s merged a reverseSink", c.name)
			}
		})
	}
}
