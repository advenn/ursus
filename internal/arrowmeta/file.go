package arrowmeta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"sync/atomic"

	"github.com/advenn/ursus/internal/uerr"
)

// An IPC file, as arrow-go's ipc.NewFileReader reads it: its last ten bytes, a
// footer's length and the magic "ARROW1"; the footer, a flatbuffer holding the schema
// and a block for each dictionary batch and record batch, its offset, its metadata's
// length and its body's; and each block's message, a length prefix, a flatbuffer and
// a body. arrow-go reads the footer and every dictionary block when it opens the
// file, and a record block when it is asked for its batch.
//
// File reads the tail and the footer when it is opened, walks the footer, and serves
// arrow-go those bytes, the ones walked, whatever the file holds by then; and it walks
// each block's message as arrow-go reads it, in the bytes it hands arrow-go, before
// arrow-go has them. arrow-go reads a block's metadata in one ReadAt of exactly the
// block's offset and metadata length (fileBlock.NewMessage, through a SectionReader),
// which is how File knows it; TestEveryBlockIsWalked holds arrow-go to that.

// magic ends an IPC file.
var magic = []byte("ARROW1")

// minFile is arrow-go's minimumOffsetSize: a file no longer is refused before any of
// it is read.
const minFile = 2*6 + 4

// File is an IPC file's bytes for arrow-go's ipc.NewFileReader, its metadata walked
// before arrow-go reads it. It is safe for concurrent ReadAt, as arrow-go's
// RecordBatchAt is.
type File struct {
	op, name string
	r        io.ReaderAt
	size     int64
	pos      int64 // Seek's and Read's

	tail   []byte // the bytes from tailAt to the end, as read and walked
	tailAt int64

	schema *Schema
	blocks map[int64][]block // by offset

	walked atomic.Int64 // block messages walked, for TestEveryBlockIsWalked
}

// block is a footer's block: its metadata's length, its body's, and whether it is a
// dictionary batch's.
type block struct {
	meta int32
	body int64
	dict bool
}

// OpenFile reads name's footer from r, of size bytes, and walks it, refusing a
// corrupt one, KindIO, op naming the operation. A file arrow-go refuses before it
// reads a footer, too short or not ending in the magic, is left to arrow-go to
// refuse; its tail is served as it was read here.
func OpenFile(op, name string, r io.ReaderAt, size int64) (*File, error) {
	f := &File{op: op, name: name, r: r, size: size, tailAt: size}
	if size <= minFile {
		return f, nil
	}
	end := make([]byte, 10)
	if err := readAt(r, end, size-10); err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, op, "reading %s's footer", name)
	}
	f.tail, f.tailAt = end, size-10
	n := int64(binary.LittleEndian.Uint32(end))
	if !bytes.Equal(end[4:], magic) || n == 0 || n+minFile > size {
		return f, nil // as arrow-go's readFooter refuses it
	}
	if n > maxBuffer {
		return nil, f.corrupt(corrupt("a footer declares "+strconv.FormatInt(n, 10)+
			" bytes, more than a walk reads"), "at its end")
	}
	tail := make([]byte, n+10)
	if err := readAt(r, tail[:n], size-10-n); err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, op, "reading %s's footer", name)
	}
	copy(tail[n:], end)
	s, blocks, err := walkFooter(tail[:n])
	if err != nil {
		return nil, f.corrupt(err, "in its footer")
	}
	f.tail, f.tailAt, f.schema = tail, size-10-n, s
	f.blocks = make(map[int64][]block)
	for _, b := range blocks {
		// arrow-go refuses any other block before it reads it (validateFileBlock).
		if b.off >= 0 && b.meta >= 4 {
			f.blocks[b.off] = append(f.blocks[b.off], b.block)
		}
	}
	return f, nil
}

