//go:build ursus07

package main

import "fmt"

// 0.7 has no Engine: the default engine's figures and the shared mode are 0.8's.

func engineStats() string { return "none, 0.7 has no Engine" }

func shared() { fmt.Println("shared needs 0.8's Engine") }
