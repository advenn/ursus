package shrink

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/compare"
	"github.com/advenn/ursus/qcheck/gen"
	"github.com/advenn/ursus/qcheck/model"
	"github.com/advenn/ursus/qcheck/self"
)

// Format is the version of a case file's layout.
const Format = 1

// A case's source: the finder that found it.
const (
	SourceSelf = "self" // the self check (package self)
	SourceDuck = "duck" // DuckDB (package compare)
)

// A case's mode: how it is run to see whether it fails.
const (
	// ModeDifferential runs the reference configuration and the failing one, whose
	// answers must agree, as the self check does.
	ModeDifferential = "differential"
	// ModeRerun runs the failing configuration twice, whose answers must agree to
	// the bit.
	ModeRerun = "rerun"
	// ModeRun runs the failing configuration once, inside fuzzcheck.Run, which fails
	// ErrInternal, a panic, an allocation past the bound or a goroutine left behind.
	ModeRun = "run"
	// ModeSchema compares ursus's CollectSchema with the AST's types.
	ModeSchema = "schema"
	// ModeDuck runs sqlgen's SQL in DuckDB and the query in ursus under the failing
	// configuration, whose answers must agree, as package compare does.
	ModeDuck = "duck"
)

// genMaxRows is the bound gen.NewCase draws each table's rows under.
const genMaxRows = 8193

// Case is a finding as data: its tables, its query, the configurations it runs
// under and how it fails. A case's JSON is its file in qcheck/testdata/corpus.
type Case struct {
	Source string // SourceSelf or SourceDuck
	Mode   string // how it runs: ModeDifferential and the rest
	Seed   uint64 // the seed of the generated case it came from
	// Open marks a known open finding: the step that fixes it, and what it is. A
	// corpus case marked open must fail, and one not marked must pass.
	Open string
	Note string
	// Signature is how it fails. Every step of a shrink keeps it.
	Signature Signature
	// Reference is what the failing configuration is compared with, in
	// ModeDifferential: model.Reference().
	Reference model.Config
	// Config is the failing configuration: ursus's, in ModeDuck.
	Config model.Config
	Tables []*Table
	Query  *ast.Query
	// Shrunk is how the case was shrunk, if it was.
	Shrunk *Provenance
}

// Provenance records a shrink: the case's size before and after, and its cost.
type Provenance struct {
	From    Size    `json:"from"`
	To      Size    `json:"to"`
	Runs    int     `json:"runs"`
	Seconds float64 `json:"seconds"`
	Spent   bool    `json:"budget_spent,omitempty"`
}

// Size is how big a case is.
type Size struct {
	Tables  int `json:"tables"`
	Rows    int `json:"rows"`
	Columns int `json:"columns"`
	Ops     int `json:"ops"`     // frame operations, scans not counted
	Exprs   int `json:"exprs"`   // expression nodes
	Factors int `json:"factors"` // configuration factors the failing one differs from the reference in
}

func (s Size) String() string {
	return fmt.Sprintf("%d table(s), %d row(s), %d column(s), %d op(s), %d expression node(s), %d factor(s)",
		s.Tables, s.Rows, s.Columns, s.Ops, s.Exprs, s.Factors)
}

// Table is one of a case's tables: generated, gen.Table of its seed, name and
// MaxRows, its shape kept to check that the generator still draws it; or its rows,
// once shrunk. A Table is never changed once made: a shrink step makes another.
type Table struct {
	Name string
	// Seed is gen.Table's seed, and the seed build.Records cuts the table by.
	Seed uint64
	// MaxRows is a generated table's bound; 0 for a table of rows.
	MaxRows int
	// Core is a generated table of the core generator's, gen.CoreTable.
	Core bool
	Rows int
	Cols []Column

	m *model.Table // the table, once made
}

// Column is a table's column: its values, in a table of rows.
type Column struct {
	Name   string
	Type   model.Type
	Values []Value
}

// Value is a cell: a null, or the value of its column's type, I for an integer and
// a Date's days, F for a float, S for text, B for a Boolean; a wide type's as
// shrink/wide.go says.
type Value struct {
	Null bool
	I    int64
	F    float64
	S    string
	B    bool
}

// Generated is whether t is drawn from its seed rather than held as rows.
func (t *Table) Generated() bool { return t.MaxRows > 0 }

