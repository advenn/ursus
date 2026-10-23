package ndjson

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// TestBlockReaderCutsAtNewlines: a stream read a block at a time comes back whole and
// in order, each block but the last ending at a newline, whatever a read hands back
// and however long a line is against a block.
func TestBlockReaderCutsAtNewlines(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 34))
	var text strings.Builder
	for i := range 400 {
		text.WriteString(strings.Repeat("x", rng.IntN(150)))
		if i%97 == 0 {
			text.WriteString(strings.Repeat("y", 1000)) // longer than the largest block
		}
		text.WriteByte('\n')
	}
	readers := []struct {
		name string
		wrap func(io.Reader) io.Reader
	}{
		{"whole", func(r io.Reader) io.Reader { return r }},
		{"a byte a read", iotest.OneByteReader},
		{"half a read", iotest.HalfReader},
		{"the end with the last bytes", iotest.DataErrReader},
	}
	for _, tail := range []string{"", "no newline at the end"} {
		in := text.String() + tail
		for _, rd := range readers {
			b := &blockReader{r: rd.wrap(strings.NewReader(in)), size: 16, top: 256}
			var blocks [][]byte
			for {
				block, err := b.next()
				if err != nil {
					t.Fatalf("%s: %v", rd.name, err)
				}
				if block == nil {
					break
				}
				if len(block) > 0 {
					blocks = append(blocks, block)
				}
			}
			if got := string(bytes.Join(blocks, nil)); got != in {
				t.Fatalf("%s, tail %q: the blocks hold %d bytes of the %d", rd.name, tail, len(got), len(in))
			}
			for i, block := range blocks[:len(blocks)-1] {
				if block[len(block)-1] != '\n' {
					t.Fatalf("%s: block %d ends %q", rd.name, i, block[len(block)-1])
				}
			}
		}
	}

	// A read's error is the block's.
	boom := errors.New("boom")
	b := &blockReader{r: io.MultiReader(strings.NewReader("a\nb\n"), iotest.ErrReader(boom)), size: 1 << 20, top: 1 << 20}
	if _, err := b.next(); !errors.Is(err, boom) {
		t.Fatalf("want the read's error, got %v", err)
	}
}

// TestCutAtNewlines: a block cut into parts, each but the last ending at a newline and
// together the block, no more of them than asked, and none much under 64 KiB.
func TestCutAtNewlines(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 55))
	var block []byte
	for len(block) < 1<<20 {
		block = append(block, strings.Repeat("y", rng.IntN(300))...)
		block = append(block, '\n')
	}
	for _, tail := range []string{"", "tail"} {
		b := append(bytes.Clone(block), tail...)
		for n := 1; n <= 20; n++ {
			parts := cutAtNewlines(b, n)
			if len(parts) > n || len(parts) < min(n, 16) {
				t.Fatalf("n %d: %d parts", n, len(parts))
			}
			if !bytes.Equal(bytes.Join(parts, nil), b) {
				t.Fatalf("n %d: the parts are not the block", n)
			}
			for i, p := range parts[:len(parts)-1] {
				if p[len(p)-1] != '\n' || len(p) < 1<<16-1<<10 {
					t.Fatalf("n %d: part %d of %d bytes ends %q", n, i, len(p), p[len(p)-1])
				}
			}
		}
	}
	if parts := cutAtNewlines(bytes.Repeat([]byte("z"), 1<<20), 8); len(parts) != 1 {
		t.Fatalf("one line is one part, got %d", len(parts))
	}
	if parts := cutAtNewlines([]byte("a\nb\nc\n"), 8); len(parts) != 1 {
		t.Fatalf("a small block is one part, got %d", len(parts))
	}
}

// TestPartPanicIsAnError: a panic parsing a part, on the goroutine that parses it,
// comes back from Next as an internal error, the process alive. Only a reader made by
// hand, its columns' type not a Struct, panics.
func TestPartPanicIsAnError(t *testing.T) {
	open := func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("{\"a\":1}\n")), nil
	}
	out, err := dtype.NewSchema(dtype.Field{Name: "a", Type: dtype.Int64, Nullable: true})
	if err != nil {
		t.Fatal(err)
	}
	src := New("t.ndjson", []File{{Name: "t.ndjson", Open: open}}, Options{})
	r := &reader{src: src, out: out, read: dtype.Int64, size: 10, threads: 2}
	defer r.Close()
	if _, err := r.Next(t.Context()); !errors.Is(err, uerr.ErrInternal) || !strings.Contains(err.Error(), "interface conversion") {
		t.Fatalf("want the panic as an internal error, got %v", err)
	}
}

