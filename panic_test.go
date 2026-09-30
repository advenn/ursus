package ursus_test

// A panic is an error, never a dead process.
//
// A panic in a worker goroutine cannot be recovered by anyone: not by the caller,
// not by a test, not by the program that asked the question. ursus runs a query's
// expressions on worker goroutines by default, so one panic in one row killed the
// host process.
//
// That is also why these cases cannot run in this test binary: a case that crashes
// would take the whole suite with it. Each one runs in a CHILD process — this test
// binary again, told by an environment variable to run one case and print what
// happened — and the parent reads the outcome: a crash, a hang, a panic that
// reached the caller, or an answer to judge.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// panicProbeEnv names the case a child process runs.
const panicProbeEnv = "URSUS_PANIC_PROBE"

// probeLine prefixes the child's one line of report.
const probeLine = "PANIC-PROBE "

// probeReport is what the child saw. A crash or a hang leaves no report at all.
type probeReport struct {
	Panic string // a panic that reached the caller, recovered there
	Err   string
	Kind  string // kindOf(err)
	Got   string
	Leak  int // goroutines still running a second after the call returned
}

type panicCase struct {
	name string
	run  func(t *testing.T) (string, error)
	want func(probeReport) string // "" when the answer is right
}

// knownPanicDefects names each case that answers wrongly today, with how. Emptied
// by the commits that fix them; a listed case that answers correctly fails as
// stale, and an unlisted one that answers wrongly fails.
//
// The value is a PREFIX of what the case reports, so a case that starts failing
// in a different way — a crash that becomes a caller panic, say — fails too.
var knownPanicDefects = map[string]string{
	"the zero Expr, as an operand": "internal error, want value: ursus: collect: recovered a panic",
	"the zero Expr, aliased":       "internal error, want value: ursus: collect: recovered a panic",
}

// boom panics on the row holding 3, which every fixture below has.
func boom(v int64) (int64, error) {
	if v == 3 {
		panic("boom at 3")
	}
	return v, nil
}

func boomBatch(vals []int64, _ []bool) ([]int64, []bool, error) {
	panic("boom in a batch")
}

// rawBoom is a udf whose kernel panics, built without the public wrapper — which
// is how a panic in one of ursus's own kernels looks to the engine.
func rawBoom() ursus.Expr {
	return ursus.ExprOf(expr.NewUDF(&expr.Col{Name: "v"}, dtype.Int64, "raw", "map_elements",
		kernel.ColumnUDF(func(context.Context, *data.Column, string) (*data.Column, error) {
			panic("kernel boom")
		})))
}

// wantErr is right when the call failed with this kind of error, naming each of
// words.
func wantErr(kind string, words ...string) func(probeReport) string {
	return func(r probeReport) string {
		if r.Err == "" {
			return fmt.Sprintf("no error, got %s; want a %s error", r.Got, kind)
		}
		if r.Kind != kind {
			return fmt.Sprintf("%s error, want %s: %s", r.Kind, kind, r.Err)
		}
		for _, w := range words {
			if !strings.Contains(r.Err, w) {
				return fmt.Sprintf("the error does not say %q: %s", w, r.Err)
			}
		}
		return ""
	}
}

// wantGot is right when the call answered exactly got.
func wantGot(got string) func(probeReport) string {
	return func(r probeReport) string {
		if r.Err != "" {
			return fmt.Sprintf("%s error, want %s: %s", r.Kind, got, r.Err)
		}
		if r.Got != got {
			return fmt.Sprintf("got %s, want %s", r.Got, got)
		}
		return ""
	}
}

// udfErr is what a panicking udf must become: the user's own error, as its
// returned errors are, naming the udf.
func udfErr(name string) func(probeReport) string { return wantErr("value", name, "panicked") }

func panicFrame() *ursus.LazyFrame {
	return ursus.Frame(ursus.Values("k", []int64{1, 2, 1, 2, 3}), ursus.Values("v", []int64{1, 2, 3, 4, 5}))
}

