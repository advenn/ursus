// Package engine is the Go side of the benchmark runner contract.
//
// It mirrors bench/engines/py/_common.py exactly: the same flags, the same JSON
// payload, the same timing rules. Measurement happens inside this process so a
// Go binary is not charged for a Python interpreter's startup, and a Python
// process is not charged for a linker's.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Args is one invocation: which query, over which data, how many times.
type Args struct {
	Engine       string
	Suite        string
	Query        string
	Data         string
	IO           string
	Iterations   int
	Threads      int
	Out          string
	Result       string
	ChecksumCols []string
}

// ChecksumMode reports whether the answer is compared as a row count plus
// column sums rather than in full. h2o answers are far too large to diff.
func (a Args) ChecksumMode() bool { return len(a.ChecksumCols) > 0 }

// TablePath locates an input table, matching Args.table_path on the Python side.
func (a Args) TablePath(table string) string {
	suffix := "parquet"
	if a.IO == "csv" {
		suffix = "csv"
	}
	if a.Suite == "pdsh" {
		return filepath.Join(a.Data, suffix, table+"."+suffix)
	}
	sub := "join"
	if table == "g1" {
		sub = "groupby"
	}
	return filepath.Join(a.Data, sub, table+"."+suffix)
}

// h2oInputs records which tables each h2o query reads, mirroring H2O_INPUTS in
// engines/py/_common.py. Loading only these keeps the eager engines from paying
// for tables the query never touches.
var h2oInputs = map[string][]string{
	"gb1": {"g1"}, "gb2": {"g1"}, "gb3": {"g1"}, "gb4": {"g1"}, "gb5": {"g1"},
	"gb6": {"g1"}, "gb7": {"g1"}, "gb8": {"g1"}, "gb9": {"g1"}, "gb10": {"g1"},
	"j1": {"x", "small"},
	"j2": {"x", "medium"},
	"j3": {"x", "medium"},
	"j4": {"x", "medium"},
	"j5": {"x", "big"},
}

var pdshTables = []string{
	"region", "nation", "supplier", "customer", "part", "partsupp", "orders", "lineitem",
}

// Inputs lists the tables this query reads.
func (a Args) Inputs() []string {
	if a.Suite == "pdsh" {
		return pdshTables
	}
	return h2oInputs[a.Query]
}

// Answer is what one execution of a query produced.
//
// Deliberately write-oriented rather than value-oriented: an engine that can
// stream its result straight to Parquet, or push a SUM back down into its own
// execution layer, should be allowed to. Forcing every engine through a common
// in-memory representation would charge some of them for a conversion the
// benchmark is not trying to measure.
type Answer interface {
	// Rows is the answer's row count.
	Rows() int
	// WriteFrame persists the complete answer as Parquet.
	WriteFrame(ctx context.Context, path string) error
	// Checksum sums the named columns as float64, skipping nulls.
	Checksum(ctx context.Context, cols []string) (map[string]float64, error)
}

// Once executes the query one time. It is called fresh for every iteration, so
// no plan, frame or file handle may survive between calls.
type Once func(ctx context.Context) (Answer, error)

// Build prepares a query for execution. Anything an engine can legitimately do
// ahead of time — reading the SQL text off disk, looking the query up in a
// registry — belongs here, not in Once.
type Build func(Args) (Once, error)

// ErrUnsupported marks a query an engine cannot express, or an engine that was
// compiled out. It is a normal outcome and lands in the results as such.
type ErrUnsupported struct{ Reason string }

func (e *ErrUnsupported) Error() string { return e.Reason }

// Unsupported is shorthand for returning an ErrUnsupported.
func Unsupported(format string, args ...any) error {
	return &ErrUnsupported{Reason: fmt.Sprintf(format, args...)}
}

var registry = map[string]Build{}

// Register adds an engine. Called from package init functions so that engines
// behind build tags simply are not there when the tag is off.
func Register(name string, build Build) {
	if _, exists := registry[name]; exists {
		panic("engine registered twice: " + name)
	}
	registry[name] = build
}

// Lookup finds a registered engine.
func Lookup(name string) (Build, bool) {
	build, ok := registry[name]
	return build, ok
}

