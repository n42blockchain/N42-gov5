// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Regression for the JMT hashed-state storage-wipe bug: when an account is
// SELFDESTRUCT'd (or CREATE2'd over an existing contract), the pluggable
// RootComputer (JMT/MPT/BMT) must remove EVERY pre-block storage slot of that
// account from the hashed state — not merely the slots the block happened to
// touch. The pre-fix computeRootViaComputer deleted only obj.blockOriginStorage
// (touched slots), leaving stale leaves and producing a wrong state root. This
// surfaced during the BLS-reseal conversion as ~16K stale JMT leaves at head
// 3.8M and a header.Root that did not match a from-scratch rebuild.
//
// The fix captures the complete pre-block slot set at wipe-registration time
// (IntraBlockState.captureWipedSlots, via the StorageEnumerator reader) and
// feeds all of it — zeroed — to ComputeRoot.
//
// These tests are external (package state_test) to avoid the state↔commitment
// import cycle, and drive the real IntraBlockState API (Selfdestruct /
// CreateAccount / IntermediateRoot) over a memdb-backed PlainStateReader.

package state_test

import (
	"context"
	"sync"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

var n42InitOnce sync.Once

func slotHash(b byte) types.Hash {
	var h types.Hash
	h[31] = b
	return h
}

func nonEmptyAccount(nonce uint64, bal uint64) *account.StateAccount {
	a := &account.StateAccount{Initialised: true, Nonce: nonce}
	a.Balance.SetUint64(bal)
	return a
}

func newJMTRootComputer() *commitment.JMTRootComputer {
	return commitment.NewJMTRootComputer(commitment.NewJMTCommitment(jmt.New(jmt.NewMemStore())))
}

// freshRootOver builds a brand-new JMT over exactly the given accounts+storage
// and returns its root — the independent oracle the incremental root must match.
func freshRootOver(
	t *testing.T,
	accts map[types.Address]*account.StateAccount,
	stor map[types.Address]map[types.Hash]*uint256.Int,
) types.Hash {
	t.Helper()
	root, err := newJMTRootComputer().ComputeRoot(accts, stor)
	require.NoError(t, err)
	return root
}

func seedPlainState(t *testing.T, tx kv.RwTx, addr types.Address, acct *account.StateAccount, slots map[types.Hash]*uint256.Int) {
	t.Helper()
	require.NoError(t, tx.Put(modules.Account, addr[:], acct.MarshalV2()))
	for slot, val := range slots {
		if val.IsZero() {
			continue
		}
		b := val.Bytes()
		require.NoError(t, tx.Put(modules.Storage,
			modules.PlainGenerateCompositeStorageKey(addr[:], slot[:]), b))
	}
}

func useN42Tables(t *testing.T) {
	n42InitOnce.Do(func() { modules.N42Init() })
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })
}

// TestJMTSelfdestructWipe_DeletesAllSlots is the core regression: an account
// with three persisted storage slots is SELFDESTRUCT'd while the block touches
// only one of them. The JMT root afterward must equal a from-scratch tree that
// contains neither the account nor ANY of its slots. Pre-fix, slots 2 and 3
// remained as stale leaves and the root diverged.
func TestJMTSelfdestructWipe_DeletesAllSlots(t *testing.T) {
	useN42Tables(t)
	db := memdb.NewTestDB(t)

	victim := types.HexToAddress("0x00000000002bde777710c370e08fc83d61b2b8e1")
	keep := types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	victimAcct := nonEmptyAccount(7, 1234)
	keepAcct := nonEmptyAccount(3, 9999)

	s1, s2, s3 := slotHash(1), slotHash(2), slotHash(3)
	keepSlot := slotHash(0x55)
	victimSlots := map[types.Hash]*uint256.Int{
		s1: uint256.NewInt(0xaa),
		s2: uint256.NewInt(0xbb),
		s3: uint256.NewInt(0xcc),
	}
	keepSlots := map[types.Hash]*uint256.Int{keepSlot: uint256.NewInt(0x77)}

	// Seed the plain state (the PlainStateReader / StorageEnumerator source).
	{
		tx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		seedPlainState(t, tx, victim, victimAcct, victimSlots)
		seedPlainState(t, tx, keep, keepAcct, keepSlots)
		require.NoError(t, tx.Commit())
	}

	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	// Build the live JMT and prime it with the full pre-block state so it holds
	// the victim account + its three slots, plus the surviving account.
	rc := newJMTRootComputer()
	primedRoot, err := rc.ComputeRoot(
		map[types.Address]*account.StateAccount{victim: victimAcct, keep: keepAcct},
		map[types.Address]map[types.Hash]*uint256.Int{victim: victimSlots, keep: keepSlots},
	)
	require.NoError(t, err)

	// Oracle: the post-state tree holds only the surviving account + its slot.
	postOnly := freshRootOver(t,
		map[types.Address]*account.StateAccount{keep: keepAcct},
		map[types.Address]map[types.Hash]*uint256.Int{keep: keepSlots},
	)
	require.NotEqual(t, postOnly, primedRoot, "priming sanity: victim must change the root")

	// Now run a block that SELFDESTRUCTs the victim, touching only slot 1.
	reader := state.NewPlainStateReader(roTx)
	ibs := state.New(reader)
	ibs.SetRootComputer(rc)

	// Touch only slot 1 (so blockOriginStorage would hold just one slot — the
	// pre-fix code would have deleted only this one).
	var got uint256.Int
	ibs.GetState(victim, &s1, &got)
	require.Equal(t, *uint256.NewInt(0xaa), got, "pre-destruct read of slot 1")

	require.True(t, ibs.Selfdestruct(victim))

	gotRoot := ibs.IntermediateRoot()

	require.Equal(t, postOnly, gotRoot,
		"JMT root after SELFDESTRUCT must drop ALL victim slots; stale leaves remain → wrong root (the 3.8M conversion bug)")
}

