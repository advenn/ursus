// Package hive reads a dataset's partition columns from its files' paths, as Hive
// lays a dataset out and Polars reads one: each directory below the dataset's root
// named key=value gives every file beneath it a column key of value (step 347).
package hive

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// DefaultPartition is the value Hive writes for a null key.
const DefaultPartition = "__HIVE_DEFAULT_PARTITION__"

// Partitions are a dataset's partition keys, in path order, and each file's values of
// them: the text its path spells, percent-decoded, and whether the value is null,
// Hive's default partition or nothing.
type Partitions struct {
	Keys []string
	Text [][]string // by file, then key
	Null [][]bool
	op   string // the scan an error names
}

// Parse reads the key=value directories of each path below root, or of the whole of
// it where root is "", as Polars reads a directory's files and a list's; every path's
// the same keys in the same order, as Polars refuses paths whose keys differ. A
// directory not of that form is passed over, and so is root's own name.
func Parse(root string, paths []string) (*Partitions, error) {
	return ParseFor("scan_parquet", root, paths)
}

// ParseFor is Parse for the scan op, which its errors and Values' name (step 360).
func ParseFor(op, root string, paths []string) (*Partitions, error) {
	p := &Partitions{Text: make([][]string, len(paths)), Null: make([][]bool, len(paths)), op: op}
	for k, path := range paths {
		rel := filepath.Dir(path)
		if root != "" {
			var err error
			rel, err = filepath.Rel(root, rel)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil, uerr.New(uerr.KindValue, op, "%s is not below %s", path, root)
			}
		}
		var keys []string
		for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
			key, raw, ok := strings.Cut(seg, "=")
			if !ok || key == "" {
				continue
			}
			value, err := url.PathUnescape(raw)
			if err != nil {
				value = raw
			}
			keys = append(keys, key)
			null := value == "" || value == DefaultPartition
			p.Text[k] = append(p.Text[k], value)
			p.Null[k] = append(p.Null[k], null)
		}
		switch {
		case k == 0:
			p.Keys = keys
		case !equal(keys, p.Keys):
			// Polars: "path contains column not present in the given Hive schema".
			return nil, uerr.New(uerr.KindSchema, op,
				"%s is partitioned by %s, where %s is by %s", path, show(keys), paths[0], show(p.Keys)).
				Hint("every file of a hive dataset is partitioned by the same keys, in the same order")
		}
	}
	return p, nil
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func show(keys []string) string {
	if len(keys) == 0 {
		return "no key"
	}
	return strings.Join(keys, "/")
}

// Types are the keys' types: given's where it names the key; a file column's of the
// same name, which the key's values replace, as Polars reads them; or the type the
// key's values infer (Infer).
func (p *Partitions) Types(given map[string]dtype.DataType, files *dtype.Schema, dates bool) []dtype.DataType {
	types := make([]dtype.DataType, len(p.Keys))
	for j, key := range p.Keys {
		if t, ok := given[key]; ok {
			types[j] = t
			continue
		}
		if i := files.IndexOf(key); i >= 0 {
			types[j] = files.Field(i).Type
			continue
		}
		vals := make([]string, 0, len(p.Text))
		for k := range p.Text {
			if !p.Null[k][j] {
				vals = append(vals, p.Text[k][j])
			}
		}
		types[j] = Infer(vals, dates)
	}
	return types
}

// Values are each file's partition values as columns of one row, by file then key,
// of the types: each key's values cast from their text, strictly, a value its type
// cannot take refused, as Polars refuses it.
func (p *Partitions) Values(types []dtype.DataType) ([][]*data.Column, error) {
	out := make([][]*data.Column, len(p.Text))
	for k := range out {
		out[k] = make([]*data.Column, len(p.Keys))
	}
	for j, key := range p.Keys {
		texts := make([]string, len(p.Text))
		valid := bitmap.NewBuilder(len(p.Text))
		for k := range p.Text {
			texts[k] = canonical(p.Text[k][j], types[j])
			valid.Append(!p.Null[k][j])
		}
		col, err := kernel.Cast(key, types[j], true, data.NewString(key, texts, valid.Finish()))
		if err != nil {
			return nil, uerr.Annotate(err, p.op, "partition key "+key)
		}
		for k := range out {
			out[k][j] = col.Slice(k, 1)
		}
	}
	return out, nil
}