// Names lists the engines compiled into this binary.
func Names() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ParseArgs reads the runner contract off the command line.
func ParseArgs() Args {
	var a Args
	var checksum string
	flag.StringVar(&a.Engine, "engine", "", "which registered engine to run")
	flag.StringVar(&a.Suite, "suite", "", "pdsh | h2o")
	flag.StringVar(&a.Query, "query", "", "query name, e.g. q1 or gb3")
	flag.StringVar(&a.Data, "data", "", "dataset directory")
	flag.StringVar(&a.IO, "io", "parquet", "parquet | csv")
	flag.IntVar(&a.Iterations, "iterations", 3, "timed iterations")
	flag.IntVar(&a.Threads, "threads", 0, "0 = all cores")
	flag.StringVar(&a.Out, "out", "", "where to write the result JSON")
	flag.StringVar(&a.Result, "result", "", "where to write the answer parquet")
	flag.StringVar(&checksum, "checksum-cols", "", "comma list; empty = compare the full frame")
	flag.Parse()

	for _, name := range strings.Split(checksum, ",") {
		if name = strings.TrimSpace(name); name != "" {
			a.ChecksumCols = append(a.ChecksumCols, name)
		}
	}
	return a
}

type payload struct {
	Engine       string    `json:"engine"`
	Suite        string    `json:"suite"`
	Query        string    `json:"query"`
	IO           string    `json:"io"`
	Threads      int       `json:"threads"`
	Iterations   []float64 `json:"iterations"`
	Rows         int       `json:"rows"`
	PeakRSSBytes int64     `json:"peak_rss_bytes"`
	StartupS     float64   `json:"startup_s"`
	Status       string    `json:"status"`
	Error        *string   `json:"error"`
	ResultPath   *string   `json:"result_path"`
}

// PeakRSS reads VmHWM, the same number the Python harness reports, from the
// same place. Anything else would be comparing two different definitions of
// "memory used".
func PeakRSS() int64 {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

// Run drives one engine end to end and writes the result JSON.
//
// The exit code is 0 for a completed or legitimately-unsupported run and 1 for
// a failure, but the driver reads the JSON either way: the file is the result,
// the exit code only distinguishes "finished" from "died".
func Run(a Args, build Build, startup time.Duration) int {
	out := payload{
		Engine:     a.Engine,
		Suite:      a.Suite,
		Query:      a.Query,
		IO:         a.IO,
		Threads:    a.Threads,
		Iterations: []float64{},
		Rows:       -1,
		StartupS:   startup.Seconds(),
		Status:     "ok",
	}

	if err := execute(a, build, &out); err != nil {
		var unsupported *ErrUnsupported
		if errors.As(err, &unsupported) {
			out.Status = "unsupported"
		} else {
			out.Status = "error"
		}
		message := err.Error()
		out.Error = &message
	}

	out.PeakRSSBytes = PeakRSS()
	if out.Status == "ok" {
		path := a.Result
		out.ResultPath = &path
	}
	writeJSON(a.Out, out)

	if out.Status == "error" {
		return 1
	}
	return 0
}

func execute(a Args, build Build, out *payload) error {
	ctx := context.Background()

	once, err := build(a)
	if err != nil {
		return err
	}

	// Warm-up: page cache, lazily-started worker pools, first-touch allocations.
	warm, err := once(ctx)
	if err != nil {
		return err
	}
	release(warm)

	var answer Answer
	for i := 0; i < a.Iterations; i++ {
		started := time.Now()
		next, err := once(ctx)
		if err != nil {
			return err
		}
		out.Iterations = append(out.Iterations, time.Since(started).Seconds())
		// Some engines hold a live connection or an allocator arena behind
		// their Answer. Dropping the reference is not enough to give it back,
		// and keeping four of them alive would distort the peak-RSS figure this
		// process is also reporting.
		release(answer)
		answer = next
	}

	out.Rows = answer.Rows()
	defer release(answer)
	return writeAnswer(ctx, a, answer)
}

// release frees an answer that is being superseded, if it holds anything.
func release(a Answer) {
	if closer, ok := a.(io.Closer); ok {
		closer.Close()
	}
}

// writeAnswer persists the answer for validation, outside the timed region.
func writeAnswer(ctx context.Context, a Args, answer Answer) error {
	if err := os.MkdirAll(filepath.Dir(a.Result), 0o755); err != nil {
		return err
	}
	if !a.ChecksumMode() {
		return answer.WriteFrame(ctx, a.Result)
	}
	sums, err := answer.Checksum(ctx, a.ChecksumCols)
	if err != nil {
		return err
	}
	return WriteChecksum(a.Result, answer.Rows(), a.ChecksumCols, sums)
}

func writeJSON(path string, out payload) {
	blob, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshalling result:", err)
		return
	}
	blob = append(blob, '\n')
	if path == "" {
		os.Stdout.Write(blob)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "creating result directory:", err)
		return
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "writing result:", err)
	}
}