// TestJMTSelfdestructWipe_Create2OverExisting covers the metamorphic case: an
// existing contract with old storage is CREATE2'd over (storage wiped) and the
// new incarnation writes only one fresh slot. The hashed-state root must reflect
// {new slot only}, with every old slot removed.
func TestJMTSelfdestructWipe_Create2OverExisting(t *testing.T) {
	useN42Tables(t)
	db := memdb.NewTestDB(t)

	addr := types.HexToAddress("0x00000000002bde777710c370e08fc83d61b2b8e1")
	oldAcct := nonEmptyAccount(1, 500)

	old1, old2 := slotHash(8), slotHash(9)
	newSlot := slotHash(6)
	oldSlots := map[types.Hash]*uint256.Int{
		old1: uint256.NewInt(0x2432),
		old2: uint256.NewInt(0x043e),
	}

	{
		tx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		seedPlainState(t, tx, addr, oldAcct, oldSlots)
		require.NoError(t, tx.Commit())
	}

	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	rc := newJMTRootComputer()
	_, err = rc.ComputeRoot(
		map[types.Address]*account.StateAccount{addr: oldAcct},
		map[types.Address]map[types.Hash]*uint256.Int{addr: oldSlots},
	)
	require.NoError(t, err)

	reader := state.NewPlainStateReader(roTx)
	ibs := state.New(reader)
	ibs.SetRootComputer(rc)

	// CREATE2 over the existing address: registers the storage wipe and captures
	// the full old slot set, then the new incarnation writes one fresh slot.
	ibs.CreateAccount(addr, true)
	ibs.SetState(addr, &newSlot, *uint256.NewInt(0x03))

	gotRoot := ibs.IntermediateRoot()

	// Oracle: build the post-state from the EXACT account encoding the IBS fed
	// to the tree (DirtyAccountData mirrors computeRootViaComputer's account
	// bytes), so the comparison can't drift on incidental account fields.
	dirty, _ := ibs.DirtyAccountData()
	enc := dirty[addr]
	require.NotNil(t, enc, "recreated account must be dirty")
	var oracleAcct account.StateAccount
	require.NoError(t, oracleAcct.DecodeForStorageV2(enc))

	want := freshRootOver(t,
		map[types.Address]*account.StateAccount{addr: &oracleAcct},
		map[types.Address]map[types.Hash]*uint256.Int{addr: {newSlot: uint256.NewInt(0x03)}},
	)

	require.Equal(t, want, gotRoot,
		"metamorphic CREATE2: old slots must be wiped from hashed state, only the new slot kept")
}

// TestJMTSelfdestructWipe_RevertedSelfdestructKeepsStorage guards the journal
// interaction: captureWipedSlots is NOT journaled, but storageWipes IS. If a
// SELFDESTRUCT is reverted and the account stays alive (here via a later balance
// bump), the stale capture must NOT cause its storage to be deleted from the
// hashed state. activeWipedSlots gates on storageWipes membership to prevent
// exactly that.
func TestJMTSelfdestructWipe_RevertedSelfdestructKeepsStorage(t *testing.T) {
	useN42Tables(t)
	db := memdb.NewTestDB(t)

	victim := types.HexToAddress("0x00000000002bde777710c370e08fc83d61b2b8e1")
	victimAcct := nonEmptyAccount(7, 1234)
	s1, s2, s3 := slotHash(1), slotHash(2), slotHash(3)
	victimSlots := map[types.Hash]*uint256.Int{
		s1: uint256.NewInt(0xaa),
		s2: uint256.NewInt(0xbb),
		s3: uint256.NewInt(0xcc),
	}

	{
		tx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		seedPlainState(t, tx, victim, victimAcct, victimSlots)
		require.NoError(t, tx.Commit())
	}

	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	rc := newJMTRootComputer()
	_, err = rc.ComputeRoot(
		map[types.Address]*account.StateAccount{victim: victimAcct},
		map[types.Address]map[types.Hash]*uint256.Int{victim: victimSlots},
	)
	require.NoError(t, err)

	reader := state.NewPlainStateReader(roTx)
	ibs := state.New(reader)
	ibs.SetRootComputer(rc)

	// SELFDESTRUCT, then revert it — the account survives.
	snap := ibs.Snapshot()
	require.True(t, ibs.Selfdestruct(victim))
	ibs.RevertToSnapshot(snap)
	// A benign balance bump keeps the (surviving) account dirty.
	ibs.AddBalance(victim, uint256.NewInt(1))

	gotRoot := ibs.IntermediateRoot()

	// Oracle: account with the bumped balance, ALL original slots intact.
	dirty, _ := ibs.DirtyAccountData()
	enc := dirty[victim]
	require.NotNil(t, enc, "surviving account must be dirty")
	var oracleAcct account.StateAccount
	require.NoError(t, oracleAcct.DecodeForStorageV2(enc))

	want := freshRootOver(t,
		map[types.Address]*account.StateAccount{victim: &oracleAcct},
		map[types.Address]map[types.Hash]*uint256.Int{victim: victimSlots},
	)

	require.Equal(t, want, gotRoot,
		"reverted SELFDESTRUCT must keep all storage; stale capture must not delete slots")
}
