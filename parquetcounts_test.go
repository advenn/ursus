package ursus_test

// Step 416: a Parquet row group whose row count disagrees with its column chunks is
// refused as corrupt, where the read stopped at the count and lost the rest.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// thriftVarint reads a varint at p, the position after it too.
func thriftVarint(t *testing.T, b []byte, p int) (uint64, int) {
	v, n := binary.Uvarint(b[p:])
	if n <= 0 {
		t.Fatalf("a bad varint at byte %d of the footer", p)
	}
	return v, p + n
}

// thriftSkip moves past a Thrift compact value of type typ at p.
func thriftSkip(t *testing.T, b []byte, p int, typ byte) int {
	switch typ {
	case 1, 2: // a Boolean field holds its value in its type
		return p
	case 3:
		return p + 1
	case 4, 5, 6:
		_, p = thriftVarint(t, b, p)
		return p
	case 7:
		return p + 8
	case 8:
		n, p := thriftVarint(t, b, p)
		return p + int(n)
	case 9, 10:
		size, et := int(b[p]>>4), b[p]&0x0f
		p++
		if size == 15 {
			n, q := thriftVarint(t, b, p)
			size, p = int(n), q
		}
		for range size {
			if et == 1 || et == 2 {
				p++ // a Boolean element is a byte
			} else {
				p = thriftSkip(t, b, p, et)
			}
		}
		return p
	case 11:
		n, p := thriftVarint(t, b, p)
		if n == 0 {
			return p
		}
		kv := b[p]
		p++
		for range n {
			p = thriftSkip(t, b, p, kv>>4)
			p = thriftSkip(t, b, p, kv&0x0f)
		}
		return p
	case 12:
		return thriftStruct(t, b, p, nil)
	}
	t.Fatalf("a Thrift type %d", typ)
	return p
}

// thriftStruct walks a struct at p, calling visit with each field's id and type and
// where its value starts, and returns the position after it. visit returns where the
// value ends, or -1 to have it skipped.
func thriftStruct(t *testing.T, b []byte, p int, visit func(id int16, typ byte, at int) int) int {
	var last int16
	for {
		h := b[p]
		p++
		if h == 0 {
			return p
		}
		typ, id := h&0x0f, last+int16(h>>4)
		if h>>4 == 0 {
			v, q := thriftVarint(t, b, p)
			id, p = int16(int64(v>>1)^-int64(v&1)), q
		}
		last = id
		end := -1
		if visit != nil {
			end = visit(id, typ, p)
		}
		if end < 0 {
			end = thriftSkip(t, b, p, typ)
		}
		p = end
	}
}

// patchI64 sets the zigzag varint at at to v, which must take as many bytes.
func patchI64(t *testing.T, out []byte, at int, v int64) int {
	old, after := thriftVarint(t, out, at)
	var buf [10]byte
	w := binary.PutUvarint(buf[:], uint64(v<<1)^uint64(v>>63))
	if w != after-at {
		t.Fatalf("%d takes %d bytes, %d does %d", int64(old>>1), after-at, v, w)
	}
	copy(out[at:], buf[:w])
	return after
}

// withFileRows is a Parquet file's bytes with FileMetaData's num_rows, its third
// field, set to rows.
func withFileRows(t *testing.T, file []byte, rows int64) []byte {
	out := bytes.Clone(file)
	n := int(binary.LittleEndian.Uint32(out[len(out)-8:]))
	done := false
	thriftStruct(t, out, len(out)-8-n, func(id int16, typ byte, at int) int {
		if id != 3 || typ != 6 {
			return -1
		}
		done = true
		return patchI64(t, out, at, rows)
	})
	if !done {
		t.Fatal("no num_rows in the footer")
	}
	return out
}

