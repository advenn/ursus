package arrowsrc

import (
	"errors"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/bitutil"

	"github.com/advenn/ursus/internal/uerr"
)

// TestAPanicIsWhoseCodeRaisedIt (step 428): a panic recovered around the caller's
// factory or reader was KindIO whoever raised it. The innermost frame outside Go's
// standard library decides: ursus's own is internal, the caller's or arrow-go's is
// the stream's I/O failure. The schema's probe, recovering in its own Once, decides
// alike.
func TestAPanicIsWhoseCodeRaisedIt(t *testing.T) {
	for _, c := range []struct {
		name     string
		f        func() error
		internal bool
	}{
		{"ursus's own", func() error {
			var m map[string]int
			m["x"] = 1
			return nil
		}, true},
		{"the standard library, called by ursus", func() error {
			_ = strings.Repeat("x", -1)
			return nil
		}, true},
		{"arrow-go", func() error {
			_ = bitutil.BitIsSet(nil, 5)
			return nil
		}, false},
	} {
		err := callerCode("reading", c.f)
		if err == nil {
			t.Fatalf("%s: no error", c.name)
		}
		if got := errors.Is(err, uerr.ErrInternal); got != c.internal {
			t.Errorf("%s: %v, internal %v, want %v", c.name, err, got, c.internal)
		}
		if !c.internal && (!errors.Is(err, uerr.ErrIO) || !strings.Contains(err.Error(), "reading the Arrow stream panicked")) {
			t.Errorf("%s: %v, want the stream's I/O failure", c.name, err)
		}

		// The schema's probe recovers in its own Once, and decides alike.
		_, err = New(func() (array.RecordReader, error) { return nil, c.f() }).Schema(t.Context())
		if err == nil || errors.Is(err, uerr.ErrInternal) != c.internal {
			t.Errorf("%s, opening for the schema: %v, internal %v", c.name, err, c.internal)
		}
	}

	for fn, want := range map[string]owner{
		"github.com/advenn/ursus/internal/source/arrowsrc.callerCode": ownedByUrsus,
		"github.com/advenn/ursus.openIPCFile":                         ownedByUrsus,
		"github.com/advenn/ursus/internal/arrowin.Schema[...]":        ownedByUrsus,
		"github.com/advenn/ursus_test.TestScan.func1":                 ownedByOthers,
		"github.com/advenn/ursusx.(*Reader).Next":                     ownedByOthers,
		"example.com/app/feed.(*Reader).Next":                         ownedByOthers,
		"main.(*reader).Next":                                         ownedByOthers,
		"main.main.func1":                                             ownedByOthers,
		"github.com/apache/arrow-go/v18/arrow/ipc.(*Reader).next":     ownedByOthers,
		"strings.Repeat":                                              ownedByGo,
		"encoding/json.(*decodeState).object":                         ownedByGo,
		"internal/bytealg.IndexByte":                                  ownedByGo,
	} {
		if got := ownerOf(fn); got != want {
			t.Errorf("ownerOf(%q) = %d, want %d", fn, got, want)
		}
	}
}
