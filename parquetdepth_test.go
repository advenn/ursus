package ursus_test

// Step 423: a Parquet schema nested past 1,000 levels is refused, where naming its
// type recursed once a level and a crafted footer of 1.6 MB overflowed the stack, a
// fatal error no recover catches.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// deepParquet is a Parquet file of no row groups whose one column is depth optional
// groups, each the next's one field, around an INT64 leaf: its footer written by hand
// in Thrift's compact protocol, as a crafted file would be.
func deepParquet(depth int) []byte {
	var f bytes.Buffer
	uv := func(v uint64) { f.Write(binary.AppendUvarint(nil, v)) }
	zz := func(v int64) uint64 { return uint64(v<<1) ^ uint64(v>>63) }
	element := func(name string, children int, leaf, optional bool) {
		last := 0
		field := func(id, typ int) { f.WriteByte(byte((id-last)<<4 | typ)); last = id }
		if leaf {
			field(1, 5) // type: INT64
			uv(zz(2))
		}
		if optional {
			field(3, 5) // repetition_type: OPTIONAL
			uv(zz(1))
		}
		field(4, 8) // name
		uv(uint64(len(name)))
		f.WriteString(name)
		if !leaf {
			field(5, 5) // num_children
			uv(zz(int64(children)))
		}
		f.WriteByte(0)
	}
	f.WriteByte(0x15) // version
	uv(zz(1))
	f.WriteByte(0x19) // schema: a list of structs
	f.WriteByte(0xFC)
	uv(uint64(depth + 2))
	element("schema", 1, false, false)
	for range depth {
		element("a", 1, false, true)
	}
	element("x", 0, true, true)
	f.WriteByte(0x16) // num_rows
	uv(zz(0))
	f.WriteByte(0x19) // row_groups: none
	f.WriteByte(0x0C)
	f.WriteByte(0)

	var file bytes.Buffer
	file.WriteString("PAR1")
	file.Write(f.Bytes())
	file.Write(binary.LittleEndian.AppendUint32(nil, uint32(f.Len())))
	file.WriteString("PAR1")
	return file.Bytes()
}

func parquetBytes(name string, b []byte) *ursus.LazyFrame {
	return ursus.ScanParquetFrom([]ursus.ParquetFile{{Name: name, Open: func(context.Context) (io.ReaderAt, int64, error) {
		return bytes.NewReader(b), int64(len(b)), nil
	}}})
}

// deepThrift is a Parquet file whose footer holds, past its schema, an unknown field
// of structs nested depth deep: a reader that skips it by recursing overflows.
func deepThrift(depth int) []byte {
	file := deepParquet(1)
	footerLen := int(binary.LittleEndian.Uint32(file[len(file)-8:]))
	footer := file[4 : 4+footerLen]
	var f bytes.Buffer
	f.Write(footer[:len(footer)-1]) // FileMetaData without its stop
	f.WriteByte(0x0C)               // field 5 (one past row_groups' 4): a struct
	for range depth - 1 {
		f.WriteByte(0x1C) // its field 1: a struct
	}
	for range depth {
		f.WriteByte(0)
	}
	f.WriteByte(0)
	var out bytes.Buffer
	out.WriteString("PAR1")
	out.Write(f.Bytes())
	out.Write(binary.LittleEndian.AppendUint32(nil, uint32(f.Len())))
	out.WriteString("PAR1")
	return out.Bytes()
}

// TestAParquetSchemaNestedPastTheLimitIsRefused: two million levels, a 1.6 MB footer,
// overflowed a 64 MB stack naming the column's type and ended the test binary; it is
// an ErrUnsupported naming the limit, measured from the footer before arrow-go, which
// overflowed there too, recursed on it. A column of 1,000 levels, its leaf counted,
// has a schema, and one of 1,001 is refused. A footer holding a million structs deep
// is refused as corrupt.
func TestAParquetSchemaNestedPastTheLimitIsRefused(t *testing.T) {
	defer debug.SetMaxStack(debug.SetMaxStack(64 << 20))
	for _, c := range []struct {
		groups int
		ok     bool
	}{{2_000_000, false}, {1_000, false}, {999, true}} {
		_, err := parquetBytes("deep.parquet", deepParquet(c.groups)).CollectSchema(t.Context())
		if c.ok {
			if err != nil {
				t.Errorf("%d groups around a leaf: %v", c.groups, err)
			}
			continue
		}
		if err == nil || !errors.Is(err, ursus.ErrUnsupported) || !strings.Contains(err.Error(), "nested deeper than 1000 levels") {
			t.Errorf("%d groups around a leaf: got %v, want refused for nesting past the limit", c.groups, err)
		}
	}
	// A footer the walk cannot skip past without recursing a million deep is corrupt.
	_, err := parquetBytes("thrift.parquet", deepThrift(1_000_000)).CollectSchema(t.Context())
	if err == nil || !errors.Is(err, ursus.ErrIO) || !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("a million structs deep in the footer: got %v, want it refused as corrupt", err)
	}
}
