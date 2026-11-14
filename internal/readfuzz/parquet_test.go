package readfuzz_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/parquet/compress"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/fuzzcheck"
	"github.com/advenn/ursus/internal/source/parquet"
	"github.com/advenn/ursus/ursustest"
)

// parquetSeeds are the Parquet target's seed corpus (step 510, step 448 of
// v0.7.2-plan.md): the Parquet fixtures other engines wrote; a frame of every flat
// type ursus writes, in row groups of eight, under each of the six codecs it writes;
// tinyParquet's file and inflatedParquet's row count, as the root's panic tests make
// them; the forged files (pqForgedSeeds); and the bytes no file is.
func parquetSeeds(f *testing.F) [][]byte {
	var seeds [][]byte
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "parquet", "*.parquet"))
	if err != nil || len(paths) == 0 {
		f.Fatalf("the Parquet fixtures: %v, %d files", err, len(paths))
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		seeds = append(seeds, b)
	}

	n := 20
	i64 := make([]int64, n)
	valid := make([]bool, n)
	f64 := make([]float64, n)
	s := make([]string, n)
	bl := make([]bool, n)
	ts := make([]time.Time, n)
	bin := make([][]byte, n)
	i32 := make([]int32, n)
	for i := range n {
		i64[i], valid[i] = int64(i*i-50), i%4 != 0
		f64[i] = float64(i) / 3
		s[i] = fmt.Sprint("s", i%5)
		bl[i] = i%3 == 0
		ts[i] = time.Date(2024, 1, 1+i, i, 0, 0, 0, time.UTC)
		bin[i] = []byte{byte(i), 0xA5, byte(255 - i)}
		i32[i] = int32(-i)
	}
	f64[1], f64[2], f64[3] = math.NaN(), math.Inf(-1), math.Copysign(0, -1)
	frame := ursus.Frame(ursus.ValuesNullable("i", i64, valid), ursus.Values("f", f64),
		ursus.Values("s", s), ursus.Values("b", bl), ursus.Values("t", ts),
		ursus.Values("x", bin), ursus.Values("n", i32))
	for _, c := range []compress.Compression{compress.Codecs.Uncompressed, compress.Codecs.Snappy,
		compress.Codecs.Gzip, compress.Codecs.Brotli, compress.Codecs.Zstd, compress.Codecs.Lz4Raw} {
		var buf bytes.Buffer
		if err := frame.WriteParquet(f.Context(), &buf, ursus.WithCompression(c), ursus.WithRowGroupRows(8)); err != nil {
			f.Fatal(err)
		}
		seeds = append(seeds, buf.Bytes())
	}

	tiny := tinyParquet(f)
	inflated := bytes.Clone(tiny)
	footer := len(inflated) - 8 - int(uint32(inflated[len(inflated)-8])|uint32(inflated[len(inflated)-7])<<8|
		uint32(inflated[len(inflated)-6])<<16|uint32(inflated[len(inflated)-5])<<24)
	copy(inflated[footer:], bytes.ReplaceAll(inflated[footer:], []byte{0x16, 74}, []byte{0x16, 126}))
	seeds = append(seeds, tiny, inflated, tiny[:len(tiny)/2])

	seeds = append(seeds, pqForgedSeeds()...)
	return append(seeds, nil, []byte("PAR1"), []byte("PAR1PAR1"), []byte("PAR1\x00\x00\x00\x00PAR1"))
}

// tinyParquet is the root's panic tests' file: 37 rows, so its row count is one
// distinctive byte in the footer.
func tinyParquet(f *testing.F) []byte {
	v := make([]int64, 37)
	s := make([]string, 37)
	for i := range v {
		v[i], s[i] = int64(i), fmt.Sprint("s", i%5)
	}
	var buf bytes.Buffer
	if err := ursus.Frame(ursus.Values("v", v), ursus.Values("s", s)).WriteParquet(f.Context(), &buf); err != nil {
		f.Fatal(err)
	}
	return buf.Bytes()
}

// FuzzScanParquet: any bytes, read as a Parquet file through the public API, are a
// frame or an error, inside fuzzcheck's limits, with every page decompressed into
// buffers of a bounded, poisoned allocator (step 510, step 448 of v0.7.2-plan.md).
// The allocator refuses one request over 16 MiB plus 16 bytes a byte of input, so a
// length taken from a header and allocated before its data is read fails the input.
//
// The read is the schema, then the whole frame on one thread and on four; where the
// whole frame reads, each other way of reading the file must agree with it:
//   - on four threads, the same frame;
//   - read again with buffers that are not poisoned, the same frame: an answer that
//     changes with what fresh memory holds read memory it had not written, the
//     pool's "not zeroed" assumption broken (pool.go);
//   - Head(1), its first row; a Select of its last column, that column;
//   - a Filter with pruning off, the same Filter of the frame in memory; with
//     pruning on, a subsequence of those rows, since a row group the statistics rule
//     out goes whole, and a file's statistics can lie;
//   - Count, its height.
//
// Where the whole frame does not read, the other ways are still read, for the
// crashes, hangs and allocations Run judges, and each error is judged with the rest.
func FuzzScanParquet(f *testing.F) {
	for _, b := range parquetSeeds(f) {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		mem := fuzzcheck.NewAllocator(len(b))
		mem.Poison = true
		defer parquet.ReadPagesWith(mem)()
		lim := limits(len(b))
		lim.Mem = mem
		fuzzcheck.Run(t, lim, func() error { return readParquet(t, b, mem.Limit) })
	})
}

