package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// decodeRethBytecode extracts the raw deployed EVM bytecode from reth's
// stored Bytecode Compact value, verified against the codehash key.
//
// Layout: [u32 BE = stored bytecode length L][stored bytecode v[4:4+L]]
// [trailer: original_len + jumptable]. For LegacyRaw/Eip7702/Eof the stored
// bytecode IS the raw code (keccak matches directly). For LegacyAnalyzed the
// stored bytecode is PADDED (original || up-to-33 zero bytes for jumpdest
// analysis); the raw code is a prefix stored[:L-p]. revm's padding amount p
// is code-dependent (observed 1..33), so we find it by matching keccak
// against the codehash key — robust regardless of the exact Compact trailer
// encoding (which is the unvendored reth-codecs 0.4.0 struct codec).
func decodeRethBytecode(codeHash, v []byte) []byte {
	if len(v) < 4 {
		return nil
	}
	L := int(binary.BigEndian.Uint32(v[0:4]))
	if 4+L > len(v) {
		return nil
	}
	stored := v[4 : 4+L]
	var kh types.Hash
	copy(kh[:], codeHash)
	// p=0 (raw) and p=33 (most common analyzed padding) first, then scan.
	if crypto.Keccak256Hash(stored) == kh {
		return stored
	}
	for _, p := range []int{33, 24, 1} {
		if p <= L && crypto.Keccak256Hash(stored[:L-p]) == kh {
			return stored[:L-p]
		}
	}
	for p := 2; p <= 64 && p <= L; p++ {
		if crypto.Keccak256Hash(stored[:L-p]) == kh {
			return stored[:L-p]
		}
	}
	return nil
}

// say prints a monitorable line to stderr (the N42 logger is silent without
// a configured handler; fmt to stderr is guaranteed visible for long runs).
func say(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}

// progress logs every 15s.
type prog struct {
	t0, last time.Time
	logger   log.Logger
	name     string
}

func newProg(name string, logger log.Logger) *prog {
	now := time.Now()
	return &prog{t0: now, last: now, logger: logger, name: name}
}
func (p *prog) tick(n uint64) {
	if time.Since(p.last) < 15*time.Second {
		return
	}
	p.last = time.Now()
	rate := float64(n) / time.Since(p.t0).Seconds()
	say("  %s progress: done=%d rate/s=%d elapsed=%s", p.name, n, int(rate),
		time.Since(p.t0).Truncate(time.Second))
}

// migrateHashedAccounts: key copy, value reth-Compact → N42 MarshalV2.
func migrateHashedAccounts(reth kv.RoDB, dst kv.RwDB, commitEvery, limit uint64, logger log.Logger) error {
	say("PHASE acc: HashedAccounts (reth Compact -> MarshalV2)")
	p := newProg("acc", logger)
	rtx, err := reth.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer rtx.Rollback()
	rc, err := rtx.Cursor(rethHashedAccounts)
	if err != nil {
		return err
	}
	defer rc.Close()

	wtx, err := dst.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer func() {
		if wtx != nil {
			wtx.Rollback()
		}
	}()

	var written, decodeFail, skipEmpty uint64
	for k, v, err := rc.First(); k != nil; k, v, err = rc.Next() {
		if err != nil {
			return fmt.Errorf("reth iter: %w", err)
		}
		if len(k) != 32 {
			continue
		}
		nonce, bal, codeHash, ok := decodeRethAccount(v)
		if !ok {
			decodeFail++
			continue
		}
		if codeHash == (types.Hash{}) {
			codeHash = emptyCodeHash
		}
		if nonce == 0 && bal.IsZero() && codeHash == emptyCodeHash {
			skipEmpty++
			continue
		}
		acc := account.StateAccount{Nonce: nonce, Balance: bal, CodeHash: codeHash}
		if err := wtx.Put(n42HashedAccounts, k, acc.MarshalV2()); err != nil {
			return fmt.Errorf("put: %w", err)
		}
		written++
		if written%commitEvery == 0 {
			if err := wtx.Commit(); err != nil {
				return err
			}
			if wtx, err = dst.BeginRw(context.Background()); err != nil {
				return err
			}
		}
		p.tick(written)
		if limit > 0 && written >= limit {
			break
		}
	}
	if err := wtx.Commit(); err != nil {
		return err
	}
	wtx = nil
	say("PHASE acc done: written=%d decodeFail=%d skipEmpty=%d", written, decodeFail, skipEmpty)
	return nil
}

