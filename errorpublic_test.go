package ursus_test

// Step 402: ursus's structured error is public, ursus.Error through errors.As, its
// kind, operation, message, node, hints and cause (C2 of v0.8-scope.md). The
// sentinels stay.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestAnErrorIsReadByErrorsAs: an unknown column, wrapped by a caller's own error, is
// an *ursus.Error of KindSchema, its operation, its plan node, its message naming the
// column and a hint of the near one, the message the error gives; errors.Is matches
// it to ErrSchema, as before, and the Error too. What errors.As gives is a copy: a
// caller writing to it changes no later answer.
func TestAnErrorIsReadByErrorsAs(t *testing.T) {
	df := engineFrame(t, 10, 3)
	_, err := df.Lazy().Select(ursus.Col("vv")).Collect(t.Context())
	if err == nil {
		t.Fatal("selected a column the frame does not have")
	}
	err = fmt.Errorf("handling a request: %w", err)
	var e *ursus.Error
	if !errors.As(err, &e) {
		t.Fatalf("errors.As found no *ursus.Error in %v", err)
	}
	if e.Kind != ursus.KindSchema || e.Op != "select" || e.Node != "Project" || !strings.Contains(e.Msg, `"vv"`) {
		t.Errorf("kind %s, op %q, node %q, message %q; want schema, select, Project and the column",
			e.Kind, e.Op, e.Node, e.Msg)
	}
	if !slices.ContainsFunc(e.Hints, func(h string) bool { return strings.Contains(h, `"v"`) }) {
		t.Errorf("hints %q name no near column", e.Hints)
	}
	if !strings.Contains(err.Error(), e.Error()) {
		t.Errorf("the Error says %q, the error %q", e.Error(), err.Error())
	}
	if !errors.Is(err, ursus.ErrSchema) || !errors.Is(e, ursus.ErrSchema) || errors.Is(e, ursus.ErrType) {
		t.Error("the sentinels no longer match it, or match another kind")
	}
	e.Hints[0], e.Msg = "changed", "changed"
	var again *ursus.Error
	if !errors.As(err, &again) || again.Msg == "changed" || again.Hints[0] == "changed" || strings.Contains(err.Error(), "changed") {
		t.Error("writing to what errors.As gave changed the error")
	}
}

// TestAnErrorsCauseUnwraps: a Parquet file that is not there is an *ursus.Error of
// KindIO whose cause is the file system's, which errors.Is finds through it.
func TestAnErrorsCauseUnwraps(t *testing.T) {
	_, err := ursus.ScanParquet(filepath.Join(t.TempDir(), "missing.parquet")).Collect(t.Context())
	var e *ursus.Error
	if !errors.As(err, &e) {
		t.Fatalf("errors.As found no *ursus.Error in %v", err)
	}
	if e.Kind != ursus.KindIO || e.Err == nil || !errors.Is(e, fs.ErrNotExist) || !errors.Is(err, ursus.ErrIO) {
		t.Errorf("kind %s, cause %v: want io, and the file system's not-exist", e.Kind, e.Err)
	}
}

// TestACancelledQueryIsItsContextsError: a query whose context is cancelled fails with
// context.Canceled, which no *ursus.Error stands for, as Error's doc says; an error
// ursus did not make is no *ursus.Error either.
func TestACancelledQueryIsItsContextsError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := engineFrame(t, 1_000, 10).Lazy().Sort(ursus.Asc(ursus.Col("v"))).Collect(ctx)
	var e *ursus.Error
	if !errors.Is(err, context.Canceled) || errors.As(err, &e) {
		t.Errorf("a cancelled query's error %v: want context.Canceled, and no *ursus.Error (%v)", err, e)
	}
	if errors.As(errors.New("not ursus's"), &e) {
		t.Error("an error ursus did not make read as an *ursus.Error")
	}
}
