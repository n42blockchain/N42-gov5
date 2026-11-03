// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Write-lock holder diagnostics (r58a).
//
// MDBX has one writer. A write transaction that waits in BeginTxn waits for
// whoever holds it, and the waiter cannot see who that was. With
// N42_RW_HOLDER_DIAG=1 every write transaction of an environment is tagged
// with its caller (the first frame outside the kv layer) when it begins,
// and its release is recorded with how long it held the writer. A
// transaction that waited can then name the transactions that held the
// writer during its wait (RwBeginBehind), e.g. for the block write's
// "beginBehind" field. Off by default: nothing is recorded or allocated.

package mdbx

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var rwHolderDiag = os.Getenv("N42_RW_HOLDER_DIAG") == "1"

// SetRwHolderDiagForTest switches the holder diagnostics and returns the
// restore.
func SetRwHolderDiagForTest(on bool) func() {
	prev := rwHolderDiag
	rwHolderDiag = on
	return func() { rwHolderDiag = prev }
}

const rwReleaseRing = 32

type rwRelease struct {
	tag        string
	held       time.Duration
	releasedAt time.Time
}

// rwHolderLog is an environment's recent write-lock releases.
type rwHolderLog struct {
	mu   sync.Mutex
	ring [rwReleaseRing]rwRelease
	next int
}

func (l *rwHolderLog) release(tag string, began time.Time) {
	now := time.Now()
	l.mu.Lock()
	l.ring[l.next%rwReleaseRing] = rwRelease{tag: tag, held: now.Sub(began), releasedAt: now}
	l.next++
	l.mu.Unlock()
}

// behind lists the releases in [since, until], oldest first, as
// "tag:heldMs" joined by "+" ("" when none).
func (l *rwHolderLog) behind(since, until time.Time) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	n := l.next
	start := n - rwReleaseRing
	if start < 0 {
		start = 0
	}
	for i := start; i < n; i++ {
		r := l.ring[i%rwReleaseRing]
		if r.releasedAt.Before(since) || r.releasedAt.After(until) {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('+')
		}
		b.WriteString(r.tag)
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(r.held.Milliseconds(), 10))
	}
	return b.String()
}

// rwCallerTag names a write transaction by the first caller outside the kv
// packages: "package.(*Type).Method".
func rwCallerTag() string {
	var pcs [24]uintptr
	n := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		fn := f.Function
		inKV := strings.Contains(f.File, "/lib/kv/") && !strings.HasSuffix(f.File, "_test.go")
		if fn != "" && !inKV {
			if i := strings.LastIndexByte(fn, '/'); i >= 0 {
				fn = fn[i+1:]
			}
			return fn
		}
		if !more {
			return "?"
		}
	}
}

// rwReleaseSlack covers a holder's release being recorded just after the
// waiter got the writer: the holder records it once its commit returns.
const rwReleaseSlack = 2 * time.Millisecond

// RwBeginBehind reports, for a write transaction begun with the holder
// diagnostics on, how long BeginTxn waited and the write transactions
// that released the writer during that wait ("" when off or none). Call it
// early in the transaction: the release log keeps the last 32 releases.
func (tx *MdbxTx) RwBeginBehind() (time.Duration, string) {
	if tx.rwTag == "" || tx.waitedFor <= 0 {
		return tx.waitedFor, ""
	}
	wait := tx.beganAt.Add(-tx.waitedFor)
	return tx.waitedFor, tx.db.rwHolders.behind(wait, tx.beganAt.Add(rwReleaseSlack))
}
