// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers miner-support helpers that were entirely untested: the no-op paths
// of logStartupStateFingerprint and voidMinerRootTrust when no QMDB tree is
// attached, the TakeBuildStallLockWait/TakeBuildStallRootLockWait diagnostic
// counters, MinerAdoptAppends' env-derived flag, NewMinerRootComputer's
// QMDB-disabled short circuit, and WaitBlockPersisted's found/timeout paths.

package internal

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestLogStartupStateFingerprintNoopWithoutQMDB(t *testing.T) {
	bc := &BlockChain{ctx: context.Background()}
	// qmdbEnabled is false by default: must return immediately, no panic.
	bc.logStartupStateFingerprint()
}

func TestVoidMinerRootTrustNoopWithoutMinerRC(t *testing.T) {
	bc := &BlockChain{}
	// minerRC is nil: must not panic, just take/release the lock.
	bc.voidMinerRootTrust("test")
}

func TestTakeBuildStallLockWaitSwapsAndResets(t *testing.T) {
	bc := &BlockChain{}
	bc.buildStallLockWaitNs.Store(int64(5 * time.Millisecond))
	got := bc.TakeBuildStallLockWait()
	if got != 5*time.Millisecond {
		t.Fatalf("TakeBuildStallLockWait() = %s, want 5ms", got)
	}
	if got2 := bc.TakeBuildStallLockWait(); got2 != 0 {
		t.Fatalf("TakeBuildStallLockWait() second call = %s, want 0 (reset)", got2)
	}
}

func TestTakeBuildStallRootLockWaitSwapsAndResets(t *testing.T) {
	bc := &BlockChain{}
	bc.buildStallRootLockWaitNs.Store(int64(3 * time.Millisecond))
	got := bc.TakeBuildStallRootLockWait()
	if got != 3*time.Millisecond {
		t.Fatalf("TakeBuildStallRootLockWait() = %s, want 3ms", got)
	}
	if got2 := bc.TakeBuildStallRootLockWait(); got2 != 0 {
		t.Fatalf("TakeBuildStallRootLockWait() second call = %s, want 0 (reset)", got2)
	}
}

func TestMinerAdoptAppendsReflectsEnvAtInit(t *testing.T) {
	// minerAdoptAppends is read once at package init from N42_MINER_ADOPT_APPENDS;
	// this only pins that the accessor reflects whatever that produced, since
	// the env var cannot be changed retroactively for an already-initialized var.
	if MinerAdoptAppends() != minerAdoptAppends {
		t.Fatalf("MinerAdoptAppends() = %v, want %v (package var)", MinerAdoptAppends(), minerAdoptAppends)
	}
}

func TestNewMinerRootComputerNilWhenQMDBDisabled(t *testing.T) {
	db := newRealignTestDB(t)
	bc := &BlockChain{ChainDB: db, ctx: context.Background()}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		rc, err := bc.NewMinerRootComputer(tx, types.Hash{})
		if err != nil {
			t.Fatalf("NewMinerRootComputer() with QMDB disabled error = %v, want nil", err)
		}
		if rc != nil {
			t.Fatalf("NewMinerRootComputer() with QMDB disabled = %v, want nil", rc)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWaitBlockPersistedFindsStoredHeaderAndTimesOutOtherwise(t *testing.T) {
	db := newRealignTestDB(t)
	bc := &BlockChain{ChainDB: db, ctx: context.Background()}

	hash := types.HexToHash("0xaa")
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteHeaderNumber(tx, hash, 1)
	}); err != nil {
		t.Fatal(err)
	}

	if !bc.WaitBlockPersisted(hash, 50*time.Millisecond) {
		t.Fatalf("WaitBlockPersisted(stored header) = false, want true")
	}

	start := time.Now()
	if bc.WaitBlockPersisted(types.HexToHash("0xbb"), 30*time.Millisecond) {
		t.Fatalf("WaitBlockPersisted(never stored) = true, want false (timeout)")
	}
	if time.Since(start) < 25*time.Millisecond {
		t.Fatalf("WaitBlockPersisted() returned before its timeout elapsed: %s", time.Since(start))
	}
}
