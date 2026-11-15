package parquet

import (
	"encoding/binary"
	"fmt"
	"maps"
	"math"
	"math/bits"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/internal/fuzzcheck"
)

// TestCheckedPagesRefuseMalformedDeltaRuns (step 531): checkedPages against arrow-go's
// own column reader, and against the generator, over generated delta-encoded data
// pages: INT64 and INT32 columns under DELTA_BINARY_PACKED, REQUIRED, OPTIONAL and the
// element of a List, and BYTE_ARRAY columns under DELTA_LENGTH_BYTE_ARRAY and
// DELTA_BYTE_ARRAY, V1 and V2, their definition levels RLE or, in V1, BIT_PACKED;
// each run in blocks of 128 to 1,024 values in miniblocks of 32 to 1,024, or now and
// then one miniblock of 65,536, the most past a run's count, its deltas 0 to 64 bits
// wide, an INT32's past 32 as arrow-go's writer packs them, and in a third of the
// pages the miniblocks holding no value giving any width. Most pages are then forged
// once, in one run: its block size or its miniblocks wrong, or a layout the format
// does not allow, blocks of a multiple of 32 values not one of 128 or miniblocks of
// fewer than 32; a miniblock holding values 65 bits wide
// or more; one miniblock of 2^17 to 2^31 values 0 bits wide; its count short of the
// values, past the page's or its column chunk's, or, in a page with nulls, past the
// values within the page's and the last miniblock's padding; the page cut short
// inside the run's blocks, or inside its header. Each page is read whole through
// arrow-go's column reader through checkedPages, inside fuzzcheck.Run's wall bound:
//
//   - checkedPages refuses the page as corrupt exactly where the generator broke the
//     format where the check looks; a header cut short it leaves to arrow-go;
//   - where it does not refuse it, the read answers what arrow-go's own read
//     answers, values, levels and error alike;
//   - where the page is honest, or forged only where the format allows, the read
//     answers the generator's values and levels.
func TestCheckedPagesRefuseMalformedDeltaRuns(t *testing.T) {
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewPCG(seed, 531))
	pool := &sync.Pool{New: func() any { return memory.NewResizableBuffer(memory.DefaultAllocator) }}
	type kind struct {
		col *schema.Column
		enc parquet.Encoding
	}
	dbp, dlba, dba := parquet.Encodings.DeltaBinaryPacked, parquet.Encodings.DeltaLengthByteArray,
		parquet.Encodings.DeltaByteArray
	req, opt := parquet.Repetitions.Required, parquet.Repetitions.Optional
	kinds := []kind{
		{deltaColumn(t, parquet.Types.Int64, req, false), dbp},
		{deltaColumn(t, parquet.Types.Int64, opt, false), dbp},
		{deltaColumn(t, parquet.Types.Int64, opt, true), dbp},
		{deltaColumn(t, parquet.Types.Int32, req, false), dbp},
		{deltaColumn(t, parquet.Types.ByteArray, req, false), dlba},
		{deltaColumn(t, parquet.Types.ByteArray, opt, false), dlba},
		{deltaColumn(t, parquet.Types.ByteArray, req, false), dba},
		{deltaColumn(t, parquet.Types.ByteArray, opt, true), dba},
	}
	tally := map[string]int{}
	pages := 3_000
	if raceDetector {
		pages = 600 // the race detector's instrumentation costs some ten times as much a page
	}
	for i := range pages {
		k := kinds[rng.IntN(len(kinds))]
		pg := genDeltaPage(rng, k.col, k.enc)
		label := fmt.Sprintf("page %d (%s)", i, pg)
		var ours, theirs deltaRead
		fuzzcheck.Run(t, fuzzcheck.Limits{Wall: 10 * time.Second}, func() error {
			ours = readDeltaPage(pg.col, pg.reader(true), pool)
			if ours.err == nil || !strings.Contains(ours.err.Error(), "corrupt: column") {
				theirs = readDeltaPage(pg.col, pg.reader(false), pool)
			}
			return nil
		})
		refused := ours.err != nil && strings.Contains(ours.err.Error(), "corrupt: column")
		tally[fmt.Sprintf("%s refused %v", pg.forged, refused)]++
		switch {
		case refused != pg.refused:
			t.Errorf("%s: checkedPages answered %v", label, ours.err)
		case refused:
		case fmt.Sprint(ours.err) != fmt.Sprint(theirs.err) || !slices.Equal(ours.vals, theirs.vals) ||
			!slices.Equal(ours.defs, theirs.defs) || !slices.Equal(ours.reps, theirs.reps):
			t.Errorf("%s: through checkedPages %d values and %v; arrow-go's own %d and %v", label, len(ours.vals),
				ours.err, len(theirs.vals), theirs.err)
		case pg.reads && (ours.err != nil || !slices.Equal(ours.vals, pg.vals) || !slices.Equal(ours.defs, pg.defs) ||
			!slices.Equal(ours.reps, pg.reps)):
			t.Errorf("%s: read as %d values, %d levels and %v; the generator's %d values and %d levels", label,
				len(ours.vals), len(ours.defs), ours.err, len(pg.vals), len(pg.defs))
		}
	}
	// The generator's own reach: each forgery, and each outcome.
	var lines []string
	for _, k := range slices.Sorted(maps.Keys(tally)) {
		lines = append(lines, fmt.Sprintf("%s: %d", k, tally[k]))
	}
	t.Logf("pages:\n%s", strings.Join(lines, "\n"))
	for _, f := range []string{"honest", "spare widths", "block", "miniblocks", "layout", "wide", "count short",
		"count past the page", "count past the values", "cut", "header cut", "huge miniblock", "chunk"} {
		if tally[f+" refused true"]+tally[f+" refused false"] < pages/150 {
			t.Errorf("forgery %q made %d pages", f, tally[f+" refused true"]+tally[f+" refused false"])
		}
	}
}

