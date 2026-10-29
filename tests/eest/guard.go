// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// guard.go — turns a panic inside one EEST case into that case's failure.

package eest

import (
	"fmt"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
)

// Guard runs fn and converts a panic into an error carrying the panic value
// and the innermost frames of its stack, so one crashing case is reported
// as a failure instead of aborting the whole run.
func Guard(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v [%s]", r, panicSite(debug.Stack()))
		}
	}()
	return fn()
}

// repoRoot is the source tree root, trimmed from reported stack frames.
var repoRoot = func() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.ToSlash(filepath.Dir(filepath.Dir(filepath.Dir(file)))) + "/"
}()

// panicSite returns the first few N42 source locations below the panic in a
// debug.Stack dump, joined on one line for the TSV report.
func panicSite(stack []byte) string {
	lines := strings.Split(string(stack), "\n")
	var sites []string
	afterPanic := false
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "panic(") {
			afterPanic = true
			continue
		}
		if !afterPanic || !strings.Contains(l, ".go:") || strings.Contains(l, "/src/runtime/") {
			continue
		}
		l = strings.TrimPrefix(l, repoRoot)
		if j := strings.LastIndex(l, " +0x"); j >= 0 {
			l = l[:j]
		}
		sites = append(sites, l)
		if len(sites) == 4 {
			break
		}
	}
	return strings.Join(sites, " <- ")
}