// collectCells runs lf and renders column name.
func collectCells(ctx context.Context, lf *ursus.LazyFrame, name string, opts ...ursus.CollectOption) (string, error) {
	df, err := lf.Collect(ctx, opts...)
	if err != nil {
		return "", err
	}
	cells, err := cellsOf(df, name)
	return fmt.Sprint(cells), err
}

func threads(n int) ursus.CollectOption { return ursus.WithThreads(n) }

func panicCases() []panicCase {
	c := ursus.Col
	udf := c("v").MapElements("boom", ursus.Int64, boom)
	withUDF := panicFrame().WithColumns(udf.Alias("w"))

	raw := rawBoom()
	withRaw := panicFrame().WithColumns(raw.Alias("w"))
	internal := wantErr("internal", "recovered a panic", "kernel boom")

	cases := []panicCase{
		// A kernel that panics is ursus's bug: an internal error, through every
		// driver and on every goroutine the engine starts.
		{"a kernel panic, 1 thread", func(t *testing.T) (string, error) {
			return collectCells(t.Context(), withRaw, "w", threads(1))
		}, internal},
		{"a kernel panic, 4 threads", func(t *testing.T) (string, error) {
			return collectCells(t.Context(), withRaw, "w", threads(4))
		}, internal},
		{"a kernel panic in a join's probe key, 4 threads", func(t *testing.T) (string, error) {
			return collectCells(t.Context(), panicFrame().Join(panicFrame(),
				ursus.JoinLeftOn(raw), ursus.JoinRightOn(c("k"))), "v", threads(4))
		}, internal},
		{"a kernel panic inside an aggregate, 4 threads", func(t *testing.T) (string, error) {
			return collectCells(t.Context(), panicFrame().GroupBy(c("k")).Agg(raw.Sum().Alias("s")), "k", threads(4))
		}, internal},
		{"a kernel panic under Count, 1 thread", func(t *testing.T) (string, error) {
			n, err := withRaw.Filter(c("w").Gt(0)).Count(t.Context(), threads(1))
			return fmt.Sprint(n), err
		}, internal},
		{"a kernel panic under CollectBatches, 1 thread", func(t *testing.T) (string, error) {
			for _, err := range withRaw.CollectBatches(t.Context(), threads(1), ursus.WithBatchSize(2)) {
				if err != nil {
					return "", err
				}
			}
			return "no error", nil
		}, internal},
		// The factory is the caller's code, and it runs inside the source's Once: a
		// panic that escaped the Once would leave it done, and the second call
		// would find no schema and no error.
		{"ScanArrow whose open panics, asked twice", func(t *testing.T) (string, error) {
			lf := ursus.ScanArrow(func() (array.RecordReader, error) { panic("open boom") })
			_, err1 := lf.CollectSchema(t.Context())
			_, err2 := lf.CollectSchema(t.Context())
			if err1 == nil || err2 == nil || err1.Error() != err2.Error() {
				return fmt.Sprintf("first %v, then %v", err1, err2), nil
			}
			return "", err2
		}, wantErr("io", "open boom")},

		// A udf is user code. Its panic is the user's, as its errors are.
		{"udf, 1 thread", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return collectCells(ctx, withUDF, "w", threads(1))
		}, wantErr("value", `udf "boom" panicked at row 2 of column "v"`, "caused by: boom at 3")},
		{"udf, 4 threads", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return collectCells(ctx, withUDF, "w", threads(4))
		}, udfErr("boom")},
		{"map_batches, 4 threads", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return collectCells(ctx, panicFrame().WithColumns(
				c("v").MapBatches("boom_batch", ursus.Int64, boomBatch).Alias("w")), "w", threads(4))
		}, udfErr("boom_batch")},
		{"udf in a join's probe key, 4 threads", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return collectCells(ctx, panicFrame().Join(panicFrame(),
				ursus.JoinLeftOn(udf), ursus.JoinRightOn(c("k"))), "v", threads(4))
		}, udfErr("boom")},
		{"udf inside an aggregate, 4 threads", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return collectCells(ctx, panicFrame().GroupBy(c("k")).Agg(udf.Sum().Alias("s")), "k", threads(4))
		}, udfErr("boom")},
		{"udf below an aggregate, 4 threads", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return collectCells(ctx, withUDF.GroupBy(c("k")).Agg(c("w").Sum().Alias("s")), "k", threads(4))
		}, udfErr("boom")},
		{"udf under Count, 1 thread", func(t *testing.T) (string, error) {
			ctx := t.Context()
			n, err := withUDF.Filter(c("w").Gt(0)).Count(ctx, threads(1))
			return fmt.Sprint(n), err
		}, udfErr("boom")},
		{"udf under CollectBatches, 1 thread", func(t *testing.T) (string, error) {
			ctx := t.Context()
			for _, err := range withUDF.CollectBatches(ctx, threads(1), ursus.WithBatchSize(2)) {
				if err != nil {
					return "", err
				}
			}
			return "no error", nil
		}, udfErr("boom")},

		// The known causes: each panicked inside ursus, or arrow-go, on input a
		// caller can write.
		{"Str().Slice to MaxInt64", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return collectCells(ctx, ursus.Frame(ursus.Values("s", []string{"abc", "x"})).
				Select(c("s").Str().Slice(1, math.MaxInt64)), "s", threads(4))
		}, wantGot("[bc ]")},
		{"Rank(RankMethod(99))", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return collectCells(ctx, panicFrame().Select(c("v").Rank(ursus.RankMethod(99), false).Alias("r")),
				"r", threads(4))
		}, wantErr("value", "rank")},
		{"Quantile(Interpolation(99))", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return collectCells(ctx, panicFrame().GroupBy(c("k")).MaintainOrder().
				Agg(c("v").Quantile(0.5, ursus.Interpolation(99)).Alias("q")), "q", threads(4))
		}, wantErr("value", "interpolation")},
		{"Parquet List(Int8)", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return readList(ctx, rawList(t, 8), ursus.Int8)
		}, wantGot("[[1 2] [3]]")},
		{"Parquet List(Int16)", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return readList(ctx, rawList(t, 16), ursus.Int16)
		}, wantGot("[[1 2] [3]]")},
		{"Parquet LZ4", func(t *testing.T) (string, error) {
			ctx := t.Context()
			var buf bytes.Buffer
			return "", panicFrame().WriteParquet(ctx, &buf, ursus.WithCompression(compress.Codecs.Lz4))
		}, wantErr("unsupported", "Lz4Raw")},
		{"Parquet LZO", func(t *testing.T) (string, error) {
			ctx := t.Context()
			var buf bytes.Buffer
			return "", panicFrame().WriteParquet(ctx, &buf, ursus.WithCompression(compress.Codecs.Lzo))
		}, wantErr("unsupported", "Lz4Raw")},
		// The control: the refusal's advice works.
		{"Parquet Lz4Raw, as the refusal advises", func(t *testing.T) (string, error) {
			var buf bytes.Buffer
			if err := panicFrame().WriteParquet(t.Context(), &buf, ursus.WithCompression(compress.Codecs.Lz4Raw)); err != nil {
				return "", err
			}
			return collectCells(t.Context(), ursus.ScanParquetBytes(buf.Bytes(), "lz4raw.parquet"), "v")
		}, wantGot("[1 2 3 4 5]")},
		{"the zero Expr, selected", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return collectCells(ctx, panicFrame().Select(ursus.Expr{}), "v")
		}, wantErr("value", "zero Expr")},
		{"the zero Expr, as an operand", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return collectCells(ctx, panicFrame().Filter(c("v").Gt(ursus.Expr{})), "v")
		}, wantErr("value", "zero Expr")},
		{"the zero Expr, aliased", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return collectCells(ctx, panicFrame().WithColumns(ursus.Expr{}.Alias("x")), "v")
		}, wantErr("value", "zero Expr")},
		{"Parquet whose row group claims more rows", func(t *testing.T) (string, error) {
			ctx := t.Context()
			b, err := inflatedParquet(ctx)
			if err != nil {
				return "", err
			}
			return collectCells(ctx, ursus.ScanParquetBytes(b, "inflated.parquet"), "v", threads(4))
		}, wantErr("io", "declares")},
		{"Parquet, every byte flipped", func(t *testing.T) (string, error) {
			return corruptSweep(t.Context())
		}, wantCorruptIsIO},
	}
	return cases
}

