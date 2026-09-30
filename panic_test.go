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
	"math"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus"
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
	"udf, 1 thread":                            "a panic reached the caller: boom at 3",
	"udf, 4 threads":                           "crash: panic: boom at 3",
	"map_batches, 4 threads":                   "crash: panic: boom in a batch",
	"udf in a join's probe key, 4 threads":     "crash: panic: boom at 3",
	"udf inside an aggregate, 4 threads":       "crash: panic: boom at 3",
	"udf below an aggregate, 4 threads":        "crash: panic: boom at 3",
	"udf under Count, 1 thread":                "a panic reached the caller: boom at 3",
	"udf under CollectBatches, 1 thread":       "a panic reached the caller: boom at 3",
	"Str().Slice to MaxInt64":                  "crash: panic: runtime error: slice bounds out of range",
	"Rank(RankMethod(99))":                     "crash: panic: runtime error: index out of range",
	"Quantile(Interpolation(99))":              "no error, got [2 3 5]",
	"Parquet List(Int8)":                       `a panic reached the caller: ursus: data: column "item" declares Int8`,
	"Parquet List(Int16)":                      `a panic reached the caller: ursus: data: column "item" declares Int16`,
	"Parquet LZ4":                              "a panic reached the caller: compression for LZ4 unimplemented",
	"Parquet LZO":                              "a panic reached the caller: compression for LZO unimplemented",
	"the zero Expr, as an operand":             "a panic reached the caller: runtime error: invalid memory address",
	"the zero Expr, aliased":                   "a panic reached the caller: runtime error: invalid memory address",
	"Parquet whose row group claims more rows": "hang",
	"Parquet, every byte flipped":              "a flipped byte panicked or hung: map[caller panic:",
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

// wantErr is right when the call failed with this kind of error, naming each of
// words.
func wantErr(kind string, words ...string) func(probeReport) string {
	return func(r probeReport) string {
		if r.Err == "" {
			return fmt.Sprintf("no error, got %s; want a %s error", r.Got, kind)
		}
		if r.Kind != kind {
			return fmt.Sprintf("a %s error, want %s: %s", r.Kind, kind, r.Err)
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
			return fmt.Sprintf("a %s error, want %s: %s", r.Kind, got, r.Err)
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

	cases := []panicCase{
		// A udf is user code. Its panic is the user's, as its errors are.
		{"udf, 1 thread", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return collectCells(ctx, withUDF, "w", threads(1))
		}, udfErr("boom")},
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
			return readList[int8](ctx, rawList(t, 8))
		}, wantGot("[[1 2] [3]]")},
		{"Parquet List(Int16)", func(t *testing.T) (string, error) {
			ctx := t.Context()
			return readList[int16](ctx, rawList(t, 16))
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
		}, wantNoCallerPanic},
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

// readList reads column L of the file at path.
func readList[T int8 | int16](ctx context.Context, path string) (string, error) {
	df, err := ursus.ScanParquet(path).Collect(ctx, threads(4))
	if err != nil {
		return "", err
	}
	rows, err := df.Rows[struct{ L []T }]()
	if err != nil {
		return "", err
	}
	out := make([][]T, len(rows))
	for i, r := range rows {
		out[i] = r.L
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
				switch {
				case err == nil:
					tally["result"]++
				case errors.Is(err, context.DeadlineExceeded):
					tally["hang"]++
				default:
					tally[kindOf(err)]++
				}
			}()
		}
	}
	return fmt.Sprint(tally), nil
}

func wantNoCallerPanic(r probeReport) string {
	if r.Err != "" {
		return "the sweep failed: " + r.Err
	}
	if strings.Contains(r.Got, "caller panic") || strings.Contains(r.Got, "hang") {
		return "a flipped byte panicked or hung: " + r.Got
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
