// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Reading account and storage state out of the QMDB tree instead of the
// address-keyed `Account` and `Storage` tables.
//
// Round 19's write probe, per write transaction carrying a full 22,857-tx block:
//
//	dirtyBytes 71.0 MB   payloadBytes 3.9 MB   amplification 18.2x
//	Account  16,096 rows/block, keyed by address, uniformly distributed
//	  -> ~64.4 MB of the 71.0 MB, one 4 KB page per row
//
// `Account` is 90% of the bytes MDBX dirties from 10% of the payload, and it is
// a DUPLICATE: qmdbEntries carries 32,204 rows to Account's 16,096 -- a new
// entry plus a deactivation per changed key -- so both record the same 16,096
// account updates, one append-only and one random-keyed.
//
// The values are byte-identical. PlainStateWriter writes account.MarshalV2()
// into modules.Account; EncodeAccountValue is `return a.MarshalV2()`. So a read
// through qmdb.Tree.Get(AccountKeyHash(addr)) returns the same bytes the plain
// table would, and account.DecodeForStorage auto-detects the encoding.
//
// This reader exists to be PROVED equivalent before anything stops writing the
// plain tables. N42_STATE_READ_QMDB=verify reads both and reports divergence
// while still answering from the plain reader; =1 answers from QMDB. Default
// off. Round 17 is the standing reminder of what a change built on an unmeasured
// premise costs.

package commitment

import (
	"bytes"
	"os"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/log"
)

// QMDBReadMode selects where account and storage point reads are answered from.
type QMDBReadMode uint8

const (
	// QMDBReadOff keeps every read on the plain tables (the default).
	QMDBReadOff QMDBReadMode = iota
	// QMDBReadVerify answers from the plain tables but also reads QMDB and
	// reports any divergence. This is the mode a round runs before the plain
	// write is allowed to stop.
	QMDBReadVerify
	// QMDBReadOn answers from QMDB.
	QMDBReadOn
)

var (
	qmdbReadModeOnce sync.Once
	qmdbReadMode     QMDBReadMode
)

// QMDBStateReadMode reports the configured mode. N42_STATE_READ_QMDB is an
// environment variable rather than a config field for the same reason
// N42_MDBX_SYNC is: it changes where consensus-critical state comes from and
// should have to be typed out.
func QMDBStateReadMode() QMDBReadMode {
	qmdbReadModeOnce.Do(func() {
		qmdbReadMode = parseQMDBReadMode(os.Getenv("N42_STATE_READ_QMDB"))
	})
	return qmdbReadMode
}

func parseQMDBReadMode(v string) QMDBReadMode {
	switch v {
	case "verify", "VERIFY":
		return QMDBReadVerify
	case "1", "true", "TRUE", "yes", "on":
		return QMDBReadOn
	default:
		return QMDBReadOff
	}
}

// Round-level verify totals. The reader is constructed per block, so its own
// counters reset every block and cannot answer "did this round diverge at all".
// These accumulate for the life of the process and are reported on a cadence,
// which is what an equivalence round reads.
var (
	verifyAccountMismatch atomic.Uint64
	verifyStorageMismatch atomic.Uint64
	verifyCompared        atomic.Uint64
	verifyNextReport      atomic.Uint64
)

// verifyReportEvery is the compare count between summary lines. A full block
// compares on the order of 10^5 reads, so this is a few lines per block rather
// than one per read.
const verifyReportEvery = 500_000

// QMDBVerifyTotals reports the process-wide verify counters.
func QMDBVerifyTotals() (accounts, storage, compared uint64) {
	return verifyAccountMismatch.Load(), verifyStorageMismatch.Load(), verifyCompared.Load()
}

func noteCompared() {
	n := verifyCompared.Add(1)
	if next := verifyNextReport.Load(); n >= next {
		if verifyNextReport.CompareAndSwap(next, n+verifyReportEvery) {
			a, st, c := QMDBVerifyTotals()
			log.Info("qmdb verify", "compared", c, "accountMismatch", a, "storageMismatch", st)
		}
	}
}

// qmdbValueSource is the narrow slice of *qmdb.Tree this reader needs. Declared
// here so the reader can be tested against a map.
type qmdbValueSource interface {
	Get(keyHash qmdb.Hash) ([]byte, bool)
}

// plainReader is the reader QMDB does not replace: code lives in its own table,
// and ForEachStorage cannot be served by a hashed key space at all (see the
// StorageEnumerator note in modules/state/interfaces.go -- a reader that cannot
// enumerate simply does not implement it, and the caller falls back to the
// touched-slot set, which is NOT equivalent for a SELFDESTRUCT). Delegating
// keeps that semantics intact instead of silently weakening it.
type plainReader interface {
	ReadAccountData(address types.Address) (*account.StateAccount, error)
	ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error)
	ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error)
	ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error)
}

// QMDBStateReader serves account and storage POINT reads from the QMDB tree and
// delegates everything else to the plain reader it wraps.
type QMDBStateReader struct {
	src   qmdbValueSource
	inner plainReader
	mode  QMDBReadMode

	accountMismatch atomic.Uint64
	storageMismatch atomic.Uint64
	compared        atomic.Uint64
}

