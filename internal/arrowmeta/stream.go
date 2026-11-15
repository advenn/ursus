package arrowmeta

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/internal/uerr"
)

// An IPC stream, as arrow-go's ipc.NewReaderFromMessageReader reads it: a schema
// message, a dictionary batch for each of its dictionaries, then record batches and
// more dictionary batches, each message a length, after the continuation marker
// 0xFFFFFFFF where the writer wrote one, a flatbuffer of that length and a body of
// the length the flatbuffer declares; and an end, a length of 0, or none.

// arrow-go's limits on a stream message (ipc's defaultMaxMetadataSize and
// defaultMaxBodySize), for a stream whose size is not known.
const (
	DefaultMaxMetadata = 64 << 20
	DefaultMaxBody     = 256 << 20
)

// continuation marks a message's length after Arrow 0.15.
const continuation = 0xFFFFFFFF

// NewMessageReader reads the messages of name's IPC stream from r for arrow-go's
// ipc.NewReaderFromMessageReader, as arrow-go's ipc.NewMessageReader reads them, under
// its limits on a message's metadata and body, maxMeta and maxBody bytes (0 for none),
// each body allocated from mem. Each message's metadata is walked before its body is
// allocated, against the schema before it, and refused as corrupt, KindIO, op naming
// the operation, where the walk finds it so or it is out of its place: a stream
// begins with a schema, has a dictionary batch for each of its dictionaries before
// any record batch, and holds nothing else.
func NewMessageReader(op, name string, r io.Reader, mem memory.Allocator, maxMeta, maxBody int64) ipc.MessageReader {
	m := &messageReader{op: op, name: name, r: r, mem: mem, maxMeta: maxMeta, maxBody: maxBody}
	m.refs.Add(1)
	return m
}

type messageReader struct {
	op, name         string
	r                io.Reader
	mem              memory.Allocator
	maxMeta, maxBody int64

	refs   atomic.Int64
	msg    *ipc.Message // the last message, released by the next
	header [4]byte

	read   int     // messages read
	schema *Schema // the stream's, once read
	dicts  int     // dictionary batches still to come before a record batch
}

func (m *messageReader) Retain() { m.refs.Add(1) }

func (m *messageReader) Release() {
	if m.refs.Add(-1) == 0 && m.msg != nil {
		m.msg.Release()
		m.msg = nil
	}
}

// Message reads the next message, which is valid until the one after; io.EOF at the
// stream's end. An end of the input where a message would begin is an error wrapping
// io.EOF, as arrow-go's message reader answers: the format makes a stream's end
// marker optional, so a stream may end there.
//
// An end within a message, once a byte of it is read, is the stream's refusal,
// KindIO, an error wrapping io.ErrUnexpectedEOF (cut): a stream cut where a part of a
// message would begin, its length after the continuation marker, its metadata or its
// body, is refused, not read as the messages before it (step 534).
func (m *messageReader) Message() (*ipc.Message, error) {
	msg := m.read + 1
	if _, err := io.ReadFull(m.r, m.header[:]); err == io.EOF {
		return nil, fmt.Errorf("reading an IPC message's length: %w", err)
	} else if err != nil {
		return nil, m.cut(err, msg, "length")
	}
	var n int32
	switch v := binary.LittleEndian.Uint32(m.header[:]); v {
	case 0:
		return nil, io.EOF
	case continuation:
		if _, err := io.ReadFull(m.r, m.header[:]); err != nil {
			return nil, m.cut(err, msg, "length")
		}
		if n = int32(binary.LittleEndian.Uint32(m.header[:])); n == 0 {
			return nil, io.EOF
		}
	default:
		n = int32(v) // before Arrow 0.15, a length alone
	}
	if n < 4 {
		return nil, fmt.Errorf("an IPC message's metadata length %d", n)
	}
	if m.maxMeta > 0 && int64(n) > m.maxMeta {
		return nil, fmt.Errorf("an IPC message's metadata length %d exceeds the limit of %d", n, m.maxMeta)
	}
	meta := make([]byte, n)
	if _, err := io.ReadFull(m.r, meta); err != nil {
		return nil, m.cut(err, msg, "metadata")
	}
	bodyLen, err := m.walk(meta)
	if err != nil {
		return nil, err
	}
	if m.maxBody > 0 && bodyLen > m.maxBody {
		return nil, fmt.Errorf("an IPC message's body length %d exceeds the limit of %d", bodyLen, m.maxBody)
	}
	body, err := m.allocate(int(bodyLen))
	if err != nil {
		return nil, err
	}
	defer body.Release()
	if _, err := io.ReadFull(m.r, body.Bytes()); err != nil {
		return nil, m.cut(err, msg, "body")
	}
	if m.msg != nil {
		m.msg.Release()
	}
	mb := memory.NewBufferBytes(meta)
	defer mb.Release()
	m.msg = ipc.NewMessage(mb, body)
	return m.msg, nil
}