// readParquet is FuzzScanParquet's one input: every error the reads answer, joined
// for Run to judge, and t failed on a disagreement.
func readParquet(t *testing.T, b []byte, limit int) error {
	ctx := t.Context()
	lf := ursus.ScanParquetBytes(b, "fuzz.parquet")
	sch, err := lf.CollectSchema(ctx)
	if err != nil {
		return err
	}
	var errs []error
	read := func(lf *ursus.LazyFrame, opts ...ursus.CollectOption) *ursus.DataFrame {
		df, err := lf.Collect(ctx, append(opts, ursus.WithVerify())...)
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		return df
	}

	one := read(lf, ursus.WithThreads(1))
	four := read(lf, ursus.WithThreads(4))
	if (one == nil) != (four == nil) {
		t.Fatalf("one thread and four disagree on whether the file reads: %v", errors.Join(errs...))
	}
	if one == nil {
		// Each other way, for what Run judges: a part of a file can read where the
		// whole does not.
		read(lf.Head(1))
		if sch.Len() > 0 {
			read(lf.Select(ursus.Col(sch.Field(sch.Len() - 1).Name)))
			read(lf.Filter(parquetFilter(sch, len(b))))
		}
		if _, err := lf.Count(ctx); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	ursustest.AssertSchemaEqual(t, one.Schema(), sch, ursustest.CheckNullability())
	ursustest.AssertFrameEqual(t, four, one)

	clean := &fuzzcheck.Allocator{Limit: limit}
	restore := parquet.ReadPagesWith(clean)
	fresh := read(lf, ursus.WithThreads(1))
	restore()
	if fresh == nil {
		t.Fatalf("the file reads into poisoned buffers and not into zeroed ones: %v", errors.Join(errs...))
	}
	ursustest.AssertFrameEqual(t, one, fresh)

	want := func(got *ursus.DataFrame, what string) *ursus.DataFrame {
		if got == nil {
			t.Fatalf("the whole file reads, and %s does not: %v", what, errors.Join(errs...))
		}
		return got
	}
	head, err := one.Head(ctx, 1)
	if err != nil {
		return err
	}
	ursustest.AssertFrameEqual(t, want(read(lf.Head(1)), "Head(1)"), head)

	if sch.Len() > 0 {
		last := ursus.Col(sch.Field(sch.Len() - 1).Name)
		col, err := one.Select(ctx, last)
		if err != nil {
			return err
		}
		ursustest.AssertFrameEqual(t, want(read(lf.Select(last)), "a Select of its last column"), col)

		pred := parquetFilter(sch, len(b))
		inMemory, err := one.Lazy().Filter(pred).Collect(ctx)
		if err != nil {
			return err
		}
		unpruned := ursus.ScanParquetBytes(b, "fuzz.parquet", ursus.WithPruning(false)).Filter(pred)
		ursustest.AssertFrameEqual(t, want(read(unpruned), "a Filter with pruning off"), inMemory)
		if pruned := read(lf.Filter(pred)); pruned != nil {
			rowsWithin(t, pruned, inMemory)
		}
	}

	n, err := lf.Count(ctx)
	if err != nil {
		t.Fatalf("the whole file reads, and Count does not: %v", err)
	}
	if n != int64(one.Height()) {
		t.Fatalf("Count is %d, and the file reads as %d rows", n, one.Height())
	}
	return errors.Join(errs...)
}

// parquetFilter is a Filter the statistics of the file's first column can prune by, or
// IsNotNull where they cannot: a comparison with a literal of the column's kind,
// moved by the input's length.
func parquetFilter(sch *ursus.Schema, n int) ursus.Expr {
	for i := range sch.Len() {
		fd := sch.Field(i)
		c := ursus.Col(fd.Name)
		switch fd.Type.ID() {
		case dtype.TypeInt8, dtype.TypeInt16, dtype.TypeInt32, dtype.TypeInt64,
			dtype.TypeUint8, dtype.TypeUint16, dtype.TypeUint32, dtype.TypeUint64:
			return c.Gt(n % 23)
		case dtype.TypeFloat32, dtype.TypeFloat64:
			return c.Gt(float64(n%23) / 4)
		case dtype.TypeString:
			return c.Gt(fmt.Sprint("s", n%5))
		}
	}
	return ursus.Col(sch.Field(0).Name).IsNotNull()
}

// rowsWithin fails t unless got's rows are some of want's, in want's order.
func rowsWithin(t *testing.T, got, want *ursus.DataFrame) {
	t.Helper()
	rows := func(df *ursus.DataFrame) []string {
		out := make([]string, df.Height())
		for i := range out {
			r, err := df.Row(i)
			if err != nil {
				t.Fatal(err)
			}
			out[i] = fmt.Sprintf("%#v", r)
		}
		return out
	}
	g, w := rows(got), rows(want)
	j := 0
	for _, r := range g {
		k := slices.Index(w[j:], r)
		if k < 0 {
			t.Fatalf("pruned, the Filter answers a row it does not answer unpruned: %s\npruned:\n%s\nunpruned:\n%s",
				r, got, want)
		}
		j += k + 1
	}
}

// TestAPageClaimingMoreThanItsChunkIsRefusedBeforeItsBuffer (D14 of v0.7.2-plan.md,
// step 510): a data page whose header claims 2^28 bytes uncompressed, or 2^26
// compressed, arrow-go's own limits, in a file of 161 bytes. arrow-go sized the
// page's buffer by the claim before reading a byte of the page, and asked ursus's
// pool for 256 MiB, or 64 MiB. A page lies inside its column chunk, so the claim is
// refused now against the largest chunk the footer declares, as corrupt, with no
// request past the bounded allocator's limit for the file's size. The honest file,
// its header's sizes its page's, reads as the values its page holds, on one thread
// and on four.
func TestAPageClaimingMoreThanItsChunkIsRefusedBeforeItsBuffer(t *testing.T) {
	vals := []int64{3, 1, 4, 1, 5, 9, 2, 6}
	page := pqInt64Page(vals)
	file := func(uncompressed, compressed int32) []byte {
		p := page
		p.uncompressed, p.compressed = uncompressed, compressed
		return pqForged{phys: pqInt64, rows: int64(len(vals)), pages: []pqPage{p}}.bytes()
	}
	want, err := ursus.Frame(ursus.Values("v", vals)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name                     string
		uncompressed, compressed int32
	}{
		{"honest", 0, 0},
		{"2^28 bytes uncompressed", 1 << 28, 0},
		{"2^26 bytes compressed", 0, 1 << 26},
	} {
		b := file(c.uncompressed, c.compressed)
		for _, threads := range []int{1, 4} {
			mem := fuzzcheck.NewAllocator(len(b))
			restore := parquet.ReadPagesWith(mem)
			got, err := ursus.ScanParquetBytes(b, "pqForged.parquet").Collect(t.Context(), ursus.WithThreads(threads))
			restore()
			for _, r := range mem.Refusals() {
				t.Errorf("%s, %d threads, a file of %d bytes: a request for %d bytes, over the limit of %d",
					c.name, threads, len(b), r.Size, r.Limit)
			}
			switch {
			case c.name == "honest" && err != nil:
				t.Errorf("the honest file, %d threads: %v", threads, err)
			case c.name == "honest":
				ursustest.AssertFrameEqual(t, got, want)
			case err == nil:
				t.Errorf("%s, %d threads: read as %d rows, and no error", c.name, threads, got.Height())
			}
		}
	}
}

// TestAChunkClaimingMoreThanItsFileIsRefused (step 510): a footer whose column chunk
// claims 2^40 bytes, or as many as the whole file from where it begins, or -1.
// arrow-go read a chunk into a slice made of its claim before the read came up short,
// and 2^40, a seed of FuzzScanParquet, asked the runtime for a terabyte: a fatal "out
// of memory", no error. The footer is refused now as corrupt, within the harness's
// bounds.
func TestAChunkClaimingMoreThanItsFileIsRefused(t *testing.T) {
	vals := []int64{3, 1, 4, 1, 5, 9, 2, 6}
	size := int64(len(pqForged{phys: pqInt64, rows: int64(len(vals)), pages: []pqPage{pqInt64Page(vals)}}.bytes()))
	for _, claim := range []int64{1 << 40, size, -1} {
		b := pqForged{phys: pqInt64, rows: int64(len(vals)), pages: []pqPage{pqInt64Page(vals)}, chunk: claim}.bytes()
		fuzzcheck.Run(t, limits(len(b)), func() error {
			df, err := ursus.ScanParquetBytes(b, "pqForged.parquet").Collect(t.Context())
			if err == nil {
				t.Errorf("a chunk claiming %d bytes in a file of %d: read as %d rows, and no error", claim, len(b), df.Height())
			}
			return nil
		})
	}
}

// TestADictionaryPageClaimingMoreValuesThanItsBytesIsRefused (step 510): a
// dictionary page of 12 bytes, two strings, whose header claims 2^31−1 values, read
// by the reader's own pages and, the footer listing an encoding they do not read, by
// arrow-go's column reader. arrow-go made a slice of the claim before decoding a
// value, 48 GB, a fatal "out of memory"; the reader's own pages refused it (step
// 417). Both refuse it now, within the harness's bounds, and the honest page reads
// the same both ways.
func TestADictionaryPageClaimingMoreValuesThanItsBytesIsRefused(t *testing.T) {
	honest := pqDictPages([]string{"ab", "cd"}, 5)
	claims := append([]pqPage(nil), honest...)
	claims[0].values = 1<<31 - 1
	var read []*ursus.DataFrame
	for _, encs := range [][]int32{nil, {pqDeltaLength}} {
		b := pqForged{phys: pqByteArray, rows: 5, pages: claims, encs: encs}.bytes()
		fuzzcheck.Run(t, limits(len(b)), func() error {
			if df, err := ursus.ScanParquetBytes(b, "forged.parquet").Collect(t.Context()); err == nil {
				t.Errorf("listing %v: 2^31−1 values in 12 bytes read as %d rows, and no error", encs, df.Height())
			}
			return nil
		})
		df, err := ursus.ScanParquetBytes(pqForged{phys: pqByteArray, rows: 5, pages: honest, encs: encs}.bytes(),
			"honest.parquet").Collect(t.Context())
		if err != nil {
			t.Fatalf("listing %v, the honest page: %v", encs, err)
		}
		read = append(read, df)
	}
	ursustest.AssertFrameEqual(t, read[1], read[0])
}

// TestATimePastItsDayIsRefused (step 510): a Parquet TIME holding a tick outside
// [0, 24h), which FuzzOwnPagesMatchArrow's first CI run made of a fixture. The
// reader built a Time column of it, and a Time is a time of day: data's boundary
// check, on in the targets, called the batch ursus's own bug. It is refused now as
// a corrupt value, read by the reader's own pages and, the footer listing an
// encoding they do not read, by arrow-go's, and inside a List. Honest ticks read as
// the same ticks cast to a Time.
func TestATimePastItsDayIsRefused(t *testing.T) {
	ctx := t.Context()
	const day = 86_400_000_000
	honest := []int64{0, 45_296_789_012, day - 1}
	micro := ursus.Col("v").Cast(ursus.TimeOf(ursus.Micro))
	want, err := ursus.Frame(ursus.Values("v", honest)).Select(micro).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, encs := range [][]int32{nil, {pqDeltaBinary}} {
		read := func(ticks []int64) (*ursus.DataFrame, error) {
			b := pqForged{phys: pqInt64, rows: int64(len(ticks)), pages: []pqPage{pqInt64Page(ticks)},
				encs: encs, conv: pqTimeMicros}.bytes()
			return ursus.ScanParquetBytes(b, "time.parquet").Collect(ctx)
		}
		got, err := read(honest)
		if err != nil {
			t.Fatalf("listing %v, honest ticks: %v", encs, err)
		}
		ursustest.AssertFrameEqual(t, got, want)
		for _, past := range [][]int64{{0, day}, {-1}, {day + 45_296_789_012}} {
			// Inside Run, which fails an ErrInternal: data's boundary check, on in this
			// binary, would otherwise answer for the reader.
			fuzzcheck.Run(t, limits(1024), func() error {
				df, err := read(past)
				if err == nil {
					t.Errorf("listing %v, the ticks %v: read as %d rows, and no error", encs, past, df.Height())
				}
				return err
			})
		}
	}

	lists := ursus.Frame(ursus.Values("v", honest)).Select(micro.Implode())
	wantLists, err := lists.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := lists.WriteParquet(ctx, &buf, ursus.WithCompression(compress.Codecs.Uncompressed)); err != nil {
		t.Fatal(err)
	}
	got, err := ursus.ScanParquetBytes(buf.Bytes(), "lists.parquet").Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, wantLists)
	tick := binary.LittleEndian.AppendUint64(nil, 45_296_789_012)
	past := binary.LittleEndian.AppendUint64(nil, day+45_296_789_012)
	if !bytes.Contains(buf.Bytes(), tick) {
		t.Fatal("the List's file does not hold its tick as it lies")
	}
	b := bytes.ReplaceAll(buf.Bytes(), tick, past)
	fuzzcheck.Run(t, limits(len(b)), func() error {
		df, err := ursus.ScanParquetBytes(b, "lists.parquet").Collect(ctx)
		if err == nil {
			t.Errorf("a List holding a Time past its day: read as %d rows, and no error", df.Height())
		}
		return err
	})
}