// rawList writes [[1 2] [3]] as a list of INT(bits) to a Parquet file. ursus's own
// writer refuses nested columns, so a file like this comes from another writer.
func rawList(t *testing.T, bits int8) string {
	opt := parquet.Repetitions.Optional
	elem, err := schema.NewPrimitiveNodeLogical("element", opt, schema.NewIntLogicalType(bits, true),
		parquet.Types.Int32, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	return writeRaw(t, t.TempDir(), "list.parquet", schema.FieldList{listNode(t, "L", opt, elem)},
		[]rawCol{{vals: []int32{1, 2, 3}, defs: []int16{3, 3, 3}, reps: []int16{0, 1, 0}}})
}

// readList reads column L of the file at path, which must be a List(elem).
func readList(ctx context.Context, path string, elem ursus.DataType) (string, error) {
	lf := ursus.ScanParquet(path)
	s, err := lf.CollectSchema(ctx)
	if err != nil {
		return "", err
	}
	if f, _ := s.ByName("L"); f.Type != dtype.List(elem) {
		return "", fmt.Errorf("L is %s, want %s", f.Type, dtype.List(elem))
	}
	// Rows decodes no list, so each element is exploded to a row of its own,
	// numbered by the list it came from.
	df, err := lf.WithRowIndex("row", 0).Explode("L").
		Select(ursus.Col("row"), ursus.Col("L").Cast(ursus.Int64)).Collect(ctx, threads(4))
	if err != nil {
		return "", err
	}
	rows, err := cellsOf(df, "row")
	if err != nil {
		return "", err
	}
	vals, err := cellsOf(df, "L")
	if err != nil {
		return "", err
	}
	var out [][]string
	for i, r := range rows {
		if i == 0 || r != rows[i-1] {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], vals[i])
	}
	return fmt.Sprint(out), nil
}

