package kernel

import (
	"encoding/binary"

	"ursus/dtype"
	"ursus/i128"
	"ursus/internal/data"
	"ursus/internal/uerr"
)

// GroupKeyEncoder turns a row's key columns into a byte string that can be used
// as a hash-map key.
//
// # The encoding, and why the tag byte is not optional
//
// Each key field is written as:
//
//	null    → 0x00                    (tag only, no payload)
//	present → 0x01 || payload
//
// Nulls form their OWN GROUP — the SQL and Polars rule — so a null must encode to
// something, and that something must not collide with any real value. The tag
// byte makes collision structurally impossible rather than merely unlikely: a
// present value's encoding always begins 0x01, so it can never equal a null's
// 0x00 no matter what its payload contains.
//
// Payloads are fixed-width big-endian for numerics and length-prefixed for
// strings. Big-endian and length-prefixed are both chosen so that the byte
// ordering of the encoding matches the value ordering — which this step does not
// use, but which makes the same encoder reusable for sort-merge joins and for
// spilling to disk without re-deriving it.
//
// Floats go through OrderKey first, so NaN groups with NaN and -0.0 groups with
// +0.0. Without that, counting NaNs would be impossible, because IEEE says no NaN
// equals itself.
type GroupKeyEncoder struct {
	cols    []*data.Column
	writers []func(dst []byte, row int) []byte
	buf     []byte
}

// NewGroupKeyEncoder resolves a per-column writer once, so the per-row cost is a
// slice of closure calls rather than a type switch per field per row.
//
// op names the user-facing operation in any error — "group_by", "unique", "join".
// It is a parameter rather than a constant because the encoder has three callers
// and a join on a List key reporting "cannot group by a List column" would point
// the user at an operation their query does not contain.
func NewGroupKeyEncoder(op string, cols []*data.Column) (*GroupKeyEncoder, error) {
	e := &GroupKeyEncoder{cols: cols, writers: make([]func([]byte, int) []byte, len(cols))}
	for i, c := range cols {
		w, err := keyWriter(op, c)
		if err != nil {
			return nil, err
		}
		e.writers[i] = w
	}
	return e, nil
}

// Encode returns the key for a row.
//
// The result aliases an internal buffer and is only valid until the next call.
// Callers that keep the key — the hash table does, on insert — must copy it. This
// is deliberate: a map lookup `m[string(key)]` does not allocate in Go, so the
// common path (an existing group) is allocation-free and only a genuinely new
// group pays for a copy.
func (e *GroupKeyEncoder) Encode(row int) []byte {
	e.buf = e.buf[:0]
	for i, w := range e.writers {
		if !e.cols[i].IsValid(row) {
			e.buf = append(e.buf, 0x00)
			continue
		}
		e.buf = append(e.buf, 0x01)
		e.buf = w(e.buf, row)
	}
	return e.buf
}