// Model is t as a model.Table, its record made once.
func (t *Table) Model() (*model.Table, error) {
	if t.m != nil {
		return t.m, nil
	}
	if t.Generated() {
		m := gen.Table(t.Seed, t.Name, t.MaxRows)
		if t.Core {
			m = gen.CoreTable(t.Seed, t.Name, t.MaxRows)
		}
		if m.Rows != t.Rows || len(m.Cols) != len(t.Cols) {
			return nil, fmt.Errorf("table %s: gen.Table draws %d rows of %d columns from seed %d, the case says %d of %d: "+
				"the generator changed", t.Name, m.Rows, len(m.Cols), t.Seed, t.Rows, len(t.Cols))
		}
		for i, c := range m.Cols {
			if c.Name != t.Cols[i].Name || c.Type != t.Cols[i].Type {
				return nil, fmt.Errorf("table %s: gen.Table draws column %s %s, the case says %s %s: the generator changed",
					t.Name, c.Name, c.Type, t.Cols[i].Name, t.Cols[i].Type)
			}
		}
		t.m = m
		return m, nil
	}
	m := &model.Table{Name: t.Name, Seed: t.Seed, Rows: t.Rows}
	fields := make([]arrow.Field, len(t.Cols))
	for i, c := range t.Cols {
		if len(c.Values) != t.Rows {
			return nil, fmt.Errorf("table %s: column %s holds %d values of %d rows", t.Name, c.Name, len(c.Values), t.Rows)
		}
		nulls := 0
		for _, v := range c.Values {
			if v.Null {
				nulls++
			}
		}
		rate := 0.0
		if t.Rows > 0 {
			rate = float64(nulls) / float64(t.Rows)
		}
		m.Cols = append(m.Cols, model.Column{Name: c.Name, Type: c.Type, Profile: model.Extreme, NullRate: rate})
		fields[i] = arrow.Field{Name: c.Name, Type: gen.ArrowType(c.Type), Nullable: true}
	}
	b := array.NewRecordBuilder(memory.NewGoAllocator(), arrow.NewSchema(fields, nil))
	defer b.Release()
	for i, c := range t.Cols {
		fb := b.Field(i)
		for _, v := range c.Values {
			if err := appendValue(fb, c.Type, v); err != nil {
				return nil, fmt.Errorf("table %s: %w", t.Name, err)
			}
		}
	}
	m.Rec = b.NewRecordBatch()
	t.m = m
	return m, nil
}

// Held is t as a table of rows, read from its record.
func (t *Table) Held() (*Table, error) {
	if !t.Generated() {
		return t, nil
	}
	m, err := t.Model()
	if err != nil {
		return nil, err
	}
	return FromModel(m)
}

// FromModel is m's rows as a Table.
func FromModel(m *model.Table) (*Table, error) {
	t := &Table{Name: m.Name, Seed: m.Seed, Rows: int(m.Rec.NumRows())}
	for j, mc := range m.Cols {
		col := m.Rec.Column(j)
		c := Column{Name: mc.Name, Type: mc.Type, Values: make([]Value, col.Len())}
		for i := range c.Values {
			v, err := valueAt(col, i)
			if err != nil {
				return nil, fmt.Errorf("table %s column %s: %w", m.Name, mc.Name, err)
			}
			c.Values[i] = v
		}
		t.Cols = append(t.Cols, c)
	}
	return t, nil
}

// Models are the case's tables, made.
func (c *Case) Models() ([]*model.Table, error) {
	out := make([]*model.Table, len(c.Tables))
	for i, t := range c.Tables {
		m, err := t.Model()
		if err != nil {
			return nil, err
		}
		out[i] = m
	}
	return out, nil
}

// TableMap is the case's tables, made, by name.
func (c *Case) TableMap() (map[string]*model.Table, error) {
	ms, err := c.Models()
	if err != nil {
		return nil, err
	}
	m := make(map[string]*model.Table, len(ms))
	for _, t := range ms {
		m[t.Name] = t
	}
	return m, nil
}

// Size is how big c is.
func (c *Case) Size() Size {
	s := Size{Tables: len(c.Tables), Factors: c.factors()}
	for _, t := range c.Tables {
		s.Rows += t.Rows
		s.Columns += len(t.Cols)
	}
	if c.Query != nil {
		ast.Walk(c.Query.Root, func(f ast.Frame) {
			if _, ok := f.(*ast.Scan); !ok {
				s.Ops++
			}
			for _, e := range slotExprs(f) {
				s.Exprs += exprNodes(e)
			}
		})
	}
	return s
}

// factors is how many factors the failing configuration differs from the
// reference in.
func (c *Case) factors() int {
	return factorCount(c.Config.Diff(model.Reference()))
}

func factorCount(f model.Factor) int {
	n := 0
	for _, one := range model.Factors {
		if f&one != 0 {
			n++
		}
	}
	return n
}

