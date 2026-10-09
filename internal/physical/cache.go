package physical

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/spill"
	"github.com/advenn/ursus/internal/uerr"
)

// sharedPlans is a query's registry of the Cache subtrees planned so far, by ID.
type sharedPlans struct {
	mu   sync.Mutex
	byID map[int]*sharedResult
}

// sharedResult is one Cache subtree, planned once, run once, and replayed by every
// site of its ID (step 143).
//
// It runs at the first Next of any site, to the end, and holds what it produced:
// in memory, charged to the budget, or past the budget in a spill file, which each
// site reads back with a reader of its own. Its child is closed as soon as it has
// run, so the subtree's own state — a join's table, a group-by's answer — does not
// live as long as the replays. The last site to close releases the rest.
type sharedResult struct {
	schema *dtype.Schema
	child  Operator
	budget *execopt.Budget
	mem    *execopt.Account

	once    sync.Once
	err     error
	batches []*data.Batch // what it produced, while it is held in memory
	dir     string        // the spill directory, once it has spilled
	path    string        // the spill file, which then holds every batch

	mu          sync.Mutex
	sites       int
	closed      int
	childClosed bool
}

// planCache plans c's subtree the first time its ID is met, and returns a replay of
// it at every site. Options built by hand, without PlanRoot, have no registry, and
// plan each site's subtree on its own, as a query did before Cache existed.
func planCache(ctx context.Context, c *plan.Cache, opts Options) (Operator, error) {
	if opts.shared == nil {
		return Plan(ctx, c.Input, opts)
	}
	opts.shared.mu.Lock()
	s := opts.shared.byID[c.ID]
	opts.shared.mu.Unlock()
	if s == nil {
		schema, err := c.Schema()
		if err != nil {
			return nil, err
		}
		child, err := planPipelined(ctx, c.Input, opts)
		if err != nil {
			return nil, err
		}
		s = &sharedResult{schema: schema, child: child, budget: opts.Budget,
			mem: opts.Budget.Account("cache")}
		opts.shared.mu.Lock()
		opts.shared.byID[c.ID] = s
		opts.shared.mu.Unlock()
	}
	s.mu.Lock()
	s.sites++
	s.mu.Unlock()
	return &cacheOp{shared: s}, nil
}

// run drains the subtree once, for every site.
func (s *sharedResult) run(ctx context.Context) error {
	s.once.Do(func() { s.err = s.drain(ctx) })
	return s.err
}

func (s *sharedResult) drain(ctx context.Context) error {
	var w *spill.Writer
	defer func() {
		if w != nil {
			w.Close()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := Pull(ctx, s.child)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if b.Rows() == 0 {
			continue
		}
		if w != nil {
			if err := w.Write(b); err != nil {
				return uerr.Wrap(err, uerr.KindIO, "cache", "writing spill file %s", s.path)
			}
			continue
		}
		s.batches = append(s.batches, b)
		s.mem.Retain(b)
		if s.budget.Limit() > 0 && s.mem.Over() {
			if w, err = s.spillHeld(); err != nil {
				return err
			}
		}
	}
	if w != nil {
		err := w.Close()
		w = nil
		if err != nil {
			return uerr.Wrap(err, uerr.KindIO, "cache", "closing spill file %s", s.path)
		}
	}
	return s.closeChild()
}

// spillHeld moves what is held so far to a spill file and returns the writer the
// rest goes to. A shared result has no partitioning to fall back on: it is replayed
// whole, so past the budget it is replayed from disk.
func (s *sharedResult) spillHeld() (*spill.Writer, error) {
	dir, err := os.MkdirTemp(s.budget.SpillDir(), "ursus-cache-")
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindResource, "cache", "creating a spill directory").
			Hint("choose a writable location with WithSpillDir")
	}
	s.dir, s.path = dir, filepath.Join(dir, "cache.ursspill")
	w, err := spill.Create(s.path, s.schema)
	if err != nil {
		return nil, err
	}
	for _, b := range s.batches {
		if err := w.Write(b); err != nil {
			w.Close()
			return nil, uerr.Wrap(err, uerr.KindIO, "cache", "writing spill file %s", s.path)
		}
	}
	s.batches = nil
	s.mem.Release()
	s.budget.NoteSpill()
	return w, nil
}

// closeChild closes the subtree once it has run, or at the last site's Close if it
// never did.
func (s *sharedResult) closeChild() error {
	s.mu.Lock()
	done := s.childClosed
	s.childClosed = true
	s.mu.Unlock()
	if done {
		return nil
	}
	return s.child.Close()
}

// release is one site's Close. The last one closes what is left.
func (s *sharedResult) release() error {
	s.mu.Lock()
	s.closed++
	last := s.closed == s.sites
	s.mu.Unlock()
	if !last {
		return nil
	}
	err := s.closeChild()
	s.batches = nil
	s.mem.Release()
	if s.dir != "" {
		if rmErr := os.RemoveAll(s.dir); rmErr != nil {
			err = errors.Join(err, uerr.Wrap(rmErr, uerr.KindIO, "cache",
				"removing spill directory %s", s.dir))
		}
	}
	return err
}

// cacheOp is one site's replay of a shared result.
type cacheOp struct {
	shared *sharedResult
	pos    int
	rd     *spill.Reader
	done   bool
}

func (c *cacheOp) Schema() *dtype.Schema { return c.shared.schema }

func (c *cacheOp) Next(ctx context.Context) (*data.Batch, error) {
	if err := c.shared.run(ctx); err != nil {
		return nil, err
	}
	if c.shared.path != "" {
		if c.rd == nil {
			rd, err := spill.Open(c.shared.path)
			if err != nil {
				return nil, uerr.Wrap(err, uerr.KindIO, "cache", "reading spill file %s", c.shared.path)
			}
			c.rd = rd
		}
		return c.rd.Next()
	}
	if c.pos >= len(c.shared.batches) {
		return nil, io.EOF
	}
	b := c.shared.batches[c.pos]
	c.pos++
	return b, nil
}

func (c *cacheOp) Close() error {
	var err error
	if c.rd != nil {
		err = c.rd.Close()
		c.rd = nil
	}
	if c.done {
		return err
	}
	c.done = true
	return errors.Join(err, c.shared.release())
}
