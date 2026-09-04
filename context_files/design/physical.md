I have everything I need. Since I'm in read-only mode without Write access, here is the complete design.

---

# `ursus` Physical / Execution Layer — Implementation Design

## 0. Decisions up front

| # | Question | Decision | Cost of reversal |
|---|---|---|---|
| Q3 | `Series[T]` vs `Column` | **`Column` owns buffers; `Series[T]` is a free typed handle.** `Series[T]` never appears in a kernel signature. | Zero — nothing depends on it. |
| Q4 | Chunked vs contiguous | **`Column` is contiguous (offset+length over one buffer set). Chunking lives one level up: `DataFrame` is a list of `Batch`.** O(1) concat is preserved; every kernel takes `[]T`. | One file (`evaluator.go`) + `Column.Runs()`. Kernel signatures never change. |
| Q5 | String layout | **offsets+data (`Utf8`/`LargeUtf8`) behind `StringAccessor`, a *struct* with a layout tag — not an interface.** Dispatch once per batch, generic over the offset width. | One switch arm per string kernel + `take`. Zero call sites. |
| — | Pull vs push | **Pull `Operator` for v0.1; every non-blocking operator is a `BatchOp` that both engines run unchanged.** | ~150 lines in `internal/exec`. Zero kernels, zero `BatchOp`s, zero evaluator. |
| D13 | Null propagation | **Kernels are split into `Total` (never see validity, never produce nulls) and `Partial` (own their nulls). The dispatcher owns all null algebra.** Correctness is enforced by the *type* of the kernel you wrote. | — |
| D14 | Differential tests | **Scalar impls compile unconditionally; only the `var` binding is build-tagged.** | — |

The single most important structural claim: **a kernel author cannot get null propagation wrong, because a `Total` kernel is not given the validity bitmaps.** That is the fix for D13, and it is a type-system fix, not a discipline fix.

---

## 1. Package layout and dependency DAG

There is an import-cycle problem the API doc doesn't notice: `ursus.Column`/`Series[T]`/`DataType`/`Schema` are public, but the engine (`internal/…`) needs them, and `ursus` must import the engine to implement `Collect`. Resolution: **the shared types live in `internal/`, and the root package re-exports them with (generic) type aliases**, which Go supports since 1.24.

```
ursus/
├─ ursus.go, expr.go, lazy.go, series.go   (root; aliases + public API)
└─ internal/
   ├─ dtype/     DataType, TypeID, Field, Schema                 ← AGENT 3
   ├─ arrowx/    dtype↔arrow.DataType, allocator, alignment      ← AGENT 3
   ├─ bitmap/    Bitmap, View, Builder                           ← me
   ├─ data/      Column, Series[T], Batch, Selection,
   │             StringAccessor, Scalar, filter/take at column level ← me
   ├─ kernel/    kernels + registry + dispatch                   ← me
   ├─ plan/      ExprNode, plan.Node, optimizer                  ← AGENT 1
   ├─ physical/  Operator, BatchOp, Source, Evaluator, Scan/Filter/Project ← me
   └─ exec/      Driver, Pipeline, Sink, terminals               ← me
```

DAG (no cycles): `dtype ← arrowx ← bitmap? no` — precisely:

```
dtype  ←  arrowx  ←  data  ←  kernel  ←  physical  ←  exec
bitmap ←──────────────┘         ↑            ↑
plan  ──────────────────────────┴────────────┘
```

`bitmap` imports only `arrow/bitutil` + `arrow/memory`. `kernel`'s *pure* kernels import only `bitmap`; only the registry trampolines import `data`.

Root re-exports:

```go
// ursus/series.go
package ursus

import (
	"ursus/internal/data"
	"ursus/internal/dtype"
)

type (
	Column           = data.Column
	Series[T any]    = data.Series[T]   // generic alias, Go >= 1.24
	DataType         = dtype.DataType
	Schema           = dtype.Schema
	Field            = dtype.Field
)

func TypedColumn[T any](c *Column) (*Series[T], error) { return data.TypedColumn[T](c) }
```

---

## 2. The null-propagation contract

This is the normative table. Everything else in the design exists to make it enforceable.

Notation: `va`/`vb` are input validity bitmaps (`nil` ⇒ all-valid). "value in invalid lanes" is what a validity-blind reader sees.

| Kernel class | Output validity | Value in invalid lanes | Why |
|---|---|---|---|
| **Arithmetic, total** (`Add`, `Sub`, `Mul`, `Neg`, `Abs`, float `Div`) | `va AND vb` — computed by the **dispatcher**, never the kernel | whatever the raw lanes produce; deterministic but unspecified | `null + 1 == null`. The kernel is given only `[]T`; it *cannot* express the wrong thing. Go floats never trap, so no signal can escape. |
| **Arithmetic with scalar** | `va` | same | A `LitNull` lowers to an all-null *column*, never to a scalar; the scalar path therefore never has a null operand. If `Scalar.Valid == false` the dispatcher short-circuits to an all-null result. |
| **Arithmetic, partial** (integer `Div`/`Mod`/`FloorDiv`, `Pow` with negative base) | `(va AND vb) AND ok`, where `ok` is written by the kernel | 0 | Integer division by zero **panics** in Go. A `Partial` kernel receives the combined input validity so it can skip lanes it must not evaluate, and marks `x/0` as null (Polars/SQL behaviour). |
| **Comparison** (`Eq Ne Lt Le Gt Ge`) | `va AND vb` (dispatcher) | value bit forced **0** | `null > 5` is null. An Arrow Boolean column has **two** bitmaps; the docs' `GtF64(out []byte, …)` signature only produced one — that is defect D13(3). The forced 0 makes `Any()`, `popcount`, and a validity-blind consumer degrade to `false`, not to garbage. |
| **Missing-comparison** (`EqMissing`, `NeMissing`) | **all-valid** — no validity buffer emitted, `nullCount == 0` | n/a | `null == null → true`. Separate kernel family, separate op codes, and it *does* see `va`/`vb` because nulls are the data. |
| **Boolean Kleene** (`And`, `Or`, `Xor`) | `And`: `(va&vb) \| (va&^a) \| (vb&^b)`; `Or`: `(va&vb) \| (va&a) \| (vb&b)`; `Xor`: `va&vb` | the Kleene result, then ANDed with output validity | `false AND null == false`; `true OR null == true`. **This is not `va AND vb`.** Getting it wrong is a silent filter bug. **Precondition:** the kernel first canonicalizes `a_eff = a & va`, `b_eff = b & vb`, because Arrow does *not* guarantee that value bits of null slots are zero — without that, `null OR false` can report *valid* on garbage. |
| **Boolean unary** (`Not`) | `va` | inverted bit | |
| **Null predicates** (`IsNull`, `IsNotNull`) | **all-valid** | n/a; the value *is* the (inverted) validity bit | By definition never null. |
| **NaN predicates** (`IsNan`, `IsNotNan`, `IsFinite`, `IsInfinite`) | `va` | value bit 0 | `null.is_nan()` is **null**, not `false`. Null ≠ NaN, all the way down. |
| **Cast, widening** (i32→i64, f32→f64) | `va` | zero | Total kernel. |
| **Cast, narrowing / non-strict** | `va AND representable(a)` | zero | Partial kernel. Strict mode returns an error naming the row index and both dtypes. |
| **Unary math** (`Sqrt`, `Log`, `Log10`) | `va AND domainOK(a)` | zero | Partial. `sqrt(-1)` → null, not NaN, matching Polars. Substituting a safe input in skipped lanes keeps scalar and SIMD **bit-identical**, which the differential test requires. |
| **Aggregate** (`Sum`, `Mean`, `Min`, `Max`, `Product`, `Std`) | 1-row column, valid iff ≥1 input lane was valid. `Sum` of all-null is **null**. | n/a | Nulls are *skipped*, not zero-filled (SQL/Polars). |
| **Aggregate, counting** (`Count`, `Len`, `NullCount`) | always valid | n/a | |
| **Aggregate, SIMD lane masking** | — | **`x.IfElse(validityMask, identityVector)`, never `x.Masked(validityMask)`** | Defect D13(2). `Masked` writes **zero**. The identity is 0 for Sum, `+Inf` for Min, `-Inf` for Max, `1` for Product, `MaxInt`/`MinInt` for integer Min/Max. Zero-filling `Min` returns 0 for `[3,null,5]`. |
| **`take` / gather** | `idx[j] != NullIndex AND valid_in[idx[j]]` | zero | A null index (outer joins, `Gather` OOB) yields a null. |
| **`filter` / compaction** | validity compacted alongside values | — | |
| **Sort / hash-group comparator** | — | — | **Total order, not IEEE** — see §2.1. A different function from `Expr.Lt`. |

### 2.1 Float total order and ±0 (defect D13(6))

`Expr.Lt` uses IEEE semantics (SIMD `Less`). Sorting and hash-grouping use a **different** function, in `internal/kernel/order.go`, and the two must never be confused:

```go
// OrderKeyF64 maps a float64 to a uint64 whose unsigned ordering is the total
// order ursus documents: -Inf < … < -0.0 == +0.0 < … < +Inf < NaN, with all
// NaNs equal. This deliberately deviates from IEEE 754 and is used ONLY by
// sort, top-k, and hash grouping. Expr.Lt / Expr.Gt use IEEE via the SIMD
// comparison kernels.
func OrderKeyF64(f float64) uint64 {
	if f != f { // NaN: single canonical bucket, above everything
		return ^uint64(0)
	}
	b := math.Float64bits(f)
	if b == 0x8000_0000_0000_0000 { // -0.0 canonicalises to +0.0
		b = 0
	}
	if b>>63 != 0 {
		return ^b // negative: flip all bits
	}
	return b | 1<<63 // positive: flip sign bit
}
```

**Hashing must canonicalize before hashing**, or `-0.0` and `+0.0` land in different groups while comparing equal: `HashF64(f) = hash(OrderKeyF64(f))`. Same for `float32`.

### 2.2 Group-by null key sentinel (defect D13(5))

Group-by is v0.2, but the encoder's contract is fixed now because it constrains what `Column` must expose. The row encoder emits, per key field:

```
null    →  0x00                    (one byte, no payload)
present →  0x01 || payload bytes   (payload = big-endian OrderKey for numerics,
                                    len-prefixed bytes for strings)
```

Collision is structurally impossible: the tag byte precedes the payload and a null emits *no* payload, so no present-value encoding can equal a null encoding regardless of payload content. This is why the encoder needs `Column.Validity()` (a `bitmap.View`) and `data.Values[T]` — both of which exist.

---

## 3. `internal/bitmap`

The offset problem solved by making the offset **unforgeable**.