// deltaColumn is a column "v" of physical type phys and repetition rep, or the
// element of a List of rep.
func deltaColumn(t *testing.T, phys parquet.Type, rep parquet.Repetition, list bool) *schema.Column {
	t.Helper()
	leaf := func(name string) schema.Node {
		switch phys {
		case parquet.Types.Int32:
			return schema.NewInt32Node(name, rep, -1)
		case parquet.Types.ByteArray:
			return schema.NewByteArrayNode(name, rep, -1)
		}
		return schema.NewInt64Node(name, rep, -1)
	}
	n := leaf("v")
	if list {
		l, err := schema.ListOf(leaf("element"), rep, -1)
		if err != nil {
			t.Fatal(err)
		}
		n = l
	}
	root, err := schema.NewGroupNode("schema", parquet.Repetitions.Required, schema.FieldList{n}, -1)
	if err != nil {
		t.Fatal(err)
	}
	return schema.NewSchema(root).Column(0)
}

// genRun is a generated delta-packed run: its header's fields and its blocks.
type genRun struct {
	block, minis, count uint64
	first               int64
	blocks              []genBlock
}

// genBlock is a block of a run: its least delta, its miniblocks' bit widths, and the
// bodies of those holding values, each padded to its full count, and how many each
// holds.
type genBlock struct {
	least  int64
	widths []byte
	bodies [][]byte
	held   []int
}

