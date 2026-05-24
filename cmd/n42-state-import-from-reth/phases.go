package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// importAccounts streams reth PlainAccountState through to N42 Account,
// re-encoding from reth-compact to N42 StateAccount.MarshalV2. Commits
// every commitEvery entries so a crash mid-import doesn't lose all
// progress and MDBX dirty pages don't balloon.
func importAccounts(n42DB kv.RwDB, rethDB kv.RoDB, clear bool, commitEvery uint64, logger log.Logger) error {
	logger.Info("PHASE 1/4 — Account import starting")
	t0 := time.Now()

	if clear {
		if err := n42DB.Update(context.Background(), func(tx kv.RwTx) error {
			logger.Info("  clearing N42 Account table")
			return tx.ClearBucket(n42Acct)
		}); err != nil {
			return fmt.Errorf("clear Account: %w", err)
		}
	}

	rethTx, err := rethDB.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer rethTx.Rollback()

	rethCur, err := rethTx.Cursor(rethAcct)
	if err != nil {
		return err
	}
	defer rethCur.Close()

	var (
		written    uint64
		decodeFail uint64
		emptyAcc   uint64
		lastLog    = t0
	)

	n42Tx, err := n42DB.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer func() {
		if n42Tx != nil {
			n42Tx.Rollback()
		}
	}()

	for k, v, err := rethCur.First(); k != nil; k, v, err = rethCur.Next() {
		if err != nil {
			return fmt.Errorf("reth iter: %w", err)
		}
		if len(k) != 20 {
			continue
		}
		nonce, balance, codeHash, ok := decodeRethAccount(v)
		if !ok {
			decodeFail++
			continue
		}
		// Reth omits codeHash for EOAs (32 zero bytes); N42 stores empty-keccak
		// for EOAs. Normalize so the trie root matches what other nodes compute.
		if codeHash == ([32]byte{}) {
			codeHash = emptyCodeHash
		}
		acc := account.StateAccount{
			Nonce:    nonce,
			Balance:  balance,
			CodeHash: codeHash,
		}
		enc := acc.MarshalV2()
		// Filter the genuinely-empty rows reth carries (nonce=0, bal=0,
		// no code). Writing them is wasted space and breaks the
		// state-root invariant that the trie omits empty accounts.
		if nonce == 0 && balance.IsZero() && codeHash == emptyCodeHash {
			emptyAcc++
			continue
		}
		if err := n42Tx.Put(n42Acct, k, enc); err != nil {
			return fmt.Errorf("put Account: %w", err)
		}
		written++

		if written%commitEvery == 0 {
			if err := n42Tx.Commit(); err != nil {
				return fmt.Errorf("commit accounts: %w", err)
			}
			n42Tx, err = n42DB.BeginRw(context.Background())
			if err != nil {
				return err
			}
		}

		if time.Since(lastLog) > 15*time.Second {
			rate := float64(written) / time.Since(t0).Seconds()
			logger.Info("  account progress",
				"written", written, "decodeFail", decodeFail, "skipEmpty", emptyAcc,
				"rate/s", int(rate), "elapsed", time.Since(t0).Truncate(time.Second))
			lastLog = time.Now()
		}
	}

	if err := n42Tx.Commit(); err != nil {
		return fmt.Errorf("final commit accounts: %w", err)
	}
	n42Tx = nil

	logger.Info("PHASE 1/4 — Account done",
		"written", written, "decodeFail", decodeFail, "skipEmpty", emptyAcc,
		"elapsed", time.Since(t0))
	return nil
}

// importStorage streams reth PlainStorageState DupSort entries through
// to N42 Storage with the DupSort schema applied in
// lib/kv/tables.go ("Storage" entry: DupFromLen=52, DupToLen=20). With
// AutoDupSortKeysConversion, callers keep using plain
// tx.Put(addr+slot, val) — MDBX splits the 52B composite key into
// (20B dup-key, 32B+val dup-value) transparently.
//
// reth value layout per dup entry: 32B slot || N-byte value
// N42 logical key: 20B addr + 32B slot, logical value: N-byte value
func importStorage(n42DB kv.RwDB, rethDB kv.RoDB, clear bool, commitEvery uint64, logger log.Logger) error {
	logger.Info("PHASE 2/4 — Storage import starting")
	t0 := time.Now()

	// ForceRecreateBucket drops + recreates with the on-disk flags
	// adjusted to match the current TableCfg (DupSort). Plain
	// ClearBucket would only clear entries, leaving the old non-DupSort
	// flags in place — subsequent writes would either error or silently
	// produce non-DupSort layout, defeating the point of the schema
	// change. Idempotent: safe if Storage was already DupSort.
	if clear {
		if err := n42DB.Update(context.Background(), func(tx kv.RwTx) error {
			migrator, ok := tx.(interface{ ForceRecreateBucket(string) error })
			if !ok {
				return fmt.Errorf("RwTx missing ForceRecreateBucket — old mdbx wrapper?")
			}
			logger.Info("  force-recreating N42 Storage as DupSort (drop + recreate with new flags)")
			return migrator.ForceRecreateBucket(n42Sto)
		}); err != nil {
			return fmt.Errorf("force-recreate Storage: %w", err)
		}
	}

	rethTx, err := rethDB.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer rethTx.Rollback()

	rethCur, err := rethTx.CursorDupSort(rethSto)
	if err != nil {
		return err
	}
	defer rethCur.Close()

	var (
		written    uint64
		skipped    uint64
		shortValue uint64
		lastLog    = t0
	)
	composite := make([]byte, 52)

	n42Tx, err := n42DB.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer func() {
		if n42Tx != nil {
			n42Tx.Rollback()
		}
	}()

	for k, v, err := rethCur.First(); k != nil; k, v, err = rethCur.Next() {
		if err != nil {
			return fmt.Errorf("reth iter: %w", err)
		}
		if len(k) != 20 {
			continue
		}
		if len(v) < 32 {
			shortValue++
			continue
		}
		// reth dup value = 32B slot + variable-length raw value (trimmed
		// leading zeros). Empty trim = zero slot which is typically
		// implicit (not stored) — guard anyway.
		slot := v[:32]
		val := v[32:]
		if len(val) == 0 {
			skipped++
			continue
		}
		copy(composite[:20], k)
		copy(composite[20:], slot)
		if err := n42Tx.Put(n42Sto, composite, val); err != nil {
			return fmt.Errorf("put Storage: %w", err)
		}
		written++

		if written%commitEvery == 0 {
			if err := n42Tx.Commit(); err != nil {
				return fmt.Errorf("commit storage: %w", err)
			}
			n42Tx, err = n42DB.BeginRw(context.Background())
			if err != nil {
				return err
			}
		}

		if time.Since(lastLog) > 15*time.Second {
			rate := float64(written) / time.Since(t0).Seconds()
			logger.Info("  storage progress",
				"written", written, "skipZero", skipped, "shortValue", shortValue,
				"rate/s", int(rate), "elapsed", time.Since(t0).Truncate(time.Second))
			lastLog = time.Now()
		}
	}

	if err := n42Tx.Commit(); err != nil {
		return fmt.Errorf("final commit storage: %w", err)
	}
	n42Tx = nil

	logger.Info("PHASE 2/4 — Storage done",
		"written", written, "skipZero", skipped, "shortValue", shortValue,
		"elapsed", time.Since(t0))
	return nil
}

