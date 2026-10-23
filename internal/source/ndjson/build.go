package ndjson

import (
	"errors"
	"slices"
	"strconv"
	"unsafe"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// builder gathers one column's values, a row at a time, in the type it was made for:
// one of the types inference gives, or a nested one of them. It decodes each value
// straight out of the line, building no tree of it.
type builder interface {
	// decode decodes the value at p, which is there and is not a null, and moves
	// past it.
	decode(p *parser) error
	addNull()
	// size readies the builder for rows values.
	size(rows int)
	finish(name string) *data.Column
}

// newBuilder is a builder for a column of type t, one inference gives or any a given
// schema declares: it builds the column in t's natural type, which a cast then makes
// t, and refuses a value as one t cannot take.
func newBuilder(t dtype.DataType) (builder, error) {
	want := t.String()
	switch natural(t).ID() {
	case dtype.TypeNull:
		return &nullBuilder{want: want}, nil
	case dtype.TypeBool:
		return &boolBuilder{want: want, vals: bitmap.NewBuilder(0)}, nil
	case dtype.TypeInt64:
		return &intBuilder{want: want}, nil
	case dtype.TypeInt128:
		return &int128Builder{want: want}, nil
	case dtype.TypeFloat64:
		return &floatBuilder{want: want}, nil
	case dtype.TypeString:
		return &stringBuilder{offs: []int32{0}}, nil
	case dtype.TypeList:
		e, err := newBuilder(t.Inner())
		if err != nil {
			return nil, err
		}
		return &listBuilder{want: want, elem: e, offs: []int32{0}}, nil
	case dtype.TypeStruct:
		s := &structBuilder{want: want, at: map[string]int{}}
		for i, f := range t.Fields() {
			b, err := newBuilder(f.Type)
			if err != nil {
				return nil, err
			}
			s.names, s.fields = append(s.names, f.Name), append(s.fields, b)
			s.at[f.Name] = i
		}
		s.seen = make([]uint32, len(s.fields))
		return s, nil
	}
	return nil, uerr.Internalf("ndjson: no builder for %s", t)
}

// decodeValue decodes the value at p into b: a null as a null, anything else as b
// takes it.
func decodeValue(p *parser, b builder) error {
	if p.pos >= len(p.s) {
		return p.fail("a value was expected")
	}
	if p.s[p.pos] == 'n' {
		if err := p.lit("null"); err != nil {
			return err
		}
		b.addNull()
		return nil
	}
	err := b.decode(p)
	if err == errNull {
		b.addNull()
		return nil
	}
	return err
}

// mismatch refuses v in a column of type t, as Polars refuses it: "cannot parse 'x'
// (string) as Int64". A type inference gave, and not a schema, may be the first
// lines' and not the file's.
func mismatch(v value, t string, inferred bool) error {
	e := refusal(uerr.KindSchema, v, t)
	if inferred {
		e.Hint("a later line's type was not among the first lines'; read more of them, or give the schema")
	}
	return e
}

// refusal is the error refusing v as a t.
func refusal(k uerr.Kind, v value, t string) *uerr.Error {
	text := v.text
	switch v.kind {
	case kBool:
		text = strconv.FormatBool(v.b)
	case kArray:
		text = "[…]"
	case kObject:
		text = "{…}"
	}
	return uerr.New(k, "read_ndjson", "cannot parse '%s' (%s) as %s", text, v.kind.what(), t)
}

// str views b as a string without copying, for a strconv parser, which reads the
// string and does not keep it.
func str(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

// owned is s for a column to take: s itself, or a copy when more than an eighth of
// its allocation is unused, which the column would hold for as long as it lives.
func owned[T any](s []T) []T {
	if cap(s)-len(s) > len(s)/8 {
		return slices.Clip(slices.Clone(s))
	}
	return s
}

// nulls records a column's null rows, for its validity: none is every row valid, with
// no bitmap.
type nulls struct {
	n    int
	rows []int32
}

func (v *nulls) valid() { v.n++ }
func (v *nulls) null()  { v.rows = append(v.rows, int32(v.n)); v.n++ }

func (v *nulls) view() bitmap.View {
	if len(v.rows) == 0 {
		return bitmap.AllSet(v.n)
	}
	b := bitmap.NewBuilder(v.n)
	at := 0
	for _, r := range v.rows {
		b.AppendMany(true, int(r)-at)
		b.Append(false)
		at = int(r) + 1
	}
	b.AppendMany(true, v.n-at)
	return b.Finish()
}

type nullBuilder struct {
	want string
	n    int
}

func (b *nullBuilder) decode(p *parser) error { return p.refuse(b.want) }
func (b *nullBuilder) addNull()               { b.n++ }
func (b *nullBuilder) size(int)               {}
func (b *nullBuilder) finish(name string) *data.Column {
	return data.NewNull(name, dtype.Null, b.n)
}

type boolBuilder struct {
	want  string
	vals  *bitmap.Builder
	valid nulls
}

func (b *boolBuilder) decode(p *parser) error {
	switch p.s[p.pos] {
	case 't':
		if err := p.lit("true"); err != nil {
			return err
		}
		b.vals.Append(true)
	case 'f':
		if err := p.lit("false"); err != nil {
			return err
		}
		b.vals.Append(false)
	default:
		return p.refuse(b.want)
	}
	b.valid.valid()
	return nil
}
func (b *boolBuilder) addNull()      { b.vals.Append(false); b.valid.null() }
func (b *boolBuilder) size(rows int) { b.vals = bitmap.NewBuilder(rows) }
func (b *boolBuilder) finish(name string) *data.Column {
	return data.NewBool(name, b.vals.Finish(), b.valid.view())
}

type intBuilder struct {
	want  string
	vals  []int64
	valid nulls
}

func (b *intBuilder) decode(p *parser) error {
	start := p.pos
	if !numeric(p.s[start]) {
		return p.refuse(b.want)
	}
	k, err := p.scanNumber()
	if err != nil {
		return err
	}
	x, ok := parseInt64(p.s[start:p.pos])
	if k != kInt || !ok {
		p.pos = start
		return p.refuse(b.want)
	}
	b.vals = append(b.vals, x)
	b.valid.valid()
	return nil
}
func (b *intBuilder) addNull()      { b.vals = append(b.vals, 0); b.valid.null() }
func (b *intBuilder) size(rows int) { b.vals = make([]int64, 0, rows) }
func (b *intBuilder) finish(name string) *data.Column {
	return data.NewFixedOwned(name, dtype.Int64, owned(b.vals), b.valid.view())
}

// parseInt64 is an integer's text as an Int64: its digits summed when there are too
// few to overflow, as most are, else strconv's, false past an Int64.
func parseInt64(text []byte) (int64, bool) {
	d := text
	if d[0] == '-' {
		d = d[1:]
	}
	if len(d) <= 18 {
		var x int64
		for _, c := range d {
			x = x*10 + int64(c-'0')
		}
		if text[0] == '-' {
			x = -x
		}
		return x, true
	}
	x, err := strconv.ParseInt(str(text), 10, 64)
	return x, err == nil
}

type int128Builder struct {
	want  string
	vals  []i128.Int128
	valid nulls
}

func (b *int128Builder) decode(p *parser) error {
	start := p.pos
	if !numeric(p.s[start]) {
		return p.refuse(b.want)
	}
	k, err := p.scanNumber()
	if err != nil {
		return err
	}
	x, ok := i128.Parse(str(p.s[start:p.pos]))
	if k != kInt || !ok {
		p.pos = start
		return p.refuse(b.want)
	}
	b.vals = append(b.vals, x)
	b.valid.valid()
	return nil
}
func (b *int128Builder) addNull()      { b.vals = append(b.vals, i128.Int128{}); b.valid.null() }
func (b *int128Builder) size(rows int) { b.vals = make([]i128.Int128, 0, rows) }
func (b *int128Builder) finish(name string) *data.Column {
	return data.NewFixedOwned(name, dtype.Int128, owned(b.vals), b.valid.view())
}

type floatBuilder struct {
	want  string
	vals  []float64
	valid nulls
}

func (b *floatBuilder) decode(p *parser) error {
	if !numeric(p.s[p.pos]) {
		return p.refuse(b.want)
	}
	f, err := p.scanFloat()
	if err != nil {
		return err
	}
	b.vals = append(b.vals, f)
	b.valid.valid()
	return nil
}
func (b *floatBuilder) addNull()      { b.vals = append(b.vals, 0); b.valid.null() }
func (b *floatBuilder) size(rows int) { b.vals = make([]float64, 0, rows) }
func (b *floatBuilder) finish(name string) *data.Column {
	return data.NewFixedOwned(name, dtype.Float64, owned(b.vals), b.valid.view())
}

// stringBuilder takes any value as its text, as Polars reads a key whose values are
// text and anything else: see appendText.
type stringBuilder struct {
	offs  []int32
	chars []byte
	valid nulls
}

func (b *stringBuilder) decode(p *parser) error {
	var err error
	if b.chars, err = p.appendText(b.chars); err != nil {
		return err
	}
	b.offs = append(b.offs, int32(len(b.chars)))
	b.valid.valid()
	return nil
}
func (b *stringBuilder) addNull() {
	b.offs = append(b.offs, int32(len(b.chars)))
	b.valid.null()
}
func (b *stringBuilder) size(rows int) {
	b.offs = make([]int32, 1, rows+1)
}
func (b *stringBuilder) finish(name string) *data.Column {
	return data.NewStringOwned(name, owned(b.offs), owned(b.chars), b.valid.view())
}

type listBuilder struct {
	want  string
	elem  builder
	offs  []int32
	valid nulls
	n     int32
}

func (b *listBuilder) decode(p *parser) error {
	if p.s[p.pos] != '[' {
		return p.refuse(b.want)
	}
	p.pos++
	p.space()
	if p.pos < len(p.s) && p.s[p.pos] == ']' {
		p.pos++
		b.offs = append(b.offs, b.n)
		b.valid.valid()
		return nil
	}
	for {
		p.space()
		if err := decodeValue(p, b.elem); err != nil {
			return err
		}
		b.n++
		p.space()
		if p.pos >= len(p.s) {
			return p.fail("the array is not closed")
		}
		switch p.s[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			b.offs = append(b.offs, b.n)
			b.valid.valid()
			return nil
		default:
			return p.fail("',' or ']' was expected")
		}
	}
}
func (b *listBuilder) addNull()      { b.offs = append(b.offs, b.n); b.valid.null() }
func (b *listBuilder) size(rows int) { b.offs = make([]int32, 1, rows+1) }
func (b *listBuilder) finish(name string) *data.Column {
	return data.NewList(name, owned(b.offs), b.elem.finish("item"), b.valid.view())
}

// structBuilder takes an object's keys as its fields, a key missing a null and one
// it does not know skipped; of a key given twice, the first, as Polars reads it.
type structBuilder struct {
	want   string
	names  []string
	fields []builder
	at     map[string]int
	valid  nulls
	seen   []uint32 // by field, the stamp of the object that last gave it
	stamp  uint32   // the object's being decoded
	// The last object's keys in its order, and their fields, -1 for a key with
	// none: lines tend to give their keys in one order, so an object's k-th key is
	// looked for first as the last one's. A plain key is one a line writes as
	// itself, with no escape.
	lastKey   []string
	lastAt    []int
	lastPlain []bool
}

func (b *structBuilder) decode(p *parser) error {
	if p.s[p.pos] != '{' {
		if !p.ignore {
			return p.refuse(b.want)
		}
		// Polars' ignore_errors reads a value that is not an object as one with
		// no keys: a row of nulls, not a null.
		if err := p.skip(); err != nil {
			return err
		}
		for _, f := range b.fields {
			f.addNull()
		}
		b.valid.valid()
		return nil
	}
	if err := b.decodeFields(p); err != nil {
		return err
	}
	b.valid.valid()
	return nil
}

// decodeFields decodes the object at p as a row of each field.
func (b *structBuilder) decodeFields(p *parser) error {
	p.pos++ // {
	if b.stamp++; b.stamp == 0 {
		clear(b.seen)
		b.stamp = 1
	}
	p.space()
	if p.pos < len(p.s) && p.s[p.pos] == '}' {
		p.pos++
	} else {
		for k := 0; ; k++ {
			p.space()
			if p.pos >= len(p.s) || p.s[p.pos] != '"' {
				return p.fail("an object's key was expected")
			}
			j, ok := b.expected(p, k)
			if !ok {
				key, err := p.key()
				if err != nil {
					return err
				}
				j = b.field(k, key)
			}
			p.space()
			if p.pos >= len(p.s) || p.s[p.pos] != ':' {
				return p.fail("':' was expected after a key")
			}
			p.pos++
			p.space()
			var err error
			if j < 0 || b.seen[j] == b.stamp {
				err = p.skip()
			} else {
				b.seen[j] = b.stamp
				if err = decodeValue(p, b.fields[j]); err != nil {
					err = underKey(err, b.names[j])
				}
			}
			if err != nil {
				return err
			}
			p.space()
			if p.pos >= len(p.s) {
				return p.fail("the object is not closed")
			}
			if p.s[p.pos] == '}' {
				p.pos++
				break
			}
			if p.s[p.pos] != ',' {
				return p.fail("',' or '}' was expected")
			}
			p.pos++
		}
	}
	for j, s := range b.seen {
		if s != b.stamp {
			b.fields[j].addNull()
		}
	}
	return nil
}

// keyError is an error in a key's value: "o.x" for x's in o's.
type keyError struct {
	key string
	err error
}

func (e *keyError) Error() string { return e.err.Error() }
func (e *keyError) Unwrap() error { return e.err }

// underKey is err, an error in a value, as one under key.
func underKey(err error, key string) error {
	var ke *keyError
	if errors.As(err, &ke) {
		ke.key = key + "." + ke.key
		return err
	}
	return &keyError{key: key, err: err}
}

// expected is the field of the object's k-th key, at p, when the key is the last
// object's k-th and plain, matched against the line's bytes and moved past; false
// when it is not.
func (b *structBuilder) expected(p *parser, k int) (int, bool) {
	if k >= len(b.lastKey) || !b.lastPlain[k] {
		return 0, false
	}
	key := b.lastKey[k]
	end := p.pos + 1 + len(key)
	if end >= len(p.s) || p.s[end] != '"' || string(p.s[p.pos+1:end]) != key {
		return 0, false
	}
	p.pos = end + 1
	return b.lastAt[k], true
}

// field is the index of key's field, -1 for none, key the object's k-th.
func (b *structBuilder) field(k int, key []byte) int {
	if k < len(b.lastKey) && b.lastKey[k] == string(key) {
		return b.lastAt[k]
	}
	j, ok := b.at[string(key)]
	if !ok {
		j = -1
	}
	isPlain := true
	for _, c := range key {
		isPlain = isPlain && plain[c]
	}
	switch {
	case k < len(b.lastKey):
		b.lastKey[k], b.lastAt[k], b.lastPlain[k] = string(key), j, isPlain
	case k == len(b.lastKey) && k < 1024:
		b.lastKey, b.lastAt = append(b.lastKey, string(key)), append(b.lastAt, j)
		b.lastPlain = append(b.lastPlain, isPlain)
	}
	return j
}

func (b *structBuilder) addNull() {
	for _, f := range b.fields {
		f.addNull()
	}
	b.valid.null()
}

func (b *structBuilder) size(rows int) {
	for _, f := range b.fields {
		f.size(rows)
	}
}

func (b *structBuilder) finish(name string) *data.Column {
	cols := make([]*data.Column, len(b.fields))
	for i, f := range b.fields {
		cols[i] = f.finish(b.names[i])
	}
	return data.NewStruct(name, cols, b.valid.view())
}