// deltaRunOf is vals delta-packed as the format lays them out, in blocks of block
// values of per values a miniblock, each delta taken as an int64's with wraparound,
// as arrow-go's writer takes an INT32's too.
func deltaRunOf(vals []int64, block, per int) genRun {
	r := genRun{block: uint64(block), minis: uint64(block / per), count: uint64(len(vals))}
	if len(vals) == 0 {
		return r
	}
	r.first = vals[0]
	for at := 1; at < len(vals); at += block {
		var deltas []int64
		for i := at; i < min(at+block, len(vals)); i++ {
			deltas = append(deltas, vals[i]-vals[i-1])
		}
		b := genBlock{least: slices.Min(deltas), widths: make([]byte, block/per)}
		for m := 0; m*per < len(deltas); m++ {
			held := deltas[m*per : min((m+1)*per, len(deltas))]
			packed := make([]uint64, per)
			var top uint64
			for i, d := range held {
				packed[i] = uint64(d - b.least)
				top = max(top, packed[i])
			}
			b.widths[m] = byte(bits.Len64(top))
			b.bodies = append(b.bodies, pack(packed, bits.Len64(top)))
			b.held = append(b.held, len(held))
		}
		r.blocks = append(r.blocks, b)
	}
	return r
}

// header is the run's header as written.
func (r genRun) header() []byte {
	b := binary.AppendUvarint(nil, r.block)
	b = binary.AppendUvarint(b, r.minis)
	b = binary.AppendUvarint(b, r.count)
	return binary.AppendVarint(b, r.first)
}

func (r genRun) bytes() []byte {
	b := r.header()
	for _, k := range r.blocks {
		b = binary.AppendVarint(b, k.least)
		b = append(b, k.widths...)
		for _, body := range k.bodies {
			b = append(b, body...)
		}
	}
	return b
}

// deltaPage is a generated delta-encoded data page, and what the generator knows of
// it.
type deltaPage struct {
	col        *schema.Column
	enc        parquet.Encoding
	v2         bool
	defEnc     parquet.Encoding
	n, present int
	levels     []byte // the page's levels, as its version lays them out
	repLen     int    // V2: the repetition levels' bytes
	defLen     int    // V2: the definition levels' bytes
	defs, reps []int16
	vals       []string // its values that are not null, each as fmt.Sprint renders it
	runs       []genRun
	tail       []byte // the bytes after the runs: a byte array page's values or suffixes
	forged     string // what the generator did to the page
	run        int    // the run forged
	cut        int    // where a cut page ends, in its forged run's bytes; -1 for none
	body       []byte // the page's values' bytes, after its levels
	refused    bool   // checkedPages must refuse it
	reads      bool   // it reads as vals and the levels
	chunk      int64  // the column chunk's values, the page's unless forged
}

func (p *deltaPage) String() string {
	return fmt.Sprintf("%s %s, v2 %v, %d levels %v, %d values, %d runs, forged %q in run %d, %d bytes of values",
		p.col.PhysicalType(), p.enc, p.v2, p.n, p.defEnc, p.present, len(p.runs), p.forged, p.run, len(p.body))
}

