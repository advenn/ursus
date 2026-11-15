package parquet

import (
	"errors"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// TestAColumnWithNoReaderIsRefusedWhateverItsType (step 521): a physical type the
// format does not define, which only a corrupt footer gives a column, has no column
// reader. FuzzOwnPagesMatchArrow's mutation of pyarrow_null_column.parquet gave one
// to its Null column, and that was an internal error, "has no level reader", where a
// column of any other type with no reader is refused as unsupported. A Null column
// is refused now as an Int32 column is, by the same error but for its type.
func TestAColumnWithNoReaderIsRefusedWhateverItsType(t *testing.T) {
	desc := schema.NewColumn(schema.NewInt32Node("c", parquet.Repetitions.Optional, -1), 1, 0)
	_, want := newColReader(nil, desc, dtype.Int32)
	if want == nil || errors.Is(want, uerr.ErrInternal) {
		t.Fatalf("an Int32 column with no reader: %v, want a refusal", want)
	}
	for _, dt := range []dtype.DataType{dtype.Null, dtype.String, dtype.Float64} {
		_, err := newColReader(nil, desc, dt)
		switch {
		case err == nil || errors.Is(err, uerr.ErrInternal):
			t.Errorf("a %s column with no reader: %v, want a refusal", dt, err)
		case !errors.Is(err, uerr.ErrUnsupported) || !errors.Is(want, uerr.ErrUnsupported):
			t.Errorf("a %s column with no reader: %v, and an Int32's %v, not both unsupported", dt, err, want)
		case err.Error() != strings.Replace(want.Error(), dtype.Int32.String(), dt.String(), 1):
			t.Errorf("a %s column with no reader: %q, and an Int32's %q", dt, err, want)
		}
	}
}