// tinyParquet is the file the corrupt cases start from: 37 rows, so its row count
// is one distinctive byte in the footer.
func tinyParquet(ctx context.Context) ([]byte, error) {
	v := make([]int64, 37)
	s := make([]string, 37)
	for i := range v {
		v[i], s[i] = int64(i), fmt.Sprint("s", i%5)
	}
	var buf bytes.Buffer
	err := ursus.Frame(ursus.Values("v", v), ursus.Values("s", s)).WriteParquet(ctx, &buf)
	return buf.Bytes(), err
}

// inflatedParquet says, in its footer, that its row group holds 63 rows where its
// column chunks hold 37. Thrift's compact protocol writes an i64 field that follows
// the previous one as the byte 0x16 and then a zigzag varint, and 37 zigzags to
// 74, one byte; 63 zigzags to 126, also one byte, so the patch keeps every offset.
func inflatedParquet(ctx context.Context) ([]byte, error) {
	b, err := tinyParquet(ctx)
	if err != nil {
		return nil, err
	}
	footer := len(b) - 8 - int(uint32(b[len(b)-8])|uint32(b[len(b)-7])<<8|uint32(b[len(b)-6])<<16|uint32(b[len(b)-5])<<24)
	n := bytes.Count(b[footer:], []byte{0x16, 74})
	if n == 0 {
		return nil, errors.New("no row count in the footer to inflate")
	}
	copy(b[footer:], bytes.ReplaceAll(b[footer:], []byte{0x16, 74}, []byte{0x16, 126}))
	return b, nil
}