// Name is the case's file name in the corpus, without its extension.
func (c *Case) Name() string { return fmt.Sprintf("%s-%d", c.Source, c.Seed) }

// FromSeed is the case of seed's generated case, as the finder that found it ran
// it: mode, and the configurations. Its signature is learnt when it is first run.
func FromSeed(source string, seed uint64, mode string, ref, cfg model.Config) *Case {
	return fromSeed(source, seed, mode, ref, cfg, false)
}

// FromCoreSeed is FromSeed of the core generator's case of seed.
func FromCoreSeed(source string, seed uint64, mode string, ref, cfg model.Config) *Case {
	return fromSeed(source, seed, mode, ref, cfg, true)
}

func fromSeed(source string, seed uint64, mode string, ref, cfg model.Config, core bool) *Case {
	g := gen.CaseOf(seed, core)
	c := &Case{Source: source, Mode: mode, Seed: seed, Reference: ref, Config: cfg, Query: g.Query}
	for _, t := range g.Tables {
		c.Tables = append(c.Tables, generated(t, core))
	}
	return c
}

// generated is m as a generated Table, its shape recorded.
func generated(m *model.Table, core bool) *Table {
	t := &Table{Name: m.Name, Seed: m.Seed, MaxRows: genMaxRows, Core: core, Rows: m.Rows, m: m}
	for _, mc := range m.Cols {
		t.Cols = append(t.Cols, Column{Name: mc.Name, Type: mc.Type})
	}
	return t
}

// FromSelf is the case of a self check's finding. Only a mismatch, a rerun, a
// fuzzcheck failure and a schema finding are a case: the others are a run's, not a
// query's.
func FromSelf(f self.Finding) (*Case, error) {
	ref := model.Reference()
	switch f.Kind {
	case self.KindMismatch:
		if f.Detail != "" && strings.HasPrefix(f.Detail, "the canary") {
			return nil, fmt.Errorf("seed %d: the canary's mismatch is the float-threads rule's, not a query's", f.Seed)
		}
		return fromSeed(SourceSelf, f.Seed, ModeDifferential, f.Ref, f.Variant, f.Core), nil
	case self.KindRerun:
		return fromSeed(SourceSelf, f.Seed, ModeRerun, ref, f.Variant, f.Core), nil
	case self.KindFuzzcheck:
		cfg := f.Variant
		if cfg == (model.Config{}) {
			cfg = ref
		}
		return fromSeed(SourceSelf, f.Seed, ModeRun, ref, cfg, f.Core), nil
	case self.KindSchema:
		return fromSeed(SourceSelf, f.Seed, ModeSchema, ref, ref, f.Core), nil
	}
	return nil, fmt.Errorf("a %s finding is not a case to shrink", f.Kind)
}

// FromDuck is the case of a DuckDB finding (package compare).
func FromDuck(f compare.Finding) (*Case, error) {
	ref := model.Reference()
	switch f.Kind {
	case compare.KindMismatch, compare.KindRefused, compare.KindSQL:
		return fromSeed(SourceDuck, f.Seed, ModeDuck, ref, f.Config, f.Core), nil
	case compare.KindFuzzcheck:
		return fromSeed(SourceDuck, f.Seed, ModeRun, ref, f.Config, f.Core), nil
	case compare.KindSchema:
		return fromSeed(SourceDuck, f.Seed, ModeSchema, ref, ref, f.Core), nil
	}
	return nil, fmt.Errorf("a %s finding is not a case to shrink", f.Kind)
}

// --- JSON -----------------------------------------------------------------------

type caseJSON struct {
	Format    int             `json:"format"`
	Source    string          `json:"source"`
	Mode      string          `json:"mode"`
	Seed      uint64          `json:"seed"`
	Open      string          `json:"open,omitempty"`
	Note      string          `json:"note,omitempty"`
	Signature Signature       `json:"signature"`
	Reference *configJSON     `json:"reference,omitempty"`
	Config    configJSON      `json:"config"`
	Tables    []tableJSON     `json:"tables"`
	Query     json.RawMessage `json:"query"`
	Shrunk    *Provenance     `json:"shrunk,omitempty"`
}

type configJSON struct {
	Threads int  `json:"threads"`
	Batch   int  `json:"batch"`
	OptOff  bool `json:"optimizer_off,omitempty"`
	Verify  bool `json:"verify,omitempty"`
	Records int  `json:"records"`
}

func toConfigJSON(c model.Config) configJSON {
	return configJSON{Threads: c.Threads, Batch: c.Batch, OptOff: c.OptOff, Verify: c.Verify, Records: c.Records}
}