// Infer is the type of a key's values, as Polars infers a partition key's: Int64,
// or Int128 past it, for integers; Float64 for numbers among them one with a point,
// an exponent, inf or NaN; Boolean for true and false in any case; with dates, Date
// for dates and Datetime for instants, in microseconds and in UTC where one has an
// offset; String for anything else, and for a key with no value.
func Infer(vals []string, dates bool) dtype.DataType {
	kind := kNone
	for _, v := range vals {
		kind = widen(kind, classify(v, dates))
	}
	switch kind {
	case kInt:
		return dtype.Int64
	case kInt128:
		return dtype.Int128
	case kFloat:
		return dtype.Float64
	case kBool:
		return dtype.Bool
	case kDate:
		return dtype.Date
	case kDatetime:
		return dtype.Datetime(dtype.Micro, "")
	case kZoned:
		return dtype.Datetime(dtype.Micro, "UTC")
	}
	return dtype.String
}

type kind uint8

const (
	kNone kind = iota
	kInt
	kInt128
	kFloat
	kBool
	kDate
	kDatetime
	kZoned
	kString
)

// widen merges a value's kind into the running one: integers and floats in the
// widest, a naive and a zoned instant in neither, anything else unlike as text.
func widen(a, b kind) kind {
	num := func(k kind) bool { return k == kInt || k == kInt128 || k == kFloat }
	switch {
	case a == kNone:
		return b
	case a == b:
		return a
	case num(a) && num(b):
		return max(a, b)
	}
	return kString
}

func classify(v string, dates bool) kind {
	switch {
	case isInteger(v):
		if _, err := strconv.ParseInt(v, 10, 64); err == nil {
			return kInt
		}
		if _, ok := i128.Parse(v); ok {
			return kInt128
		}
		return kString
	case isFloat(v):
		return kFloat
	case strings.EqualFold(v, "true"), strings.EqualFold(v, "false"):
		return kBool
	}
	if dates {
		if _, ok := parseDate(v); ok {
			return kDate
		}
		if _, zoned, ok := parseDatetime(v); ok {
			if zoned {
				return kZoned
			}
			return kDatetime
		}
	}
	return kString
}

// isInteger is Polars' -?\d+.
func isInteger(v string) bool {
	if strings.HasPrefix(v, "-") {
		v = v[1:]
	}
	return v != "" && digits(v) == len(v)
}

// isFloat is Polars' float: a sign, then inf, NaN, or digits with a point, an
// exponent, or both, at least one digit beside the point.
func isFloat(v string) bool {
	if v != "" && (v[0] == '-' || v[0] == '+') {
		v = v[1:]
	}
	if v == "inf" || v == "NaN" {
		return true
	}
	whole := digits(v)
	v = v[whole:]
	frac, point := 0, false
	if strings.HasPrefix(v, ".") {
		point = true
		v = v[1:]
		frac = digits(v)
		v = v[frac:]
	}
	if whole+frac == 0 {
		return false
	}
	if v == "" {
		return point
	}
	if v[0] != 'e' && v[0] != 'E' {
		return false
	}
	v = v[1:]
	if v != "" && (v[0] == '-' || v[0] == '+') {
		v = v[1:]
	}
	return v != "" && digits(v) == len(v)
}

func digits(v string) int {
	n := 0
	for n < len(v) && v[n] >= '0' && v[n] <= '9' {
		n++
	}
	return n
}

