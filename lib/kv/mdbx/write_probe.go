// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Per-commit write attribution.
//
// The OS tells you a node writes ~0.77 MB per block; table statistics tell
// you almost none of that is growth, so nearly all of it is copy-on-write
// page rewriting. Neither says WHICH table caused it. This records, per write
// transaction, how many rows each table received and how many bytes MDBX
// actually dirtied (SpaceDirty), and logs the pair at commit.
//
// Off unless N42_WRITE_PROBE=1: the fast path is a single already-loaded bool,
// and nothing is allocated for a transaction while it is off.

package mdbx

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var writeProbeEnabled = func() bool {
	v, _ := strconv.ParseBool(os.Getenv("N42_WRITE_PROBE"))
	return v
}()

// WriteProbeEnabled reports whether per-commit write attribution is on.
func WriteProbeEnabled() bool { return writeProbeEnabled }

type tableWrites struct {
	mu   sync.Mutex
	rows map[string]*tableWriteStat
}

type tableWriteStat struct {
	puts, dels int64
	bytes      int64
}

func (t *tableWrites) note(table string, n int, isDelete bool) {
	t.mu.Lock()
	s := t.rows[table]
	if s == nil {
		s = &tableWriteStat{}
		t.rows[table] = s
	}
	if isDelete {
		s.dels++
	} else {
		s.puts++
		s.bytes += int64(n)
	}
	t.mu.Unlock()
}

// noteWrite records one row written to table. Cheap no-op while the probe is
// off, which is the only state production runs in.
func (tx *MdbxTx) noteWrite(table string, payload int, isDelete bool) {
	if !writeProbeEnabled || tx.readOnly {
		return
	}
	if tx.tableWrites == nil {
		tx.tableWrites = &tableWrites{rows: make(map[string]*tableWriteStat, 24)}
	}
	tx.tableWrites.note(table, payload, isDelete)
}

// logWriteProbe reports the transaction's attribution. Called just before the
// commit, while SpaceDirty still describes this transaction.
func (tx *MdbxTx) logWriteProbe() {
	if !writeProbeEnabled || tx.readOnly || tx.tableWrites == nil {
		return
	}
	dirty, limit, err := tx.SpaceDirty()
	if err != nil {
		return
	}
	tx.tableWrites.mu.Lock()
	type row struct {
		name       string
		puts, dels int64
		bytes      int64
	}
	rows := make([]row, 0, len(tx.tableWrites.rows))
	var totalRows, totalBytes int64
	for name, s := range tx.tableWrites.rows {
		rows = append(rows, row{name, s.puts, s.dels, s.bytes})
		totalRows += s.puts + s.dels
		totalBytes += s.bytes
	}
	tx.tableWrites.mu.Unlock()
	if totalRows == 0 {
		return
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].puts+rows[i].dels != rows[j].puts+rows[j].dels {
			return rows[i].puts+rows[i].dels > rows[j].puts+rows[j].dels
		}
		return rows[i].name < rows[j].name
	})
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(r.name)
		b.WriteByte('=')
		b.WriteString(strconv.FormatInt(r.puts, 10))
		if r.dels > 0 {
			b.WriteByte('-')
			b.WriteString(strconv.FormatInt(r.dels, 10))
		}
		b.WriteByte('/')
		b.WriteString(strconv.FormatInt(r.bytes, 10))
	}
	// dirty is what MDBX will actually write; payload is what the caller
	// handed over. Their ratio IS the write amplification, per commit.
	tx.db.log.Info("write probe",
		"label", tx.db.opts.label,
		"dirtyBytes", dirty,
		"dirtyLimit", limit,
		"rows", totalRows,
		"payloadBytes", totalBytes,
		"amplification", ratio(dirty, totalBytes),
		"tables", b.String())
}

func ratio(dirty uint64, payload int64) string {
	if payload <= 0 {
		return "n/a"
	}
	return strconv.FormatFloat(float64(dirty)/float64(payload), 'f', 1, 64)
}
