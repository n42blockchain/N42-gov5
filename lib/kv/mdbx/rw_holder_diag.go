// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Write-lock holder accounting (r58a, always on since r89a).
//
// MDBX has one writer. A write transaction that waits in BeginTxn waits for
// whoever holds it, and the waiter cannot see who that was. Every write
// transaction of an environment carries a tag -- its context's tag
// (kv.WithRwTag), else its caller (the first frame outside the kv layer)
// with N42_RW_HOLDER_DIAG=1, else "other" -- and the environment records:
//
//   - the current holder (tag and grant time), so a transaction that starts
//     to wait names the holder at that instant (RwWaitHolder);
//   - each release, stamped as soon as the native commit or abort returns
//     (before any logging), with how long the writer was held, so a waiter
//     can name every holder that released during its wait (RwBeginBehind);
//   - per tag, the transactions, the writer time held and the time their
//     own begin waited, logged once a second for the chain database
//     ("mdbx writer holders").
//
// The cost is a mutex and a few counters per write transaction (tens per
// second on a node); the caller lookup stays behind the diagnostics switch.

package mdbx

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
)

var rwHolderDiag = os.Getenv("N42_RW_HOLDER_DIAG") == "1"

// SetRwHolderDiagForTest switches the caller lookup of untagged write
// transactions and returns the restore.
func SetRwHolderDiagForTest(on bool) func() {
	prev := rwHolderDiag
	rwHolderDiag = on
	return func() { rwHolderDiag = prev }
}

const rwReleaseRing = 32

// rwHolderLogInterval is how often an environment's per-holder totals are
// logged (only for the chain database).
var rwHolderLogInterval = time.Second

type rwRelease struct {
	tag        string
	held       time.Duration
	releasedAt time.Time
}

type rwHolderStat struct {
	n    int64
	held time.Duration
	wait time.Duration
}

// rwHolderLog is an environment's write-lock accounting.
type rwHolderLog struct {
	mu   sync.Mutex
	ring [rwReleaseRing]rwRelease
	next int
	// cur is the transaction holding the writer ("" when none).
	curTag   string
	curBegan time.Time
	// total: per tag since open; win: per tag since winStart (logged).
	total    map[string]*rwHolderStat
	win      map[string]*rwHolderStat
	winStart time.Time
}

// acquired records that tag got the writer at began after waiting waited.
func (l *rwHolderLog) acquired(tag string, began time.Time) {
	l.mu.Lock()
	l.curTag, l.curBegan = tag, began
	l.mu.Unlock()
}

// holder names the current holder and how long it has held the writer
// ("" when none).
func (l *rwHolderLog) holder(now time.Time) string {
	l.mu.Lock()
	tag, began := l.curTag, l.curBegan
	l.mu.Unlock()
	if tag == "" {
		return ""
	}
	return tag + ":" + strconv.FormatInt(now.Sub(began).Milliseconds(), 10)
}

// release records tag's release of the writer it got at began after waiting
// waited. It returns the window to log when one is due (nil otherwise).
func (l *rwHolderLog) release(tag string, began time.Time, waited time.Duration, logDue bool) (map[string]*rwHolderStat, time.Duration) {
	now := time.Now()
	held := now.Sub(began)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ring[l.next%rwReleaseRing] = rwRelease{tag: tag, held: held, releasedAt: now}
	l.next++
	if l.curTag == tag && l.curBegan.Equal(began) {
		l.curTag, l.curBegan = "", time.Time{}
	}
	if l.total == nil {
		l.total = make(map[string]*rwHolderStat)
	}
	if l.win == nil {
		l.win = make(map[string]*rwHolderStat)
		l.winStart = began.Add(-waited)
	}
	for _, m := range []map[string]*rwHolderStat{l.total, l.win} {
		s := m[tag]
		if s == nil {
			s = &rwHolderStat{}
			m[tag] = s
		}
		s.n++
		s.held += held
		s.wait += waited
	}
	if !logDue || now.Sub(l.winStart) < rwHolderLogInterval {
		return nil, 0
	}
	out, span := l.win, now.Sub(l.winStart)
	l.win, l.winStart = make(map[string]*rwHolderStat, len(out)), now
	return out, span
}