// genDeltaPage generates a page of column desc under enc.
func genDeltaPage(rng *rand.Rand, desc *schema.Column, enc parquet.Encoding) *deltaPage {
	maxDef, maxRep := desc.MaxDefinitionLevel(), desc.MaxRepetitionLevel()
	p := &deltaPage{col: desc, enc: enc, v2: rng.IntN(2) == 0, defEnc: parquet.Encodings.RLE,
		n: 1 + rng.IntN(700), forged: "honest", cut: -1}
	p.chunk = int64(p.n)
	if !p.v2 && maxDef > 0 && rng.IntN(5) == 0 {
		p.defEnc = parquet.Encodings.BitPacked
	}
	// The levels: a List's repetition levels 0 or 1, its definition levels 0 to 3,
	// an element mostly there; an OPTIONAL column's 0 or 1, null one time in four.
	defs, reps := make([]uint64, p.n), make([]uint64, p.n)
	for i := range p.n {
		switch {
		case maxRep > 0:
			defs[i], reps[i] = uint64(maxDef), uint64(rng.IntN(2))
			if rng.IntN(4) == 0 {
				defs[i] = uint64(rng.IntN(int(maxDef)))
			}
		case maxDef > 0 && rng.IntN(4) > 0:
			defs[i] = 1
		}
		if maxDef == 0 || defs[i] == uint64(maxDef) {
			p.present++
		}
	}
	reps[0] = 0
	for i := range p.n {
		if maxDef > 0 {
			p.defs = append(p.defs, int16(defs[i]))
		}
		if maxRep > 0 {
			p.reps = append(p.reps, int16(reps[i]))
		}
	}
	lw := bits.Len16(uint16(maxDef))
	if maxRep > 0 {
		r := genRuns(rng, reps, bits.Len16(uint16(maxRep)), nil)
		if p.v2 {
			p.levels, p.repLen = r, len(r)
		} else {
			p.levels = append(binary.LittleEndian.AppendUint32(nil, uint32(len(r))), r...)
		}
	}
	if maxDef > 0 {
		switch {
		case p.v2:
			r := genRuns(rng, defs, lw, nil)
			p.levels, p.defLen = append(p.levels, r...), len(r)
		case p.defEnc == parquet.Encodings.BitPacked:
			p.levels = append(p.levels, pack(defs, lw)...)
		default:
			r := genRuns(rng, defs, lw, nil)
			p.levels = append(append(p.levels, binary.LittleEndian.AppendUint32(nil, uint32(len(r)))...), r...)
		}
	}

	// The values, and the runs that pack them.
	var runVals [][]int64
	if enc == parquet.Encodings.DeltaBinaryPacked {
		ints := genDeltaInts(rng, p.present, desc.PhysicalType() == parquet.Types.Int32)
		for _, v := range ints {
			p.vals = append(p.vals, fmt.Sprint(v))
		}
		runVals = [][]int64{ints}
	} else {
		strs := genDeltaStrings(rng, p.present)
		var lens, prefixes, suffixes []int64
		prev := ""
		for _, s := range strs {
			p.vals = append(p.vals, fmt.Sprint([]byte(s)))
			lens = append(lens, int64(len(s)))
			k := 0
			for k < min(len(s), len(prev)) && s[k] == prev[k] {
				k++
			}
			prefixes, suffixes = append(prefixes, int64(k)), append(suffixes, int64(len(s)-k))
			if enc == parquet.Encodings.DeltaLengthByteArray {
				p.tail = append(p.tail, s...)
			} else {
				p.tail = append(p.tail, s[k:]...)
			}
			prev = s
		}
		runVals = [][]int64{lens}
		if enc == parquet.Encodings.DeltaByteArray {
			runVals = [][]int64{prefixes, suffixes}
		}
	}
	layout := func() (block, per int) {
		if rng.IntN(60) == 0 {
			return maxMiniblock, maxMiniblock // the largest miniblock past a run's count
		}
		block = 128 * []int{1, 1, 1, 2, 4, 8}[rng.IntN(6)]
		for {
			per = 32 << rng.IntN(6)
			if per <= block {
				return block, per
			}
		}
	}
	for _, v := range runVals {
		block, per := layout()
		p.runs = append(p.runs, deltaRunOf(v, block, per))
	}
	if rng.IntN(3) == 0 {
		// The miniblocks holding no value, in each run's last block, give any width.
		for i := range p.runs {
			if b := len(p.runs[i].blocks); b > 0 {
				last := &p.runs[i].blocks[b-1]
				for m := len(last.bodies); m < len(last.widths); m++ {
					last.widths[m] = byte(rng.IntN(256))
				}
			}
		}
		p.forged = "spare widths"
	}
	p.reads = true
	if rng.IntN(5) > 0 {
		p.forge(rng, runVals)
	}
	for i, r := range p.runs {
		b := r.bytes()
		if i == p.run && p.cut >= 0 {
			p.body = append(p.body, b[:p.cut]...)
			break
		}
		p.body = append(p.body, b...)
		if i == len(p.runs)-1 {
			p.body = append(p.body, p.tail...)
		}
	}
	return p
}