// importCode streams reth Bytecodes → N42 Code (same key/value
// layout: codeHash → bytecode).
func importCode(n42DB kv.RwDB, rethDB kv.RoDB, clear bool, commitEvery uint64, logger log.Logger) error {
	logger.Info("PHASE 3/4 — Code import starting")
	t0 := time.Now()

	if clear {
		if err := n42DB.Update(context.Background(), func(tx kv.RwTx) error {
			logger.Info("  clearing N42 Code table")
			return tx.ClearBucket(n42Code)
		}); err != nil {
			return fmt.Errorf("clear Code: %w", err)
		}
	}

	rethTx, err := rethDB.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer rethTx.Rollback()

	rethCur, err := rethTx.Cursor(rethCode)
	if err != nil {
		return err
	}
	defer rethCur.Close()

	var (
		written uint64
		skipped uint64
		lastLog = t0
	)

	n42Tx, err := n42DB.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer func() {
		if n42Tx != nil {
			n42Tx.Rollback()
		}
	}()

	for k, v, err := rethCur.First(); k != nil; k, v, err = rethCur.Next() {
		if err != nil {
			return fmt.Errorf("reth iter: %w", err)
		}
		if len(k) != 32 {
			skipped++
			continue
		}
		// reth stores bytecodes with a 1-byte prefix (the BytecodeType:
		// 0=Raw, 1=EOF, etc.). The actual EVM bytecode for type=0 is
		// from byte 1 onwards. For codeHash compatibility we strip the
		// prefix if value[0] == 0 (raw).
		code := v
		if len(v) >= 1 && v[0] == 0 {
			code = v[1:]
		}
		if err := n42Tx.Put(n42Code, k, code); err != nil {
			return fmt.Errorf("put Code: %w", err)
		}
		written++

		if written%commitEvery == 0 {
			if err := n42Tx.Commit(); err != nil {
				return fmt.Errorf("commit code: %w", err)
			}
			n42Tx, err = n42DB.BeginRw(context.Background())
			if err != nil {
				return err
			}
		}

		if time.Since(lastLog) > 15*time.Second {
			rate := float64(written) / time.Since(t0).Seconds()
			logger.Info("  code progress",
				"written", written, "skipped", skipped,
				"rate/s", int(rate), "elapsed", time.Since(t0).Truncate(time.Second))
			lastLog = time.Now()
		}
	}

	if err := n42Tx.Commit(); err != nil {
		return fmt.Errorf("final commit code: %w", err)
	}
	n42Tx = nil

	logger.Info("PHASE 3/4 — Code done",
		"written", written, "skipped", skipped, "elapsed", time.Since(t0))
	return nil
}

func clearCommitmentBranches(n42DB kv.RwDB, logger log.Logger) error {
	logger.Info("PHASE 4/4 — ClearBucket CommitmentBranches (HPH will rebuild)")
	t0 := time.Now()
	if err := n42DB.Update(context.Background(), func(tx kv.RwTx) error {
		return tx.ClearBucket(n42Cmt)
	}); err != nil {
		return err
	}
	logger.Info("  CommitmentBranches cleared", "elapsed", time.Since(t0))
	return nil
}

func writeHeadPointer(n42DB kv.RwDB, headBlock uint64, logger log.Logger) error {
	logger.Info("writing ethel-last-block", "headBlock", headBlock)
	return n42DB.Update(context.Background(), func(tx kv.RwTx) error {
		v := make([]byte, 8)
		binary.BigEndian.PutUint64(v, headBlock)
		return tx.Put(n42Prog, []byte("ethel-last-block"), v)
	})
}
