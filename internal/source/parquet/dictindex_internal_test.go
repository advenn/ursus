package parquet

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"
)

// TestCheckedPagesRefuseWhereArrowGoGrowsItsDictionary (step 517): checkedPages
// against arrow-go's own column reader, its oracle, over generated dictionary-encoded
// data pages of an INT64 column, REQUIRED, OPTIONAL and the element of a List, V1 and
// V2, their definition levels RLE or, in V1, BIT_PACKED, their indices in RLE and
// bit-packed runs at bit widths up to 32, some of them 2^20 or more, past a dictionary
// of at most 50 values, some only in a bit-packed run's padding, and some pages cut
// short or holding a header arrow-go refuses. Each page is read whole through
// arrow-go's column reader, once over arrow-go's pages as they are and once through
// checkedPages, the dictionary page read in turn or, for a fifth of the pages, out of
// turn through GetDictionaryPage:
//
//   - through checkedPages, the read allocates no more than its page needs, the bound
//     arrow-go's dictConverter.ensure broke: an index of 2^20 grew its dictionary by 8
//     MiB;
//   - where arrow-go's own read grew its dictionary, checkedPages refused the page;
//   - where checkedPages did not refuse a page, the read answered what arrow-go's
//     own did, values, levels and error alike;
//   - for a page not cut short and with no refused header, checkedPages refused it
//     exactly when an index arrow-go reads, one of the page's values that are not
//     null, is past the dictionary, which is when arrow-go grew it: no page arrow-go
//     reads is refused, whatever its padding holds.
func TestCheckedPagesRefuseWhereArrowGoGrowsItsDictionary(t *testing.T) {
	rng := rand.New(rand.NewPCG(517, 9))
	pool := &sync.Pool{New: func() any { return memory.NewResizableBuffer(memory.DefaultAllocator) }}
	cols := []*schema.Column{indexColumn(t, parquet.Repetitions.Required, false),
		indexColumn(t, parquet.Repetitions.Optional, false), indexColumn(t, parquet.Repetitions.Optional, true)}
	var refused, grown, kept, cases int
	for i := range 2_000 {
		desc := cols[rng.IntN(len(cols))]
		pg := genIndexPage(rng, desc)
		pg.outOfTurn = i%5 == 0
		theirs := readIndexPage(desc, pg.reader(false), pool)
		ours := readIndexPage(desc, pg.reader(true), pool)
		cases++
		label := fmt.Sprintf("page %d (%s)", i, pg)
		past := ours.err != nil && strings.Contains(ours.err.Error(), "past its dictionary")
		if past {
			refused++
		}
		if theirs.grew {
			grown++
		}
		switch {
		case ours.grew:
			t.Errorf("%s: read through checkedPages, its dictionary grew by %d bytes", label, ours.alloc)
		case theirs.grew && !past:
			t.Errorf("%s: arrow-go grew its dictionary by %d bytes, and checkedPages answered %v", label, theirs.alloc, ours.err)
		case !past && (fmt.Sprint(ours.err) != fmt.Sprint(theirs.err) || !slices.Equal(ours.vals, theirs.vals) ||
			!slices.Equal(ours.defs, theirs.defs) || !slices.Equal(ours.reps, theirs.reps)):
			t.Errorf("%s: through checkedPages %d values and %v; arrow-go's own %d and %v",
				label, len(ours.vals), ours.err, len(theirs.vals), theirs.err)
		case pg.wellFormed && past != pg.reachablePast:
			t.Errorf("%s: an index arrow-go reads is past the dictionary: %v; checkedPages refused it: %v (%v)",
				label, pg.reachablePast, past, ours.err)
		case pg.wellFormed && theirs.grew != pg.reachableFar:
			t.Errorf("%s: an index arrow-go reads is 2^20 or more: %v; arrow-go grew its dictionary: %v (%v)",
				label, pg.reachableFar, theirs.grew, theirs.err)
		}
		if pg.wellFormed && !past {
			kept++
		}
	}
	// The generator's own reach: pages of each outcome.
	t.Logf("of %d pages, %d refused, %d grew arrow-go's dictionary, %d well formed and read", cases, refused, grown, kept)
	if refused < cases/10 || grown < cases/10 || kept < cases/4 {
		t.Errorf("of %d pages, %d refused, %d grew arrow-go's dictionary, %d well formed and read", cases, refused,
			grown, kept)
	}
}