// parseDate reads a date as Polars infers one: year, month and day apart by '-',
// '/' or '.', or day, month and year, the year of four digits.
func parseDate(v string) (time.Time, bool) {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '-' || r == '/' || r == '.' })
	if len(parts) != 3 || strings.Count(v, string(v[len(parts[0])])) != 2 {
		return time.Time{}, false
	}
	for _, p := range parts {
		if p == "" || digits(p) != len(p) {
			return time.Time{}, false
		}
	}
	y, m, d := parts[0], parts[1], parts[2]
	if len(y) != 4 {
		y, d = d, y
	}
	if len(y) != 4 || len(m) > 2 || len(d) > 2 {
		return time.Time{}, false
	}
	return civil(y, m, d)
}

func civil(y, m, d string) (time.Time, bool) {
	yy, _ := strconv.Atoi(y)
	mm, _ := strconv.Atoi(m)
	dd, _ := strconv.Atoi(d)
	t := time.Date(yy, time.Month(mm), dd, 0, 0, 0, 0, time.UTC)
	if t.Year() != yy || int(t.Month()) != mm || t.Day() != dd {
		return time.Time{}, false // 2024-13-01, 2024-02-30
	}
	return t, true
}

// parseDatetime reads an instant as Polars infers one: a date of year, month and day,
// then 'T' or a space, then hours and minutes, seconds and a fraction if any, and an
// offset, Z or ±hh[:]mm, if any. The fraction past microseconds is cut.
func parseDatetime(v string) (time.Time, bool, bool) {
	i := strings.IndexAny(v, "T ")
	if i < 0 {
		return time.Time{}, false, false
	}
	day, ok := parseDate(v[:i])
	if !ok || len(strings.FieldsFunc(v[:i], func(r rune) bool { return r == '-' || r == '/' || r == '.' })[0]) != 4 {
		return time.Time{}, false, false
	}
	rest := v[i+1:]
	zoned, offset := false, 0
	switch {
	case strings.HasSuffix(rest, "Z"):
		zoned, rest = true, rest[:len(rest)-1]
	default:
		if j := strings.LastIndexAny(rest, "+-"); j > 0 {
			off := strings.ReplaceAll(rest[j+1:], ":", "")
			if len(off) != 4 || digits(off) != 4 {
				return time.Time{}, false, false
			}
			h, _ := strconv.Atoi(off[:2])
			m, _ := strconv.Atoi(off[2:])
			offset = (h*60 + m) * 60
			if rest[j] == '-' {
				offset = -offset
			}
			zoned, rest = true, rest[:j]
		}
	}
	hms := strings.Split(rest, ":")
	if len(hms) < 2 || len(hms) > 3 {
		return time.Time{}, false, false
	}
	frac := ""
	if len(hms) == 3 {
		if s, f, ok := strings.Cut(hms[2], "."); ok {
			if f == "" || digits(f) != len(f) {
				return time.Time{}, false, false
			}
			hms[2], frac = s, f
		}
	}
	var n [3]int
	for k, p := range hms {
		if p == "" || len(p) > 2 || digits(p) != len(p) || k > 0 && len(p) != 2 {
			return time.Time{}, false, false
		}
		n[k], _ = strconv.Atoi(p)
	}
	if n[0] > 23 || n[1] > 59 || n[2] > 59 {
		return time.Time{}, false, false
	}
	frac = (frac + "000000")[:6]
	us, _ := strconv.Atoi(frac)
	t := day.Add(time.Duration(n[0])*time.Hour + time.Duration(n[1])*time.Minute +
		time.Duration(n[2])*time.Second + time.Duration(us)*time.Microsecond - time.Duration(offset)*time.Second)
	return t, zoned, true
}

// canonical is v as the cast to t reads it: a date and an instant in the layouts
// ParseTemporal takes, the instant in UTC.
func canonical(v string, t dtype.DataType) string {
	switch t.ID() {
	case dtype.TypeDate:
		if d, ok := parseDate(v); ok {
			return d.Format("2006-01-02")
		}
	case dtype.TypeDatetime:
		if d, _, ok := parseDatetime(v); ok {
			return d.Format("2006-01-02T15:04:05.999999Z07:00")
		}
		if d, ok := parseDate(v); ok {
			return d.Format("2006-01-02T15:04:05Z07:00")
		}
	}
	return v
}