// corruptSweep reads tinyParquet with each byte flipped, two ways, and tallies the
// outcomes. A worker's panic crashes the child and leaves no tally.
func corruptSweep(ctx context.Context) (string, error) {
	b, err := tinyParquet(ctx)
	if err != nil {
		return "", err
	}
	tally := map[string]int{}
	for i := range b {
		for _, mask := range []byte{0xff, 0x01} {
			bad := slices.Clone(b)
			bad[i] ^= mask
			func() {
				defer func() {
					if p := recover(); p != nil {
						tally["caller panic"]++
					}
				}()
				ctx, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				_, err := ursus.ScanParquetBytes(bad, "flipped.parquet").Collect(ctx, threads(4))
				var p *uerr.PanicError
				switch {
				case err == nil:
					tally["result"]++
				case errors.Is(err, context.DeadlineExceeded):
					tally["hang"]++
				case kindOf(err) == "io" && errors.As(err, &p):
					tally["io from a panic"]++
				default:
					tally[kindOf(err)]++
				}
			}()
		}
	}
	return fmt.Sprint(tally), nil
}

// wantCorruptIsIO: a corrupt file is the file's problem, never ursus's bug — so no
// flipped byte may panic, hang, or be an internal error. And at least one must
// reach a decoder's panic, or the sweep no longer tests the recovers at all: 17
// did when this was written, in the footer, in opening a column and in a page.
func wantCorruptIsIO(r probeReport) string {
	if r.Err != "" {
		return "the sweep failed: " + r.Err
	}
	for _, bad := range []string{"caller panic", "hang", "internal"} {
		if strings.Contains(r.Got, bad) {
			return "flipped bytes gave " + bad + ": " + r.Got
		}
	}
	if !strings.Contains(r.Got, "io from a panic") {
		return "no flipped byte reached a decoder's panic: " + r.Got
	}
	return ""
}

// TestPanicProbeHelper is the child half of TestPanicsAreErrors, and is skipped
// unless panicProbeEnv names a case. It runs that case once, recovering a panic
// that reaches it, and prints what it saw.
func TestPanicProbeHelper(t *testing.T) {
	name, ok := os.LookupEnv(panicProbeEnv)
	if !ok {
		t.Skip("the child half of TestPanicsAreErrors")
	}
	i := slices.IndexFunc(panicCases(), func(c panicCase) bool { return c.name == name })
	if i < 0 {
		t.Fatalf("no case %q", name)
	}
	c := panicCases()[i]

	base := runtime.NumGoroutine()
	var r probeReport
	func() {
		defer func() {
			if p := recover(); p != nil {
				r.Panic = fmt.Sprint(p)
			}
		}()
		got, err := c.run(t)
		r.Got = got
		if err != nil {
			r.Err, r.Kind = err.Error(), kindOf(err)
		}
	}()
	for deadline := time.Now().Add(time.Second); ; time.Sleep(5 * time.Millisecond) {
		if r.Leak = runtime.NumGoroutine() - base; r.Leak <= 0 || time.Now().After(deadline) {
			r.Leak = max(r.Leak, 0)
			break
		}
	}
	line, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(probeLine + string(line))
}

// probe runs one case in a child process and says what went wrong, or "".
func probe(t *testing.T, c panicCase) string {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestPanicProbeHelper$", "-test.count=1", "-test.timeout=5s")
	cmd.Env = append(os.Environ(), panicProbeEnv+"="+c.name)
	out, runErr := cmd.CombinedOutput()

	var r probeReport
	reported := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 1<<20)
	var first string // the first line of a crash
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, probeLine); ok {
			if err := json.Unmarshal([]byte(rest), &r); err != nil {
				t.Fatalf("the child's report: %v", err)
			}
			reported = true
		}
		if first == "" && (strings.HasPrefix(line, "panic: ") || strings.HasPrefix(line, "fatal error: ")) {
			first = line
		}
	}
	switch {
	case reported:
	case bytes.Contains(out, []byte("test timed out")):
		return "hang"
	case first != "":
		return "crash: " + first
	default:
		t.Fatalf("the child reported nothing (%v):\n%s", runErr, out)
	}

	var wrong []string
	if r.Panic != "" {
		wrong = append(wrong, "a panic reached the caller: "+r.Panic)
	} else if w := c.want(r); w != "" {
		wrong = append(wrong, w)
	}
	if r.Leak > 0 {
		wrong = append(wrong, fmt.Sprintf("%d goroutines leaked", r.Leak))
	}
	return strings.Join(wrong, "; ")
}