// forge breaks one run of page p, or forges it only where the format allows, as the
// generator picks, and says what checkedPages and the read must answer.
func (p *deltaPage) forge(rng *rand.Rand, runVals [][]int64) {
	p.run = rng.IntN(len(p.runs))
	r := &p.runs[p.run]
	whole := p.enc != parquet.Encodings.DeltaBinaryPacked
	broken := func(what string) { p.forged, p.refused, p.reads = what, true, false }
	switch rng.IntN(12) {
	case 10:
		// One miniblock past maxMiniblock and the count, 0 bits wide: its values are
		// the first, but the page is refused before any is read.
		per := uint64(1) << (17 + rng.IntN(15))
		r.block, r.minis, r.blocks = per, 1, nil
		if r.count > 1 {
			r.blocks = []genBlock{{least: rng.Int64N(9) - 4, widths: []byte{0}}}
		}
		broken("huge miniblock")
	case 11:
		// A column chunk of fewer values than the run counts: the page claims more.
		if r.count == 0 {
			return
		}
		p.chunk = rng.Int64N(int64(r.count))
		broken("chunk")
	case 0:
		if rng.IntN(2) == 0 {
			// A layout the format does not allow: blocks of a multiple of 32 values
			// that is not one of 128, in miniblocks of 32.
			block := 32 * (1 + rng.IntN(40))
			if block%128 == 0 {
				block += 32
			}
			*r = deltaRunOf(runVals[p.run], block, 32)
		} else {
			bad := []uint64{0, uint64(1 + rng.IntN(127)), r.block + uint64(1+rng.IntN(127)), 1 << 32,
				1<<32 + 128*uint64(rng.IntN(4)), 1 << 40}
			r.block = bad[rng.IntN(len(bad))]
		}
		broken("block")
	case 1:
		for {
			m := uint64(rng.IntN(int(r.block) + 2))
			if m == 0 || r.block%m != 0 || r.block/m%32 != 0 {
				r.minis = m
				break
			}
		}
		broken("miniblocks")
	case 2:
		block := int(r.block)
		*r = deltaRunOf(runVals[p.run], block, []int{1, 2, 4, 8, 16}[rng.IntN(5)])
		broken("layout")
	case 3:
		var at [][2]int // the miniblocks holding values
		for b := range r.blocks {
			for m := range r.blocks[b].bodies {
				at = append(at, [2]int{b, m})
			}
		}
		if len(at) == 0 {
			return
		}
		w := at[rng.IntN(len(at))]
		r.blocks[w[0]].widths[w[1]] = byte(65 + rng.IntN(191))
		broken("wide")
	case 4:
		if p.present == 0 {
			return
		}
		r.count = uint64(rng.IntN(p.present))
		broken("count short")
	case 5:
		r.count = uint64(p.n) + 1 + uint64(rng.Int64N(1<<40))
		broken("count past the page")
	case 6:
		// Past the values that are not null, within the page's; for a run decoded
		// whole, within its last miniblock's padding, whose values it decodes and the
		// page does not use.
		room := p.n - p.present
		if whole {
			if len(r.blocks) == 0 {
				return
			}
			last := r.blocks[len(r.blocks)-1]
			room = min(room, int(r.block/r.minis)-last.held[len(last.held)-1])
		}
		if room <= 0 {
			return
		}
		r.count += uint64(1 + rng.IntN(room))
		p.forged = "count past the values"
	case 7, 8:
		h := len(r.header())
		if size := len(r.bytes()); size > h {
			p.cut = h + rng.IntN(size-h)
			broken("cut")
		}
	case 9:
		p.cut = rng.IntN(len(r.header()))
		p.forged, p.reads = "header cut", false
	}
}