// TestSchemaPanicIsNotForgotten: a panic inferring the schema is an error on every
// call, not only the first.
func TestSchemaPanicIsNotForgotten(t *testing.T) {
	boom := func(context.Context) (io.ReadCloser, error) { panic("open boom") }
	s := New("boom.ndjson", []File{{Name: "boom.ndjson", Open: boom}}, Options{InferLines: DefaultInferLines})
	for i := range 2 {
		if sc, err := s.Schema(t.Context()); sc != nil || err == nil || !strings.Contains(err.Error(), "open boom") {
			t.Fatalf("call %d: %v, %v", i, sc, err)
		}
	}
}

// TestScanFloatIsParseFloat: every number reads as strconv.ParseFloat reads it, bit for
// bit: the edges of the range and past them, subnormals, the halfway cases between two
// floats, more digits than an integer holds, and random floats written every way.
func TestScanFloatIsParseFloat(t *testing.T) {
	check := func(text string) {
		t.Helper()
		p := parser{s: []byte(text)}
		got, err := p.scanFloat()
		want, _ := strconv.ParseFloat(text, 64)
		if err != nil || p.pos != len(text) || math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("%s: %v (%#x), %v, at %d; want %v (%#x)", text, got, math.Float64bits(got), err, p.pos,
				want, math.Float64bits(want))
		}
	}
	for _, text := range []string{
		"0", "-0", "0.0", "-0.0", "0e5", "1", "-1", "0.1", "0.2", "0.30000000000000004", "1e23", "1e22", "1e-22",
		"1e-23", "1.7976931348623157e308", "1.7976931348623158e308", "1.7976931348623159e308", "1e308", "1e309",
		"-1e309", "1e400", "5e-324", "4.9406564584124654e-324", "2.4703282292062327e-324", "2.4703282292062328e-324",
		"1e-324", "1e-400", "2.2250738585072011e-308", "2.2250738585072012e-308", "2.2250738585072014e-308",
		"9007199254740991", "9007199254740992", "9007199254740993", "9007199254740995", "18014398509481986",
		"18446744073709551615", "18446744073709551616", "9999999999999999999", "99999999999999999999",
		"123456789012345678901234567890", "1234567890123456789.5", "0.000000000000000000000000000000000000001",
		"100000000000000000000000000000000000000000e-20", "1.0000000000000000000000000000001",
		"4503599627370496.5", "4503599627370497.5", "8.98846567431158e307", "9.5e-5", "1e0", "1E+2", "1e-0",
		"0.0000000000000000000000000000000000000000000000000000000000001e60", "1e1000000000", "1e-1000000000",
		"2.225073858507201136057409796709131975934819546351645648023426109724822222021076945516529523908135087914149158913039621106870086438694594645527657207407820621743379988141063267329253552286881372149012981122451451889849057222307285255133155755015914397476397983411801999323962548289017107081850690630666655994938275772572015763062690663332647565300009245888316433037779791869612049497390377829704905051080609940730262937128958950003583799967207254304360284078895771796150945516748243471030702609144621572289880258182545180325707018860872113128079512233426288368622321503775666622503982534335974568884423900265498198385487948292206894721689831099698365846814022854243330660339850886445804001034933970427567186443383770486037861622771738545623065874679014086723327636718751234567890123456789012345678901e-308",
	} {
		check(text)
	}
	rng := rand.New(rand.NewPCG(9, 99))
	// The halfway cases: (2^53 + 1) × 2^k, between two floats exactly.
	for k := range 12 {
		check(strconv.FormatUint((1<<53+1)<<k, 10))
		check(strconv.FormatUint((1<<53+3)<<k, 10))
	}
	for range 200_000 {
		f := math.Float64frombits(rng.Uint64())
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		check(strconv.FormatFloat(f, 'g', -1, 64))
		check(strconv.FormatFloat(f, 'e', rng.IntN(25), 64))
		if math.Abs(f) < 1e30 && math.Abs(f) > 1e-30 {
			check(strconv.FormatFloat(f, 'f', -1, 64))
		}
	}
	// Random digits, a point anywhere among them, and an exponent across the range.
	for range 200_000 {
		var b []byte
		if rng.IntN(2) == 0 {
			b = append(b, '-')
		}
		n := 1 + rng.IntN(25)
		b = append(b, byte('1'+rng.IntN(9)))
		for range n - 1 {
			b = append(b, byte('0'+rng.IntN(10)))
		}
		if dot := rng.IntN(n + 1); dot > 0 && dot < n {
			b = append(b[:len(b)-n+dot], append([]byte{'.'}, b[len(b)-n+dot:]...)...)
		}
		if rng.IntN(2) == 0 {
			b = append(b, 'e')
			b = strconv.AppendInt(b, int64(rng.IntN(700)-350), 10)
		}
		check(string(b))
	}
}