// migrateHashedStorages: DupSort byte-copy. reth dup-value = 32B slotHash +
// trimmed U256; N42 (post-incarnation) identical. We reconstruct the 64B
// logical key (addrHash + slotHash) and Put via AutoConv, which splits it
// back into the same dup-key/dup-value.
func migrateHashedStorages(reth kv.RoDB, dst kv.RwDB, commitEvery, limit uint64, logger log.Logger) error {
	say("PHASE sto: HashedStorages (DupSort byte-copy)")
	p := newProg("sto", logger)
	rtx, err := reth.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer rtx.Rollback()
	rc, err := rtx.CursorDupSort(rethHashedStorages)
	if err != nil {
		return err
	}
	defer rc.Close()

	wtx, err := dst.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer func() {
		if wtx != nil {
			wtx.Rollback()
		}
	}()

	composite := make([]byte, 64)
	var written, shortVal uint64
	for k, v, err := rc.First(); k != nil; k, v, err = rc.Next() {
		if err != nil {
			return fmt.Errorf("reth iter: %w", err)
		}
		if len(k) != 32 { // dup-key = 32B addrHash
			continue
		}
		if len(v) < 32 { // dup-value = 32B slotHash + value
			shortVal++
			continue
		}
		slotHash := v[:32]
		val := v[32:]
		if len(val) == 0 {
			continue
		}
		copy(composite[:32], k)
		copy(composite[32:], slotHash)
		if err := wtx.Put(n42HashedStorage, composite, val); err != nil {
			return fmt.Errorf("put: %w", err)
		}
		written++
		if written%commitEvery == 0 {
			if err := wtx.Commit(); err != nil {
				return err
			}
			if wtx, err = dst.BeginRw(context.Background()); err != nil {
				return err
			}
		}
		p.tick(written)
		if limit > 0 && written >= limit {
			break
		}
	}
	if err := wtx.Commit(); err != nil {
		return err
	}
	wtx = nil
	say("PHASE sto done: written=%d shortVal=%d", written, shortVal)
	return nil
}

// migrateAccountsTrie: full byte-copy. reth StoredNibbles key (1 nibble/byte,
// no length) == N42 TrieOfAccounts key; BranchNodeCompact value identical.
func migrateAccountsTrie(reth kv.RoDB, dst kv.RwDB, commitEvery, limit uint64, logger log.Logger) error {
	say("PHASE tacc: AccountsTrie -> TrieOfAccounts (byte-copy)")
	p := newProg("tacc", logger)
	rtx, err := reth.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer rtx.Rollback()
	rc, err := rtx.Cursor(rethAccountsTrie)
	if err != nil {
		return err
	}
	defer rc.Close()

	wtx, err := dst.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer func() {
		if wtx != nil {
			wtx.Rollback()
		}
	}()

	var written uint64
	for k, v, err := rc.First(); k != nil; k, v, err = rc.Next() {
		if err != nil {
			return fmt.Errorf("reth iter: %w", err)
		}
		kk := append([]byte(nil), k...)
		vv := append([]byte(nil), v...)
		if err := wtx.Put(n42TrieAccounts, kk, vv); err != nil {
			return fmt.Errorf("put: %w", err)
		}
		written++
		if written%commitEvery == 0 {
			if err := wtx.Commit(); err != nil {
				return err
			}
			if wtx, err = dst.BeginRw(context.Background()); err != nil {
				return err
			}
		}
		p.tick(written)
		if limit > 0 && written >= limit {
			break
		}
	}
	if err := wtx.Commit(); err != nil {
		return err
	}
	wtx = nil
	say("PHASE tacc done: written=%d", written)
	return nil
}

