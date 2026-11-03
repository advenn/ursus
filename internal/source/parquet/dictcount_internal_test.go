package parquet

import (
	"encoding/binary"
	"runtime"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
)

// TestADictionaryPagesCountIsCheckedAgainstItsBytes: a BYTE_ARRAY dictionary page of
// 12 bytes whose header says ten million values allocated its offsets by the count,
// 40 MB, before finding its values missing; H2's prober drew 76 MB from a 2.4 KB file,
// and a count can ask for 8 GiB. Each value holds a 4-byte length, so the count is
// checked against the bytes first: the page is refused as corrupt, having allocated
// next to nothing. A page whose count its bytes hold reads.
func TestADictionaryPagesCountIsCheckedAgainstItsBytes(t *testing.T) {
	body := make([]byte, 0, 12)
	for _, v := range []string{"ab", "cd"} {
		body = binary.LittleEndian.AppendUint32(body, uint32(len(v)))
		body = append(body, v...)
	}
	page := func(n int32) *file.DictionaryPage {
		return file.NewDictionaryPage(memory.NewBufferBytes(body), n, parquet.Encodings.Plain)
	}

	c := &pageChunk{name: "s"}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := c.readDictionary(page(10_000_000))
	runtime.ReadMemStats(&after)
	if err == nil || !strings.Contains(err.Error(), "dictionary") {
		t.Fatalf("ten million values in 12 bytes: got %v, want the page refused", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Errorf("refusing it allocated %d bytes", grew)
	}

	c = &pageChunk{name: "s"}
	if err := c.readDictionary(page(2)); err != nil || c.dictN != 2 || string(c.dictChars) != "abcd" {
		t.Errorf("two values: %v, %d values, %q", err, c.dictN, c.dictChars)
	}
}
