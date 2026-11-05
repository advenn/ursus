package physical

import (
	"context"

	"github.com/advenn/ursus/internal/kernel"
)

// noLookup marks a probe row whose null key matches nothing, in joinProbeOp.found.
const noLookup = int32(-2)

// enter looks up the current probe row's match list.
// checkSeen is Validate's uniqueness check on the left side, for a key this level
// decides: resident, or matching nothing. A ROUTED key is the sub-join's to police,
// so inserting it here would make the map O(all distinct probe keys) at every level
// instead of once — the unbounded structure spilling exists to bound. A null key
// reaches here only under NullsEqual, where it is a key like any other.
func (p *joinProbeOp) checkSeen(k []byte) error {
	if p.seen == nil {
		return nil
	}
	if _, dup := p.seen[string(k)]; dup {
		return duplicateKeyErr(p.spec, false, nil, p.row)
	}
	p.seen[string(k)] = struct{}{}
	return nil
}

func (p *joinProbeOp) enter() error {
	p.entered = true
	p.hit, p.nHit = 0, 0
	p.routed = false

	var id int32
	if len(p.keys) == 0 {
		id = 0
		if p.t.nKeys == 0 {
			return nil // cross join against an empty build side
		}
	} else {
		if p.found[p.row] == noLookup {
			// A null key matches nothing — without a lookup, so the two NullsEqual
			// settings cannot leak into each other even if a null-keyed build row is
			// somehow present.
			return nil
		}
		// Looked up in startBatch. The key's bytes are needed only to route it or to
		// police Validate, so they are encoded again only then.
		id = p.found[p.row]
		if id < 0 && !p.routes && p.seen == nil {
			// Nothing spilled and nothing validates, so the key's bytes have no use:
			// it matches nothing. Encoding them again, and hashing them byte by byte
			// to look for a bucket that cannot exist, was about 16% of PDS-H q17,
			// whose lineitem rows nearly all miss its 200 parts (step 158).
			return nil
		}
		if id < 0 {
			k := p.enc.Encode(p.row)
			// Not resident. Three lines decide the rest, and they need no split-state
			// branch:
			//
			//   - a key in ids has ALL its build rows resident, by the invariant;
			//   - a key not in ids has its build rows in bucket b, or nowhere at all;
			//   - so if bucket b has no build file, this key matches nothing.
			//
			// The third case is why no probe file is ever written for a bucket with no
			// build rows — which removes, by construction, the silent row loss of a
			// replay that walked build files and dropped the probe rows sitting beside
			// an absent one. Left, Full and Anti emit those rows here, inline, IN PROBE
			// ORDER, rather than at the end.
			if b := partitionOf(k, p.level); p.buildFile(b) != "" {
				p.ppend[b] = append(p.ppend[b], int32(p.row))
				p.routed = true
				return nil
			}
			// It matches nothing — and Validate still counts it. A left key that is
			// duplicated is duplicated whether or not the right side has it, which is
			// what Polars checks too. It used to return here, so 1:m passed over
			// [1, 1, 2] joined to [2], and under NullsEqual over two null keys.
			return p.checkSeen(k)
		}
		if p.seen != nil {
			if err := p.checkSeen(p.enc.Encode(p.row)); err != nil {
				return err
			}
		}
	}
	if p.t.off == nil {
		// Semi/Anti keep no row lists; presence in the table is the whole answer.
		// With a residual they DO keep row lists — needBuildRows is true — so this
		// arm is not taken and the real range below is used instead.
		p.nHit = 1
		return nil
	}
	p.hit, p.nHit = p.t.off[id], p.t.off[id+1]
	if p.matched != nil && p.nHit > p.hit {
		p.matched[id] = true
	}
	return nil
}

// appendMatches emits as much of the current row's match list as fits.
func (p *joinProbeOp) appendMatches(ctx context.Context) error {
	if p.routed {
		// A THIRD outcome, and it has to be explicit. Without it a routed row falls
		// into the !matched arm below, which emits (row, NullIndex) whenever the kind
		// wants unmatched probe rows — so Left, Full and Anti would emit every routed
		// row here AND again from its sub-join. Duplicates, no error, no length
		// mismatch.
		p.hit = p.nHit
		return nil
	}
	matched := p.nHit > p.hit

	// The residual turns "a partner exists" into "a partner satisfying p exists",
	// and it has to be decided HERE, before the verdict below, rather than by
	// filtering the output afterwards. Filtering the output filters left rows that
	// already survived, which answers a different question.
	if p.spec.maskOnly && len(p.spec.residual) > 0 {
		var err error
		if matched, err = p.residualMatch(ctx); err != nil {
			return err
		}
	}

	if p.spec.maskOnly {
		// Semi and Anti emit at most ONE row per probe row and no right column. A
		// semi join implemented by reusing the inner loop below returns THREE rows
		// for one left row against three matching right rows — see
		// TestSemiJoinDoesNotDuplicate.
		//
		// A SELECTION VECTOR, not a mask. The mask this used to build was positionally
		// tied to a CONTIGUOUS run of the probe batch — emit sliced p.cur[p.row-n :
		// p.row] and filtered it — and routing punches holes in that run: a routed row
		// contributes no bit, the window slides left by however many were routed, and
		// the filter lands on the WRONG ROWS. The row count stays right, which is what
		// made it invisible until a spilling fixture compared values past row ten.
		//
		// Appending a bit for routed rows does not fix it either: Anti has emitMatched
		// false, so an unmatched routed row would be selected here AND again by its
		// sub-join.
		if matched == p.spec.emitMatched {
			p.lsel = append(p.lsel, int32(p.row))
		}
		p.hit = p.nHit // consume the row
		return nil
	}

	if !matched {
		if p.spec.emitProbeUnmatched {
			p.lsel = append(p.lsel, int32(p.row))
			p.rsel = append(p.rsel, kernel.NullIndex)
		}
		p.hit = p.nHit
		return nil
	}
	for p.hit < p.nHit && p.outLen() < p.spec.batchSize {
		p.lsel = append(p.lsel, int32(p.row))
		p.rsel = append(p.rsel, p.t.rows[p.hit])
		p.hit++
	}
	return nil
}