// readAt reads all of p at off.
func readAt(r io.ReaderAt, p []byte, off int64) error {
	n, err := r.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// corrupt is err, found walking where, as the file's refusal.
func (f *File) corrupt(err error, where string) *uerr.Error {
	return corruptInput(err, f.op, f.name, where)
}

// corruptInput is err, a walk's refusal or a panic's, found walking where in name.
func corruptInput(err error, op, name, where string) *uerr.Error {
	var c corrupt
	if !errors.As(err, &c) {
		var e *uerr.Error
		if errors.As(err, &e) {
			return e
		}
		return uerr.Wrap(err, uerr.KindInternal, op, "walking %s's Arrow IPC metadata", name)
	}
	return uerr.New(uerr.KindIO, op, "%s is corrupt: %s, %s", name, where, c).
		Hint("ursus checks every count and offset of Arrow IPC metadata against its bytes before reading it")
}

// ReadAt reads p at off: from the tail where it lies within it, and from the file
// otherwise; and where p is a block's metadata, as arrow-go reads one, it walks it,
// and refuses it, KindIO, where the walk finds it corrupt.
func (f *File) ReadAt(p []byte, off int64) (n int, err error) {
	if off >= f.tailAt && off <= f.size && int64(len(p)) <= f.size-off {
		n = copy(p, f.tail[off-f.tailAt:])
	} else if n, err = f.r.ReadAt(p, off); n < len(p) {
		return n, err
	}
	for _, b := range f.blocks[off] {
		if int64(b.meta) == int64(len(p)) {
			if werr := f.walkBlock(p, b, off); werr != nil {
				return 0, werr
			}
		}
	}
	return n, err
}

// walkBlock walks p, the metadata of the block b at off: its message, less the length
// prefix arrow-go takes off it (fileBlock.NewMessage), a dictionary batch for a
// dictionary block and a record batch for a record block, a message of any other
// header refused; each of a body of the block's length, which is what arrow-go reads.
func (f *File) walkBlock(p []byte, b block, off int64) (err error) {
	what := "a record batch's block"
	if b.dict {
		what = "a dictionary's block"
	}
	defer uerr.Catch(&err, f.op)
	f.walked.Add(1)
	prefix := 4
	switch binary.LittleEndian.Uint32(p) {
	case 0:
		prefix = 0
	case 0xFFFFFFFF:
		prefix = 8
	}
	if len(p)-prefix < 4 {
		return nil // arrow-go refuses it before it reads it
	}
	w, err := newWalker(p[prefix:])
	if err == nil {
		err = walkBlockMessage(w, f.schema, b)
	}
	if err != nil {
		return f.corrupt(err, "in the message of "+what+" at byte "+strconv.FormatInt(off, 10))
	}
	return nil
}

func walkBlockMessage(w *walker, s *Schema, b block) error {
	kind, header, _, err := w.message()
	if err != nil {
		return err
	}
	switch {
	case b.dict && kind == messageDictionary:
		return w.dictionaryBatch(header, s, b.body)
	case !b.dict && kind == messageRecord:
		return w.batch(header, s.views, b.body, "a record batch")
	case b.dict:
		return w.fail("a dictionary's block holds a %s message", kind)
	}
	return w.fail("a record batch's block holds a %s message", kind)
}

// Seek sets where Read reads next. arrow-go asks it for the file's size alone.
func (f *File) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += f.pos
	case io.SeekEnd:
		offset += f.size
	default:
		return 0, errors.New("arrowmeta: Seek: invalid whence")
	}
	if offset < 0 {
		return 0, errors.New("arrowmeta: Seek: negative position")
	}
	f.pos = offset
	return offset, nil
}

// Read reads from where Seek left it, through ReadAt.
func (f *File) Read(p []byte) (int, error) {
	if f.pos >= f.size {
		return 0, io.EOF
	}
	p = p[:min(int64(len(p)), f.size-f.pos)]
	n, err := f.ReadAt(p, f.pos)
	f.pos += int64(n)
	return n, err
}

// footerBlock is a footer's block and its offset.
type footerBlock struct {
	block
	off int64
}

// walkFooter walks a Footer table: its version, its schema, its dictionary blocks
// and record batch blocks, each a struct of 24 bytes, an offset, a metadata length
// and a body length, and its custom metadata.
func walkFooter(b []byte) (*Schema, []footerBlock, error) {
	w, err := newWalker(b)
	if err != nil {
		return nil, nil, err
	}
	t, err := w.root("a footer")
	if err != nil {
		return nil, nil, err
	}
	if err := w.scalar(t, 0, 2, "a footer's version"); err != nil {
		return nil, nil, err
	}
	at, ok, err := w.ref(t, 1, "a footer's schema")
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, w.fail("a footer has no schema")
	}
	s, err := w.schema(at)
	if err != nil {
		return nil, nil, err
	}
	var blocks []footerBlock
	for _, v := range []struct {
		id   int
		dict bool
		what string
	}{{2, true, "a footer's dictionary blocks"}, {3, false, "a footer's record batch blocks"}} {
		start, n, _, err := w.vector(t, v.id, 24, v.what)
		if err != nil {
			return nil, nil, err
		}
		for i := range n {
			p := start + 24*i
			blocks = append(blocks, footerBlock{
				block: block{meta: int32(w.b.u32(p + 8)), body: w.b.i64(p + 16), dict: v.dict},
				off:   w.b.i64(p),
			})
		}
	}
	if err := w.keyValues(t, 4, "a footer's custom metadata"); err != nil {
		return nil, nil, err
	}
	return s, blocks, nil
}