func (c configJSON) config() model.Config {
	return model.Config{Threads: c.Threads, Batch: c.Batch, OptOff: c.OptOff, Verify: c.Verify, Records: c.Records}
}

type tableJSON struct {
	Name    string       `json:"name"`
	Seed    uint64       `json:"seed"`
	MaxRows int          `json:"max_rows,omitempty"`
	Core    bool         `json:"core,omitempty"`
	Rows    int          `json:"rows"`
	Columns []columnJSON `json:"columns"`
}

type columnJSON struct {
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Values []json.RawMessage `json:"values,omitempty"`
}

// MarshalJSON is the case's file.
func (c *Case) MarshalJSON() ([]byte, error) {
	q, err := encodeQuery(c.Query)
	if err != nil {
		return nil, err
	}
	j := caseJSON{Format: Format, Source: c.Source, Mode: c.Mode, Seed: c.Seed, Open: c.Open, Note: c.Note,
		Signature: c.Signature, Config: toConfigJSON(c.Config), Query: q, Shrunk: c.Shrunk}
	if c.Mode == ModeDifferential {
		r := toConfigJSON(c.Reference)
		j.Reference = &r
	}
	for _, t := range c.Tables {
		tj := tableJSON{Name: t.Name, Seed: t.Seed, MaxRows: t.MaxRows, Core: t.Core, Rows: t.Rows, Columns: []columnJSON{}}
		for _, col := range t.Cols {
			cj := columnJSON{Name: col.Name, Type: col.Type.String()}
			if !t.Generated() {
				cj.Values = make([]json.RawMessage, len(col.Values))
				for i, v := range col.Values {
					if cj.Values[i], err = encodeValue(col.Type, v); err != nil {
						return nil, fmt.Errorf("table %s column %s: %w", t.Name, col.Name, err)
					}
				}
			}
			tj.Columns = append(tj.Columns, cj)
		}
		j.Tables = append(j.Tables, tj)
	}
	return json.Marshal(j)
}

// UnmarshalJSON reads a case's file, its query through ast's constructors and
// ast.Check.
func (c *Case) UnmarshalJSON(b []byte) error {
	var j caseJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	if j.Format != Format {
		return fmt.Errorf("a case of format %d; this qcheck reads %d", j.Format, Format)
	}
	*c = Case{Source: j.Source, Mode: j.Mode, Seed: j.Seed, Open: j.Open, Note: j.Note, Signature: j.Signature,
		Reference: model.Reference(), Config: j.Config.config(), Shrunk: j.Shrunk}
	if j.Reference != nil {
		c.Reference = j.Reference.config()
	}
	for _, tj := range j.Tables {
		t := &Table{Name: tj.Name, Seed: tj.Seed, MaxRows: tj.MaxRows, Core: tj.Core, Rows: tj.Rows}
		for _, cj := range tj.Columns {
			typ, ok := model.ParseType(cj.Type)
			if !ok || !TableType(typ) {
				return fmt.Errorf("table %s column %s: a table holds no column of %q", tj.Name, cj.Name, cj.Type)
			}
			col := Column{Name: cj.Name, Type: typ}
			if !t.Generated() {
				if len(cj.Values) != tj.Rows {
					return fmt.Errorf("table %s column %s: %d values of %d rows", tj.Name, cj.Name, len(cj.Values), tj.Rows)
				}
				col.Values = make([]Value, len(cj.Values))
				for i, raw := range cj.Values {
					v, err := decodeValue(typ, raw)
					if err != nil {
						return fmt.Errorf("table %s column %s row %d: %w", tj.Name, cj.Name, i, err)
					}
					col.Values[i] = v
				}
			}
			t.Cols = append(t.Cols, col)
		}
		c.Tables = append(c.Tables, t)
	}
	q, err := decodeQuery(j.Query)
	if err != nil {
		return fmt.Errorf("the query: %w", err)
	}
	c.Query = q
	tables, err := c.TableMap()
	if err != nil {
		return err
	}
	if err := ast.Check(q, tables); err != nil {
		return fmt.Errorf("the query is ill-formed over the tables: %w", err)
	}
	return nil
}