// withRowGroupRows is a Parquet file's bytes with its first row group's num_rows, a
// field of FileMetaData's fourth, set to rows, which must take as many varint bytes as
// the count it replaces.
func withRowGroupRows(t *testing.T, file []byte, rows int64) []byte {
	out := bytes.Clone(file)
	n := int(binary.LittleEndian.Uint32(out[len(out)-8:]))
	footer := len(out) - 8 - n
	done := false
	thriftStruct(t, out, footer, func(id int16, typ byte, at int) int {
		if id != 4 || typ != 9 || done {
			return -1
		}
		// list<RowGroup>: patch the first one's field 3, an i64.
		p := at
		size := int(out[p] >> 4)
		p++
		if size == 15 {
			_, p = thriftVarint(t, out, p)
		}
		end := thriftStruct(t, out, p, func(id int16, typ byte, at int) int {
			if id != 3 || typ != 6 {
				return -1
			}
			done = true
			return patchI64(t, out, at, rows)
		})
		_ = end
		return thriftSkip(t, out, at, typ)
	})
	if !done {
		t.Fatal("no row group's num_rows in the footer")
	}
	return out
}

// TestARowGroupsCountIsCheckedAgainstItsColumns: a file of 300 rows whose row group
// says 257, as a flipped byte made it, read 257 rows and no error, and counted 257,
// where DuckDB 1.5.5 and pyarrow read 300; it is refused as corrupt, each way it is
// read. So is a count of more rows than the columns hold, a file's total that is not
// its row groups', and a count of more rows than a column of lists holds values.
func TestARowGroupsCountIsCheckedAgainstItsColumns(t *testing.T) {
	ids := make([]int64, 300)
	names := make([]string, 300)
	for i := range ids {
		ids[i], names[i] = int64(i), strings.Repeat("x", i%7)
	}
	var buf bytes.Buffer
	if err := ursus.Frame(ursus.Values("id", ids), ursus.Values("name", names)).
		WriteParquet(t.Context(), &buf); err != nil {
		t.Fatal(err)
	}
	// A file of lists alone: 300 rows of two values each, 600 values.
	var lists bytes.Buffer
	type row struct{ Pair []int64 }
	pairs := make([]row, 300)
	for i := range pairs {
		pairs[i] = row{[]int64{int64(i), int64(-i)}}
	}
	if err := ursus.FromRows(pairs).WriteParquet(t.Context(), &lists); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, bad := range []struct {
		what string
		file []byte
	}{
		{"a row group's 257", withRowGroupRows(t, buf.Bytes(), 257)},
		{"a row group's 301", withRowGroupRows(t, buf.Bytes(), 301)},
		{"the file's 257", withFileRows(t, buf.Bytes(), 257)},
		// The file's total made to agree, so the columns alone can tell.
		{"both 257", withFileRows(t, withRowGroupRows(t, buf.Bytes(), 257), 257)},
		{"both 301", withFileRows(t, withRowGroupRows(t, buf.Bytes(), 301), 301)},
		{"lists' 601", withFileRows(t, withRowGroupRows(t, lists.Bytes(), 601), 601)},
	} {
		path := filepath.Join(dir, "bad.parquet")
		if err := os.WriteFile(path, bad.file, 0o644); err != nil {
			t.Fatal(err)
		}
		reads := map[string]func() error{
			"Collect": func() error { _, err := ursus.ScanParquet(path).Collect(t.Context()); return err },
			"Count":   func() error { _, err := ursus.ScanParquet(path).Count(t.Context()); return err },
			"a column": func() error {
				_, err := ursus.ScanParquet(path).Head(1).Collect(t.Context())
				return err
			},
		}
		for name, read := range reads {
			err := read()
			if err == nil || !errors.Is(err, ursus.ErrIO) || !strings.Contains(err.Error(), "corrupt") {
				t.Errorf("%s, %s: got %v, want the file refused as corrupt", bad.what, name, err)
			}
		}
	}
	path := filepath.Join(dir, "good.parquet")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if df, err := ursus.ScanParquet(path).Collect(t.Context()); err != nil || df.Height() != 300 {
		t.Errorf("the file as written: %v", err)
	}
}