// stats returns a copy of the per-tag totals since open (tests).
func (l *rwHolderLog) stats() map[string]rwHolderStat {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]rwHolderStat, len(l.total))
	for k, v := range l.total {
		out[k] = *v
	}
	return out
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
	if b.Len() == 0 && n > 0 {
		// Nobody released during the wait: name the last release before it
		// and how long before, so an unexplained wait is not left blank.
		r := l.ring[(n-1)%rwReleaseRing]
		b.WriteString("last=")
		b.WriteString(r.tag)
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(r.held.Milliseconds(), 10))
		b.WriteString("@-")
		b.WriteString(strconv.FormatInt(since.Sub(r.releasedAt).Milliseconds(), 10))
	}
	return b.String()
}

// formatRwHolders renders a window as "tag=n/heldMs/waitMs" by held time,
// and the share of the span the writer was held.
func formatRwHolders(win map[string]*rwHolderStat, span time.Duration) (string, float64) {
	type row struct {
		tag string
		s   *rwHolderStat
	}
	rows := make([]row, 0, len(win))
	var held time.Duration
	for k, v := range win {
		rows = append(rows, row{k, v})
		held += v.held
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].s.held != rows[j].s.held {
			return rows[i].s.held > rows[j].s.held
		}
		return rows[i].tag < rows[j].tag
	})
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(r.tag)
		b.WriteByte('=')
		b.WriteString(strconv.FormatInt(r.s.n, 10))
		b.WriteByte('/')
		b.WriteString(strconv.FormatFloat(float64(r.s.held.Microseconds())/1000, 'f', 1, 64))
		b.WriteByte('/')
		b.WriteString(strconv.FormatFloat(float64(r.s.wait.Microseconds())/1000, 'f', 1, 64))
	}
	share := 0.0
	if span > 0 {
		share = float64(held) / float64(span)
	}
	return b.String(), share
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
// waiter got the writer: the holder records it once its native commit
// returns.
const rwReleaseSlack = 2 * time.Millisecond

// releaseWriter records this write transaction's release of the writer,
// once, right after its native commit or abort returned.
func (tx *MdbxTx) releaseWriter() {
	if tx.readOnly || tx.rwReleased {
		return
	}
	tx.rwReleased = true
	logDue := tx.db.opts.label == kv.ChainDB
	win, span := tx.db.rwHolders.release(tx.rwTag, tx.beganAt, tx.waitedFor, logDue)
	if win == nil {
		return
	}
	holders, share := formatRwHolders(win, span)
	writeProbeLogger.RLock()
	logger := writeProbeLogger.log
	writeProbeLogger.RUnlock()
	logger("mdbx writer holders",
		"label", tx.db.opts.label, "env", filepath.Base(tx.db.opts.path),
		"spanMs", span.Milliseconds(), "heldShare", strconv.FormatFloat(share, 'f', 3, 64),
		"holders", holders, "tMs", time.Now().UnixMilli())
}

// RwBeginBehind reports, for a write transaction, how long BeginTxn waited
// and the write transactions that released the writer during that wait
// ("" when it did not wait). Call it early in the transaction: the release
// log keeps the last 32 releases.
func (tx *MdbxTx) RwBeginBehind() (time.Duration, string) {
	if tx.rwTag == "" || tx.waitedFor <= 0 {
		return tx.waitedFor, ""
	}
	wait := tx.beganAt.Add(-tx.waitedFor)
	return tx.waitedFor, tx.db.rwHolders.behind(wait, tx.beganAt.Add(rwReleaseSlack))
}

// RwWaitHolder names the transaction that held the writer when this one
// began to wait for it, as "tag:heldMs" (how long it had held it then;
// "" when the writer was free).
func (tx *MdbxTx) RwWaitHolder() string { return tx.waitHolder }

// RwHolderStatsForTest returns the environment's per-tag totals: write
// transactions, writer time held, and begin wait.
func (db *MdbxKV) RwHolderStatsForTest() map[string][3]int64 {
	out := map[string][3]int64{}
	for k, v := range db.rwHolders.stats() {
		out[k] = [3]int64{v.n, int64(v.held), int64(v.wait)}
	}
	return out
}