// genDeltaInts are n integers in runs that change their spread: steps of a few bits
// or many, a value anywhere, and the type's least and greatest. An INT32's are
// int32s, their deltas spanning up to 33 bits.
func genDeltaInts(rng *rand.Rand, n int, int32s bool) []int64 {
	out := make([]int64, n)
	var v int64
	spread := rng.IntN(63)
	for i := range out {
		switch rng.IntN(40) {
		case 0:
			spread = rng.IntN(63)
		case 1:
			v = int64(rng.Uint64())
		case 2:
			v = []int64{math.MinInt64, math.MaxInt64, 0}[rng.IntN(3)]
		default:
			v += rng.Int64N(1<<spread+1) - 1<<spread/2
		}
		if int32s {
			v = int64(int32(v))
		}
		out[i] = v
	}
	return out
}

// genDeltaStrings are n strings of up to 40 bytes, some empty, many sharing a prefix
// with the one before.
func genDeltaStrings(rng *rand.Rand, n int) []string {
	out := make([]string, n)
	prev := ""
	for i := range out {
		b := []byte(prev[:rng.IntN(len(prev)+1)])
		if rng.IntN(3) == 0 {
			b = b[:0]
		}
		for range rng.IntN(max(1, 41-len(b))) {
			b = append(b, byte(rng.IntN(256)))
		}
		out[i] = string(b)
		prev = out[i]
	}
	return out
}

// reader is the page through checkedPages where checked.
func (p *deltaPage) reader(checked bool) file.PageReader {
	body := slices.Concat(p.levels, p.body)
	var data file.Page
	if p.v2 {
		data = file.NewDataPageV2(memory.NewBufferBytes(body), int32(p.n), int32(p.n-p.present), int32(p.n),
			p.enc, int32(p.defLen), int32(p.repLen), int32(len(body)), false)
	} else {
		data = file.NewDataPageV1(memory.NewBufferBytes(body), int32(p.n), p.enc, p.defEnc, parquet.Encodings.RLE,
			int32(len(body)))
	}
	r := &listedPages{pages: []file.Page{data}}
	if !checked {
		return r
	}
	return &checkedPages{PageReader: r, maxDef: p.col.MaxDefinitionLevel(), maxRep: p.col.MaxRepetitionLevel(),
		name: p.col.Path(), phys: p.col.PhysicalType(), chunkValues: p.chunk}
}

// deltaRead is what a read of a page answered.
type deltaRead struct {
	vals       []string
	defs, reps []int16
	err        error
}

// readDeltaPage reads pr's pages whole through arrow-go's column reader for desc,
// as ursus's readers read a chunk: to its end, or an error, the chunk's own where a
// batch reads nothing (batchErr). A panic is an error.
func readDeltaPage(desc *schema.Column, pr file.PageReader, pool *sync.Pool) (r deltaRead) {
	defer func() {
		if v := recover(); v != nil {
			r.err = fmt.Errorf("panic: %v", v)
		}
	}()
	cr := file.NewColumnReader(desc, pr, memory.DefaultAllocator, pool)
	defs, reps := make([]int16, 64), make([]int16, 64)
	for {
		var total int64
		var n int
		var err error
		switch c := cr.(type) {
		case *file.Int64ColumnChunkReader:
			vals := make([]int64, 64)
			total, n, err = c.ReadBatch(64, vals, defs, reps)
			for _, v := range vals[:n] {
				r.vals = append(r.vals, fmt.Sprint(v))
			}
		case *file.Int32ColumnChunkReader:
			vals := make([]int32, 64)
			total, n, err = c.ReadBatch(64, vals, defs, reps)
			for _, v := range vals[:n] {
				r.vals = append(r.vals, fmt.Sprint(int64(v)))
			}
		case *file.ByteArrayColumnChunkReader:
			vals := make([]parquet.ByteArray, 64)
			total, n, err = c.ReadBatch(64, vals, defs, reps)
			for _, v := range vals[:n] {
				r.vals = append(r.vals, fmt.Sprint([]byte(v)))
			}
		}
		if desc.MaxDefinitionLevel() > 0 {
			r.defs = append(r.defs, defs[:total]...)
		}
		if desc.MaxRepetitionLevel() > 0 {
			r.reps = append(r.reps, reps[:total]...)
		}
		if r.err = batchErr(cr, total, err); r.err != nil || total == 0 {
			return r
		}
	}
}