// migrateStoragesTrie: reth DupSort key=32B addrHash, dup-value = 65B
// StoredNibblesSubKey (64 padded + 1 len byte) + BranchNodeCompact. N42 key =
// 32B addrHash + nibble-path (path = subkey[:subkey[64]]), value = node bytes.
func migrateStoragesTrie(reth kv.RoDB, dst kv.RwDB, commitEvery, limit uint64, logger log.Logger) error {
	say("PHASE tsto: StoragesTrie -> TrieOfStorage (extract nibble path)")
	p := newProg("tsto", logger)
	rtx, err := reth.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer rtx.Rollback()
	rc, err := rtx.CursorDupSort(rethStoragesTrie)
	if err != nil {
		return err
	}
	defer rc.Close()

	wtx, err := dst.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer func() {
		if wtx != nil {
			wtx.Rollback()
		}
	}()

	var written, badSub uint64
	keyBuf := make([]byte, 0, 32+64)
	for k, v, err := rc.First(); k != nil; k, v, err = rc.Next() {
		if err != nil {
			return fmt.Errorf("reth iter: %w", err)
		}
		if len(k) != 32 {
			continue
		}
		if len(v) < 65 { // 65B subkey + node
			badSub++
			continue
		}
		sub := v[:65]
		node := v[65:]
		pathLen := int(sub[64])
		if pathLen > 64 {
			badSub++
			continue
		}
		path := sub[:pathLen] // nibble path, 1 nibble/byte
		keyBuf = append(keyBuf[:0], k...)
		keyBuf = append(keyBuf, path...)
		kk := append([]byte(nil), keyBuf...)
		vv := append([]byte(nil), node...)
		if err := wtx.Put(n42TrieStorage, kk, vv); err != nil {
			return fmt.Errorf("put: %w", err)
		}
		written++
		if written%commitEvery == 0 {
			if err := wtx.Commit(); err != nil {
				return err
			}
			if wtx, err = dst.BeginRw(context.Background()); err != nil {
				return err
			}
		}
		p.tick(written)
		if limit > 0 && written >= limit {
			break
		}
	}
	if err := wtx.Commit(); err != nil {
		return err
	}
	wtx = nil
	say("PHASE tsto done: written=%d badSub=%d", written, badSub)
	return nil
}

// migrateBytecodes: key copy, strip reth 1B BytecodeType prefix (0=Raw).
func migrateBytecodes(reth kv.RoDB, dst kv.RwDB, commitEvery, limit uint64, logger log.Logger) error {
	say("PHASE code: Bytecodes -> Code (strip 1B type prefix)")
	p := newProg("code", logger)
	rtx, err := reth.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer rtx.Rollback()
	rc, err := rtx.Cursor(rethBytecodes)
	if err != nil {
		return err
	}
	defer rc.Close()

	wtx, err := dst.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer func() {
		if wtx != nil {
			wtx.Rollback()
		}
	}()

	// Clear stale Code first (the earlier 1-byte-strip migration was wrong).
	if err := wtx.ClearBucket(n42Code); err != nil {
		return fmt.Errorf("clear Code: %w", err)
	}

	var written, skipped, decodeFail uint64
	for k, v, err := rc.First(); k != nil; k, v, err = rc.Next() {
		if err != nil {
			return fmt.Errorf("reth iter: %w", err)
		}
		if len(k) != 32 {
			skipped++
			continue
		}
		raw := decodeRethBytecode(k, v)
		if raw == nil {
			decodeFail++
			continue
		}
		kk := append([]byte(nil), k...)
		code := append([]byte(nil), raw...)
		if err := wtx.Put(n42Code, kk, code); err != nil {
			return fmt.Errorf("put: %w", err)
		}
		written++
		if written%commitEvery == 0 {
			if err := wtx.Commit(); err != nil {
				return err
			}
			if wtx, err = dst.BeginRw(context.Background()); err != nil {
				return err
			}
		}
		p.tick(written)
		if limit > 0 && written >= limit {
			break
		}
	}
	if err := wtx.Commit(); err != nil {
		return err
	}
	wtx = nil
	say("PHASE code done: written=%d skipped=%d decodeFail=%d", written, skipped, decodeFail)
	return nil
}
