// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// report.go — optional per-case TSV report for the EEST runners.

package eest

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// Reporter appends one TSV line per executed case to the file named by
// EnvReport. A nil Reporter (env unset) discards everything.
type Reporter struct {
	mu sync.Mutex
	f  *os.File
}

// OpenReporter opens the EnvReport file for appending, or returns nil.
func OpenReporter() (*Reporter, error) {
	path := os.Getenv(EnvReport)
	if path == "" {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Reporter{f: f}, nil
}

// Record writes one case: suite, network, fixture file, test id, status
// ("pass", "fail" or "skip") and the error text (empty on pass).
func (r *Reporter) Record(suite, network, file, test, status, msg string) {
	if r == nil {
		return
	}
	clean := func(s string) string {
		return strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(s)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.f, "%s\t%s\t%s\t%s\t%s\t%s\n", suite, network, file, clean(test), status, clean(msg))
}

// Close flushes and closes the report file.
func (r *Reporter) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}