// TestArrowGoDeltaPagesReadAsTheirPlainTwin (step 531): writePagesFixture's columns,
// every integer one under DELTA_BINARY_PACKED and its strings and binaries under
// DELTA_LENGTH_BYTE_ARRAY and DELTA_BYTE_ARRAY, written by arrow-go's writer, which
// ursus's writer is made of (it writes no delta page itself), read batch for batch as
// the same rows written PLAIN: V1 and V2 pages, pages of a hundred values and of the
// default size, Zstandard, each under four null patterns. Its u32 column spans
// INT32's range, and arrow-go packs its deltas 33 bits wide, past what pyarrow
// reads. No page arrow-go writes is refused by the check.
func TestArrowGoDeltaPagesReadAsTheirPlainTwin(t *testing.T) {
	var deltas []parquet.WriterProperty
	for _, c := range []string{"i8", "i16", "i32", "u8", "u16", "u32", "i64", "u64", "date", "ts", "tms", "tus",
		"dec32", "dec64"} {
		deltas = append(deltas, parquet.WithEncodingFor(c, parquet.Encodings.DeltaBinaryPacked))
	}
	for _, c := range []string{"s", "wide"} {
		deltas = append(deltas, parquet.WithEncodingFor(c, parquet.Encodings.DeltaLengthByteArray))
	}
	for _, c := range []string{"s_req", "bin"} {
		deltas = append(deltas, parquet.WithEncodingFor(c, parquet.Encodings.DeltaByteArray))
	}
	v1 := parquet.WithDataPageVersion(parquet.DataPageV1)
	v2 := parquet.WithDataPageVersion(parquet.DataPageV2)
	plain := parquet.WithDictionaryDefault(false)
	small := parquet.WithDataPageSize(512)
	zstd := parquet.WithCompression(compress.Codecs.Zstd)
	dir := t.TempDir()
	wide := false
	patterns, batches := []string{"none", "scattered", "runs", "all"}, []int{1024, 333}
	if raceDetector {
		patterns, batches = []string{"scattered", "all"}, []int{333} // the instrumentation's cost
	}
	for _, l := range []struct {
		name  string
		props []parquet.WriterProperty
	}{
		{"v1", []parquet.WriterProperty{v1, plain}},
		{"v2", []parquet.WriterProperty{v2, plain}},
		{"v1 small pages", []parquet.WriterProperty{v1, plain, small}},
		{"v2 small pages zstd", []parquet.WriterProperty{v2, plain, small, zstd}},
	} {
		for _, nulls := range patterns {
			name := fmt.Sprintf("%s, nulls %s", l.name, nulls)
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".parquet")
			twin := strings.TrimSuffix(path, ".parquet") + "-plain.parquet"
			writePagesFixture(t, path, 4000, nulls, append(slices.Clone(l.props), deltas...)...)
			writePagesFixture(t, twin, 4000, nulls, l.props...)
			checkDeltaRead(t, path, len(deltas))
			wide = wide || deltaWiderThan(t, path, "u32", 32)
			for _, batch := range batches {
				got, want := readAllPages(t, path, batch, true), readAllPages(t, twin, batch, true)
				if !slices.Equal(got, want) {
					t.Fatalf("%s, batches of %d: the delta pages read apart from the PLAIN ones", name, batch)
				}
			}
		}
	}
	if !wide {
		t.Error("no page of u32 packs a miniblock wider than 32 bits")
	}
}

