package ursustest_test

// AssertFrameEqual on List and Struct columns.
//
// renderValue used to end in a placeholder, "<unrenderable list<int64>>", that List
// and Struct both reached. The placeholder is the same string for every row, so two
// nested frames holding DIFFERENT values rendered identically and compared equal —
// at every call site with a nested column. Each case below that must fail passed
// before this file existed.

import (
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source/memsrc"
	"github.com/advenn/ursus/ursustest"
)

// frameFrom collects a frame holding exactly these columns.
func frameFrom(t *testing.T, cols ...*data.Column) *ursus.DataFrame {
	t.Helper()
	fields := make([]dtype.Field, len(cols))
	for i, c := range cols {
		fields[i] = dtype.Field{Name: c.Name(), Type: c.DType(), Nullable: true}
	}
	schema, err := dtype.NewSchema(fields...)
	if err != nil {
		t.Fatal(err)
	}
	b, err := data.NewBatch(schema, cols)
	if err != nil {
		t.Fatal(err)
	}
	src, err := memsrc.FromBatch(b)
	if err != nil {
		t.Fatal(err)
	}
	df, err := ursus.Scan(src).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return df
}

// ints builds a List(Int64) column; a nil row is a null list, and a nil element
// pointer is a null element.
func ints(name string, rows [][]*int64) *data.Column {
	var elems []int64
	ev := bitmap.NewBuilder(0)
	offs := []int32{0}
	rv := bitmap.NewBuilder(len(rows))
	for _, r := range rows {
		for _, e := range r {
			if e == nil {
				elems = append(elems, 0)
				ev.Append(false)
				continue
			}
			elems = append(elems, *e)
			ev.Append(true)
		}
		offs = append(offs, int32(len(elems)))
		rv.Append(r != nil)
	}
	child := data.NewFixed("item", dtype.Int64, elems, ev.Finish())
	return data.NewList(name, offs, child, rv.Finish())
}

func p(v int64) *int64 { return &v }

// row is shorthand for a list row of non-null elements.
func row(vs ...int64) []*int64 {
	out := make([]*int64, len(vs))
	for i := range vs {
		out[i] = p(vs[i])
	}
	return out
}

func strs(name string, vals []string, valid ...bool) *data.Column {
	v := bitmap.AllSet(len(vals))
	if valid != nil {
		b := bitmap.NewBuilder(len(valid))
		for _, ok := range valid {
			b.Append(ok)
		}
		v = b.Finish()
	}
	return data.NewString(name, vals, v)
}

// person builds a Struct{name, age}; valid[i] false makes row i a NULL struct, as
// opposed to a struct whose fields are null.
func person(name string, names []string, namesValid []bool, ages []int64,
	agesValid []bool, valid []bool) *data.Column {

	nv, av, sv := bitmap.NewBuilder(len(names)), bitmap.NewBuilder(len(ages)), bitmap.NewBuilder(len(valid))
	for i := range names {
		nv.Append(namesValid[i])
		av.Append(agesValid[i])
		sv.Append(valid[i])
	}
	return data.NewStruct(name, []*data.Column{
		data.NewString("name", names, nv.Finish()),
		data.NewFixed("age", dtype.Int64, ages, av.Finish()),
	}, sv.Finish())
}

