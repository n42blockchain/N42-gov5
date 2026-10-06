// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// N42_QMDB_SINGLE_FOLD: the leader's write path uses QMDBRootComputer's
// ComputeRootShared (which still applies and folds today, v1 -- see its own
// doc comment) instead of plain ComputeRoot, logging an explicit
// matched/mismatched count instead of only comparing roots inline
// (docs/QS_BLOCK_TIME_BUDGET.md 6f9). Unset (default) = today, byte-for-byte.

package internal

import (
	"os"
	"sync"
)

var (
	qmdbSingleFoldOnce sync.Once
	qmdbSingleFoldOn   bool
)

// QMDBSingleFoldEnabled reports whether the leader's write path should use
// ComputeRootShared's own explicit equivalence guard instead of the plain
// ComputeRoot + inline comparison.
func QMDBSingleFoldEnabled() bool {
	qmdbSingleFoldOnce.Do(func() {
		qmdbSingleFoldOn = parseQMDBSingleFold(os.Getenv("N42_QMDB_SINGLE_FOLD"))
	})
	return qmdbSingleFoldOn
}

func parseQMDBSingleFold(v string) bool {
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	default:
		return false
	}
}
