package kernel

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// TestPadRefusesWhatAStringColumnCannotHold: a pad is refused before it allocates
// when its output would pass maxStringBytes, and the bytes are counted exactly, so a
// pad that fits is never refused (audit.md S22). The limit is lowered to 4 KiB so
// that what is refused here is small enough to allocate if the bound were gone.
func TestPadRefusesWhatAStringColumnCannotHold(t *testing.T) {
	defer func(old int64) { maxStringBytes = old }(maxStringBytes)
	maxStringBytes = 4 << 10

	// Three strings and a null, which pads to nothing.
	c := data.NewString("s", []string{"ab", "", "-7", "x"},
		func() bitmap.View {
			b := bitmap.NewBuilder(4)
			for _, v := range []bool{true, true, true, false} {
				b.Append(v)
			}
			return b.Finish()
		}())
	cases := []struct {
		name string
		fn   expr.CallFn
		args []any
		fits bool
	}{
		{"three rows to 1000 bytes fit", expr.FnStrPadStart, []any{int64(1000), " "}, true},
		{"three rows to 1366 bytes do not", expr.FnStrPadEnd, []any{int64(1366), " "}, false},
		// A three-byte fill triples the padding: 500 runes of € is 1500 bytes a row.
		{"a wide fill is counted at its width", expr.FnStrPadStart, []any{int64(500), "€"}, false},
		{"ZFill to 1365 fits", expr.FnStrZFill, []any{int64(1365)}, true},
		{"ZFill to 1400 does not", expr.FnStrZFill, []any{int64(1400)}, false},
		{"a width past int64 arithmetic is refused, not wrapped", expr.FnStrPadStart,
			[]any{int64(1) << 62, "€"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := StrCall(tc.fn, "s", dtype.String, c, tc.args, nil)
			switch {
			case tc.fits && err != nil:
				t.Fatalf("refused a pad that fits: %v", err)
			case tc.fits && out.Strings().Get(0) == "ab":
				t.Fatal("answered without padding")
			case !tc.fits && err == nil:
				t.Fatalf("padded %d bytes past the %d a column holds",
					len(out.RawChars()), maxStringBytes)
			case !tc.fits && !errors.Is(err, uerr.ErrValue):
				t.Fatalf("want a value error, got %v", err)
			case !tc.fits && !strings.Contains(err.Error(), "width"):
				t.Fatalf("the refusal does not name the width: %v", err)
			}
		})
	}
}