func TestPanicsAreErrors(t *testing.T) {
	cases := panicCases()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			wrong := probe(t, c)
			why, known := knownPanicDefects[c.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownPanicDefects (%s)", why)
			case known && !strings.HasPrefix(wrong, why):
				t.Errorf("wrong in a new way: %s\nlisted as: %s", wrong, why)
			case known:
				t.Logf("known defect: %s", wrong)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownPanicDefects {
		if !slices.ContainsFunc(cases, func(c panicCase) bool { return c.name == name }) {
			t.Errorf("knownPanicDefects names %q, which is not a case", name)
		}
	}
}

// TestALoopBodyPanicIsTheCallers: CollectBatches recovers a panic in planning and in
// every operator, and must not recover one in the caller's own loop body. Go forbids
// a range function to swallow one — "range function recovered a loop body panic and
// did not resume panicking" — so a recover around yield would change what the
// caller's own recover sees.
func TestALoopBodyPanicIsTheCallers(t *testing.T) {
	for _, name := range []string{"CollectBatches", "CollectRecords"} {
		got := func() (v any) {
			defer func() { v = recover() }()
			switch name {
			case "CollectBatches":
				for _, err := range panicFrame().CollectBatches(t.Context()) {
					if err != nil {
						return err
					}
					panic("mine")
				}
			case "CollectRecords":
				for _, err := range panicFrame().CollectRecords(t.Context()) {
					if err != nil {
						return err
					}
					panic("mine")
				}
			}
			return nil
		}()
		if got != "mine" {
			t.Errorf("%s: the loop body's panic came out as %v", name, got)
		}
	}
}

// TestEveryEntryPointRecovers reflects over *LazyFrame for every method that runs
// a query — whose last result is an error, or which returns an iterator of values
// and errors — and runs each on a plan that panics as it is resolved: an Alias with
// no child, which no public constructor builds. Each must return ErrInternal.
//
// A new entry point is swept the day it is written. One with a parameter this
// cannot supply fails, rather than being skipped.
func TestEveryEntryPointRecovers(t *testing.T) {
	bad := ursus.FromPlan(&plan.Project{Input: panicFrame().Plan(),
		Exprs: []expr.Node{&expr.Alias{Name: "x"}}})
	errType := reflect.TypeFor[error]()
	arg := func(in reflect.Type) reflect.Value {
		switch in {
		case reflect.TypeFor[context.Context]():
			return reflect.ValueOf(t.Context())
		case reflect.TypeFor[io.Writer]():
			return reflect.ValueOf(io.Writer(&bytes.Buffer{}))
		case reflect.TypeFor[string]():
			return reflect.ValueOf(filepath.Join(t.TempDir(), "out"))
		}
		t.Fatalf("no argument for a %s", in)
		return reflect.Value{}
	}

	v := reflect.ValueOf(bad)
	var swept []string
	for i := range v.NumMethod() {
		name, fn := v.Type().Method(i).Name, v.Method(i)
		ft := fn.Type()
		isErr := ft.NumOut() > 0 && ft.Out(ft.NumOut()-1) == errType
		isSeq := ft.NumOut() == 1 && ft.Out(0).Kind() == reflect.Func && ft.Out(0).NumIn() == 1 &&
			ft.Out(0).In(0).NumIn() == 2 && ft.Out(0).In(0).In(1) == errType
		if (!isErr && !isSeq) || name == "Err" { // Err reports a construction error; it runs nothing
			continue
		}
		var args []reflect.Value
		for j := range ft.NumIn() {
			if ft.IsVariadic() && j == ft.NumIn()-1 {
				break
			}
			args = append(args, arg(ft.In(j)))
		}

		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("panicked: %v", p)
				}
			}()
			out := fn.Call(args)
			if isErr {
				err, _ = out[len(out)-1].Interface().(error)
				return
			}
			yield := reflect.MakeFunc(out[0].Type().In(0), func(in []reflect.Value) []reflect.Value {
				if e, _ := in[1].Interface().(error); e != nil && err == nil {
					err = e
				}
				return []reflect.Value{reflect.ValueOf(true)}
			})
			out[0].Call([]reflect.Value{yield})
		}()
		if !recoveredPanic(err) {
			t.Errorf("%s: want a recovered panic, got %v", name, err)
		}
		swept = append(swept, name)
	}

	// A generic method is not in the reflected method set.
	if _, err := bad.CollectInto[struct{}](t.Context()); !recoveredPanic(err) {
		t.Errorf("CollectInto: want a recovered panic, got %v", err)
	}

	for _, want := range []string{"Collect", "CollectBatches", "CollectRecords", "CollectSchema",
		"Count", "Explain", "SinkCSV", "SinkParquet", "WriteCSV", "WriteParquet"} {
		if !slices.Contains(swept, want) {
			t.Errorf("the sweep did not reach %s; it reached %v", want, swept)
		}
	}
}

