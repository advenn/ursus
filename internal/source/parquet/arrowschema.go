package parquet

import (
	"bytes"
	"encoding/base64"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/parquet/metadata"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowout"
	"github.com/advenn/ursus/internal/uerr"
)

// An Array in Parquet (step 341).
//
// Parquet has no fixed-size list: Polars and PyArrow write an Array as a LIST, and
// say what it was in the footer, under "ARROW:schema", the Arrow schema of the file
// as an IPC stream of no batch, in base64. They read it back as an Array from there,
// and so does ursus: a top-level LIST that schema calls a FixedSizeList is an Array
// of its size, each of its rows checked to have it as it is read (listCol.closeRow).
// ursus writes the key when a column is an Array, so Polars reads one back as it
// wrote it, and leaves every other file's footer as it was.
//
// The key is decoded with the IPC reader alone: arrow-go's own helpers live in its
// flight package, which this reader does not take on, for the reason types.go gives
// for pqarrow.

// arrowSchemaKey is the footer key Arrow's writers keep their schema under.
const arrowSchemaKey = "ARROW:schema"

// arraysOf is the size of each top-level column the footer's Arrow schema calls a
// FixedSizeList, by name; nil without one. A schema that does not decode is passed
// over: the Parquet types stand.
func arraysOf(kv metadata.KeyValueMetadata) map[string]int {
	v := kv.FindValue(arrowSchemaKey)
	if v == nil {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(*v)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(*v); err != nil {
			return nil
		}
	}
	r, err := ipc.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	defer r.Release()
	var out map[string]int
	for _, f := range r.Schema().Fields() {
		if fs, ok := f.Type.(*arrow.FixedSizeListType); ok {
			if out == nil {
				out = make(map[string]int)
			}
			out[f.Name] = int(fs.Len())
		}
	}
	return out
}

// hasArray reports whether s holds an Array at its top level.
func hasArray(s *dtype.Schema) bool {
	for _, f := range s.All() {
		if f.Type.ID() == dtype.TypeArray {
			return true
		}
	}
	return false
}

// arrowSchemaValue is s's Arrow schema as Arrow's writers keep it in a footer.
func arrowSchemaValue(s *dtype.Schema) (string, error) {
	as, err := arrowout.Schema(s)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	w := ipc.NewWriter(&b, ipc.WithSchema(as))
	if err := w.Close(); err != nil {
		return "", uerr.Wrap(err, uerr.KindInternal, "sink_parquet", "writing the Arrow schema")
	}
	return base64.StdEncoding.EncodeToString(b.Bytes()), nil
}