// checkDeltaRead requires want of path's columns to list a delta encoding in each row
// group, and each such column to be read through arrow-go's column reader, its pages
// through checkedPages, not the reader's own pages.
func checkDeltaRead(t *testing.T, path string, want int) {
	t.Helper()
	pf, err := file.OpenParquetFile(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer pf.Close()
	sc := pf.MetaData().Schema
	full, err := openSource(t, path).Schema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for g := range pf.NumRowGroups() {
		rg := pf.MetaData().RowGroup(g)
		delta := 0
		for ci := range sc.NumColumns() {
			cc, err := rg.ColumnChunk(ci)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(cc.Encodings(), func(e parquet.Encoding) bool {
				return e == parquet.Encodings.DeltaBinaryPacked || e == parquet.Encodings.DeltaLengthByteArray ||
					e == parquet.Encodings.DeltaByteArray
			}) {
				continue
			}
			delta++
			if ownable(rg, ci, sc.Column(ci), full.Field(ci).Type) {
				t.Errorf("%s: row group %d's delta-encoded column %s is read from its own pages", path, g,
					sc.Column(ci).Path())
			}
		}
		if delta != want {
			t.Errorf("%s: row group %d has %d delta-encoded columns, not %d", path, g, delta, want)
		}
	}
}

// deltaWiderThan is whether a DELTA_BINARY_PACKED page of column name in path has a
// miniblock holding values wider than w bits, read by the format's layout.
func deltaWiderThan(t *testing.T, path, name string, w int) bool {
	t.Helper()
	pf, err := file.OpenParquetFile(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer pf.Close()
	ci := pf.MetaData().Schema.ColumnIndexByName(name)
	desc := pf.MetaData().Schema.Column(ci)
	for g := range pf.NumRowGroups() {
		pr, err := pf.RowGroup(g).GetColumnPageReader(ci)
		if err != nil {
			t.Fatal(err)
		}
		c := &checkedPages{maxDef: desc.MaxDefinitionLevel(), maxRep: desc.MaxRepetitionLevel()}
		for pr.Next() {
			pg := pr.Page()
			if parquet.Encoding(pg.Encoding()) != parquet.Encodings.DeltaBinaryPacked {
				continue
			}
			l, ok := c.levels(pg)
			if !ok {
				t.Fatalf("%s: a page's levels do not read", name)
			}
			if widest(l.data, c.present(l)) > w {
				pr.Close()
				return true
			}
		}
		pr.Close()
	}
	return false
}

// widest is the widest miniblock holding values of the honest run at the start of
// b, of present values.
func widest(b []byte, present int) int {
	var h [4]uint64
	at := 0
	for i := range h {
		v, k := binary.Uvarint(b[at:])
		h[i], at = v, at+k
	}
	per, most := h[0]/h[1], 0
	for left := max(present, 1) - 1; left > 0; {
		_, k := binary.Uvarint(b[at:])
		at += k
		widths := b[at : at+int(h[1])]
		at += int(h[1])
		for _, w := range widths {
			if left == 0 {
				break
			}
			most = max(most, int(w))
			at += int(w) * int(per) / 8
			left -= min(left, int(per))
		}
	}
	return most
}

// TestPackedCountOfOneBitCountsEachBit (step 531): packedCount's count of 1-bit
// values, by bits set eight bytes a load, against a brute force reading each bit in
// turn, over random bytes of 0 to 40, n from 0 to past their bits, and want 0, 1 and
// 2.
func TestPackedCountOfOneBitCountsEachBit(t *testing.T) {
	rng := rand.New(rand.NewPCG(531, 1))
	for range 5_000 {
		b := make([]byte, rng.IntN(41))
		for i := range b {
			b[i] = byte(rng.IntN(256))
		}
		n := rng.IntN(len(b)*8 + 10)
		want := int16(rng.IntN(3))
		var hits, read int
		for i := 0; i < n && i < len(b)*8; i++ {
			if int16(b[i/8]>>(i%8)&1) == want {
				hits++
			}
			read++
		}
		if h, r := packedCount(b, 1, n, want); h != hits || r != read {
			t.Fatalf("% x, n %d, want %d: %d of %d, the brute force %d of %d", b, n, want, h, r, hits, read)
		}
	}
}