// NewQMDBStateReader wraps inner. A nil source or QMDBReadOff mode makes every
// call a straight delegation, so the constructor is safe to use unconditionally.
func NewQMDBStateReader(src qmdbValueSource, inner plainReader, mode QMDBReadMode) *QMDBStateReader {
	if src == nil {
		mode = QMDBReadOff
	}
	return &QMDBStateReader{src: src, inner: inner, mode: mode}
}

// Mismatches reports how many divergences verify mode has seen, and how many
// reads it compared.
func (r *QMDBStateReader) Mismatches() (accounts, storage, compared uint64) {
	return r.accountMismatch.Load(), r.storageMismatch.Load(), r.compared.Load()
}

// emptyByPlainPolicy mirrors modules/state.stateObject.empty(), the predicate
// the plain write path applies before EIP-161 removes an account from the
// `Account` table.
//
// The reader has to apply it because the table's CONTENTS are defined by that
// policy, and this reader's contract is to return what the table holds. It is
// not a workaround for the commitment path's unreachable isAccountEmpty (see
// docs/QS_BLOCK_TIME_BUDGET.md section 6g): that defect is real, lives in the
// state root, and is unaffected either way.
//
// What licenses this is measured, not argued. Round 20 compared 1,000,001 reads
// a node across three legs and seven nodes: 26,072 divergences, EVERY one of
// them plainNil=true / qmdbNil=false, and ZERO in the other direction. So QMDB
// is a strict superset of `Account`, the values agree byte for byte on the
// intersection, and the extra entries are exactly the ones EIP-161 deletes.
// Filtering them here reproduces the table exactly.
//
// A chain before SpuriousDragon does not remove empty accounts, so a reader for
// one would have to skip this. The QMDB commitment is the native chain's, which
// is well past it.
func emptyByPlainPolicy(a *account.StateAccount) bool {
	return a.Nonce == 0 && a.Balance.IsZero() && a.CodeHash == emptyCodeHash
}

func (r *QMDBStateReader) qmdbAccount(address types.Address) (*account.StateAccount, bool, error) {
	enc, ok := r.src.Get(qmdb.Hash(AccountKeyHash(address)))
	if !ok || len(enc) == 0 {
		return nil, ok, nil
	}
	var a account.StateAccount
	if err := a.DecodeForStorage(enc); err != nil {
		return nil, true, err
	}
	if emptyByPlainPolicy(&a) {
		// EIP-161 removed it from `Account`; QMDB retained it. Report what the
		// table would.
		return nil, true, nil
	}
	return &a, true, nil
}

func (r *QMDBStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	if r.mode == QMDBReadOff {
		return r.inner.ReadAccountData(address)
	}
	got, _, err := r.qmdbAccount(address)
	if r.mode == QMDBReadOn {
		return got, err
	}
	// verify: the plain reader still answers, so a divergence cannot change a
	// block's outcome while it is being measured.
	want, werr := r.inner.ReadAccountData(address)
	r.compared.Add(1)
	noteCompared()
	if err == nil && werr == nil && !sameAccount(got, want) {
		verifyAccountMismatch.Add(1)
		if n := r.accountMismatch.Add(1); n <= 8 {
			log.Warn("qmdb state read: account diverges from the plain table",
				"address", address, "qmdbNil", got == nil, "plainNil", want == nil)
		}
	}
	return want, werr
}

func (r *QMDBStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	if r.mode == QMDBReadOff {
		return r.inner.ReadAccountStorage(address, key)
	}
	var got []byte
	if enc, ok := r.src.Get(qmdb.Hash(StorageKeyHash(address, *key))); ok {
		// QMDB stores 32 bytes zero-padded; the plain Storage table stores the
		// value trimmed to ByteLen. Match the plain representation, or every
		// non-full-width slot reads as a divergence that is not one.
		got = trimLeadingZeros(enc)
	}
	if r.mode == QMDBReadOn {
		return got, nil
	}
	want, werr := r.inner.ReadAccountStorage(address, key)
	r.compared.Add(1)
	noteCompared()
	if werr == nil && !bytes.Equal(got, want) {
		verifyStorageMismatch.Add(1)
		if n := r.storageMismatch.Add(1); n <= 8 {
			log.Warn("qmdb state read: storage slot diverges from the plain table",
				"address", address, "slot", key, "qmdbLen", len(got), "plainLen", len(want))
		}
	}
	return want, werr
}

// Code is not in QMDB; these are straight delegations in every mode.
func (r *QMDBStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	return r.inner.ReadAccountCode(address, codeHash)
}

func (r *QMDBStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	return r.inner.ReadAccountCodeSize(address, codeHash)
}

func trimLeadingZeros(b []byte) []byte {
	i := 0
	for i < len(b) && b[i] == 0 {
		i++
	}
	if i == len(b) {
		return nil
	}
	return b[i:]
}

func sameAccount(a, b *account.StateAccount) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Nonce == b.Nonce &&
		a.Balance.Eq(&b.Balance) &&
		a.Root == b.Root &&
		a.CodeHash == b.CodeHash
}