```go
// Package bitmap implements Arrow-compatible validity and boolean bitmaps.
//
// Arrow bit order is LSB-first: bit i lives at byte i/8, mask 1<<(i%8). A
// *Bitmap's bytes can be handed to arrow-go with no conversion.
//
// DESIGN RULE: a bit range is ALWAYS a View, which carries its own bit offset.
// There is no API that yields a bare []byte whose offset you could forget.
// Slicing an Arrow array leaves the validity bitmap bit-unaligned; forgetting
// that offset is the single most common Arrow kernel bug, and this package
// makes it unrepresentable.
package bitmap

import (
	"encoding/binary"
	"math/bits"

	"github.com/apache/arrow-go/v18/arrow/bitutil"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// ---------------------------------------------------------------- View ------

// View is an immutable, offset-carrying window over a bit buffer. The zero
// View, and any View with bits == nil, means "all bits set" — Arrow's
// omitted-validity-buffer convention.
type View struct {
	bits []byte
	off  int // bit offset of element 0
	n    int
}

func AllSet(n int) View { return View{n: n} }

func NewView(bits []byte, off, n int) View {
	if bits == nil {
		return View{n: n}
	}
	return View{bits: bits, off: off, n: n}
}

func (v View) IsAllSet() bool { return v.bits == nil }
func (v View) Len() int       { return v.n }

// Raw returns the three values together, so it is impossible to obtain the
// bytes without the offset. bits is nil when the View is all-set.
func (v View) Raw() (bits []byte, off, n int) { return v.bits, v.off, v.n }

func (v View) Get(i int) bool {
	if v.bits == nil {
		return true
	}
	return bitutil.BitIsSet(v.bits, v.off+i)
}

func (v View) CountSet() int {
	if v.bits == nil {
		return v.n
	}
	return bitutil.CountSetBits(v.bits, v.off, v.n)
}

func (v View) Slice(off, n int) View {
	if v.bits == nil {
		return View{n: n}
	}
	return View{bits: v.bits, off: v.off + off, n: n}
}

// Aligned reports whether the view starts on a byte boundary, and if so
// returns bytes whose bit 0 is element 0. Kernels use this for a fast path.
func (v View) Aligned() ([]byte, bool) {
	if v.bits == nil || v.off%8 != 0 {
		return nil, false
	}
	return v.bits[v.off/8:], true
}

// --------------------------------------------------------------- Bitmap -----

// Bitmap owns a bit buffer whose element 0 is always at bit offset 0.
type Bitmap struct {
	buf *memory.Buffer
	n   int
	set int
}

func (b *Bitmap) Buffer() *memory.Buffer { return b.buf }
func (b *Bitmap) Len() int               { return b.n }
func (b *Bitmap) CountSet() int          { return b.set }
func (b *Bitmap) View() View             { return View{bits: b.buf.Bytes(), off: 0, n: b.n} }
func (b *Bitmap) Retain()                { b.buf.Retain() }
func (b *Bitmap) Release()               { b.buf.Release() }

// AndInto computes dst &= src in place. dst must be freshly built by the caller.
func AndInto(dst, src *Bitmap) {
	if src == nil {
		return
	}
	bitutil.BitmapAnd(dst.buf.Bytes(), src.buf.Bytes(), 0, 0, dst.buf.Bytes(), 0, int64(dst.n))
	dst.set = bitutil.CountSetBits(dst.buf.Bytes(), 0, dst.n)
}

// -------------------------------------------------------------- Builder -----

// Builder packs bits LSB-first into a freshly allocated, 64-byte-padded, offset-0
// buffer. It accumulates 64 bits at a time and flushes 8 bytes, so it is correct
// for ANY number of bits per Put — 2, 4, 8, 16, 32, or 64 lanes. This is the
// answer to "writeMask is never defined" and to the 128-bit-width straddling
// problem (defect D14b).
type Builder struct {
	buf  *memory.Buffer
	n    int    // capacity in bits
	pos  int    // byte offset of the next 8-byte flush
	acc  uint64 // pending bits, LSB-first
	used uint   // bits currently in acc, 0..63
	set  int
	done bool
}

func NewBuilder(mem memory.Allocator, n int) *Builder {
	nb := int(bitutil.BytesForBits(int64(n)))
	nb = (nb + 63) &^ 63 // 64-byte pad, per the Arrow spec recommendation
	if nb == 0 {
		nb = 64
	}
	buf := memory.NewResizableBuffer(mem)
	buf.Resize(nb)
	clear(buf.Bytes()) // do NOT assume the allocator zeroes; pooling ones do not
	return &Builder{buf: buf, n: n}
}

func lowBits(n uint) uint64 {
	if n >= 64 {
		return ^uint64(0)
	}
	return 1<<n - 1
}

// PutBits appends the low n bits of w (LSB = next element). n may be 1..64.
func (b *Builder) PutBits(w uint64, n uint) {
	w &= lowBits(n)
	b.set += bits.OnesCount64(w)
	b.acc |= w << b.used
	if b.used+n < 64 {
		b.used += n
		return
	}
	binary.LittleEndian.PutUint64(b.buf.Bytes()[b.pos:], b.acc)
	b.pos += 8
	// Go defines x >> k == 0 for unsigned x and k >= 64, so used == 0 needs no
	// special case.
	b.acc = w >> (64 - b.used)
	b.used = b.used + n - 64
}

func (b *Builder) PutBool(v bool) {
	if v {
		b.PutBits(1, 1)
	} else {
		b.PutBits(0, 1)
	}
}

// PutView appends every bit of v, honouring v's own offset.
func (b *Builder) PutView(v View) {
	raw, off, n := v.Raw()
	if raw == nil {
		for i := 0; i < n; i += 64 {
			k := min(64, n-i)
			b.PutBits(lowBits(uint(k)), uint(k))
		}
		return
	}
	r := bitutil.NewBitmapWordReader(raw, off, n)
	for i := r.Words(); i > 0; i-- {
		b.PutBits(r.NextWord(), 64)
	}
	for i := r.TrailingBytes(); i > 0; i-- {
		bt, valid := r.NextTrailingByte()
		b.PutBits(uint64(bt), uint(valid))
	}
}

// Finish flushes the tail and yields an owned Bitmap. Bits never written are
// left zero. The Builder must not be used afterwards.
func (b *Builder) Finish() *Bitmap {
	if !b.done {
		if b.used > 0 {
			w := b.acc & lowBits(b.used)
			nb := int((b.used + 7) / 8)
			var tmp [8]byte
			binary.LittleEndian.PutUint64(tmp[:], w)
			copy(b.buf.Bytes()[b.pos:b.pos+nb], tmp[:nb])
		}
		b.done = true
	}
	return &Bitmap{buf: b.buf, n: b.n, set: b.set}
}

func (b *Builder) SetCount() int { return b.set }

// ---------------------------------------------------- derived constructors --

func Zeros(mem memory.Allocator, n int) *Bitmap { return NewBuilder(mem, n).Finish() }

func Ones(mem memory.Allocator, n int) *Bitmap {
	b := NewBuilder(mem, n)
	for i := 0; i < n; i += 64 {
		k := min(64, n-i)
		b.PutBits(lowBits(uint(k)), uint(k))
	}
	return b.Finish()
}

// Materialize copies v into a fresh offset-0 Bitmap. It returns (nil, 0) when v
// is all-set, matching Arrow's "no validity buffer" convention.
func Materialize(mem memory.Allocator, v View) (bm *Bitmap, nulls int) {
	if v.IsAllSet() {
		return nil, 0
	}
	b := NewBuilder(mem, v.Len())
	b.PutView(v)
	out := b.Finish()
	return out, out.n - out.set
}

// And computes a AND b as a fresh offset-0 Bitmap, handling all-set inputs and
// arbitrary bit offsets. Returns (nil, 0) when the result is all-set. This is
// THE null-propagation primitive; kernels never call it, dispatchers always do.
func And(mem memory.Allocator, a, b View) (*Bitmap, int) {
	switch {
	case a.IsAllSet() && b.IsAllSet():
		return nil, 0
	case a.IsAllSet():
		return Materialize(mem, b)
	case b.IsAllSet():
		return Materialize(mem, a)
	}
	n := a.Len()
	out := NewBuilder(mem, n).Finish() // zeroed, offset 0, 64-byte padded
	ra, oa, _ := a.Raw()
	rb, ob, _ := b.Raw()
	if n > 0 {
		// bitutil.BitmapAnd handles arbitrary bit offsets on BOTH inputs and the
		// output (aligned fast path + word-reader slow path). Verified in v18.7.0.
		bitutil.BitmapAnd(ra, rb, int64(oa), int64(ob), out.buf.Bytes(), 0, int64(n))
	}
	out.set = bitutil.CountSetBits(out.buf.Bytes(), 0, n)
	return out, n - out.set
}
```

`bitutil.BitmapAnd` does a read-modify-write on the output's edge bytes, so the output buffer **must** be zeroed — hence the explicit `clear()` in `NewBuilder`. That is a real trap in a pooling allocator.

---

## 4. `internal/data` — `Column`, `Series[T]`, `Batch`

### 4.1 `Column`

`Column` wraps `arrow.ArrayData`, not raw buffers and not `arrow.Array`. Reasons: `ArrayData` is exactly the buffer container we want; zero-copy in (`arr.Data()`) and out (`array.MakeFromData`) are free; O(1) slicing is `array.NewData` with a bumped offset; and we never hand-roll buffer lifetime.

```go
package data

import (
	"sync/atomic"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/bitutil"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"ursus/internal/arrowx"
	"ursus/internal/bitmap"
	"ursus/internal/dtype"
)

// Column is the dtype-erased, engine-facing column: a name, a logical DataType,
// and one contiguous set of Arrow buffers with an (offset, length) window.
//
// It is CONTIGUOUS, not chunked (open question #4). Chunking lives one level up:
// a DataFrame is a list of Batches, which makes Concat O(1) without putting a
// chunk dimension into every kernel signature.
//
// A Column is immutable once constructed. Slicing is O(1) and zero-copy.
type Column struct {
	name  string
	dt    dtype.DataType
	data  arrow.ArrayData
	nulls atomic.Int64 // cache; -1 = not yet computed
}

func newColumn(name string, dt dtype.DataType, d arrow.ArrayData) *Column {
	c := &Column{name: name, dt: dt, data: d}
	c.nulls.Store(int64(d.NullN())) // may be array.UnknownNullCount (-1)
	return c
}

func (c *Column) Name() string          { return c.name }
func (c *Column) DType() dtype.DataType { return c.dt }
func (c *Column) Len() int              { return c.data.Len() }
func (c *Column) Offset() int           { return c.data.Offset() }
func (c *Column) ArrowData() arrow.ArrayData { return c.data }

// Retain / Release implement the ownership discipline in §4.5. With the default
// GoAllocator they are effectively no-ops (Free is a no-op and there are no
// finalizers), so correctness NEVER depends on them; they exist so that a
// pooling or CheckedAllocator can catch leaks and double-frees in tests.
func (c *Column) Retain()  { c.data.Retain() }
func (c *Column) Release() { c.data.Release() }

func (c *Column) NullCount() int {
	if n := c.nulls.Load(); n >= 0 {
		return int(n)
	}
	v := c.Validity()
	n := c.Len() - v.CountSet()
	c.nulls.Store(int64(n))
	return n
}

// Validity returns an offset-carrying view of the validity bitmap. An all-valid
// column yields bitmap.AllSet — there is no other way to observe validity, and
// there is no accessor that returns the bytes without the offset.
func (c *Column) Validity() bitmap.View {
	bufs := c.data.Buffers()
	if len(bufs) == 0 || bufs[0] == nil {
		return bitmap.AllSet(c.data.Len())
	}
	if n := c.nulls.Load(); n == 0 {
		return bitmap.AllSet(c.data.Len())
	}
	return bitmap.NewView(bufs[0].Bytes(), c.data.Offset(), c.data.Len())
}

// Slice is O(1) and zero-copy. Note that the result generally has a NON-ZERO
// offset, which means a BIT-UNALIGNED validity bitmap. Every consumer goes
// through Validity() -> bitmap.View, so this cannot silently corrupt anything.
func (c *Column) Slice(off, n int) *Column {
	d := array.NewData(c.data.DataType(), n, c.data.Buffers(), c.data.Children(),
		array.UnknownNullCount, c.data.Offset()+off)
	return newColumn(c.name, c.dt, d)
}

func (c *Column) Renamed(name string) *Column {
	c.data.Retain()
	return newColumn(name, c.dt, c.data)
}

// Chunks always returns 1: Columns are contiguous by construction (see §Q4).
// Kept for API compatibility with the documented Series surface.
func (c *Column) Chunks() int { return 1 }

// -------------------------------------------------------- typed accessors ---

// Values reinterprets the values buffer as []T, already adjusted for the
// column's offset. Zero-copy; the slice aliases the Column and is valid only
// while the Column lives. T must be the column's PHYSICAL storage type
// (Datetime is int64, Date is int32, Categorical is uint32).
//
// The returned slice is capacity-clamped. arrow.GetValues returns a two-index
// slice whose cap runs to the end of the whole buffer, so an accidental append
// would write into rows this Column does not own.
func Values[T arrow.FixedWidthType](c *Column) ([]T, error) {
	if got, want := storageKind[T](), c.dt.Physical().ID; got != want {
		return nil, &TypeMismatchError{Column: c.name, Want: want, Got: got}
	}
	v := arrow.GetValues[T](c.data, 1)
	return v[:len(v):len(v)], nil
}

// storageKind maps a Go type parameter to the TypeID it can view. It is a
// free function, not an interface method: interfaces cannot declare generic
// methods, so all type-parameterised polymorphism must be free functions.
func storageKind[T any]() dtype.TypeID {
	var z T
	switch any(z).(type) {
	case int8:
		return dtype.TypeInt8
	case int16:
		return dtype.TypeInt16
	case int32:
		return dtype.TypeInt32
	case int64:
		return dtype.TypeInt64
	case uint8:
		return dtype.TypeUint8
	case uint16:
		return dtype.TypeUint16
	case uint32:
		return dtype.TypeUint32
	case uint64:
		return dtype.TypeUint64
	case float32:
		return dtype.TypeFloat32
	case float64:
		return dtype.TypeFloat64
	default:
		return dtype.TypeNull
	}
}

// BoolValues returns the boolean payload bitmap (buffer 1), NOT the validity
// bitmap (buffer 0). An Arrow Boolean column has two bitmaps; conflating them
// is defect D13(3).
func BoolValues(c *Column) (bitmap.View, error) {
	if c.dt.ID != dtype.TypeBoolean {
		return bitmap.View{}, &TypeMismatchError{Column: c.name, Want: dtype.TypeBoolean, Got: c.dt.ID}
	}
	bufs := c.data.Buffers()
	if len(bufs) < 2 || bufs[1] == nil {
		return bitmap.AllSet(c.Len()), nil // degenerate all-true
	}
	return bitmap.NewView(bufs[1].Bytes(), c.data.Offset(), c.data.Len()), nil
}
```

### 4.2 Column construction — writing directly into output buffers

The `array.Builder` interface is sealed and has no `Set(i, v)`. The verified public bypass:

```go
// NumericOut is a numeric column under construction. Kernels write into Vals
// directly; the dispatcher supplies validity; Finish publishes an immutable
// Column. This is the documented bypass of the sealed array.Builder interface:
// memory.NewResizableBuffer + Resize + arrow.GetData[T] -> writable []T.
type NumericOut[T arrow.FixedWidthType] struct {
	mem  memory.Allocator
	dt   dtype.DataType
	name string
	buf  *memory.Buffer
	Vals []T
	n    int
}

func NewNumericOut[T arrow.FixedWidthType](mem memory.Allocator, name string, dt dtype.DataType, n int) *NumericOut[T] {
	var z T
	buf := memory.NewResizableBuffer(mem)
	buf.Resize(n * int(unsafe.Sizeof(z))) // GoAllocator gives 64-byte alignment
	vals := arrow.GetData[T](buf.Bytes())
	return &NumericOut[T]{mem: mem, dt: dt, name: name, buf: buf, Vals: vals[:n:n], n: n}
}

// Finish publishes the column. valid may be nil (all-valid); ownership of valid
// and of the values buffer transfers to the returned Column.
func (o *NumericOut[T]) Finish(valid *bitmap.Bitmap, nulls int) (*Column, error) {
	at, err := arrowx.ToArrowType(o.dt) // AGENT 3 contract
	if err != nil {
		return nil, err
	}
	var vb *memory.Buffer
	if valid != nil {
		vb = valid.Buffer()
	}
	d := array.NewData(at, o.n, []*memory.Buffer{vb, o.buf}, nil, nulls, 0)
	o.buf.Release() // NewData retained
	if valid != nil {
		valid.Release()
	}
	return newColumn(o.name, o.dt, d), nil
}

// NewBooleanColumn builds a Boolean column from its TWO bitmaps.
func NewBooleanColumn(name string, values, valid *bitmap.Bitmap, nulls int) *Column {
	var vb *memory.Buffer
	if valid != nil {
		vb = valid.Buffer()
	}
	d := array.NewData(arrow.FixedWidthTypes.Boolean, values.Len(),
		[]*memory.Buffer{vb, values.Buffer()}, nil, nulls, 0)
	values.Release()
	if valid != nil {
		valid.Release()
	}
	return newColumn(name, dtype.Bool, d)
}
```