// recoveredPanic is ErrInternal whose cause is a recovered panic, and not an
// internal error raised some other way.
func recoveredPanic(err error) bool {
	var p *uerr.PanicError
	return errors.Is(err, ursus.ErrInternal) && errors.As(err, &p)
}

// goroutineSites names every go statement in the engine, by file, enclosing
// function and order within it, with the test that makes a panic there an error.
// TestEveryGoroutineIsCovered fails on a site not listed here, so a new goroutine
// cannot be started without saying what recovers its panics.
var goroutineSites = map[string]string{
	"internal/physical/parallel.go launch 1":    "TestParallelDispatcherPanicIsAnError",
	"internal/physical/parallel.go launch 2":    "TestParallelWorkerPanicIsAnError",
	"internal/physical/parjoin.go launch 1":     "TestParProbeDispatcherPanicIsAnError",
	"internal/physical/parjoin.go launch 2":     "TestPanicsAreErrors", // a kernel panic in a join's probe key
	"internal/physical/parallelsink.go drain 1": "TestParallelSinkWorkerPanicStopsConsuming",
	"internal/source/csv/csv.go convertAll 1":   "TestConvertAllRecoversAPanic",
}

func TestEveryGoroutineIsCovered(t *testing.T) {
	found := map[string]bool{}
	tests := map[string]bool{}
	repoFiles(t, func(path string, src []byte) {
		path = filepath.ToSlash(path)
		if strings.HasPrefix(path, ".claude/") {
			return
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(path, "_test.go") {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && strings.HasPrefix(fd.Name.Name, "Test") {
					tests[fd.Name.Name] = true
				}
			}
			return
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			n := 0
			ast.Inspect(fd, func(x ast.Node) bool {
				if _, ok := x.(*ast.GoStmt); ok {
					n++
					found[fmt.Sprintf("%s %s %d", path, fd.Name.Name, n)] = true
				}
				return true
			})
		}
	})
	for site := range found {
		if _, ok := goroutineSites[site]; !ok {
			t.Errorf("%s starts a goroutine and goroutineSites does not list it: recover "+
				"a panic in what it runs, and name the test that shows it", site)
		}
	}
	for site, test := range goroutineSites {
		if !found[site] {
			t.Errorf("goroutineSites lists %s, which starts no goroutine", site)
		}
		if !tests[test] {
			t.Errorf("goroutineSites names %s for %s, and there is no such test", test, site)
		}
	}
}