// indexColumn is an INT64 column of rep, or the element of rep in a List of rep.
func indexColumn(t *testing.T, rep parquet.Repetition, list bool) *schema.Column {
	t.Helper()
	var n schema.Node = schema.NewInt64Node("v", rep, -1)
	if list {
		l, err := schema.ListOf(schema.NewInt64Node("element", rep, -1), rep, -1)
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

// indexPage is a generated dictionary page and data page of an INT64 column, and
// what the generator knows of them.
type indexPage struct {
	col       *schema.Column
	dictN     int
	v2        bool
	defEnc    parquet.Encoding
	n, values int    // levels, and values not null
	width     int    // the indices' bit width
	levels    []byte // the page's levels, as its version lays them out
	repLen    int    // V2: the repetition levels' bytes
	defLen    int    // V2: the definition levels' bytes
	indices   []byte // the runs of indices, after the width
	outOfTurn bool   // the dictionary is read through GetDictionaryPage, not in turn

	reachablePast bool // an index of the first values is past the dictionary
	reachableFar  bool // and 2^20 or more
	wellFormed    bool // no run cut short, no header arrow-go refuses
}

func (p *indexPage) String() string {
	return fmt.Sprintf("dictionary %d, v2 %v, %d levels %v, %d values, width %d, %d bytes of indices, "+
		"out of turn %v, past %v, far past %v, well formed %v", p.dictN, p.v2, p.n, p.defEnc, p.values, p.width,
		len(p.indices), p.outOfTurn, p.reachablePast, p.reachableFar, p.wellFormed)
}

// genIndexPage generates a page of column desc.
func genIndexPage(rng *rand.Rand, desc *schema.Column) *indexPage {
	maxDef, maxRep := desc.MaxDefinitionLevel(), desc.MaxRepetitionLevel()
	p := &indexPage{col: desc, dictN: 1 + rng.IntN(50), v2: rng.IntN(2) == 0, defEnc: parquet.Encodings.RLE,
		n: 1 + rng.IntN(300), wellFormed: true}
	if !p.v2 && maxDef > 0 && rng.IntN(5) == 0 {
		p.defEnc = parquet.Encodings.BitPacked
	}
	// The levels: a List's repetition levels 0 or 1, its definition levels 0 to 3;
	// an OPTIONAL column's 0 or 1, null one time in four.
	defs, reps := make([]uint64, p.n), make([]uint64, p.n)
	for i := range p.n {
		switch {
		case maxRep > 0:
			defs[i], reps[i] = uint64(rng.IntN(int(maxDef)+1)), uint64(rng.IntN(2))
		case maxDef > 0 && rng.IntN(4) > 0:
			defs[i] = 1
		}
		if maxDef == 0 || defs[i] == uint64(maxDef) {
			p.values++
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

	// The indices: half the pages hold some past the dictionary, 2^20 and more, which
	// takes a width of 21 bits; any page's padding may hold them.
	// The indices' width: what the dictionary needs, and up to 32; for half the pages
	// 21 at least, to hold an index of 2^20, by which arrow-go's growth shows.
	need := bits.Len(uint(p.dictN - 1))
	if rng.IntN(2) == 0 {
		need = max(need, 21)
	}
	p.width = need + rng.IntN(33-need)
	room := uint64(1)<<p.width - uint64(p.dictN) // indices the width holds past the dictionary
	big := p.width >= 21
	// Some of the page's values are past the dictionary: far past it, or, as arrow-go
	// stops at the first index past it, on another page just past it.
	pastIn := room > 0 && rng.IntN(2) == 0
	farIn := big && rng.IntN(2) == 0
	far := func() uint64 { return 1<<20 + uint64(rng.IntN(1<<20)) }
	pick := func() uint64 {
		switch {
		case !pastIn || rng.IntN(40) > 0:
			return uint64(rng.IntN(p.dictN))
		case farIn:
			return far()
		}
		if rng.IntN(2) == 0 {
			return uint64(p.dictN) // the first index past it
		}
		return uint64(p.dictN) + uint64(rng.IntN(int(min(room, 4))))
	}
	garbage := func() uint64 {
		if big && rng.IntN(2) == 0 {
			return far()
		}
		return uint64(rng.Uint32()) & (1<<p.width - 1)
	}
	var idx []uint64
	for len(idx) < p.values {
		if rng.IntN(2) == 0 {
			v := pick()
			for range 1 + rng.IntN(20) {
				idx = append(idx, v)
			}
			continue
		}
		for range 8 * (1 + rng.IntN(3)) {
			if len(idx) < p.values {
				idx = append(idx, pick())
			} else {
				idx = append(idx, garbage())
			}
		}
	}
	for _, v := range idx[:p.values] {
		p.reachablePast = p.reachablePast || v >= uint64(p.dictN)
		p.reachableFar = p.reachableFar || v >= 1<<20
	}
	var bounds []int
	p.indices = genRuns(rng, idx, p.width, &bounds)
	if p.values == 0 && rng.IntN(2) == 0 {
		p.indices = nil // a page of nulls may hold no runs at all
	}
	// Some pages cut short, or holding a header RleDecoder.Next refuses: a zero count,
	// RLE or bit-packed, or ten bytes that do not end.
	switch rng.IntN(10) {
	case 0:
		if len(p.indices) > 0 {
			p.indices, p.wellFormed = p.indices[:rng.IntN(len(p.indices))], false
		}
	case 1:
		at := 0
		if len(bounds) > 0 {
			at = bounds[rng.IntN(len(bounds))]
		}
		bad := [][]byte{{0}, {1}, slices.Repeat([]byte{0xff}, 10)}[rng.IntN(3)]
		p.indices, p.wellFormed = slices.Concat(p.indices[:at], bad, p.indices[at:]), false
	}
	return p
}

// genRuns writes vals, each of width bits, in runs of the hybrid encoding: where a
// value repeats, an RLE run of its repeats; elsewhere a bit-packed run, which takes
// eight values a group and a group's last, past vals, as 0. bounds, if not nil, gets
// each run's first byte.
func genRuns(rng *rand.Rand, vals []uint64, width int, bounds *[]int) []byte {
	var b []byte
	for i := 0; i < len(vals); {
		if bounds != nil {
			*bounds = append(*bounds, len(b))
		}
		j := i + 1
		for j < len(vals) && vals[j] == vals[i] {
			j++
		}
		if j-i >= 2 && rng.IntN(4) > 0 {
			b = binary.AppendUvarint(b, uint64(j-i)<<1)
			for k := range (width + 7) / 8 {
				b = append(b, byte(vals[i]>>(8*k)))
			}
			i = j
			continue
		}
		g := 1 + rng.IntN(3)
		group := make([]uint64, 8*g)
		copy(group, vals[i:min(len(vals), i+8*g)])
		b = binary.AppendUvarint(b, uint64(g)<<1|1)
		b = append(b, pack(group, width)...)
		i += 8 * g
	}
	return b
}

// pack lays vals out width bits each, the least significant bit first.
func pack(vals []uint64, width int) []byte {
	b := make([]byte, (len(vals)*width+7)/8)
	for i, v := range vals {
		for k := range width {
			if v>>k&1 != 0 {
				at := i*width + k
				b[at/8] |= 1 << (at % 8)
			}
		}
	}
	return b
}

// reader is the page's pages, through checkedPages where checked.
func (p *indexPage) reader(checked bool) file.PageReader {
	dict := make([]byte, 0, 8*p.dictN)
	for i := range p.dictN {
		dict = binary.LittleEndian.AppendUint64(dict, uint64(3*i+1))
	}
	body := slices.Concat(p.levels, []byte{byte(p.width)}, p.indices)
	if len(p.indices) == 0 && p.values == 0 {
		body = slices.Clone(p.levels)
	}
	var data file.Page
	if p.v2 {
		data = file.NewDataPageV2(memory.NewBufferBytes(body), int32(p.n), int32(p.n-p.values), int32(p.n),
			parquet.Encodings.RLEDict, int32(p.defLen), int32(p.repLen), int32(len(body)), false)
	} else {
		data = file.NewDataPageV1(memory.NewBufferBytes(body), int32(p.n), parquet.Encodings.RLEDict, p.defEnc,
			parquet.Encodings.RLE, int32(len(body)))
	}
	r := &listedPages{dict: file.NewDictionaryPage(memory.NewBufferBytes(dict), int32(p.dictN), parquet.Encodings.Plain)}
	if p.outOfTurn {
		r.pages = []file.Page{data}
	} else {
		r.pages = []file.Page{r.dict, data}
	}
	if !checked {
		return r
	}
	return &checkedPages{PageReader: r, maxDef: p.col.MaxDefinitionLevel(), maxRep: p.col.MaxRepetitionLevel()}
}

// listedPages is a PageReader of pages given, the dictionary also through
// GetDictionaryPage, as a column chunk's would give them.
type listedPages struct {
	pages []file.Page
	dict  *file.DictionaryPage
	cur   file.Page
}

func (r *listedPages) Next() bool {
	if len(r.pages) == 0 {
		r.cur = nil
		return false
	}
	r.cur, r.pages = r.pages[0], r.pages[1:]
	return true
}

func (r *listedPages) Page() file.Page                                  { return r.cur }
func (r *listedPages) Err() error                                       { return nil }
func (r *listedPages) GetDictionaryPage() (*file.DictionaryPage, error) { return r.dict, nil }
func (r *listedPages) SetMaxPageHeaderSize(int)                         {}
func (r *listedPages) SeekToPageWithRow(int64) error                    { return nil }
func (r *listedPages) Close() error                                     { return nil }
func (r *listedPages) Reset(parquet.BufferedReader, int64, compress.Compression, *file.CryptoContext) {
}

// indexRead is what a read of a page answered, and what it allocated.
type indexRead struct {
	vals       []int64
	defs, reps []int16
	err        error
	alloc      uint64
	grew       bool // it allocated what a dictionary grown to 2^20 values takes
}

// readIndexPage reads pr's pages whole through arrow-go's column reader for desc.
func readIndexPage(desc *schema.Column, pr file.PageReader, pool *sync.Pool) indexRead {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	before := ms.TotalAlloc
	cr := file.NewColumnReader(desc, pr, memory.DefaultAllocator, pool).(*file.Int64ColumnChunkReader)
	var r indexRead
	vals, defs, reps := make([]int64, 64), make([]int16, 64), make([]int16, 64)
	for {
		// As ursus's readers read a chunk: to its end, or an error, the chunk's own
		// where a batch reads nothing (batchErr).
		total, n, err := cr.ReadBatch(64, vals, defs, reps)
		r.vals = append(r.vals, vals[:n]...)
		r.defs = append(r.defs, defs[:total]...)
		r.reps = append(r.reps, reps[:total]...)
		if r.err = batchErr(cr, total, err); r.err != nil || total == 0 {
			break
		}
	}
	runtime.ReadMemStats(&ms)
	r.alloc = ms.TotalAlloc - before
	r.grew = r.alloc >= 4<<20
	return r
}
