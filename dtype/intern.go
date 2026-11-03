package dtype

import (
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unsafe"
	"weak"
)

// typeExt is the payload of a parameterised or nested type.
//
// Instances are INTERNED: exactly one *typeExt exists per distinct payload, so
// pointer equality is semantic equality and DataType stays comparable with ==.
// Nothing here is ever mutated after interning.
//
// The table holds each payload WEAKLY (step 427): a payload no type holds is
// collected, and a cleanup deletes its entry. It held them strongly, so every
// struct, list, enum and zone ever built stayed, and a service reading files of
// varied schemas grew without end. Equality holds still: two types held at once
// share their payload, since while one is held its entry answers the other, and a
// payload nobody holds has nothing to compare with. A parent's key names its inner
// types by address, which is safe for the same reason: a held parent holds its
// inner types, so their addresses are not reused while its entry answers.
type typeExt struct {
	tz     string   // Datetime
	inner  DataType // List, Array
	size   int      // Array
	fields []Field  // Struct
	cats   []string // Enum
	catIdx map[string]uint32
}

var (
	internMu sync.RWMutex
	interned = map[string]weak.Pointer[typeExt]{}
)

// lookup returns the payload interned under k, if it is still held.
func lookup(k string) *typeExt {
	if w, ok := interned[k]; ok {
		return w.Value()
	}
	return nil
}

// forget is the cleanup of a payload collected: its entry goes, unless the key has
// been interned again since, as another payload.
func forget(e entryRef) {
	internMu.Lock()
	defer internMu.Unlock()
	if interned[e.key] == e.w {
		delete(interned, e.key)
	}
}

type entryRef struct {
	key string
	w   weak.Pointer[typeExt]
}

// intern returns the canonical *typeExt equal to e, registering e if it is new.
//
// Types are constructed once per query at plan-build time, never per row, so the
// mutex is not on any hot path. The read lock covers the overwhelmingly common
// case of re-deriving a type that already exists.
func intern(e *typeExt) *typeExt {
	k := e.key()

	internMu.RLock()
	got := lookup(k)
	internMu.RUnlock()
	if got != nil {
		return got
	}

	if len(e.cats) > 0 {
		e.catIdx = make(map[string]uint32, len(e.cats))
		for i, c := range e.cats {
			if _, dup := e.catIdx[c]; !dup {
				e.catIdx[c] = uint32(i)
			}
		}
	}

	internMu.Lock()
	defer internMu.Unlock()
	if got := lookup(k); got != nil { // lost the race; use the winner
		return got
	}
	w := weak.Make(e)
	interned[k] = w
	runtime.AddCleanup(e, forget, entryRef{k, w})
	return e
}

// key is the canonical identity of a payload.
//
// It must be injective over payloads that should compare unequal. Two rules make
// it so:
//
//   - Every variable-length part is written length-prefixed, so no rearrangement
//     of contents can produce the same string ("ab"+"c" vs "a"+"bc").
//   - Nested types are encoded by identity (typeKey) rather than by String().
//     String() is for humans and is deliberately allowed to be pretty; using it
//     here would make interning depend on rendering choices such as whether Enum
//     joins its categories with ", " or ",".
func (e *typeExt) key() string {
	var b strings.Builder

	writeLP(&b, "tz", e.tz)

	b.WriteString("|inner:")
	b.WriteString(typeKey(e.inner))

	b.WriteString("|size:")
	b.WriteString(strconv.Itoa(e.size))

	b.WriteString("|fields:")
	b.WriteString(strconv.Itoa(len(e.fields)))
	for _, f := range e.fields {
		writeLP(&b, "", f.Name)
		b.WriteByte(':')
		b.WriteString(typeKey(f.Type))
		if f.Nullable {
			b.WriteString(":n")
		} else {
			b.WriteString(":.")
		}
	}

	b.WriteString("|cats:")
	b.WriteString(strconv.Itoa(len(e.cats)))
	for _, c := range e.cats {
		writeLP(&b, "", c)
	}

	return b.String()
}

// typeKey encodes a DataType's identity. The ext pointer is safe to use because
// it is always already interned, so equal payloads have equal pointers.
func typeKey(d DataType) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(int(d.id)))
	b.WriteByte('.')
	b.WriteString(strconv.Itoa(int(d.unit)))
	b.WriteByte('.')
	b.WriteString(strconv.Itoa(int(d.prec)))
	b.WriteByte('.')
	b.WriteString(strconv.Itoa(int(d.scale)))
	b.WriteByte('.')
	b.WriteString(strconv.FormatUint(uint64(uintptr(unsafe.Pointer(d.ext))), 16))
	return b.String()
}

// writeLP writes a length-prefixed string: "|<tag>:<len>:<s>".
func writeLP(b *strings.Builder, tag, s string) {
	b.WriteByte('|')
	b.WriteString(tag)
	b.WriteByte(':')
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
}