// cut is err, what io.ReadFull answered reading part of the stream's message msg,
// as the read's error. Where the input ends inside the message, io.EOF where none of
// the part is there and io.ErrUnexpectedEOF otherwise, it is the stream's refusal,
// KindIO, wrapping io.ErrUnexpectedEOF. Any other error is the input's own, wrapped,
// any io.EOF in it made io.ErrUnexpectedEOF (uerr.Truncated), so that it is not taken
// for the stream's end, which an error wrapping io.EOF where a message begins is.
func (m *messageReader) cut(err error, msg int, part string) error {
	if err != io.EOF && err != io.ErrUnexpectedEOF {
		return fmt.Errorf("reading an IPC message's %s: %w", part, uerr.Truncated(err))
	}
	return uerr.Wrap(io.ErrUnexpectedEOF, uerr.KindIO, m.op, "%s ends inside its message %d, in its %s", m.name, msg, part)
}

// allocate is a body of n bytes from the reader's allocator, whose refusal, raised as
// ursus raises one, is the read's error.
func (m *messageReader) allocate(n int) (b *memory.Buffer, err error) {
	defer uerr.Catch(&err, m.op)
	b = memory.NewResizableBuffer(m.mem)
	b.Resize(n)
	return b, nil
}

// walk walks one message's metadata against what the stream has said, and is its
// body's length.
func (m *messageReader) walk(meta []byte) (body int64, err error) {
	defer uerr.Catch(&err, m.op)
	m.read++
	where := fmt.Sprintf("in its message %d", m.read)
	w, err := newWalker(meta)
	if err != nil {
		return 0, corruptInput(err, m.op, m.name, where)
	}
	kind, header, body, err := w.message()
	if err == nil && body < 0 {
		err = w.fail("a %s message declares a body of %d bytes", kind, body)
	}
	if err == nil {
		err = m.walkHeader(w, kind, header, body)
	}
	if err != nil {
		return 0, corruptInput(err, m.op, m.name, where)
	}
	return body, nil
}

func (m *messageReader) walkHeader(w *walker, kind messageType, header uint32, body int64) error {
	switch {
	case m.schema == nil:
		if kind != messageSchema {
			return w.fail("the stream begins with a %s message, not a Schema", kind)
		}
		s, err := w.schema(header)
		if err != nil {
			return err
		}
		m.schema, m.dicts = s, len(s.dicts)
		return nil
	case kind == messageDictionary:
		m.dicts = max(m.dicts-1, 0)
		return w.dictionaryBatch(header, m.schema, body)
	case m.dicts > 0:
		return w.fail("a %s message where the schema's %d dictionaries come first", kind, len(m.schema.dicts))
	case kind == messageRecord:
		return w.batch(header, m.schema.views, body, "a record batch")
	}
	return w.fail("a %s message, which a stream after its schema does not hold", kind)
}
