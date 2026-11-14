package ursus_test

// A Struct of no fields keeps its rows (step 518). Polars' Struct({}) is what a JSON
// object field holding {} reads as, and ursus took a struct's length from its first
// field, so a struct of none had no rows and a read of {"a":{}} failed as a batch whose
// columns disagree. testdata/oracle/emptystruct.json holds Polars 2.1's answers to the
// reads and the operations; the differential here pushes generated frames through the
// operations and the writers, each frame with the struct and without it.

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// esFrame is one generated frame: base holds i (the row), b, k and w; with is base and
// a, a Struct of no fields that is {} where w is true and null where it is false, as
// source built it.
type esFrame struct {
	source     string
	n          int
	base, with *ursus.LazyFrame
	w          []bool
}

// esRow is a row of the frame as FromRows reads one; A is the struct, nil a null.
type esRow struct {
	I int64     `ursus:"i"`
	B *int64    `ursus:"b"`
	K *string   `ursus:"k"`
	W bool      `ursus:"w"`
	A *struct{} `ursus:"a"`
}

// esSources are the ways the struct is built: decoded from JSON text, read from
// NDJSON and from JSON, by FromRows, and through an IPC file.
var esSources = []string{"json_decode", "ndjson", "json", "from_rows", "ipc"}

// esGenerate draws a frame of n rows from r and builds it as source says.
func esGenerate(t *testing.T, r *rand.Rand, n int, source string) esFrame {
	t.Helper()
	i, b, k, w := make([]int64, n), make([]int64, n), make([]string, n), make([]bool, n)
	bOK, kOK := make([]bool, n), make([]bool, n)
	keys := []string{"x", "y", "z"}
	for row := range n {
		i[row] = int64(row)
		b[row], bOK[row] = r.Int64N(101)-50, r.IntN(10) != 0
		k[row], kOK[row] = keys[r.IntN(len(keys))], r.IntN(10) != 0
		w[row] = r.IntN(10) < 7
	}
	base := ursus.Frame(ursus.Values("i", i), ursus.ValuesNullable("b", b, bOK), ursus.ValuesNullable("k", k, kOK),
		ursus.Values("w", w))
	f := esFrame{source: source, n: n, base: base, w: w}
	decoded := func() *ursus.LazyFrame {
		text := make([]string, n)
		for row := range n {
			text[row] = "{}"
		}
		return base.HStack(ursus.Frame(ursus.ValuesNullable("j", text, w))).
			WithColumns(ursus.Col("j").Str().JsonDecode(ursus.StructOf()).Alias("a")).Drop("j")
	}
	switch source {
	case "json_decode":
		f.with = decoded()
	case "ndjson", "json":
		var sb strings.Builder
		for row := range n {
			if source == "json" {
				sb.WriteString(map[bool]string{true: "[", false: ","}[row == 0])
			}
			fmt.Fprintf(&sb, `{"i":%d,"b":%s,"k":%s,"w":%t`, i[row], esText(bOK[row], strconv.FormatInt(b[row], 10)),
				esText(kOK[row], strconv.Quote(k[row])), w[row])
			switch {
			case w[row]:
				sb.WriteString(`,"a":{}`)
			case r.IntN(2) == 0:
				sb.WriteString(`,"a":null`)
			}
			sb.WriteString("}")
			if source == "ndjson" {
				sb.WriteString("\n")
			}
		}
		if source == "json" {
			sb.WriteString("]")
			f.with = ursus.ScanJSONBytes([]byte(sb.String()), "es.json")
		} else {
			f.with = ursus.ScanNDJSONBytes([]byte(sb.String()), "es.ndjson")
		}
		// A row whose key a is missing is null, as one whose a is; the columns in the
		// order base has them.
		f.with = f.with.Select(ursus.Col("i"), ursus.Col("b"), ursus.Col("k"), ursus.Col("w"), ursus.Col("a"))
	case "from_rows":
		rows := make([]esRow, n)
		for row := range n {
			rows[row] = esRow{I: i[row], W: w[row]}
			if bOK[row] {
				rows[row].B = &b[row]
			}
			if kOK[row] {
				rows[row].K = &k[row]
			}
			if w[row] {
				rows[row].A = &struct{}{}
			}
		}
		f.with = ursus.FromRows(rows)
	case "ipc":
		var buf bytes.Buffer
		if err := decoded().WriteIPC(t.Context(), &buf); err != nil {
			t.Fatalf("writing the IPC file: %v", err)
		}
		f.with = ursus.ScanIPCBytes(buf.Bytes(), "es.arrow")
	default:
		t.Fatalf("no source %q", source)
	}
	return f
}