### 4.3 `Series[T]` — the typed view (open question #3, resolved)

```go
// Series is a typed VIEW over a Column. The Column owns the memory; a Series is
// a handle that costs one allocation to construct and nothing to use.
//
// Series is an ERGONOMICS surface, not a performance surface. No kernel ever
// takes a Series: kernels take []T + bitmap.View. That separation is what lets
// Series support logical types (string, time.Time) with a per-element decoder
// without slowing the engine down by a nanosecond.
type Series[T any] struct {
	col  *Column
	vals []T             // non-nil iff T is the physical storage type
	get  func(i int) T   // non-nil iff the element must be decoded (string, bool)
}

// TypedColumn constructs a Series[T] over c, or reports why T is wrong.
func TypedColumn[T any](c *Column) (*Series[T], error) {
	var z T
	switch any(z).(type) {
	case string:
		if c.dt.ID != dtype.TypeString {
			return nil, &TypeMismatchError{Column: c.name, Want: c.dt.ID, Got: dtype.TypeString}
		}
		acc, err := NewStringAccessor(c)
		if err != nil {
			return nil, err
		}
		return &Series[T]{col: c, get: func(i int) T {
			// unsafe.String over the shared data buffer: zero-copy and safe,
			// because Column buffers are immutable for the Column's lifetime.
			return any(acc.Str(i)).(T)
		}}, nil
	case bool:
		bv, err := BoolValues(c)
		if err != nil {
			return nil, err
		}
		return &Series[T]{col: c, get: func(i int) T { return any(bv.Get(i)).(T) }}, nil
	}
	// Fixed-width fast path: a genuine zero-copy slice.
	if fw, ok := any(z).(arrow.FixedWidthType); ok {
		_ = fw
	}
	return typedFixed[T](c)
}

func (s *Series[T]) Name() string            { return s.col.Name() }
func (s *Series[T]) DType() dtype.DataType   { return s.col.DType() }
func (s *Series[T]) Len() int                { return s.col.Len() }
func (s *Series[T]) NullCount() int          { return s.col.NullCount() }
func (s *Series[T]) IsValid(i int) bool      { return s.col.Validity().Get(i) }
func (s *Series[T]) Column() *Column         { return s.col }
func (s *Series[T]) Chunks() int             { return 1 }
func (s *Series[T]) Rechunk() *Series[T]     { return s } // already contiguous

func (s *Series[T]) Get(i int) (T, bool) {
	if !s.col.Validity().Get(i) {
		var z T
		return z, false
	}
	if s.vals != nil {
		return s.vals[i], true
	}
	return s.get(i), true
}

// Values is zero-copy for fixed-width storage types and returns nil for types
// that must be decoded (string, bool). Callers that need a materialised slice
// for those types use All() or Map. Values IGNORES validity, per the API doc.
func (s *Series[T]) Values() []T { return s.vals }

func (s *Series[T]) All() iter.Seq2[T, bool] {
	return func(yield func(T, bool) bool) {
		v := s.col.Validity()
		for i, n := 0, s.col.Len(); i < n; i++ {
			var val T
			ok := v.Get(i)
			if ok {
				if s.vals != nil {
					val = s.vals[i]
				} else {
					val = s.get(i)
				}
			}
			if !yield(val, ok) {
				return
			}
		}
	}
}

func (s *Series[T]) Slice(off, n int) *Series[T] {
	sub := s.col.Slice(off, n)
	out, _ := TypedColumn[T](sub)
	return out
}

// Generic METHODS on a generic type — verified legal in Go 1.27 (§18.2).
func (s *Series[T]) Map[U any](fn func(T) U) *Series[U]              { /* … */ }
func (s *Series[T]) MapErr[U any](fn func(T) (U, error)) (*Series[U], error)
func (s *Series[T]) Fold[A any](init A, fn func(A, T) A) A
func (s *Series[T]) Cast[U any]() (*Series[U], error)
```

**Why `Series[T]` cannot be zero-copy for every `T`, and how we stay honest:** `Series[time.Time]` cannot alias an `int64` timestamp buffer. So `Values()` is documented as returning `nil` for decoded types, and `TypedColumn[T]` fails loudly rather than silently reinterpreting. The docs' `Values() []T // zero-copy` is only true for storage types; that is a documentation bug we fix here.

### 4.4 `Batch`, `Selection`, `Scalar`, `StringAccessor`

```go
// Batch is the unit flowing between operators: a schema, N equal-length columns,
// and a row count. Immutable once constructed.
type Batch struct {
	schema *dtype.Schema
	cols   []*Column
	rows   int

	// sel is a PENDING row filter. In v0.1 it is always nil: FilterOp compacts
	// eagerly. The field exists because "a Batch may carry a pending selection"
	// is a CONTRACT that every BatchOp author must know from day one — bolting
	// late materialisation onto 30 operators later is the expensive move, while
	// carrying a always-nil pointer costs one branch. mustDense() makes it
	// impossible for the v0.1 code to silently produce a wrong answer.
	sel *Selection

	// meta is v0.2 morsel/spill bookkeeping. Zero in v0.1.
	meta BatchMeta
}

type BatchMeta struct {
	Partition int32 // hash partition id, for spilling and morsel affinity
	Ordinal   int64 // source ordinal, for order-preserving sinks
}

func NewBatch(s *dtype.Schema, cols []*Column, rows int) (*Batch, error) {
	if len(cols) != s.Len() {
		return nil, fmt.Errorf("ursus: batch has %d columns, schema has %d", len(cols), s.Len())
	}
	for i, c := range cols {
		if c.Len() != rows {
			return nil, fmt.Errorf("ursus: column %q has %d rows, batch has %d", c.Name(), c.Len(), rows)
		}
		if !c.DType().Equal(s.Field(i).Type) {
			return nil, fmt.Errorf("ursus: column %q is %s, schema says %s", c.Name(), c.DType(), s.Field(i).Type)
		}
	}
	return &Batch{schema: s, cols: cols, rows: rows}, nil
}

func (b *Batch) Schema() *dtype.Schema { return b.schema }
func (b *Batch) Rows() int             { return b.rows }
func (b *Batch) Width() int            { return len(b.cols) }

// Column returns a BORROWED pointer, valid only while b lives. Retain to outlive.
func (b *Batch) Column(i int) *Column { b.mustDense(); return b.cols[i] }

// mustDense asserts the v0.1 invariant. An internal invariant violation is a
// programming bug, which is what panic is for.
func (b *Batch) mustDense() {
	if b.sel != nil {
		panic("ursus: Batch carries a pending Selection; call Materialize first")
	}
}

func (b *Batch) Retain()  { for _, c := range b.cols { c.Retain() } }
func (b *Batch) Release() { for _, c := range b.cols { c.Release() } }

// Slice is O(1) and produces columns with a NON-ZERO offset — i.e. bit-unaligned
// validity. Only the driver uses it (Limit/Head).
func (b *Batch) Slice(off, n int) *Batch {
	cols := make([]*Column, len(b.cols))
	for i, c := range b.cols {
		cols[i] = c.Slice(off, n)
	}
	return &Batch{schema: b.schema, cols: cols, rows: n, meta: b.meta}
}

func (b *Batch) Empty() *Batch { return &Batch{schema: b.schema, cols: nil, rows: 0, meta: b.meta} }
```

```go
// Selection is the physical result of a predicate: either a bitmap over the
// batch or an explicit sorted index list. Operators produce whichever is
// cheaper; consumers pay a conversion at most once.
type Selection struct {
	bits  *bitmap.Bitmap
	idx   []uint32
	count int
	n     int
}

const NullIndex = ^uint32(0) // an index that gathers a NULL (outer joins)

func (s *Selection) Count() int { return s.count }

// SelectionFromBool implements SQL WHERE semantics: a NULL predicate row is NOT
// selected. sel = values AND validity.
func SelectionFromBool(mem memory.Allocator, c *Column) (*Selection, error) {
	vals, err := BoolValues(c)
	if err != nil {
		return nil, err
	}
	bm, _ := bitmap.And(mem, vals, c.Validity())
	if bm == nil { // both all-set: every row selected
		return &Selection{count: c.Len(), n: c.Len()}, nil
	}
	return &Selection{bits: bm, count: bm.CountSet(), n: c.Len()}, nil
}
```

```go
// Scalar is a single typed value with a validity flag. It is the literal fast
// path: Col("x").Gt(5) must NOT allocate a length-N broadcast column.
type Scalar struct {
	DType dtype.DataType
	Valid bool
	I     int64   // signed physical
	U     uint64  // unsigned physical
	F     float64 // float physical
	B     []byte  // string / binary
}

func ScalarAs[T arrow.FixedWidthType](s Scalar) (T, error) { /* switch on storageKind[T]() */ }
```

```go
// StringLayout resolves open question #5. StringAccessor is a STRUCT with a
// layout tag, not an interface: dispatch happens ONCE per batch in the kernel
// prologue, never per element. Adding Utf8View later means one new arm here and
// one new arm in each generated string kernel — and zero call-site changes.
type StringLayout uint8

const (
	LayoutOffset32 StringLayout = iota // arrow Utf8 / Binary            (v0.1)
	LayoutOffset64                     // arrow LargeUtf8 / LargeBinary  (v0.1)
	LayoutView                         // arrow Utf8View                 (v0.2)
)

type StringAccessor struct {
	Layout StringLayout
	Off32  []int32
	Off64  []int64
	Data   []byte
	Views  []arrow.ViewHeader // LayoutView
	Bufs   [][]byte           // LayoutView
	N      int
}

func NewStringAccessor(c *Column) (*StringAccessor, error) {
	d := c.ArrowData()
	switch c.DType().ID {
	case dtype.TypeString, dtype.TypeBinary:
		switch d.DataType().ID() {
		case arrow.STRING, arrow.BINARY:
			return &StringAccessor{Layout: LayoutOffset32,
				Off32: arrow.GetOffsets[int32](d, 1), Data: d.Buffers()[2].Bytes(), N: c.Len()}, nil
		case arrow.LARGE_STRING, arrow.LARGE_BINARY:
			return &StringAccessor{Layout: LayoutOffset64,
				Off64: arrow.GetOffsets[int64](d, 1), Data: d.Buffers()[2].Bytes(), N: c.Len()}, nil
		case arrow.STRING_VIEW, arrow.BINARY_VIEW:
			return nil, ErrUnsupportedLayout // v0.1: declared, not implemented
		}
	}
	return nil, &TypeMismatchError{Column: c.Name(), Want: dtype.TypeString, Got: c.DType().ID}
}

func (a *StringAccessor) Bytes(i int) []byte {
	switch a.Layout {
	case LayoutOffset32:
		return a.Data[a.Off32[i]:a.Off32[i+1]]
	case LayoutOffset64:
		return a.Data[a.Off64[i]:a.Off64[i+1]]
	}
	panic("ursus: unsupported string layout")
}

// Str returns a zero-copy string. Safe: Column buffers are immutable while the
// Column is alive, and the returned string does not outlive the Series.
func (a *StringAccessor) Str(i int) string {
	b := a.Bytes(i)
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}
```

### 4.5 Ownership and lifetime rules

Given the verified facts — `GoAllocator.Free` is a no-op, buffers are plain Go heap, there are **zero finalizers** in `arrow`/`arrow/array`/`arrow/memory` — refcounting is *advisory* and is hidden entirely behind a GC-managed API. The rules:

1. A `*Column` is **immutable after construction**. Kernels write into buffers they allocated, before the Column wrapping them is published.
2. Whoever calls a constructor (`NumericOut.Finish`, `NewBooleanColumn`, `FilterColumn`, `TakeColumn`, a kernel trampoline) **owns** the result and `Release()`s it exactly once.
3. A `Batch` **owns** its columns. `Batch.Column(i)` returns a **borrowed** pointer valid only while the Batch lives; `Retain()` to outlive it.
4. `BatchOp.Apply` **borrows** `in` and **owns** its return. It must not `Release(in)`.
5. `Operator.Next` **transfers** ownership of the returned Batch to the caller.
6. **Correctness never depends on `Release` being called.** Production runs on `memory.GoAllocator` where `Release` is a no-op and the GC is the real owner. Tests run under `memory.NewCheckedAllocator`, which makes rule violations fail loudly with `AssertSize(t, 0)` in `TestMain`. That is how we get leak/double-free detection without paying finalizer cost or exposing `Retain`/`Release` to users.

