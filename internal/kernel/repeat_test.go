package kernel

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// TestRepeaterSlicesOneRepeat is step 359's: a value repeated for batches of several
// sizes, a String's and an Int64's and a null, each batch's rows the value.
func TestRepeaterSlicesOneRepeat(t *testing.T) {
	str := data.NewString("p", []string{"dir/k=1/x.arrow"}, bitmap.AllSet(1))
	other := data.NewString("p", []string{"y"}, bitmap.AllSet(1))
	num := data.NewFixed("k", dtype.Int64, []int64{7}, bitmap.AllSet(1))
	null := data.NewString("p", []string{""}, bitmap.Zeros(1))
	var r Repeater
	for _, tc := range []struct {
		v    *data.Column
		n    int
		want string
	}{
		{str, 3, "dir/k=1/x.arrow"}, {str, 1, "dir/k=1/x.arrow"}, {str, 5, "dir/k=1/x.arrow"}, {str, 0, ""},
		{other, 2, "y"}, {str, 2, "dir/k=1/x.arrow"}, {num, 4, "7"}, {null, 3, "<nil>"},
	} {
		got, err := r.Repeat(tc.v, tc.n)
		if err != nil {
			t.Fatal(err)
		}
		if got.Len() != tc.n || got.DType() != tc.v.DType() || got.Name() != tc.v.Name() {
			t.Fatalf("%v ×%d: %d rows of %s named %s", tc.want, tc.n, got.Len(), got.DType(), got.Name())
		}
		for i := range tc.n {
			var s string
			switch {
			case !got.IsValid(i):
				s = "<nil>"
			case got.DType() == dtype.String:
				s = got.Strings().Get(i)
			default:
				s = "7"
				if data.MustValues[int64](got)[i] != 7 {
					s = "not 7"
				}
			}
			if s != tc.want {
				t.Errorf("%v ×%d: row %d is %q", tc.want, tc.n, i, s)
			}
		}
	}
}