// Marshal is c's file: JSON, indented.
func Marshal(c *Case) ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Load reads a case's file.
func Load(path string) (*Case, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &Case{}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Save writes c's file.
func Save(c *Case, path string) error {
	b, err := Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// encodeValue is v as JSON: an integer or a Date's days a number; a float its
// shortest text, "NaN", "+Inf", "-Inf" and "-0" among them, and a NaN of another
// payload "NaN:" and its bits; text a string, or {"hex": …} where it is not UTF-8;
// a Boolean true or false; a null null.
func encodeValue(t model.Type, v Value) (json.RawMessage, error) {
	if v.Null {
		return json.RawMessage("null"), nil
	}
	if b, ok, err := encodeWide(t, v); ok {
		return b, err
	}
	switch t {
	case model.Int8, model.Int16, model.Int32, model.Int64, model.Date:
		return json.RawMessage(strconv.FormatInt(v.I, 10)), nil
	case model.Float64:
		return json.Marshal(floatText(v.F))
	case model.String:
		if !utf8.ValidString(v.S) {
			return json.Marshal(map[string]string{"hex": hex.EncodeToString([]byte(v.S))})
		}
		return json.Marshal(v.S)
	case model.Bool:
		return json.Marshal(v.B)
	}
	return nil, fmt.Errorf("no value of %s", t)
}

func decodeValue(t model.Type, raw json.RawMessage) (Value, error) {
	s := strings.TrimSpace(string(raw))
	if s == "null" {
		return Value{Null: true}, nil
	}
	if v, ok, err := decodeWide(t, raw); ok {
		return v, err
	}
	switch t {
	case model.Int8, model.Int16, model.Int32, model.Int64, model.Date:
		i, err := strconv.ParseInt(s, 10, 64)
		return Value{I: i}, err
	case model.Float64:
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return Value{}, err
		}
		f, err := parseFloatText(text)
		return Value{F: f}, err
	case model.String:
		if strings.HasPrefix(s, "{") {
			var h map[string]string
			if err := json.Unmarshal(raw, &h); err != nil {
				return Value{}, err
			}
			b, err := hex.DecodeString(h["hex"])
			return Value{S: string(b)}, err
		}
		var text string
		err := json.Unmarshal(raw, &text)
		return Value{S: text}, err
	case model.Bool:
		var b bool
		err := json.Unmarshal(raw, &b)
		return Value{B: b}, err
	}
	return Value{}, fmt.Errorf("no value of %s", t)
}

var canonicalNaN = math.Float64bits(math.NaN())

// floatText is f as text that parseFloatText reads to its bits.
func floatText(f float64) string {
	if math.IsNaN(f) {
		if b := math.Float64bits(f); b != canonicalNaN {
			return fmt.Sprintf("NaN:%016x", b)
		}
		return "NaN"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func parseFloatText(s string) (float64, error) {
	if s == "NaN" {
		return math.NaN(), nil
	}
	if h, ok := strings.CutPrefix(s, "NaN:"); ok {
		b, err := strconv.ParseUint(h, 16, 64)
		return math.Float64frombits(b), err
	}
	return strconv.ParseFloat(s, 64)
}

// litValue is a literal's value as a Value.
func litValue(l *ast.Lit) Value {
	if l.Null {
		return Value{Null: true}
	}
	switch v := l.V.(type) {
	case int64:
		return Value{I: v}
	case int32:
		return Value{I: int64(v)}
	case uint64:
		return Value{I: int64(v)}
	case float64:
		return Value{F: v}
	case float32:
		return Value{F: float64(v)}
	case string:
		if l.T.IsDecimal() {
			return decValue(v)
		}
		return Value{S: v}
	case bool:
		return Value{B: v}
	}
	panic(fmt.Sprintf("shrink: a literal of %T", l.V))
}

// newLit is the literal of type t holding v.
func newLit(t model.Type, v Value) *ast.Lit {
	if v.Null {
		return &ast.Lit{T: t, Null: true}
	}
	switch {
	case t.IsUnsigned():
		return &ast.Lit{T: t, V: uint64(v.I)}
	case t == model.Float32:
		return &ast.Lit{T: t, V: float32(v.F)}
	case t.IsDatetime(), t.IsDuration(), t.IsTime():
		return &ast.Lit{T: t, V: v.I}
	case t.IsDecimal():
		return &ast.Lit{T: t, V: decimalOf(v)}
	}
	switch t {
	case model.Int8, model.Int16, model.Int32, model.Int64:
		return &ast.Lit{T: t, V: v.I}
	case model.Date:
		return &ast.Lit{T: t, V: int32(v.I)}
	case model.Float64:
		return &ast.Lit{T: t, V: v.F}
	case model.String:
		return &ast.Lit{T: t, V: v.S}
	case model.Bool:
		return &ast.Lit{T: t, V: v.B}
	}
	panic("shrink: no literal of " + t.String())
}