Public exposure: `Release()` appears on a `DataFrame` **only** when it was obtained via zero-copy `FromArrow` (matching the doc's leaning in open question #2). Everything else is GC-managed.

---

## 5. `internal/kernel`

### 5.1 Kernel classes — the D13 fix expressed in types

```go
package kernel

// Numeric is the set of physical fixed-width storage types kernels operate on.
type Numeric interface {
	~int8 | ~int16 | ~int32 | ~int64 |
		~uint8 | ~uint16 | ~uint32 | ~uint64 | ~float32 | ~float64
}

// ============================================================================
// CLASS A — TOTAL kernels.
//
// A Total kernel NEVER produces a null and NEVER SEES a validity bitmap. It
// computes a defined value for every lane, including lanes whose inputs are
// null (it cannot know which those are). Output validity is derived entirely
// by the dispatcher as `va AND vb`.
//
// This is the structural fix for defect D13(1): the documented signature
//   func(dst, a, b []T, validity *bitmap.Bitmap)
// took ONE bitmap and no input validities, so `null + 1 == null` was
// inexpressible. Here it is not merely expressible, it is unavoidable.
// ============================================================================

type TotalBinary[T Numeric] func(dst, a, b []T)
type TotalBinaryScalar[T Numeric] func(dst, a []T, s T)
type TotalUnary[T Numeric] func(dst, a []T)

// Comparisons write the boolean PAYLOAD bitmap. The dispatcher writes the
// separate validity bitmap. Two bitmaps — defect D13(3).
type TotalCompare[T Numeric] func(out *bitmap.Builder, a, b []T)
type TotalCompareScalar[T Numeric] func(out *bitmap.Builder, a []T, s T)

// ============================================================================
// CLASS B — PARTIAL kernels.
//
// A Partial kernel may itself introduce nulls: integer division by zero (which
// PANICS in Go, so the kernel must skip those lanes), non-representable casts,
// sqrt/log of a value outside the domain. It receives the COMBINED input
// validity so it can skip lanes it must not evaluate, and writes its own output
// validity, which the dispatcher then ANDs with the input validity.
// ============================================================================

type PartialBinary[T Numeric] func(dst []T, ok *bitmap.Builder, a, b []T, in bitmap.View)
type PartialUnary[T Numeric] func(dst []T, ok *bitmap.Builder, a []T, in bitmap.View)
type PartialCast[From, To Numeric] func(dst []To, ok *bitmap.Builder, a []From, in bitmap.View)

// ============================================================================
// CLASS C — MISSING comparison. Nulls are DATA, not absence.
// null == null is TRUE; the output is ALWAYS valid (no validity buffer).
// This is the second kernel family demanded by defect D13(3).
// ============================================================================

type MissingCompare[T Numeric] func(out *bitmap.Builder, a, b []T, va, vb bitmap.View)

// ============================================================================
// CLASS D — Kleene boolean. Inherently three-valued; must see validity.
// ============================================================================

type KleeneBinary func(outVals, outValid *bitmap.Builder, a, va, b, vb bitmap.View)

// ============================================================================
// CLASS E — aggregates. Defect D13(4): no aggregate signature existed at all.
//
// Modelled as a dtype-erased interface, because interfaces cannot declare
// generic methods. The IMPLEMENTATIONS are generic structs; only the interface
// is erased. Nulls are SKIPPED, not zero-filled; the result of an all-null
// input is NULL, not zero.
// ============================================================================

type Accumulator interface {
	Update(c *data.Column) error
	Merge(other Accumulator) error // required for morsel parallelism and spilling
	Finish(mem memory.Allocator, name string) (*data.Column, error) // 1-row column
}

type AccumFactory func(dt dtype.DataType) (Accumulator, error)
```

### 5.2 The comparison family, fully worked

**`cmp_scalar.go` — no build tag. Compiled in every build, always.** This is the fix for defect D14: with the documented `//go:build !goexperiment.simd` on the scalar twin, only one implementation is ever linked and *nothing can be compared*.

```go
// cmp_scalar.go  — NO BUILD TAG. The scalar implementations are ALWAYS linked,
// in every build, so the differential test has something to compare against.
// Only the DISPATCH (which of the two the engine calls) is build-tagged.

package kernel

import "ursus/internal/bitmap"

// gtF64Scalar writes a > s into the boolean payload bitmap, 64 lanes at a time.
// It never sees validity: it is a Class A Total kernel.
func gtF64Scalar(out *bitmap.Builder, a []float64, s float64) {
	i := 0
	for ; i+64 <= len(a); i += 64 {
		var w uint64
		for j := 0; j < 64; j++ {
			if a[i+j] > s {
				w |= 1 << uint(j)
			}
		}
		out.PutBits(w, 64)
	}
	if i < len(a) {
		n := len(a) - i
		var w uint64
		for j := 0; j < n; j++ {
			if a[i+j] > s {
				w |= 1 << uint(j)
			}
		}
		out.PutBits(w, uint(n))
	}
}

func leF64Scalar(out *bitmap.Builder, a []float64, s float64) { /* identical shape, <= */ }
func ltF64Scalar(out *bitmap.Builder, a []float64, s float64) { /* … */ }
func geF64Scalar(out *bitmap.Builder, a []float64, s float64) { /* … */ }
func eqF64Scalar(out *bitmap.Builder, a []float64, s float64) { /* … */ }
func neF64Scalar(out *bitmap.Builder, a []float64, s float64) { /* … */ }
```

**`movemask_simd.go` — the width-safe helper. Written once, used everywhere.**

```go
//go:build goexperiment.simd

package kernel

import (
	"simd"
	"simd/archsimd"
)

// maxLanes64 is the largest lane count for a 64-bit element type (512/64).
const maxLanes64 = 8

// packMask64 converts a 64-bit-lane mask into Arrow bit order (LSB = lane 0).
//
// The type assertion on ToArch() is WIDTH-DEPENDENT and will PANIC if written
// as a single-case assertion: under GODEBUG=simd=256 the dynamic type is
// archsimd.Mask64x4, and under simd=0 it is simd/internal/bridge.Mask64s, a
// type this package cannot name. Hence the four-way switch with a portable
// default. THIS FUNCTION IS THE ONLY PLACE PERMITTED TO CALL ToArch ON A
// 64-BIT MASK; every kernel goes through it.
//
// archsimd's ToBits is LSB-first with bit i == lane i, which is exactly the
// Arrow bitmap bit order — no reversal is needed.
func packMask64(m simd.Mask64s) uint64 {
	switch a := m.ToArch().(type) {
	case archsimd.Mask64x8:
		return uint64(a.ToBits()) // uint8: exactly one Arrow bitmap byte
	case archsimd.Mask64x4:
		return uint64(a.ToBits())
	case archsimd.Mask64x2:
		return uint64(a.ToBits())
	default:
		// Emulated mode (GODEBUG=simd=0). ToInt64s yields -1 / 0 per lane.
		// buf is zero-initialised each call, so lanes beyond Len() read 0.
		var buf [maxLanes64]int64
		m.ToInt64s().Store(buf[:])
		var w uint64
		for i, v := range buf {
			if v != 0 {
				w |= 1 << uint(i)
			}
		}
		return w
	}
}

// packMask32 is the same for 32-bit lanes (float32, int32, uint32, Date32).
func packMask32(m simd.Mask32s) uint64 {
	switch a := m.ToArch().(type) {
	case archsimd.Mask32x16:
		return uint64(a.ToBits()) // uint16 = two Arrow bitmap bytes
	case archsimd.Mask32x8:
		return uint64(a.ToBits())
	case archsimd.Mask32x4:
		return uint64(a.ToBits())
	default:
		var buf [16]int32
		m.ToInt32s().Store(buf[:])
		var w uint64
		for i, v := range buf {
			if v != 0 {
				w |= 1 << uint(i)
			}
		}
		return w
	}
}
```

**`cmp_simd.go` — the SIMD twin.**

```go
//go:build goexperiment.simd

package kernel

import (
	"simd"

	"ursus/internal/bitmap"
)

func gtF64SIMD(out *bitmap.Builder, a []float64, s float64) {
	vs := simd.BroadcastFloat64s(s)
	lanes := vs.Len() // RUNTIME value: 2, 4 or 8. Not a constant.
	i := 0
	for ; i+lanes <= len(a); i += lanes {
		m := simd.LoadFloat64s(a[i:]).Greater(vs)
		// PutBits accumulates into a uint64 and flushes 8 bytes, so it is
		// correct for 2, 4 and 8 lanes alike. At 128-bit width 8 lanes is NOT
		// one byte — that is defect D14b, and this is the fix.
		out.PutBits(packMask64(m), uint(lanes))
	}
	if i < len(a) {
		v, cnt := simd.LoadFloat64sPart(a[i:])
		// LoadFloat64sPart ZERO-FILLS lanes >= cnt. If s < 0 those zero lanes
		// compare TRUE, so masking off the garbage bits is MANDATORY, not
		// cosmetic.
		out.PutBits(packMask64(v.Greater(vs))&lowBitsU(uint(cnt)), uint(cnt))
	}
}

func lowBitsU(n uint) uint64 {
	if n >= 64 {
		return ^uint64(0)
	}
	return 1<<n - 1
}

// Note on mask Not/Xor/AndNot: portable simd's MaskNs has only And, Or, String,
// ToArch, ToIntNs. Where we need a negated mask we either invert the PREDICATE
// (LessEqual instead of Greater — free) or round-trip
// m.ToInt64s().Not().ToMask(). We prefer inverting the predicate.
```

**The dispatch — the only build-tagged part.**

```go
// cmp_dispatch.go — NO BUILD TAG. Declares the indirection and a safe default.
package kernel

type cmpF64Scalar func(out *bitmap.Builder, a []float64, s float64)

var (
	gtF64 cmpF64Scalar = gtF64Scalar // overridden by init() where SIMD is linked
	leF64 cmpF64Scalar = leF64Scalar
	// …
)

// forceScalar lets ONE binary benchmark and test both implementations.
var forceScalar = os.Getenv("URSUS_KERNELS") == "scalar"

func pickF64(simdImpl, scalarImpl cmpF64Scalar) cmpF64Scalar {
	if forceScalar {
		return scalarImpl
	}
	return simdImpl
}
```

```go
// cmp_dispatch_simd.go
//go:build goexperiment.simd

package kernel

// Package-level var initialisers run before init(), so this override is
// well-ordered.
func init() {
	gtF64 = pickF64(gtF64SIMD, gtF64Scalar)
	leF64 = pickF64(leF64SIMD, leF64Scalar)
}
```

```go
// cmp_dispatch_nosimd.go
//go:build !goexperiment.simd

package kernel

func init() {} // the scalar defaults stand
```

**The differential test.**

```go
// cmp_diff_test.go
//go:build goexperiment.simd

package kernel

// TestGtF64Differential asserts BIT-IDENTICAL output between the SIMD and the
// scalar implementations. This test can only exist because cmp_scalar.go has NO
// build tag: both implementations are in the binary simultaneously. Under the
// design the docs proposed (scalar behind //go:build !goexperiment.simd), only
// one is ever linked and this test is unwritable — defect D14.
func TestGtF64Differential(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	rng := rand.New(rand.NewPCG(1, 2))
	// Lengths that straddle every plausible lane count and every 64-bit flush
	// boundary: 0,1,2,3,7,8,9,15,16,17,31,32,33,63,64,65,127,128,129,1000.
	for _, n := range []int{0, 1, 2, 3, 7, 8, 9, 15, 16, 17, 31, 32, 33, 63, 64, 65, 127, 128, 129, 1000} {
		a := make([]float64, n)
		for i := range a {
			switch rng.IntN(8) {
			case 0:
				a[i] = math.NaN()
			case 1:
				a[i] = math.Inf(1)
			case 2:
				a[i] = math.Inf(-1)
			case 3:
				a[i] = math.Copysign(0, -1) // -0.0
			default:
				a[i] = rng.NormFloat64() * 100
			}
		}
		// Negative thresholds are the case that catches an unmasked ragged tail:
		// LoadFloat64sPart zero-fills, and 0 > -1 is TRUE.
		for _, s := range []float64{-1, 0, 5, math.NaN(), math.Inf(-1)} {
			bs := bitmap.NewBuilder(mem, n)
			gtF64Scalar(bs, a, s)
			bv := bitmap.NewBuilder(mem, n)
			gtF64SIMD(bv, a, s)
			want, got := bs.Finish(), bv.Finish()
			if !bitutil.BitmapEquals(want.Buffer().Bytes(), got.Buffer().Bytes(), 0, 0, int64(n)) {
				t.Fatalf("n=%d s=%v width=%d: SIMD != scalar", n, s, simd.VectorBitSize())
			}
			want.Release()
			got.Release()
		}
	}
}
```

### 5.3 Kleene boolean — the bug the docs would have shipped

```go
// kleene.go — NO BUILD TAG. arrow/bitutil's bitmap ops are already
// SIMD-accelerated (AVX2/SSE4/NEON with a noasm fallback) and handle arbitrary
// bit offsets on both inputs and the output, so there is no reason to write
// these in `simd`.

// kleeneOr implements SQL three-valued OR:
//   true OR null == true,  false OR null == null,  null OR null == null
//
// The canonicalisation step (a_eff = a AND va) is NOT optional. Arrow does not
// guarantee that the value bits of null slots are zero — "null slots may
// contain arbitrary bytes". Without it, `null OR false` where the null slot's
// garbage value bit happens to be 1 reports VALID TRUE. That is a silent
// wrong-answer bug in every filter, and it is exactly the class of defect this
// design exists to prevent.
func kleeneOr(mem memory.Allocator, aVals, aValid, bVals, bValid bitmap.View) (vals, valid *bitmap.Bitmap, nulls int) {
	n := aVals.Len()
	aEff, _ := bitmap.And(mem, aVals, aValid) // nil ⇒ all-true
	bEff, _ := bitmap.And(mem, bVals, bValid)

	// out = a_eff OR b_eff
	out := bitmap.Or(mem, viewOf(aEff, n), viewOf(bEff, n))

	// valid = (va AND vb) OR a_eff OR b_eff
	both, _ := bitmap.And(mem, aValid, bValid)
	v := bitmap.Or(mem, viewOf(both, n), viewOf(out, n))

	return out, v, n - v.CountSet()
}

// kleeneAnd: valid = (va AND vb) OR (va AND NOT a) OR (vb AND NOT b)
//            out   = a_eff AND b_eff
func kleeneAnd(mem memory.Allocator, aVals, aValid, bVals, bValid bitmap.View) (vals, valid *bitmap.Bitmap, nulls int)

// kleeneXor: valid = va AND vb   (no short-circuit exists for XOR)
func kleeneXor(mem memory.Allocator, aVals, aValid, bVals, bValid bitmap.View) (vals, valid *bitmap.Bitmap, nulls int)
```

### 5.4 The registry and dispatch

The dispatch problem the docs never address: **the evaluator is dtype-erased. It holds a `DataType`, not a `T`. It cannot call `r.Binary[T](op)`.**

Resolution: **generics for *building*, erased `func` values for *calling*.**

```go
package kernel

type Op uint16

const (
	OpAdd Op = iota
	OpSub
	OpMul
	OpDiv
	OpFloorDiv
	OpMod
	OpEq
	OpNe
	OpLt
	OpLe
	OpGt
	OpGe
	OpEqMissing
	OpNeMissing
	OpAnd
	OpOr
	OpXor
	OpNot
	OpNeg
	OpAbs
	OpIsNull
	OpIsNotNull
	OpIsNan
	OpIsNotNan
	OpCast
	_opCount
)

// Ctx carries per-invocation execution state. In v0.2 it gains a memory
// Reservation so kernels participate in the memory budget and can trigger spill.
type Ctx struct {
	Mem memory.Allocator
}

// The ERASED forms. These are what the evaluator calls. There is exactly one
// `any` unbox per KERNEL INVOCATION — i.e. per 65 536-row batch — which is
// unmeasurable.
type (
	BinaryFn       func(kc Ctx, a, b *data.Column) (*data.Column, error)
	BinaryScalarFn func(kc Ctx, a *data.Column, s data.Scalar) (*data.Column, error)
	UnaryFn        func(kc Ctx, a *data.Column) (*data.Column, error)
)

type key struct {
	op Op
	dt dtype.TypeID
}

// Registry is a concrete struct so it can carry generic methods (Go 1.27).
type Registry struct {
	binary   map[key]BinaryFn
	binarySc map[key]BinaryScalarFn
	unary    map[key]UnaryFn
	accum    map[key]AccumFactory
	typed    map[key]any // the generic façade, for Series[T] and for tests
}

func (r *Registry) Binary(op Op, dt dtype.TypeID) (BinaryFn, bool) {
	f, ok := r.binary[key{op, dt}]
	return f, ok
}
func (r *Registry) BinaryScalar(op Op, dt dtype.TypeID) (BinaryScalarFn, bool) {
	f, ok := r.binarySc[key{op, dt}]
	return f, ok
}

// TypedTotalBinary is the generic façade the API doc asked for. A generic METHOD
// on a concrete struct — verified legal in Go 1.27. Used by Series[T] arithmetic
// and by kernel tests; the engine uses the erased form above.
func (r *Registry) TypedTotalBinary[T Numeric](op Op, dt dtype.TypeID) (TotalBinary[T], bool) {
	v, ok := r.typed[key{op, dt}]
	if !ok {
		return nil, false
	}
	k, ok := v.(TotalBinary[T])
	return k, ok
}
```

**The trampoline for a Class A comparison-with-scalar. This is where ALL the null algebra lives.**

```go
// RegisterTotalCompareScalar wraps a Class A comparison kernel in the dispatcher
// that owns null propagation. Registration is generic; the stored value is
// erased. The kernel author writes pure math and CANNOT get nulls wrong.
func RegisterTotalCompareScalar[T Numeric](r *Registry, op Op, dt dtype.DataType, k TotalCompareScalar[T]) {
	r.binarySc[key{op, dt.ID}] = func(kc Ctx, a *data.Column, s data.Scalar) (*data.Column, error) {
		n := a.Len()

		// A NULL scalar makes every comparison null. Short-circuit: values all
		// false, validity all false.
		if !s.Valid {
			return data.NewBooleanColumn(a.Name(),
				bitmap.Zeros(kc.Mem, n), bitmap.Zeros(kc.Mem, n), n), nil
		}

		av, err := data.Values[T](a)
		if err != nil {
			return nil, err
		}
		sv, err := data.ScalarAs[T](s)
		if err != nil {
			return nil, err
		}

		// 1. VALUES — pure math. The kernel never sees validity.
		vb := bitmap.NewBuilder(kc.Mem, n)
		k(vb, av, sv)
		vals := vb.Finish()

		// 2. VALIDITY — derived by the dispatcher: out_valid = valid_a.
		valid, nulls := bitmap.Materialize(kc.Mem, a.Validity())

		// 3. Force value bits FALSE in invalid lanes, so a validity-blind
		//    consumer (Any, popcount, a boolean sink) degrades to `false`
		//    rather than reading garbage. Contract row "Comparison".
		if valid != nil {
			bitmap.AndInto(vals, valid)
		}
		return data.NewBooleanColumn(a.Name(), vals, valid, nulls), nil
	}
	r.typed[key{op, dt.ID}] = k
}

// RegisterTotalBinary — same shape, but out_valid = valid_a AND valid_b.
func RegisterTotalBinary[T Numeric](r *Registry, op Op, dt dtype.DataType, k TotalBinary[T]) {
	r.binary[key{op, dt.ID}] = func(kc Ctx, a, b *data.Column) (*data.Column, error) {
		if a.Len() != b.Len() {
			return nil, ErrLengthMismatch
		}
		av, err := data.Values[T](a)
		if err != nil {
			return nil, err
		}
		bv, err := data.Values[T](b)
		if err != nil {
			return nil, err
		}
		out := data.NewNumericOut[T](kc.Mem, a.Name(), a.DType(), a.Len())
		k(out.Vals, av, bv) // pure math; invalid lanes computed but never read
		valid, nulls := bitmap.And(kc.Mem, a.Validity(), b.Validity())
		return out.Finish(valid, nulls)
	}
	r.typed[key{op, dt.ID}] = k
}

// RegisterPartialBinary — Class B. The kernel owns its own nulls; the
// dispatcher ANDs them with the input-derived validity.
func RegisterPartialBinary[T Numeric](r *Registry, op Op, dt dtype.DataType, k PartialBinary[T]) {
	r.binary[key{op, dt.ID}] = func(kc Ctx, a, b *data.Column) (*data.Column, error) {
		n := a.Len()
		av, _ := data.Values[T](a)
		bv, _ := data.Values[T](b)

		in, _ := bitmap.And(kc.Mem, a.Validity(), b.Validity())
		inView := viewOf(in, n) // AllSet(n) when in == nil

		out := data.NewNumericOut[T](kc.Mem, a.Name(), a.DType(), n)
		okb := bitmap.NewBuilder(kc.Mem, n)
		k(out.Vals, okb, av, bv, inView) // kernel skips lanes where inView is 0
		ok := okb.Finish()

		valid, nulls := bitmap.And(kc.Mem, viewOf(ok, n), inView)
		ok.Release()
		if in != nil {
			in.Release()
		}
		return out.Finish(valid, nulls)
	}
}
```

Example Class B kernel — integer division, the one that would otherwise **panic**:

```go
// divIntScalar is a Class B Partial kernel. Go PANICS on integer division by
// zero, so it must (a) skip lanes whose inputs are null, and (b) emit a null
// rather than a panic for x/0, matching SQL and Polars.
func divInt64(dst []int64, ok *bitmap.Builder, a, b []int64, in bitmap.View) {
	for i := range a {
		if !in.Get(i) || b[i] == 0 {
			dst[i] = 0
			ok.PutBool(false)
			continue
		}
		dst[i] = a[i] / b[i]
		ok.PutBool(true)
	}
}
```

**Registration wiring** (one generated file per dtype; here the hand-written float64 one):

```go
// register_float64.go
func init() {
	r := Default()
	RegisterTotalCompareScalar(r, OpGt, dtype.Float64, TotalCompareScalar[float64](func(o *bitmap.Builder, a []float64, s float64) { gtF64(o, a, s) }))
	RegisterTotalCompareScalar(r, OpLe, dtype.Float64, TotalCompareScalar[float64](func(o *bitmap.Builder, a []float64, s float64) { leF64(o, a, s) }))
	RegisterTotalBinary(r, OpAdd, dtype.Float64, TotalBinary[float64](addF64))
	// …
}
```

### 5.5 `take` and `filter` — missing entirely from the docs

```go
// take.go — NO BUILD TAG.
//
// Deliberately SCALAR for v0.1. SIMD compaction without AVX-512 Compress is not
// a win, and portable `simd` has NO Compress/Expand at all — only archsimd does,
// amd64-only. The word-at-a-time TrailingZeros64 loop below is the standard fast
// scalar formulation and is within ~1.5x of a Compress implementation at
// moderate selectivity. archsimd Compress specialisation is a v0.2 item.

// FilterValues compacts src into dst using sel (one bit per src element).
// Returns the count written. len(dst) must be >= sel.CountSet().
// sel's bit offset is honoured via bitutil.NewBitmapWordReader.
func FilterValues[T any](dst, src []T, sel bitmap.View) int {
	raw, off, n := sel.Raw()
	if raw == nil { // all selected
		return copy(dst, src[:n])
	}
	out, base := 0, 0
	r := bitutil.NewBitmapWordReader(raw, off, n)
	for i := r.Words(); i > 0; i-- {
		w := r.NextWord()
		for w != 0 {
			j := bits.TrailingZeros64(w)
			dst[out] = src[base+j]
			out++
			w &= w - 1
		}
		base += 64
	}
	for i := r.TrailingBytes(); i > 0; i-- {
		bt, valid := r.NextTrailingByte()
		for j := 0; j < valid; j++ {
			if bt&(1<<uint(j)) != 0 {
				dst[out] = src[base+j]
				out++
			}
		}
		base += valid
	}
	return out
}

// FilterBits compacts a bitmap through a selection (used for validity and for
// boolean payloads).
func FilterBits(out *bitmap.Builder, src, sel bitmap.View) {
	if src.IsAllSet() {
		// Result is all-set; the caller drops the validity buffer entirely.
		return
	}
	raw, off, n := sel.Raw()
	if raw == nil {
		out.PutView(src)
		return
	}
	base := 0
	r := bitutil.NewBitmapWordReader(raw, off, n)
	for i := r.Words(); i > 0; i-- {
		w := r.NextWord()
		for w != 0 {
			j := bits.TrailingZeros64(w)
			out.PutBool(src.Get(base + j))
			w &= w - 1
		}
		base += 64
	}
	for i := r.TrailingBytes(); i > 0; i-- {
		bt, valid := r.NextTrailingByte()
		for j := 0; j < valid; j++ {
			if bt&(1<<uint(j)) != 0 {
				out.PutBool(src.Get(base + j))
			}
		}
		base += valid
	}
}

// TakeValues gathers src[idx[j]] into dst[j]. NullIndex produces the zero value;
// the caller's validity builder marks it null.
func TakeValues[T any](dst, src []T, idx []uint32) {
	for j, i := range idx {
		if i == data.NullIndex {
			var z T
			dst[j] = z
			continue
		}
		dst[j] = src[i]
	}
}
```

Column-level, dtype-erased:

```go
// data/compact.go

// FilterColumn produces a new, contiguous, offset-0 Column containing the
// selected rows. It handles the bit-unaligned validity of a sliced input
// transparently, because it only ever reads validity through bitmap.View.
func FilterColumn(mem memory.Allocator, c *Column, sel bitmap.View, n int) (*Column, error) {
	switch c.DType().Physical().ID {
	case dtype.TypeInt64:
		return filterFixed[int64](mem, c, sel, n)
	case dtype.TypeFloat64:
		return filterFixed[float64](mem, c, sel, n)
	case dtype.TypeInt32:
		return filterFixed[int32](mem, c, sel, n)
	// … the ten primitive widths, generated
	case dtype.TypeBoolean:
		return filterBoolean(mem, c, sel, n)
	case dtype.TypeString, dtype.TypeBinary:
		return filterString(mem, c, sel, n)
	}
	return nil, fmt.Errorf("ursus: filter unsupported for %s", c.DType())
}

func filterFixed[T arrow.FixedWidthType](mem memory.Allocator, c *Column, sel bitmap.View, n int) (*Column, error) {
	src, err := Values[T](c)
	if err != nil {
		return nil, err
	}
	out := NewNumericOut[T](mem, c.Name(), c.DType(), n)
	kernel.FilterValues(out.Vals, src, sel)

	v := c.Validity()
	if v.IsAllSet() {
		return out.Finish(nil, 0)
	}
	vb := bitmap.NewBuilder(mem, n)
	kernel.FilterBits(vb, v, sel)
	valid := vb.Finish()
	return out.Finish(valid, n-valid.CountSet())
}

// filterString: two passes so the data buffer is allocated exactly once.
// Pass 1 sums the selected byte lengths; pass 2 copies bytes and writes offsets.
func filterString(mem memory.Allocator, c *Column, sel bitmap.View, n int) (*Column, error) { /* … */ }

// FilterBatch applies one selection to every column.
func FilterBatch(mem memory.Allocator, b *Batch, sel *Selection) (*Batch, error) {
	cols := make([]*Column, len(b.cols))
	for i, c := range b.cols {
		out, err := FilterColumn(mem, c, sel.bits.View(), sel.count)
		if err != nil {
			for _, d := range cols[:i] {
				d.Release()
			}
			return nil, err
		}
		cols[i] = out
	}
	return NewBatch(b.schema, cols, sel.count)
}
```

---

## 6. `internal/physical` — the evaluator, never named in the docs

### 6.1 `Value` — a shape tag, because `len == 1` is ambiguous

```go
type Shape uint8

const (
	ShapeArray  Shape = iota // len == batch.Rows()
	ShapeScalar              // len == 1, broadcasts
)

// Value is a Column plus an explicit shape. The shape MUST be a tag and not
// inferred from len == 1, because a batch can legitimately have exactly one row,
// in which case an aggregate result and an elementwise result are
// indistinguishable by length. The docs never mention this.
type Value struct {
	Col   *data.Column
	Shape Shape
}
```

### 6.2 Compiled expression program

Resolution (name→ordinal, op→kernel, literal→scalar) happens **once at plan time**; evaluation is a flat walk over pre-resolved steps.

```go
// Program is a compiled expression: a flat instruction list over a register
// file. A flat program (rather than a closure tree) is chosen because it gives
// (a) a reusable, allocation-free register file per batch, (b) deterministic
// release points for intermediates, (c) a natural per-step profiling hook, and
// (d) the place where v0.2 kernel fusion and common-subexpression reuse land.
type Program struct {
	steps []step
	nregs int
	out   int
	name  string // output column name after Alias resolution
	dt    dtype.DataType
}

type stepKind uint8

const (
	stLoadCol stepKind = iota // regs[dst] = borrowed batch column `col`
	stLiteral                 // regs[dst] = pre-built 1-row literal column
	stBinary                  // regs[dst] = fn2(regs[a], regs[b])
	stBinaryScalar            // regs[dst] = fn2s(regs[a], scal)
	stUnary                   // regs[dst] = fn1(regs[a])
)

type step struct {
	kind   stepKind
	dst    int
	a, b   int
	col    int
	lit    *data.Column // shared across batches, retained once
	scal   data.Scalar
	fn2    kernel.BinaryFn
	fn2s   kernel.BinaryScalarFn
	fn1    kernel.UnaryFn
	shape  Shape
}

type Evaluator struct {
	reg  *kernel.Registry
	mem  memory.Allocator
	regs []Value // reused across batches
	own  []bool  // whether regs[i] is owned (must be released) or borrowed
}

func (ev *Evaluator) Run(ctx context.Context, p *Program, b *data.Batch) (Value, error) {
	if err := ctx.Err(); err != nil {
		return Value{}, err
	}
	if cap(ev.regs) < p.nregs {
		ev.regs = make([]Value, p.nregs)
		ev.own = make([]bool, p.nregs)
	}
	regs, own := ev.regs[:p.nregs], ev.own[:p.nregs]
	clear(regs)
	clear(own)
	defer func() {
		for i := range regs {
			if own[i] && i != p.out && regs[i].Col != nil {
				regs[i].Col.Release()
			}
		}
	}()

	kc := kernel.Ctx{Mem: ev.mem}
	for i := range p.steps {
		s := &p.steps[i]
		switch s.kind {
		case stLoadCol:
			regs[s.dst] = Value{Col: b.Column(s.col), Shape: ShapeArray}
			own[s.dst] = false // borrowed from the batch
		case stLiteral:
			regs[s.dst] = Value{Col: s.lit, Shape: ShapeScalar}
			own[s.dst] = false // owned by the Program, not the run
		case stBinaryScalar:
			c, err := s.fn2s(kc, regs[s.a].Col, s.scal)
			if err != nil {
				return Value{}, err
			}
			regs[s.dst], own[s.dst] = Value{Col: c, Shape: regs[s.a].Shape}, true
		case stBinary:
			c, err := s.fn2(kc, regs[s.a].Col, regs[s.b].Col)
			if err != nil {
				return Value{}, err
			}
			regs[s.dst], own[s.dst] = Value{Col: c, Shape: ShapeArray}, true
		case stUnary:
			c, err := s.fn1(kc, regs[s.a].Col)
			if err != nil {
				return Value{}, err
			}
			regs[s.dst], own[s.dst] = Value{Col: c, Shape: regs[s.a].Shape}, true
		}
	}
	out := regs[p.out]
	if !own[p.out] {
		out.Col.Retain() // caller always owns the result
	}
	return out, nil
}
```

### 6.3 `Compile` — the name→ordinal, expr→kernel resolution

```go
// Compile lowers a plan.ExprNode into a Program against `in`. All name
// resolution, dtype checking and kernel lookup happen HERE, once per query —
// never per batch. If Compile succeeds, Run cannot fail for a schema reason.
func Compile(reg *kernel.Registry, e plan.ExprNode, in *dtype.Schema) (*Program, error) {
	c := &compiler{reg: reg, schema: in}
	out, sh, dt, err := c.emit(e)
	if err != nil {
		return nil, err
	}
	_ = sh
	return &Program{steps: c.steps, nregs: c.nregs, out: out, name: c.outName(e), dt: dt}, nil
}

func (c *compiler) emit(e plan.ExprNode) (reg int, sh Shape, dt dtype.DataType, err error) {
	switch n := e.(type) {
	case *plan.ColumnRef:
		// CONTRACT (agent 1): Index is already resolved by the planner against
		// this operator's INPUT schema. The evaluator must never do a map
		// lookup per batch.
		if n.Index < 0 || n.Index >= c.schema.Len() {
			return 0, 0, dtype.DataType{}, fmt.Errorf("ursus: unresolved column %q", n.Name)
		}
		r := c.alloc()
		c.steps = append(c.steps, step{kind: stLoadCol, dst: r, col: n.Index})
		return r, ShapeArray, c.schema.Field(n.Index).Type, nil

	case *plan.Literal:
		r := c.alloc()
		lit, err := data.LiteralColumn(c.mem, n.DType, n.Value) // 1-row, built ONCE
		if err != nil {
			return 0, 0, dtype.DataType{}, err
		}
		c.steps = append(c.steps, step{kind: stLiteral, dst: r, lit: lit})
		return r, ShapeScalar, n.DType, nil

	case *plan.BinaryExpr:
		la, lsh, ldt, err := c.emit(n.Left)
		if err != nil {
			return 0, 0, dtype.DataType{}, err
		}
		rb, rsh, rdt, err := c.emit(n.Right)
		if err != nil {
			return 0, 0, dtype.DataType{}, err
		}
		// CONTRACT (agent 1): type coercion has ALREADY run. The physical layer
		// does NOT coerce. This keeps the kernel matrix N, not N-squared.
		if !ldt.Equal(rdt) {
			return 0, 0, dtype.DataType{}, fmt.Errorf(
				"ursus: internal: uncoerced binary op %v between %s and %s", n.Op, ldt, rdt)
		}
		op := kernel.OpOf(n.Op)
		r := c.alloc()

		// Literal fast path: Col("x").Gt(5) must NOT materialise a length-N
		// broadcast column.
		if rsh == ShapeScalar && lsh == ShapeArray {
			if fn, ok := c.reg.BinaryScalar(op, ldt.Physical().ID); ok {
				sc, err := data.ScalarOf(n.Right.(*plan.Literal))
				if err != nil {
					return 0, 0, dtype.DataType{}, err
				}
				c.steps = append(c.steps, step{kind: stBinaryScalar, dst: r, a: la, scal: sc, fn2s: fn})
				return r, ShapeArray, kernel.ResultType(op, ldt), nil
			}
		}
		fn, ok := c.reg.Binary(op, ldt.Physical().ID)
		if !ok {
			return 0, 0, dtype.DataType{}, &kernel.NotImplementedError{Op: op, DType: ldt}
		}
		c.steps = append(c.steps, step{kind: stBinary, dst: r, a: la, b: rb, fn2: fn})
		return r, ShapeArray, kernel.ResultType(op, ldt), nil

	case *plan.AliasExpr:
		return c.emit(n.Child)
	case *plan.UnaryExpr:  /* … */
	case *plan.CastExpr:   /* … */
	case *plan.AggExpr:
		return 0, 0, dtype.DataType{}, fmt.Errorf(
			"ursus: aggregate %v is not valid in this context (v0.1)", n.Op)
	}
	return 0, 0, dtype.DataType{}, fmt.Errorf("ursus: unsupported expression %T", e)
}
```

---

## 7. The physical `Operator` interface

### 7.1 Pull vs push — the decision

**v0.1 is pull.** `Operator.Next(ctx) (*Batch, error)`. Reasons: it is debuggable (a stack trace shows the whole pipeline), it needs zero goroutines so there is nothing to leak, and it is the natural shape for `CollectBatches`, where the *consumer* drives.

**But the operator implementations are push-ready.** The key move: `Filter` and `Project` are not `Operator`s. They are `BatchOp`s — stateless per-batch transforms — and a 20-line `stage` adapter turns a `BatchOp` into an `Operator`. In v0.2, the *same* `BatchOp` values are handed to the morsel scheduler with no modification.

```go
// Operator is the pull-based execution interface. v0.1 executes a single-
// threaded volcano over batches.
type Operator interface {
	Schema() *dtype.Schema
	// Next returns the next NON-EMPTY batch, or (nil, io.EOF) at end of stream.
	// Implementations MUST check ctx.Err() before doing work.
	// Ownership of the returned Batch transfers to the caller.
	Next(ctx context.Context) (*data.Batch, error)
	Close() error // idempotent
}

// BatchOp is a stateless, order-independent transform of one batch into one
// batch. THIS is the unit v0.2's morsel scheduler runs in parallel. Every
// operator that CAN be a BatchOp MUST be one, so that switching from pull to
// push touches no operator code, no kernel and no evaluator.
//
// Apply BORROWS `in` and OWNS its return. It must not Release(in).
// It may return a zero-row batch; the caller loops.
type BatchOp interface {
	Schema() *dtype.Schema
	Apply(ctx context.Context, in *data.Batch) (*data.Batch, error)
	Close() error
}

// Source is a batch producer that MUST be safe for concurrent Next. v0.1 has
// exactly one consumer; v0.2 has `threads` morsel workers. The mutex goes in
// NOW so that v0.2 does not discover a data race in a shipped scan.
type Source interface {
	Schema() *dtype.Schema
	Next(ctx context.Context) (*data.Batch, error)
	Close() error
}

// stage adapts a BatchOp into the pull chain. This 20-line type is the ENTIRE
// cost of having picked pull.
type stage struct {
	child Operator
	op    BatchOp
}

func (s *stage) Schema() *dtype.Schema { return s.op.Schema() }

func (s *stage) Next(ctx context.Context) (*data.Batch, error) {
	for {
		in, err := s.child.Next(ctx)
		if err != nil {
			return nil, err
		}
		out, err := s.op.Apply(ctx, in)
		in.Release()
		if err != nil {
			return nil, err
		}
		if out.Rows() == 0 {
			out.Release()
			continue // a fully-filtered batch must not surface as EOF
		}
		return out, nil
	}
}

func (s *stage) Close() error { return errors.Join(s.op.Close(), s.child.Close()) }
```

**Precisely what changes in v0.2 if push wins.** Only these:

1. `Source` is already the push-mode producer; nothing changes.
2. `BatchOp` is already the push-mode operator; nothing changes.
3. `stage` (20 lines) becomes unused for the parallel path and stays for `CollectBatches`.
4. `exec.Driver`'s `for { root.Next(ctx) }` loop is replaced by a morsel scheduler over the same `Pipeline` value.
5. Pipeline breakers — hash join build, hash agg, sort — get a `Sink`+`Source` pair instead of one `Operator`. **None of them exist in v0.1**, so nothing is rewritten.
6. ~~`Batch.meta.Partition` and `Batch.sel` are already present.~~ **[CORRECTED, step 5]** Neither exists. `data.Batch` is `{schema, cols, rows}` — no metadata, no selection vector. Step 5 parallelised the pipeline without either; ordering is carried by round-robin lane assignment rather than by a per-batch tag.

Zero kernels, zero `BatchOp`s, zero evaluator changes. That is the price of the bet, and it is paid up front.

### 7.2 Scan

```go
// ParquetScan is the leaf. It is a Source (mutex-guarded) as well as an
// Operator, so it drops straight into the v0.2 morsel scheduler.
type ParquetScan struct {
	mu     sync.Mutex
	f      *os.File
	pf     *file.Reader
	fr     *pqarrow.FileReader
	rr     pqarrow.RecordReader
	schema *dtype.Schema
	closed bool
	forceUnaligned bool // test hook; see §8
}

type ScanOpts struct {
	Mem        memory.Allocator
	BatchRows  int64 // default 65536: also bounds CANCELLATION LATENCY
	Projection []int // leaf column indices, from projection pushdown
	RowGroups  []int // from statistics-based row-group pruning
}

func NewParquetScan(ctx context.Context, path string, o ScanOpts) (*ParquetScan, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	pf, err := file.NewParquetReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	// BatchSize is what bounds how long a cancelled query keeps working:
	// pqarrow.RecordReader.Next() takes no context, so ctx is only observable
	// BETWEEN batches. 65536 rows is ~a few ms. This is an honest limitation of
	// arrow-go; the real fix is a ctx-aware parquet.ReaderProperties source.
	fr, err := pqarrow.NewFileReader(pf, pqarrow.ArrowReadProperties{
		BatchSize: o.BatchRows,
		Parallel:  false, // v0.1: parallelism belongs to the engine, not the reader
	}, o.Mem)
	if err != nil {
		pf.Close()
		f.Close()
		return nil, err
	}
	rr, err := fr.GetRecordReader(ctx, o.Projection, o.RowGroups)
	if err != nil {
		pf.Close()
		f.Close()
		return nil, err
	}
	sc, err := arrowx.SchemaFromArrow(rr.Schema()) // AGENT 3 contract
	if err != nil {
		return nil, err
	}
	return &ParquetScan{f: f, pf: pf, fr: fr, rr: rr, schema: sc,
		forceUnaligned: os.Getenv("URSUS_FORCE_UNALIGNED") == "1"}, nil
}

func (s *ParquetScan) Schema() *dtype.Schema { return s.schema }

func (s *ParquetScan) Next(ctx context.Context) (*data.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !s.rr.Next() {
			if err := s.rr.Err(); err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			return nil, io.EOF
		}
		rec := s.rr.RecordBatch()
		b, err := arrowx.BatchFromRecord(rec, s.schema) // zero-copy; retains buffers
		if err != nil {
			return nil, err
		}
		if b.Rows() == 0 {
			b.Release()
			continue
		}
		if s.forceUnaligned {
			b = data.DebugMisalign(b, 3) // see §8: run the whole suite bit-unaligned
		}
		return b, nil
	}
}

func (s *ParquetScan) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil // idempotent: Close is reached from both defer and error paths
	}
	s.closed = true
	s.rr.Release()
	return errors.Join(s.pf.Close(), s.f.Close())
}
```

> **Note on the `pqarrow` dependency wart.** `parquet/pqarrow` imports `arrow/flight` for one call, dragging in gRPC + protobuf. For v0.1 we accept it and record it; the fix (a vendored `GetRecordReader` path, or an upstream PR splitting the import) is a v0.2 item and does not affect any interface here.

### 7.3 Filter and Project — both `BatchOp`s

```go
// FilterOp is a BatchOp: stateless, order-independent, morsel-ready.
type FilterOp struct {
	ev     *Evaluator
	prog   *Program
	schema *dtype.Schema
	mem    memory.Allocator
}

func NewFilterOp(reg *kernel.Registry, mem memory.Allocator, pred plan.ExprNode, in *dtype.Schema) (*FilterOp, error) {
	p, err := Compile(reg, pred, in)
	if err != nil {
		return nil, err
	}
	if p.dt.ID != dtype.TypeBoolean {
		return nil, fmt.Errorf("ursus: filter predicate is %s, must be Boolean", p.dt)
	}
	return &FilterOp{ev: NewEvaluator(reg, mem), prog: p, schema: in, mem: mem}, nil
}

func (f *FilterOp) Schema() *dtype.Schema { return f.schema }

func (f *FilterOp) Apply(ctx context.Context, in *data.Batch) (*data.Batch, error) {
	v, err := f.ev.Run(ctx, f.prog, in)
	if err != nil {
		return nil, err
	}
	defer v.Col.Release()

	if v.Shape == ShapeScalar {
		// A constant predicate: all rows or none. The optimizer should have
		// folded this away, but the physical layer must not assume it did.
		val, ok := data.BoolScalar(v.Col)
		if ok && val {
			in.Retain()
			return in, nil
		}
		return in.Empty(), nil
	}

	// SQL WHERE semantics: a NULL predicate row is NOT selected.
	// sel = values AND validity. This is the single line where the difference
	// between "null is false" and "null is absent" is decided, and it is
	// centralised so it can be tested once.
	sel, err := data.SelectionFromBool(f.mem, v.Col)
	if err != nil {
		return nil, err
	}
	switch sel.Count() {
	case 0:
		return in.Empty(), nil
	case in.Rows():
		in.Retain() // zero-copy pass-through: nothing is copied when nothing is filtered
		return in, nil
	}
	return data.FilterBatch(f.mem, in, sel)
}

func (f *FilterOp) Close() error { return nil }
```

```go
// ProjectOp implements both Select (output schema = the exprs) and, with an
// identity prefix, WithColumns.
type ProjectOp struct {
	ev     *Evaluator
	progs  []*Program
	names  []string
	out    *dtype.Schema
	mem    memory.Allocator
}

func (p *ProjectOp) Schema() *dtype.Schema { return p.out }

func (p *ProjectOp) Apply(ctx context.Context, in *data.Batch) (*data.Batch, error) {
	cols := make([]*data.Column, len(p.progs))
	release := func(n int) {
		for _, c := range cols[:n] {
			if c != nil {
				c.Release()
			}
		}
	}
	for i, prog := range p.progs {
		v, err := p.ev.Run(ctx, prog, in)
		if err != nil {
			release(i)
			return nil, err
		}
		c := v.Col
		if v.Shape == ShapeScalar {
			// select-context broadcast: a scalar expands to the batch height.
			bc, err := data.Broadcast(p.mem, c, in.Rows())
			c.Release()
			if err != nil {
				release(i)
				return nil, err
			}
			c = bc
		}
		if c.Name() != p.names[i] {
			r := c.Renamed(p.names[i])
			c.Release()
			c = r
		}
		cols[i] = c
	}
	return data.NewBatch(p.out, cols, in.Rows())
}

func (p *ProjectOp) Close() error { return nil }
```

For the walking skeleton `Select(Col("a"), Col("b"))`, every program is a single `stLoadCol` step, so `Apply` retains two columns and builds a `Batch` header. Zero bytes copied. That is the projection fast path, and it falls out of the design rather than being special-cased.

---

## 8. `internal/exec` — the driver and the v0.2 seam

```go
package exec

// Pipeline is a DESCRIPTION, not a behaviour: a Source, a linear chain of
// BatchOps, and a Sink. v0.1 has exactly one Pipeline, executed by the pull
// driver. v0.2's morsel scheduler consumes the SAME value, running `Threads`
// workers that each pull from Source and run the same []BatchOp.
//
// Keeping Pipeline as data (one description, two executors) is why the pull/push
// choice is cheap to revisit.
type Pipeline struct {
	Source physical.Source
	Ops    []physical.BatchOp
}

// Sink is the push-side terminal. v0.2 requires Consume to be safe for
// concurrent use; v0.1's implementations already are (or are documented as
// single-consumer and used that way).
type Sink interface {
	Consume(ctx context.Context, b *data.Batch) error
	Finish(ctx context.Context) error
	Close() error
}

type Options struct {
	Mem       memory.Allocator
	Registry  *kernel.Registry
	BatchRows int  // default 65536
	Threads   int  // v0.1: ignored (always 1). v0.2: morsel workers.
	Strict    bool // WithStrictStreaming
}

type Driver struct {
	pipe Pipeline
	root physical.Operator
	opts Options
}

func NewDriver(p Pipeline, o Options) *Driver {
	root := physical.Operator(sourceOperator{p.Source})
	for _, op := range p.Ops {
		root = physical.NewStage(root, op)
	}
	return &Driver{pipe: p, root: root, opts: o}
}

// Result is the physical shape of a collected query. ursus.DataFrame wraps
// exactly this. It is a LIST OF BATCHES, not one concatenated frame — that is
// the other half of the open-question-#4 decision: contiguous Columns, chunked
// DataFrames. Concat is therefore O(1) at the frame level without a chunk
// dimension in any kernel.
type Result struct {
	Schema  *dtype.Schema
	Batches []*data.Batch
	Rows    int64
}

func (d *Driver) Collect(ctx context.Context) (res *Result, err error) {
	defer func() {
		if cerr := d.root.Close(); err == nil {
			err = cerr
		}
	}()
	out := make([]*data.Batch, 0, 8)
	var rows int64
	for {
		if err := ctx.Err(); err != nil {
			releaseAll(out)
			return nil, err
		}
		b, err := d.root.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			releaseAll(out)
			return nil, err
		}
		out = append(out, b)
		rows += int64(b.Rows())
	}
	return &Result{Schema: d.root.Schema(), Batches: out, Rows: rows}, nil
}

// Batches is the CollectBatches terminal. The `defer d.root.Close()` inside the
// iterator body is the important line: under Go 1.27 range-over-func, a `break`
// in the consumer makes yield return false, we return, and the defer runs. That
// closes the parquet file handle on early exit. Without it, `for b := range
// lf.CollectBatches(ctx) { break }` leaks an fd — and in v0.2, goroutines.
// Test this path with /debug/pprof/goroutineleak.
func (d *Driver) Batches(ctx context.Context) iter.Seq2[*data.Batch, error] {
	return func(yield func(*data.Batch, error) bool) {
		defer d.root.Close()
		for {
			if err := ctx.Err(); err != nil {
				yield(nil, err)
				return
			}
			b, err := d.root.Next(ctx)
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(b, nil) {
				b.Release() // consumer broke out; we still own this batch
				return
			}
		}
	}
}

// RunSink is the push-shaped terminal used by SinkParquet/SinkCSV. In v0.1 it
// is driven by the pull chain; in v0.2 the scheduler drives it directly.
func (d *Driver) RunSink(ctx context.Context, s Sink) (err error) {
	defer func() {
		if cerr := errors.Join(d.root.Close(), s.Close()); err == nil {
			err = cerr
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := d.root.Next(ctx)
		if errors.Is(err, io.EOF) {
			return s.Finish(ctx)
		}
		if err != nil {
			return err
		}
		err = s.Consume(ctx, b)
		b.Release()
		if err != nil {
			return err
		}
	}
}
```

**Public terminals** (in the root package, on `LazyFrame` — agent 1 owns the type, I own the body):

```go
func (lf *LazyFrame) Collect(ctx context.Context, opts ...CollectOption) (*DataFrame, error) {
	if lf.err != nil {
		return nil, lf.err
	}
	o := applyCollectOptions(opts)
	optimized, err := plan.Optimize(lf.plan, o.OptFlags)   // agent 1
	if err != nil {
		return nil, err
	}
	pipe, err := physical.Build(optimized, o)              // me
	if err != nil {
		return nil, err
	}
	res, err := exec.NewDriver(pipe, o.ExecOptions()).Collect(ctx)
	if err != nil {
		return nil, err
	}
	return newDataFrame(res), nil
}

func (lf *LazyFrame) CollectBatches(ctx context.Context, opts ...CollectOption) iter.Seq2[*DataFrame, error] {
	return func(yield func(*DataFrame, error) bool) {
		// … same build, then:
		for b, err := range exec.NewDriver(pipe, eo).Batches(ctx) {
			if !yield(dataFrameOf(b), err) {
				return
			}
		}
	}
}
```

---

## 9. The chunking / offset story — the #1 correctness trap

**The trap.** `array.NewSlice(arr, i, j)` is O(1) and bumps `data.offset`. **The offset applies to the validity bitmap too**, at *bit* granularity. `NullN()` is `bitutil.CountSetBits(nullBitmapBytes, offset, length)`. So a sliced array has a **bit-unaligned** validity bitmap, and a kernel that does `bm[i/8]` on it silently reads the wrong bits — no crash, no error, wrong answer.

**The four-layer defence:**

1. **Values are pre-sliced; validity cannot be.** `data.Values[T]` returns `arrow.GetValues[T](d, 1)`, which already applies the offset, capacity-clamped so an accidental `append` can't escape the window. Kernels therefore index values from 0 and never see the offset.

2. **Validity is only reachable as a `bitmap.View`.** There is no `Column.NullBitmapBytes()`. `View` has no `Bytes()` method; it has `Raw() (bits, off, n)` which returns all three *together*, so obtaining the bytes without the offset is not expressible. `Aligned() ([]byte, bool)` is the only way to get bare bytes, and it returns `false` when `off%8 != 0`.

3. **All bitmap arithmetic delegates to `arrow/bitutil`, which handles arbitrary offsets on both inputs and the output** (`BitmapAnd`/`Or`/`AndNot`/`Xor` pick an aligned fast path or a `BitmapWordReader` slow path). Element-wise iteration uses `bitutil.NewBitmapWordReader(bits, off, n)`. So the offset is handled by verified upstream code, not by our loops. The one thing we must get right ourselves is that the *output* buffer of `BitmapAnd` is **zeroed**, because it does read-modify-write on edge bytes — hence the explicit `clear()` in `bitmap.NewBuilder`.

4. **All kernel *outputs* are offset-0.** `bitmap.Builder` and `NumericOut` always start at bit/element 0. So an offset can only enter the pipeline from `Column.Slice`, `Batch.Slice`, or an incoming Arrow array — never from our own computation. Offsets therefore do not accumulate.

**And the part that actually prevents the bug from shipping:** a build-independent test hook.

```go
// DebugMisalign returns a batch whose columns all carry a `bits`-bit validity
// offset while representing exactly the same logical rows. Enabled by
// URSUS_FORCE_UNALIGNED=1, it makes the ENTIRE test suite exercise the
// bit-unaligned path. Presence of a slow path is not evidence that it is
// correct; running every test through it is.
func DebugMisalign(b *Batch, bits int) *Batch {
	pad, _ := Concat(nullPad(b.schema, bits), b) // prepend `bits` null rows
	return pad.Slice(bits, b.Rows())             // then slice them off: offset = bits
}
```

**On chunking (open question #4), stated as a decision with its cost:**

A `Column` is **one contiguous buffer set**. It is not a chunk list. The docs' proposal — "chunked internally, `Rechunk()` before kernels, and measure" — has the worst of both worlds: you pay the chunk dimension in the type *and* you pay the copy anyway. Chunking exists for exactly one reason (O(1) `Concat`/`vstack`), and a batch-at-a-time engine already has a name for a chunk: it's a `Batch`. So chunking lives at the `DataFrame`/`Result` level (`Batches []*Batch`), where `Concat` is an append of two slices, and every kernel signature stays `[]T`.

`Series[T].Chunks()` and `Rechunk()` remain in the API and return `1` / `self`, because `DataFrame.Column[T](name)` is the only place that must materialise across batches, and it does so explicitly and visibly.

**Cost of reversal**, deliberately kept small: if real chunked columns are ever needed *inside* one batch, we add `func (c *Column) Runs() iter.Seq[*Column]` and change the *evaluator* to loop over runs. Kernels are unaffected because they already take contiguous `[]T` — which is precisely why kernels take `[]T` and not `*Column`. Two files change: `data/column.go` and `physical/eval.go`.

---

## 10. The SIMD build and test scheme

**The problem (D14).** "Scalar twin behind `//go:build !goexperiment.simd` + a differential test asserting identical results" is unsatisfiable: with those tags exactly one implementation is linked in any build, so there is nothing to compare.

**The fix.** Three files per kernel family, with the tag on the *binding*, not the *implementation*:

| File | Build tag | Contents |
|---|---|---|
| `cmp_scalar.go` | **none** | `gtF64Scalar` etc. Linked in **every** build. |
| `cmp_simd.go` | `goexperiment.simd` | `gtF64SIMD` etc. |
| `movemask_simd.go` | `goexperiment.simd` | `packMask64`, `packMask32` — the 4-way width switch, written once. |
| `cmp_dispatch.go` | **none** | `var gtF64 = gtF64Scalar` + `URSUS_KERNELS` override. |
| `cmp_dispatch_simd.go` | `goexperiment.simd` | `init() { gtF64 = pickF64(gtF64SIMD, gtF64Scalar) }` |
| `cmp_dispatch_nosimd.go` | `!goexperiment.simd` | empty `init()` |
| `cmp_diff_test.go` | `goexperiment.simd` | the differential test, referencing **both** |

Both implementations are in the SIMD binary, so the test is writable. `URSUS_KERNELS=scalar` makes one binary benchmark both. Expect ~4x kernel code size (the compiler clones every `simd`-using function per width: `@simd128`/`@simd256`/`@simd512`/`@simd0` appear in symbol tables and stack traces) — this is a documented, accepted cost, and it is why we hold the `simd`-using surface to comparison/arithmetic and keep `take`/`filter` scalar.

### CI matrix

| Job | `GOEXPERIMENT` | `GODEBUG` | Extra | What it catches |
|---|---|---|---|---|
| `baseline` | *(unset)* | — | | The library **must** build, vet and pass with no experiment. Scalar-only path. |
| `simd-512` | `simd` | `simd=512` | | Native AVX-512. 8 float64 lanes = exactly 1 bitmap byte. Differential tests run. |
| `simd-256` | `simd` | `simd=256` | | `Mask64x4` arm of `packMask64`; 4 lanes ≠ 1 byte. |
| `simd-128` | `simd` | `simd=128` | | `Mask64x2` arm; **2 lanes**. This is the width where the docs' `bm[i/8] = bits` idiom is flatly wrong — defect D14b. |
| `simd-emulated` | `simd` | `simd=0` | | `ToArch()` returns `simd/internal/bridge.Mask64s`, which we cannot name → exercises the `default:` fallback arm. A single-case type assertion panics here. |
| `unaligned` | `simd` | `simd=512` | `URSUS_FORCE_UNALIGNED=1` | Every batch's validity offset by 3 bits. The whole suite runs bit-unaligned. |
| `checked-alloc` | `simd` | `simd=512` | `URSUS_ALLOC=checked` | `memory.CheckedAllocator` + `AssertSize(t, 0)` in `TestMain`: leaks and double-frees. |
| `race` | `simd` | `simd=512` | `-race` | Driver, `Source` concurrency, teardown. |
| `leak` | `simd` | `simd=512` | `/debug/pprof/goroutineleak` | Early `break` out of `CollectBatches`, cancelled `ctx`. |
| `arm64` | `simd` | — | `GOARCH=arm64` | Portable `simd` only; `archsimd` amd64 files excluded by build tag → `packMask64` falls to the portable arm. |

`GOEXPERIMENT=simd` is **mandatory for every build, test and vet** of the SIMD files — without it, `simd` fails with "build constraints exclude all Go files". So it goes in `Makefile`/`go.work` guidance, not in developers' heads.

### The `archsimd` budget

Portable `simd` is missing things we will need: `Int64s`/`Uint64s` have no `Mul`, `Min`, `Max`; `Int64s` has no `ShiftAllRight`; `Uint8s` has **no ordering comparisons** at all (only `Equal`/`NotEqual`); and there is **no `Compress`/`Expand`** — the AVX-512 filter/take primitive. `archsimd` has all of these. Plan: an `internal/kernel/arch_amd64.go` layer behind `//go:build goexperiment.simd && amd64`, gated at runtime by `archsimd.X86.AVX512()`, covering exactly four things — validity popcount/AND-fold, `Compress`-based compaction, string-view 4-byte prefix compare, and hash-probe vectors. Everything else stays portable. Each `archsimd` kernel gets the same three-file scalar/SIMD/dispatch treatment and the same differential test.

---

## 11. Contracts required from the other designers

### From agent 1 (`internal/plan`)

The physical layer type-switches on expression nodes, and **Go cannot type-switch on unexported types from another package**. So `plan` must **export** its node structs (they are in `internal/`, so this is not public API):

```go
package plan

type ExprNode interface{ exprNode() }

type ColumnRef struct {
	Name  string
	Index int // REQUIRED: resolved by the planner against the operator's INPUT
	           // schema. The evaluator must NEVER do a map lookup per batch.
	           // Index == -1 is an error, not a fallback.
}
type Literal    struct{ Value any; DType dtype.DataType } // Value == nil ⇒ typed NULL
type BinaryExpr struct{ Op BinOp; Left, Right ExprNode }
type UnaryExpr  struct{ Op UnOp; Child ExprNode }
type CastExpr   struct{ Child ExprNode; To dtype.DataType; Strict bool }
type AliasExpr  struct{ Child ExprNode; Name string }
type AggExpr    struct{ Op AggOp; Child ExprNode }
```

Hard requirements:

1. **`BinOp` distinguishes `Eq` from `EqMissing`** and `Ne` from `NeMissing`. They are different kernel families with opposite null semantics; a single `Eq` with a flag buried elsewhere will get lost.
2. **`BinOp` distinguishes `Div` from `FloorDiv` from `Mod`.** Integer `Div` is a Class B (partial) kernel; float `Div` is Class A.
3. **Type coercion has already run.** By the time `Compile` sees a `BinaryExpr`, both children have identical `DataType`s — except that a `Literal` child may remain unconverted so the scalar fast path can pick it up, in which case the literal's `DType` must still equal the other side's. **The physical layer does not coerce.** Mismatched dtypes are an *internal* error, not a runtime cast. This is what keeps the kernel matrix N instead of N².
4. **Expression expansion has already run.** `Col("a", "b")`, regex, `All()`, selectors — all expanded into individual nodes by the planner. `Compile` produces exactly **one** output column per `ExprNode`.
5. **Alias resolution is final.** Output column names are on `AliasExpr` or derivable from the root; `Compile` does not implement Polars' name-derivation rules.
6. **Plan-node shape needed for `physical.Build`:** `plan.Scan{Path, Projection []int, RowGroups []int, Predicate ExprNode}`, `plan.Filter{Input, Predicate}`, `plan.Project{Input, Exprs, Names}`. Projection and predicate pushdown must have deposited `Projection`/`RowGroups` on the scan node; the physical scan does not re-derive them.
7. `AggExpr` may appear only under an aggregate context node. `Compile` errors on it elsewhere.

### From agent 3 (`internal/dtype`, `internal/arrowx`)

```go
package dtype
// DataType, TypeID, Field, Schema, exactly as specified in api §2.
// REQUIRED beyond that:
func (d DataType) Physical() DataType // Categorical→Uint32, Date→Int32,
                                      // Datetime→Int64, Duration→Int64,
                                      // Time→Int64, Enum→Uint32
func (d DataType) Equal(o DataType) bool
// Schema must be a POINTER type or cheaply copyable; the physical layer stores
// *Schema on every Batch.

package arrowx
func ToArrowType(d dtype.DataType) (arrow.DataType, error)
func FromArrowType(a arrow.DataType) (dtype.DataType, error)
func SchemaFromArrow(s *arrow.Schema) (*dtype.Schema, error)
func BatchFromRecord(r arrow.RecordBatch, s *dtype.Schema) (*data.Batch, error) // zero-copy, retains
func RecordFromBatch(b *data.Batch) (arrow.RecordBatch, error)                  // zero-copy
func Allocator() memory.Allocator // 64-byte aligned; GoAllocator by default,
                                  // memory.NewCheckedAllocator under URSUS_ALLOC=checked
```

Hard requirements:

1. **`dtype` and `arrowx` must not import the root `ursus` package.** The root package aliases *down*, never the reverse; otherwise the whole engine is unbuildable.
2. **`Physical()` must be total for every `TypeID` we ship**, because kernel dispatch keys on `dt.Physical().ID`. A `Categorical` that does not map to `Uint32` silently fails to find a kernel.
3. **Do not import `arrow/cdata`, and do not build with `-tags mallocator` or `-tags ccalloc`.** Those are the only three ways buffers stop being plain Go heap; the entire "hide refcounting behind a GC-managed API" decision depends on avoiding them.
4. `Allocator()` must guarantee 64-byte alignment (`GoAllocator` already does, by over-allocating and shifting) so SIMD loops need no scalar prologue.
5. `BatchFromRecord` must **retain** the incoming buffers, so the physical layer's ownership rules hold even if the reader releases.

---

## 12. What I deliberately left out, and where it slots in

| Deferred | The seam that is already in place |
|---|---|
| **Morsel parallelism** | `Source` is already documented and implemented as concurrency-safe (mutex in `ParquetScan`, added now so v0.2 doesn't discover a race in shipped code). `BatchOp` is already stateless and order-independent. `Pipeline` is already data rather than behaviour. v0.2 adds a scheduler that runs N workers over the same `Pipeline`. |
| **Spilling** | `kernel.Ctx` is a struct with one field today; v0.2 adds `Reservation *exec.MemReservation` without changing a single kernel signature. `Batch.meta.Partition` already exists for radix-partitioned spill. Spill is a `Sink`+`Source` pair over a partition file — both interfaces exist. |
| **Hash join / hash agg** | These are pipeline **breakers**, so they are *not* `BatchOp`s: they are a `Sink` (build side) plus a `Source` (probe/emit side). `Pipeline` already models a `Sink`, so a breaker splits one `Pipeline` into two. `kernel.Accumulator` already has `Merge`, which is what parallel and spilled aggregation require. The group-key null sentinel (§2.2) is specified now because it constrains `Column`'s accessors. |
| **Sort / top-k** | Also a breaker. `OrderKeyF64` (§2.1) already defines the total order, so the sort comparator will not accidentally reuse the IEEE comparison kernels. External merge sort is a `Sink`+`Source` pair. |
| **Streaming sinks** (`SinkParquet`, `SinkCSV`) | `exec.Sink` and `Driver.RunSink` exist and work today; only the sink implementations are missing. |
| **Late materialisation** | `Batch.sel *Selection` and `Selection`'s dual bitmap/index representation are already in place, with `mustDense()` guarding the v0.1 invariant. v0.2's `FilterOp` stops compacting and leaves `sel` attached; `ProjectOp` materialises only the projected columns. |
| **AVX-512 `Compress` compaction** | `FilterValues`/`TakeValues` are already isolated single functions with the same three-file scalar/SIMD/dispatch pattern ready to apply. |
| **`Utf8View`** | `StringAccessor` already has a `LayoutView` case returning `ErrUnsupportedLayout`. Adding it is one arm per string kernel plus `take` support; zero call sites. |
| **Nested types (List/Struct/Map)** | `Column` wraps `arrow.ArrayData`, which already carries `Children()`. Nothing structural blocks them; only kernels are missing. Struct validity is `parent AND child` — noted, not implemented. |
| **Categorical/Enum, Decimal128** | `ArrayData.Dictionary()` exists; `arrow.FixedWidthType` already admits the decimal types, so `NumericOut[decimal128.Num]` compiles today. |
| **`Float16`, `Int128`/`Uint128`** | Not in the `Numeric` constraint. Adding them is one line plus generated kernels. |
| **Cancellation *inside* a parquet read** | Honestly limited: `pqarrow.RecordReader.Next()` takes no context. Mitigated by `BatchSize` bounding cancellation latency to one batch (~ms at 65 536 rows). The real fix — a ctx-aware `parquet.ReaderProperties` source — is upstream work and changes nothing here. |

---

## 13. Implementation order

1. `internal/bitmap` — `View`, `Builder`, `And`/`Or`/`Materialize`. **Test first**, including every length from 0 to 129 and every offset from 0 to 7. This package is the foundation of every correctness claim.
2. `internal/data` — `Column`, `NumericOut`, `NewBooleanColumn`, `Batch`, `Scalar`. Round-trip tests against `arrow-go` arrays.
3. `internal/kernel` — the comparison family (scalar first, then SIMD, then the differential test), `Registry`, the three trampolines, `FilterValues`/`FilterBits`.
4. `internal/data/compact.go` — `FilterColumn`, `FilterBatch` for the primitive widths + `Boolean` + `Utf8`.
5. `internal/physical` — `Value`/`Program`/`Compile`/`Evaluator`, then `Operator`/`BatchOp`/`Source`/`stage`.
6. `internal/physical` — `ParquetScan`, `FilterOp`, `ProjectOp`, `Build`.
7. `internal/exec` — `Driver`, `Collect`, `Batches`, `RunSink`.
8. `internal/data` — `Series[T]`, `TypedColumn`, `StringAccessor`. (Last: it depends on nothing else and blocks nothing.)
9. Wire `LazyFrame.Collect` / `CollectBatches`, the end-to-end test, and the eight-job CI matrix.

Steps 1–4 are where every silent-wrongness bug lives; they should account for roughly two-thirds of the test code.

---

### Critical Files for Implementation

- `/home/beck/GolandProjects/ursus/internal/bitmap/bitmap.go` — `View`, `Builder`, `And`/`Materialize`. Every null-correctness and offset-correctness claim in this design bottoms out here.
- `/home/beck/GolandProjects/ursus/internal/data/column.go` — `Column` over `arrow.ArrayData`, `Values[T]`, `Validity()`, `NumericOut[T]`, `NewBooleanColumn`. Resolves open questions #3 and #4.
- `/home/beck/GolandProjects/ursus/internal/kernel/registry.go` — the `Total`/`Partial`/`Missing`/`Kleene` kernel classes and the trampolines that own all null propagation. This file is the fix for defect D13.
- `/home/beck/GolandProjects/ursus/internal/kernel/movemask_simd.go` — the width-safe `packMask64`/`packMask32`. The only place permitted to call `ToArch()`; fixes defect D14b.
- `/home/beck/GolandProjects/ursus/internal/physical/eval.go` — `Program`/`Compile`/`Evaluator`, the component the docs never name, and the file that would change if open question #4 is ever reversed.
- `/home/beck/GolandProjects/ursus/internal/physical/operator.go` — `Operator`/`BatchOp`/`Source`/`stage`. The pull-vs-push seam; the only ~150 lines that change in v0.2 if push wins.