func TestNestedValuesAreCompared(t *testing.T) {
	yes := []bool{true, true}

	for _, c := range []struct {
		name      string
		got, want func(t *testing.T) *ursus.DataFrame
		mustFail  bool
	}{
		{
			name:     "a different element",
			got:      func(t *testing.T) *ursus.DataFrame { return frameFrom(t, ints("l", [][]*int64{row(1, 2)})) },
			want:     func(t *testing.T) *ursus.DataFrame { return frameFrom(t, ints("l", [][]*int64{row(1, 3)})) },
			mustFail: true,
		},
		{
			name:     "a null list against an empty one",
			got:      func(t *testing.T) *ursus.DataFrame { return frameFrom(t, ints("l", [][]*int64{nil})) },
			want:     func(t *testing.T) *ursus.DataFrame { return frameFrom(t, ints("l", [][]*int64{{}})) },
			mustFail: true,
		},
		{
			name:     "a null element against a zero",
			got:      func(t *testing.T) *ursus.DataFrame { return frameFrom(t, ints("l", [][]*int64{{p(1), nil}})) },
			want:     func(t *testing.T) *ursus.DataFrame { return frameFrom(t, ints("l", [][]*int64{row(1, 0)})) },
			mustFail: true,
		},
		{
			name: "elements moved across a row boundary",
			got: func(t *testing.T) *ursus.DataFrame {
				return frameFrom(t, ints("l", [][]*int64{row(1), row(2, 3)}))
			},
			want: func(t *testing.T) *ursus.DataFrame {
				return frameFrom(t, ints("l", [][]*int64{row(1, 2), row(3)}))
			},
			mustFail: true,
		},
		{
			name: "a null struct against a struct of nulls",
			got: func(t *testing.T) *ursus.DataFrame {
				return frameFrom(t, person("p", []string{"", ""}, []bool{false, false},
					[]int64{0, 0}, []bool{false, false}, []bool{true, false}))
			},
			want: func(t *testing.T) *ursus.DataFrame {
				return frameFrom(t, person("p", []string{"", ""}, []bool{false, false},
					[]int64{0, 0}, []bool{false, false}, []bool{true, true}))
			},
			mustFail: true,
		},
		{
			name: "a different field value",
			got: func(t *testing.T) *ursus.DataFrame {
				return frameFrom(t, person("p", []string{"ann", "bo"}, yes, []int64{30, 40}, yes, yes))
			},
			want: func(t *testing.T) *ursus.DataFrame {
				return frameFrom(t, person("p", []string{"ann", "bo"}, yes, []int64{30, 41}, yes, yes))
			},
			mustFail: true,
		},
		{
			name: "a list of strings whose elements join to the same text",
			got: func(t *testing.T) *ursus.DataFrame {
				s := strs("item", []string{"a,b"})
				return frameFrom(t, data.NewList("l", []int32{0, 1}, s, bitmap.AllSet(1)))
			},
			want: func(t *testing.T) *ursus.DataFrame {
				s := strs("item", []string{"a", "b"})
				return frameFrom(t, data.NewList("l", []int32{0, 2}, s, bitmap.AllSet(1)))
			},
			mustFail: true,
		},
		{
			name: "a list inside a struct",
			got: func(t *testing.T) *ursus.DataFrame {
				return frameFrom(t, data.NewStruct("s", []*data.Column{
					ints("tags", [][]*int64{row(1, 2)}),
				}, bitmap.AllSet(1)))
			},
			want: func(t *testing.T) *ursus.DataFrame {
				return frameFrom(t, data.NewStruct("s", []*data.Column{
					ints("tags", [][]*int64{row(2, 1)}),
				}, bitmap.AllSet(1)))
			},
			mustFail: true,
		},
		{
			name: "identical nested frames",
			got: func(t *testing.T) *ursus.DataFrame {
				return frameFrom(t,
					ints("l", [][]*int64{row(1, 2), nil, {}, {p(4), nil}}),
					person("p", []string{"a", "", "c", ""}, []bool{true, false, true, false},
						[]int64{1, 0, 3, 0}, []bool{true, false, true, false},
						[]bool{true, true, true, false}))
			},
			want: func(t *testing.T) *ursus.DataFrame {
				return frameFrom(t,
					ints("l", [][]*int64{row(1, 2), nil, {}, {p(4), nil}}),
					person("p", []string{"a", "", "c", ""}, []bool{true, false, true, false},
						[]int64{1, 0, 3, 0}, []bool{true, false, true, false},
						[]bool{true, true, true, false}))
			},
			mustFail: false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, want := c.got(t), c.want(t)
			failed := didFail(func(tb testing.TB) { ursustest.AssertFrameEqual(tb, got, want) })
			if failed != c.mustFail {
				t.Errorf("AssertFrameEqual failed = %v, want %v\n got:\n%s\nwant:\n%s",
					failed, c.mustFail, got, want)
			}
		})
	}
}

// TestASlicedListRendersOnlyItsRows: Tail keeps the parent's whole child and absolute
// offsets, so a renderer walking the child from zero would print elements of rows
// the frame no longer has. Both directions are asserted — equal to its own literal,
// and different from a literal holding the first row's elements.
func TestASlicedListRendersOnlyItsRows(t *testing.T) {
	full := frameFrom(t, ints("l", [][]*int64{row(5, 6), row(1, 1, 2), {}, row(7)}))
	tail, err := full.Tail(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if lo, _ := tail.Batch().Column(0).Lists().Window(); lo == 0 {
		t.Fatal("Tail rebuilt the list from zero, so this test no longer holds a sliced List")
	}

	literal := frameFrom(t, ints("l", [][]*int64{{}, row(7)}))
	if didFail(func(tb testing.TB) { ursustest.AssertFrameEqual(tb, tail, literal) }) {
		t.Errorf("a sliced List must equal the literal holding the same rows\n got:\n%s\nwant:\n%s",
			tail, literal)
	}

	head := frameFrom(t, ints("l", [][]*int64{row(5, 6), row(7)}))
	if !didFail(func(tb testing.TB) { ursustest.AssertFrameEqual(tb, tail, head) }) {
		t.Error("a sliced List compared equal to rows it does not hold")
	}
}