// esText is v, or null where ok is false, as JSON writes it.
func esText(ok bool, v string) string {
	if !ok {
		return "null"
	}
	return v
}

// esCollect collects lf, failing the test on an error.
func esCollect(t *testing.T, what string, lf *ursus.LazyFrame, opts ...ursus.CollectOption) *ursus.DataFrame {
	t.Helper()
	df, err := lf.Collect(t.Context(), opts...)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	return df
}

// esSame holds got, an answer over the frame with the struct, to want, the same
// question's over the frame without it: the same height, want's columns equal, and
// the struct, where it is there beside w, null exactly where w is not true.
func esSame(t *testing.T, got, want *ursus.DataFrame) {
	t.Helper()
	if got.Height() != want.Height() {
		t.Fatalf("%d rows with the struct, %d without it\nwith:\n%s\nwithout:\n%s", got.Height(), want.Height(), got, want)
	}
	cols := make([]ursus.Expr, 0, want.Width())
	for _, name := range want.Columns() {
		cols = append(cols, ursus.Col(name))
	}
	others, err := got.Select(t.Context(), cols...)
	if err != nil {
		t.Fatalf("the columns beside the struct: %v", err)
	}
	ursustest.AssertFrameEqual(t, others, want)
	esWitness(t, got)
}

// esWitness holds a frame's struct a to its witness w: null exactly where w is not
// true.
func esWitness(t *testing.T, df *ursus.DataFrame) {
	t.Helper()
	have := map[string]bool{}
	for _, name := range df.Columns() {
		have[name] = true
	}
	if !have["a"] || !have["w"] {
		return
	}
	got, err := df.Select(t.Context(), ursus.Col("a").IsNotNull().Alias("w"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := df.Select(t.Context(), ursus.Col("w").FillNullWith(false).Alias("w"))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)
}

// TestEmptyStructDifferential: frames of a Struct of no fields beside other columns,
// generated from a seed and built five ways, through filter, head, tail and slice,
// sort, reverse, concat, joins carrying it and on it, unnest, a cast, is_null, shift,
// drop_nulls, a group-by with it a value and a key, unique, over, implode and explode,
// and the IPC, NDJSON and JSON writers read back, at the default batch size and at 7;
// each answer is the same question's over the frame without it, the struct null where
// its witness is (esSame). Parquet and CSV refuse it, never with ErrInternal, and
// write the frame without it. The rows a frame hands out, its display, ToStruct and
// Gather are held to the frame without it too, and a large frame's sort, group-by and
// join spill.
func TestEmptyStructDifferential(t *testing.T) {
	r := rand.New(rand.NewPCG(518, 2026))
	c, asc := ursus.Col, ursus.Asc
	right := ursus.Frame(ursus.Values("k", []string{"x", "y", "q"}), ursus.Values("v", []int64{10, 20, 30}))
	// key is the struct a where the frame has it, and its witness w where not: {} one
	// group and null another, as true and false are.
	key := func(a bool) ursus.Expr {
		if a {
			return c("a")
		}
		return c("w")
	}
	// witness is the key's witness: the struct not null, or w itself.
	witness := func(a bool) ursus.Expr {
		if a {
			return c("a").IsNotNull().Alias("w")
		}
		return c("w")
	}
	written := func(write func(*ursus.LazyFrame, *bytes.Buffer) error, read func([]byte) *ursus.LazyFrame) func(*ursus.LazyFrame, bool) *ursus.LazyFrame {
		return func(lf *ursus.LazyFrame, _ bool) *ursus.LazyFrame {
			var buf bytes.Buffer
			if err := write(lf, &buf); err != nil {
				t.Fatalf("writing: %v", err)
			}
			return read(buf.Bytes())
		}
	}
	type op struct {
		name string
		f    func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame
	}
	ops := func(n int) []op {
		return []op{
			{"filter", func(lf *ursus.LazyFrame, _ bool) *ursus.LazyFrame { return lf.Filter(c("b").Gt(0)) }},
			{"filter_on_w", func(lf *ursus.LazyFrame, _ bool) *ursus.LazyFrame { return lf.Filter(c("w")) }},
			{"head", func(lf *ursus.LazyFrame, _ bool) *ursus.LazyFrame { return lf.Head(n / 3) }},
			{"tail", func(lf *ursus.LazyFrame, _ bool) *ursus.LazyFrame { return lf.Tail(n / 4) }},
			{"slice", func(lf *ursus.LazyFrame, _ bool) *ursus.LazyFrame { return lf.Slice(n/5, n/2) }},
			{"sort", func(lf *ursus.LazyFrame, _ bool) *ursus.LazyFrame {
				return lf.Sort(asc(c("b")).NullsLast(), asc(c("i")))
			}},
			{"sort_descending", func(lf *ursus.LazyFrame, _ bool) *ursus.LazyFrame {
				return lf.Sort(ursus.Desc(c("k")), ursus.Desc(c("i")))
			}},
			{"reverse", func(lf *ursus.LazyFrame, _ bool) *ursus.LazyFrame { return lf.Reverse() }},
			{"concat", func(lf *ursus.LazyFrame, _ bool) *ursus.LazyFrame {
				return ursus.Concat([]*ursus.LazyFrame{lf, lf.Slice(1, 5), lf.Tail(2)})
			}},
			{"with_columns", func(lf *ursus.LazyFrame, _ bool) *ursus.LazyFrame {
				return lf.WithColumns(c("b").Add(int64(1)).Alias("b1"))
			}},
			{"join_inner", func(lf *ursus.LazyFrame, _ bool) *ursus.LazyFrame {
				return lf.Join(right, ursus.JoinOn(c("k"))).Sort(asc(c("i")))
			}},
			{"join_left", func(lf *ursus.LazyFrame, _ bool) *ursus.LazyFrame {
				return right.Join(lf, ursus.JoinOn(c("k")), ursus.JoinHow(ursus.JoinLeft)).
					Sort(asc(c("v")), asc(c("i")).NullsLast())
			}},
			{"join_full", func(lf *ursus.LazyFrame, _ bool) *ursus.LazyFrame {
				return lf.Join(right, ursus.JoinOn(c("k")), ursus.JoinHow(ursus.JoinFull)).
					Sort(asc(c("i")).NullsLast(), asc(c("v")))
			}},
			{"join_build_side", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				cols := []ursus.Expr{c("i"), c("w")}
				if a {
					cols = append(cols, c("a"))
				}
				return lf.Select(c("i"), c("b"), c("k")).Join(lf.Select(cols...), ursus.JoinOn(c("i"))).Sort(asc(c("i")))
			}},
			{"join_on_it", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				other := lf.Filter(c("i").Lt(int64(6))).Select(key(a), c("i").Alias("j"))
				return lf.Join(other, ursus.JoinOn(key(a)), ursus.JoinNullsEqual(true)).Sort(asc(c("i")), asc(c("j")))
			}},
			{"unnest", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				if a {
					return lf.Unnest("a")
				}
				return lf
			}},
			{"unnest_expr", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				if a {
					return lf.Select(c("i"), c("a").Struct().Unnest(), c("w"))
				}
				return lf.Select(c("i"), c("w"))
			}},
			{"cast", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				if a {
					return lf.WithColumns(c("a").Cast(ursus.StructOf()))
				}
				return lf
			}},
			{"is_null", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				if a {
					return lf.WithColumns(c("a").IsNull().Alias("null"))
				}
				return lf.WithColumns(c("w").Not().Alias("null"))
			}},
			{"shift", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				if a {
					return lf.WithColumns(c("a").Shift(3), c("w").Shift(3))
				}
				return lf.WithColumns(c("w").Shift(3))
			}},
			{"drop_nulls", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				if a {
					return lf.DropNulls("a")
				}
				return lf.Filter(c("w"))
			}},
			{"select_alone", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				if a {
					return lf.Select(c("a"), c("w"))
				}
				return lf.Select(c("w"))
			}},
			{"group_value", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				aggs := []ursus.Expr{c("i").Min().Alias("lo"), c("w").First(), c("w").Last().Alias("wl")}
				if a {
					aggs = append(aggs, c("a").First(), c("a").Count().Cast(ursus.Int64).Alias("n"))
				} else {
					aggs = append(aggs, c("w").Cast(ursus.Int64).Sum().Cast(ursus.Int64).Alias("n"))
				}
				return lf.GroupBy(c("k")).Agg(aggs...).Sort(asc(c("k")))
			}},
			{"group_list", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				if a {
					return lf.GroupBy(c("k")).Agg(c("i"), c("a"), c("w")).Sort(asc(c("k"))).Explode("i", "a", "w")
				}
				return lf.GroupBy(c("k")).Agg(c("i"), c("w")).Sort(asc(c("k"))).Explode("i", "w")
			}},
			{"group_key", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				return lf.GroupBy(key(a)).Agg(c("i").Min().Alias("lo"), c("i").Max().Alias("hi"), ursus.Len().Alias("len")).
					Sort(asc(c("lo"))).Select(c("lo"), c("hi"), c("len"), witness(a))
			}},
			{"group_two_keys", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				return lf.GroupBy(c("k"), key(a)).Agg(c("i").Min().Alias("lo"), ursus.Len().Alias("len")).
					Sort(asc(c("lo"))).Select(c("lo"), c("k"), c("len"))
			}},
			{"unique", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				return lf.Select(key(a), c("k")).Unique().Select(witness(a), c("k")).Sort(asc(c("w")), asc(c("k")))
			}},
			{"over", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				return lf.WithColumns(c("i").Max().Over(key(a)).Alias("m"))
			}},
			{"implode_explode", func(lf *ursus.LazyFrame, a bool) *ursus.LazyFrame {
				if a {
					return lf.Select(c("a").Implode(), c("w").Implode()).Explode("a", "w")
				}
				return lf.Select(c("w").Implode()).Explode("w")
			}},
			{"ipc", written(func(lf *ursus.LazyFrame, b *bytes.Buffer) error { return lf.WriteIPC(t.Context(), b) },
				func(b []byte) *ursus.LazyFrame { return ursus.ScanIPCBytes(b, "es.arrow") })},
			{"ipc_stream", written(func(lf *ursus.LazyFrame, b *bytes.Buffer) error { return lf.WriteIPCStream(t.Context(), b) },
				func(b []byte) *ursus.LazyFrame { return ursus.ScanIPCStreamBytes(b, "es.arrows") })},
			{"ndjson", written(func(lf *ursus.LazyFrame, b *bytes.Buffer) error { return lf.WriteNDJSON(t.Context(), b) },
				func(b []byte) *ursus.LazyFrame { return ursus.ScanNDJSONBytes(b, "es.ndjson") })},
			{"json", written(func(lf *ursus.LazyFrame, b *bytes.Buffer) error { return lf.WriteJSON(t.Context(), b) },
				func(b []byte) *ursus.LazyFrame { return ursus.ScanJSONBytes(b, "es.json") })},
		}
	}
	for trial := range 10 {
		source := esSources[trial%len(esSources)]
		n := 20 + r.IntN(300)
		t.Run(fmt.Sprintf("%s_%d_rows", source, n), func(t *testing.T) {
			f := esGenerate(t, r, n, source)
			with, base := esCollect(t, "the frame with the struct", f.with), esCollect(t, "the frame", f.base)
			esSame(t, with, base)
			for _, o := range ops(n) {
				for _, batch := range []int{0, 7} {
					var opts []ursus.CollectOption
					if batch > 0 {
						opts = append(opts, ursus.WithBatchSize(batch))
					}
					t.Run(fmt.Sprintf("%s_batch_%d", o.name, batch), func(t *testing.T) {
						got := esCollect(t, "with the struct", o.f(f.with, true), opts...)
						want := esCollect(t, "without it", o.f(f.base, false), opts...)
						esSame(t, got, want)
					})
				}
			}
			t.Run("refused", func(t *testing.T) {
				for name, write := range map[string]func(*ursus.LazyFrame, *bytes.Buffer) error{
					"parquet": func(lf *ursus.LazyFrame, b *bytes.Buffer) error { return lf.WriteParquet(t.Context(), b) },
					"csv":     func(lf *ursus.LazyFrame, b *bytes.Buffer) error { return lf.WriteCSV(t.Context(), b) },
				} {
					var buf bytes.Buffer
					if err := write(f.base, &buf); err != nil {
						t.Fatalf("%s of the frame without the struct: %v", name, err)
					}
					err := write(f.with, &buf)
					if err == nil || errors.Is(err, ursus.ErrInternal) {
						t.Errorf("%s of a Struct of no fields: %v, where a clean refusal belongs", name, err)
					}
				}
			})
			t.Run("eager", func(t *testing.T) {
				for row := range n {
					v, err := with.ItemAt(row, "a")
					if err != nil {
						t.Fatal(err)
					}
					if (v != nil) != f.w[row] {
						t.Fatalf("row %d of the struct reads %v, and its witness is %v", row, v, f.w[row])
					}
				}
				if a, b := strings.Count(with.String(), "\n"), strings.Count(base.String(), "\n"); a != b {
					t.Errorf("the display has %d lines with the struct, %d without it:\n%s", a, b, with)
				}
				col := with.ToStruct("s")
				if col.Len() != n {
					t.Fatalf("ToStruct of %d rows is %d long", n, col.Len())
				}
				back := esCollect(t, "unnesting ToStruct", ursus.Frame(col).Unnest("s"))
				ursustest.AssertFrameEqual(t, back, with)
				idx := []int{n - 1, 0, n / 2, -1, 3}
				got, err := with.Gather(t.Context(), idx, false)
				if err != nil {
					t.Fatal(err)
				}
				want, err := base.Gather(t.Context(), idx, false)
				if err != nil {
					t.Fatal(err)
				}
				esSame(t, got, want)
			})
		})
	}
	t.Run("spilled", func(t *testing.T) {
		const n = 6000
		f := esGenerate(t, r, n, "json_decode")
		for _, o := range ops(n) {
			switch o.name {
			case "sort", "sort_descending", "group_value", "group_key", "join_build_side", "join_on_it":
			default:
				continue
			}
			var st ursus.MemoryStats
			got := esCollect(t, o.name+" with the struct", o.f(f.with, true), ursus.WithBatchSize(64),
				ursus.WithMemoryLimit(64<<10), ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
			if (strings.HasPrefix(o.name, "sort") || o.name == "join_build_side") && st.Spills == 0 {
				t.Errorf("%s answered without spilling, so it proves nothing", o.name)
			}
			esSame(t, got, esCollect(t, o.name+" without it", o.f(f.base, false)))
		}
	})
}
