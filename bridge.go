package ursus

import (
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/exprbridge"
)

// Package selector builds the root's expressions through exprbridge (step 322), so
// the root's API names no internal type and selector needs no root function
// taking one: the root sets the bridge's Wrap to its own wrap when it initialises.
func init() { exprbridge.Wrap = func(n expr.Node) any { return wrap(n) } }
