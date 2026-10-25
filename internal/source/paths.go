package source

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// WithPathColumn is schema with a String column name after its others, each row's
// file path, as Polars' include_file_paths (step 359); op names the scan in the
// refusal of a name the files' columns have already, Polars' "column name for file
// paths conflicts with column name from file".
func WithPathColumn(op string, schema *dtype.Schema, name string) (*dtype.Schema, error) {
	if schema.IndexOf(name) >= 0 {
		return nil, uerr.New(uerr.KindSchema, op,
			"the file paths' column %q is a column of the files already", name)
	}
	return dtype.NewSchema(append(schema.FieldSlice(), dtype.Field{Name: name, Type: dtype.String})...)
}

// WithColumn is b, of schema's columns but the one at pos, with col inserted there.
func WithColumn(schema *dtype.Schema, pos int, b *data.Batch, col *data.Column) (*data.Batch, error) {
	cols := make([]*data.Column, 0, schema.Len())
	cols = append(cols, b.Columns()[:pos]...)
	cols = append(cols, col)
	cols = append(cols, b.Columns()[pos:]...)
	return data.NewBatch(schema, cols)
}
