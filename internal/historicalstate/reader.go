// Package historicalstate implements the reth-style "state-as-of-block"
// query path on top of N42's MPHF+fp compressed history coldstore.
//
// Design choice (reth parity): N42 archive nodes persist only the latest
// trie (~37-50 GB). Historical proof and historical state are served on
// demand by walking the history coldstore. This package is the read API.
//
// Backing layout (built by cmd/n42-history-build, verified by
// cmd/n42-history-verify):
//
//	<dir>/account.{mphf,idx,kv}    key = addr20            blob = packed history
//	<dir>/storage.{mphf,idx,kv}    key = addr20 || slot32  blob = packed history
//
// "Packed history" = varint count, then per-entry [deltaBlock][valueLen][value].
// Value semantics: at the START of `block`, the leaf had this value
// (matches AccountChangeSets / StorageChangeSets old-value convention).
//
// Memory cost is minimal: idx is read once at Open (kilobytes per GB of
// kv), kv stays on disk and is read per-query via os.File.ReadAt. Both
// readers are safe for concurrent Get.
package historicalstate

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/internal/history"
)

// Reader opens an account+storage history coldstore directory and
// answers (addr, block) and (addr, slot, block) as-of queries.
type Reader struct {
	account *history.MPHFReader
	storage *history.MPHFReader
}

// Open mounts both account.* and storage.* MPHF files under dir. Either
// can be missing — the corresponding *AsOf will return ErrDomainAbsent.
func Open(dir string) (*Reader, error) {
	r := &Reader{}
	if a, err := history.OpenMPHF(dir, "account"); err == nil {
		r.account = a
	} else if !isMissingFile(err) {
		return nil, fmt.Errorf("historicalstate: open account: %w", err)
	}
	if s, err := history.OpenMPHF(dir, "storage"); err == nil {
		r.storage = s
	} else if !isMissingFile(err) {
		if r.account != nil {
			r.account.Close()
		}
		return nil, fmt.Errorf("historicalstate: open storage: %w", err)
	}
	if r.account == nil && r.storage == nil {
		return nil, fmt.Errorf("historicalstate: neither account nor storage history found in %s", dir)
	}
	return r, nil
}

func (r *Reader) Close() error {
	var firstErr error
	if r.account != nil {
		if err := r.account.Close(); err != nil {
			firstErr = err
		}
	}
	if r.storage != nil {
		if err := r.storage.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ErrDomainAbsent is returned when the queried domain (account or
// storage) was not present in the coldstore directory.
var ErrDomainAbsent = errors.New("historicalstate: domain not mounted")

// AccountAsOf returns the OldValue recorded at the most recent block ≤
// `block` that modified this account. found=false means the account
// was never modified at or before `block`.
//
// IMPORTANT — reth-style query semantics:
//
//   - The returned bytes are the changeset OldValue convention: the
//     account state JUST BEFORE that modification block ran.
//   - For exact-block queries (block = modification block), this IS the
//     state at the start of that block.
//   - For interior queries (block > last modification but < next), this
//     is NOT the state at `block`. To get state-at-block, combine with
//     the latest snapshot via StateAt() (the reverse-apply path).
//   - Contracts whose nonce/balance/code never change (typical ERC-20)
//     have exactly ONE entry (deployment). All later queries return
//     the deployment-time OldValue, which is empty (didn't exist).
//
// Use this for: building reverse-apply pipelines, fraud-proof witness
// generation, audit/explorer "show last modification" views.
// Don't use this for: "what's the balance at block N". Use StateAt.
func (r *Reader) AccountAsOf(addr [20]byte, block uint64) ([]byte, bool, error) {
	if r.account == nil {
		return nil, false, ErrDomainAbsent
	}
	blob, ok, err := r.account.Get(addr[:])
	if err != nil {
		return nil, false, fmt.Errorf("historicalstate: account get %x: %w", addr, err)
	}
	if !ok {
		// Address was never touched in any indexed block. Treat as
		// "does not exist before any block we have history for". The
		// caller's snapshot is the source of truth for any later block.
		return nil, false, nil
	}
	val, found, err := history.AsOf(blob, block)
	if err != nil {
		return nil, false, fmt.Errorf("historicalstate: account asof %x@%d: %w", addr, block, err)
	}
	return val, found, nil
}

// StorageAsOf returns the encoded storage value at the START of `block`.
// Same semantics as AccountAsOf — found=false means the slot was never
// modified at or before `block`.
func (r *Reader) StorageAsOf(addr [20]byte, slot [32]byte, block uint64) ([]byte, bool, error) {
	if r.storage == nil {
		return nil, false, ErrDomainAbsent
	}
	var key [52]byte
	copy(key[:20], addr[:])
	copy(key[20:], slot[:])
	blob, ok, err := r.storage.Get(key[:])
	if err != nil {
		return nil, false, fmt.Errorf("historicalstate: storage get %x/%x: %w", addr, slot, err)
	}
	if !ok {
		return nil, false, nil
	}
	val, found, err := history.AsOf(blob, block)
	if err != nil {
		return nil, false, fmt.Errorf("historicalstate: storage asof %x/%x@%d: %w", addr, slot, block, err)
	}
	return val, found, nil
}

// AccountHistory returns the full packed history blob for addr (nil if
// absent). Useful for explorer-style "show me every change to this
// account" queries; cheaper than repeated AsOf calls when you want many
// time points for the same key.
func (r *Reader) AccountHistory(addr [20]byte) ([]history.Change, bool, error) {
	if r.account == nil {
		return nil, false, ErrDomainAbsent
	}
	blob, ok, err := r.account.Get(addr[:])
	if err != nil || !ok {
		return nil, ok, err
	}
	changes, err := history.UnpackHistory(blob)
	if err != nil {
		return nil, false, err
	}
	return changes, true, nil
}

// StorageHistory is the slot-history equivalent of AccountHistory.
func (r *Reader) StorageHistory(addr [20]byte, slot [32]byte) ([]history.Change, bool, error) {
	if r.storage == nil {
		return nil, false, ErrDomainAbsent
	}
	var key [52]byte
	copy(key[:20], addr[:])
	copy(key[20:], slot[:])
	blob, ok, err := r.storage.Get(key[:])
	if err != nil || !ok {
		return nil, ok, err
	}
	changes, err := history.UnpackHistory(blob)
	if err != nil {
		return nil, false, err
	}
	return changes, true, nil
}

// Stats exposes per-domain key counts and page counts for diagnostics.
type Stats struct {
	AccountKeys      uint64
	AccountPageCount int
	StorageKeys      uint64
	StoragePageCount int
}

func (r *Reader) Stats() Stats {
	var s Stats
	if r.account != nil {
		s.AccountKeys = r.account.KeyCount()
		s.AccountPageCount = r.account.PageCount()
	}
	if r.storage != nil {
		s.StorageKeys = r.storage.KeyCount()
		s.StoragePageCount = r.storage.PageCount()
	}
	return s
}

func isMissingFile(err error) bool {
	// MPHFReader returns wrapped "no such file" — treat any open error
	// that mentions missing file as "domain absent" so partial dirs work.
	return err != nil && (errors.Is(err, errFileNotExist) || isENOENT(err))
}