func keyWriter(op string, c *data.Column) (func([]byte, int) []byte, error) {
	if c.DType().ID() == dtype.TypeBool {
		bits := c.Bools()
		return func(dst []byte, row int) []byte {
			if bits.Get(row) {
				return append(dst, 1)
			}
			return append(dst, 0)
		}, nil
	}

	if c.DType().HasStringStorage() {
		acc := c.Strings()
		return func(dst []byte, row int) []byte {
			s := acc.Get(row)
			// Length-prefixed so that {"ab",""} and {"a","b"} cannot encode alike.
			dst = binary.BigEndian.AppendUint32(dst, uint32(len(s)))
			return append(dst, s...)
		}, nil
	}

	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return intKey[int8](c, 1, true)
	case dtype.TypeInt16:
		return intKey[int16](c, 2, true)
	case dtype.TypeInt32:
		return intKey[int32](c, 4, true)
	case dtype.TypeInt64:
		return intKey[int64](c, 8, true)
	case dtype.TypeUint8:
		return intKey[uint8](c, 1, false)
	case dtype.TypeUint16:
		return intKey[uint16](c, 2, false)
	case dtype.TypeUint32:
		return intKey[uint32](c, 4, false)
	case dtype.TypeUint64:
		return intKey[uint64](c, 8, false)

	case dtype.TypeFloat32:
		v, err := data.Values[float32](c)
		if err != nil {
			return nil, err
		}
		return func(dst []byte, row int) []byte {
			return binary.BigEndian.AppendUint32(dst, OrderKeyF32(v[row]))
		}, nil

	case dtype.TypeFloat64:
		v, err := data.Values[float64](c)
		if err != nil {
			return nil, err
		}
		return func(dst []byte, row int) []byte {
			return binary.BigEndian.AppendUint64(dst, OrderKeyF64(v[row]))
		}, nil

	case dtype.TypeInt128:
		v, err := data.Values[i128.Int128](c)
		if err != nil {
			return nil, err
		}
		// AppendBigEndian already flips the sign bit, so the encoding is
		// order-preserving like every other integer width.
		return func(dst []byte, row int) []byte {
			return v[row].AppendBigEndian(dst)
		}, nil

	default:
		return nil, uerr.New(uerr.KindUnsupported, op,
			"cannot use a %s column as a key", c.DType()).
			Hint("keys must be hashable: numeric, temporal, string or boolean")
	}
}

// intKey writes an integer key.
//
// SIGNED values are biased by the sign bit so that the unsigned byte ordering of
// the encoding matches the signed value ordering: -128 → 0x00, 0 → 0x80,
// 127 → 0xFF for int8, and correspondingly at wider widths.
//
// UNSIGNED values must NOT be biased. Flipping the sign bit on a uint8 sends 100
// to 0xE4 and 200 to 0x48, so 100 would encode above 200. That is harmless for
// grouping — XOR is a bijection either way, so equal values still collide and
// unequal ones still don't — but it silently breaks the order-preservation
// property this encoder documents and which a future sort-merge join or a spill
// file would rely on. Correct-for-the-current-caller is not good enough for an
// encoding that is meant to be reused.
func intKey[T data.Primitive](c *data.Column, width int, signed bool) (func([]byte, int) []byte, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	return func(dst []byte, row int) []byte {
		u := uint64(int64(v[row]))
		switch width {
		case 1:
			b := byte(u)
			if signed {
				b ^= 0x80
			}
			return append(dst, b)
		case 2:
			h := uint16(u)
			if signed {
				h ^= 0x8000
			}
			return binary.BigEndian.AppendUint16(dst, h)
		case 4:
			w := uint32(u)
			if signed {
				w ^= 0x8000_0000
			}
			return binary.BigEndian.AppendUint32(dst, w)
		default:
			if signed {
				u ^= 0x8000_0000_0000_0000
			}
			return binary.BigEndian.AppendUint64(dst, u)
		}
	}, nil
}

// HashKey hashes an encoded group key.
//
// # It must be the ENCODED key, never the raw column values
//
// NewGroupKeyEncoder is the single authority on grouping equality: floats go
// through OrderKey first, so NaN groups with NaN and -0.0 with +0.0. A hash over
// raw bits would send the two NaNs of one column to different radix partitions,
// they would be aggregated by separate sinks, and the output would carry TWO rows
// where one belongs — each with a plausible sub-total and no error anywhere.
// nuniqueAcc reuses this encoder for exactly the same reason.
//
// # Seedless and deterministic, deliberately
//
// hash/maphash is faster and its seed can only come from MakeSeed(), so the group
// ORDER of a spilled aggregation would differ between processes — a flake that
// reproduces only under a memory limit and only sometimes. FNV-1a costs a
// multiply-xor per byte on a path that is already doing file I/O per batch.
//
// The splitmix64 finaliser is not decoration: FNV avalanches poorly in its low
// bits, and the low bits are exactly what a radix partitioner reads.
func HashKey(b []byte) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for _, c := range b {
		h ^= uint64(c)
		h *= prime64
	}
	return Mix64(h)
}

// Mix64 is splitmix64's finaliser: a bijection that spreads every input bit over
// the whole output.
func Mix64(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}
