package state

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

type journalCursorErrorTx struct {
	kv.RwTx
	err error
}

func (tx *journalCursorErrorTx) Cursor(string) (kv.Cursor, error) {
	return nil, tx.err
}

func journalTestCfg(d kv.TableCfg) kv.TableCfg {
	d[modules.Account] = kv.TableCfgItem{}
	d[modules.Code] = kv.TableCfgItem{}
	d[modules.Storage] = kv.TableCfgItem{
		Flags: kv.DupSort, AutoDupSortKeysConversion: true, DupFromLen: 52, DupToLen: 20,
	}
	return d
}

// warmEntry reads the raw warm-table entry via SeekExact (handles Storage
// DupSort AutoConv), returning (value, present).
func warmEntry(t *testing.T, tx kv.Tx, table string, key []byte) ([]byte, bool) {
	t.Helper()
	cur, err := tx.Cursor(table)
	require.NoError(t, err)
	defer cur.Close()
	k, v, err := cur.SeekExact(key)
	require.NoError(t, err)
	if k == nil {
		return nil, false
	}
	return append([]byte(nil), v...), true
}

// TestBlockRevDiffRestore verifies that capturing a block's warm writes through
// NewCapturingTx and then BlockRevDiff.Restore reverts the Account/Storage/Code
// tables to their EXACT pre-block state — the foundation of in-memory reorg
// unwind for snapshot-direct (minimal) mode, which has no on-disk changesets.
func TestBlockRevDiffRestore(t *testing.T) {
	ctx := context.Background()
	db := mdbx.NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(journalTestCfg).MustOpen()
	defer db.Close()

	var A1, A2, A3 types.Address
	A1[0], A2[0], A3[0] = 1, 2, 3
	var S1, S2, S3 types.Hash
	S1[0], S2[0], S3[0] = 1, 2, 3
	var CH1 types.Hash
	CH1[0] = 0xc1

	sk := func(a types.Address, s types.Hash) []byte {
		return modules.PlainGenerateCompositeStorageKey(a.Bytes(), s.Bytes())
	}

	// --- seed the warm tier = post-(B-1) state ---
	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	acc100 := bal(100)
	acc100.Nonce = 1
	require.NoError(t, tx.Put(modules.Account, A1[:], acc100.MarshalV2())) // A1 present
	require.NoError(t, tx.Put(modules.Account, A3[:], bal(7).MarshalV2())) // A3 present
	// A2 intentionally absent
	require.NoError(t, tx.Put(modules.Storage, sk(A1, S1), []byte{0x11})) // present
	require.NoError(t, tx.Put(modules.Storage, sk(A1, S3), []byte{0x44})) // present
	// (A1,S2) intentionally absent
	// CH1 intentionally absent in Code
	require.NoError(t, tx.Commit())

	// snapshot the exact pre-block entries for every key the block will touch
	type snap struct {
		table string
		key   []byte
	}
	touched := []snap{
		{modules.Account, A1[:]}, {modules.Account, A2[:]}, {modules.Account, A3[:]},
		{modules.Storage, sk(A1, S1)}, {modules.Storage, sk(A1, S2)}, {modules.Storage, sk(A1, S3)},
		{modules.Code, CH1[:]},
	}
	before := map[string]struct {
		v []byte
		p bool
	}{}
	rtx, err := db.BeginRo(ctx)
	require.NoError(t, err)
	for _, s := range touched {
		v, p := warmEntry(t, rtx, s.table, s.key)
		before[s.table+string(s.key)] = struct {
			v []byte
			p bool
		}{v, p}
	}
	rtx.Rollback()

	// --- execute block B through the capturing writer ---
	tx, err = db.BeginRw(ctx)
	require.NoError(t, err)
	diff := NewBlockRevDiff()
	w := NewOverlayStateWriter(NewCapturingTx(tx, diff))

	// A1: update (present -> new). Written TWICE to prove first-wins keeps the
	// true pre-block value (bal 100), not the intermediate (bal 200).
	require.NoError(t, w.UpdateAccountData(A1, acc100, bal(200)))
	require.NoError(t, w.UpdateAccountData(A1, bal(200), bal(300)))
	// A2: create (absent -> new)
	require.NoError(t, w.UpdateAccountData(A2, nil, bal(50)))
	// A3: delete (present -> tombstone)
	require.NoError(t, w.DeleteAccount(A3, bal(7)))
	// storage A1.S1: overwrite (present -> new)
	require.NoError(t, w.WriteAccountStorage(A1, S1, *uint256.NewInt(0x11), *uint256.NewInt(0x22)))
	// storage A1.S2: create (absent -> new)
	require.NoError(t, w.WriteAccountStorage(A1, S2, *uint256.NewInt(0), *uint256.NewInt(0x33)))
	// storage A1.S3: SSTORE slot->0 (present -> tombstone)
	require.NoError(t, w.WriteAccountStorage(A1, S3, *uint256.NewInt(0x44), *uint256.NewInt(0)))
	// code CH1: create (absent -> code bytes)
	require.NoError(t, w.UpdateAccountCode(A1, CH1, []byte{0xde, 0xad, 0xbe, 0xef}))
	require.NoError(t, tx.Commit())

	// sanity: the block actually changed things
	rtx, err = db.BeginRo(ctx)
	require.NoError(t, err)
	v, p := warmEntry(t, rtx, modules.Account, A1[:])
	require.True(t, p)
	require.Equal(t, bal(300).MarshalV2(), v, "A1 post-block = bal(300)")
	_, p = warmEntry(t, rtx, modules.Code, CH1[:])
	require.True(t, p, "code written")
	rtx.Rollback()

	// --- unwind: apply the reverse-diff ---
	tx, err = db.BeginRw(ctx)
	require.NoError(t, err)
	require.NoError(t, diff.Restore(tx))
	require.NoError(t, tx.Commit())

	// --- assert every touched key is byte-identical to its pre-block state ---
	rtx, err = db.BeginRo(ctx)
	require.NoError(t, err)
	defer rtx.Rollback()
	for _, s := range touched {
		want := before[s.table+string(s.key)]
		gotV, gotP := warmEntry(t, rtx, s.table, s.key)
		require.Equal(t, want.p, gotP, "presence mismatch after restore for %s/%x", s.table, s.key)
		if want.p {
			require.Equal(t, want.v, gotV, "value mismatch after restore for %s/%x", s.table, s.key)
		}
	}
}

// TestCaptureFailureAbortsWrite prevents a missing pre-image from being encoded
// as "previously absent". That approximation would make a later reorg delete a
// value that actually existed before the block.
func TestCaptureFailureAbortsWrite(t *testing.T) {
	ctx := context.Background()
	db := mdbx.NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(journalTestCfg).MustOpen()
	defer db.Close()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	wantErr := errors.New("injected cursor failure")
	diff := NewBlockRevDiff()
	capturing := NewCapturingTx(&journalCursorErrorTx{RwTx: tx, err: wantErr}, diff)
	key := []byte("account")
	if err := capturing.Put(modules.Account, key, []byte("new")); !errors.Is(err, wantErr) {
		t.Fatalf("Put error = %v, want %v", err, wantErr)
	}
	if diff.Len() != 0 {
		t.Fatalf("failed capture recorded %d bogus reverse entries", diff.Len())
	}
	if got, err := tx.GetOne(modules.Account, key); err != nil || got != nil {
		t.Fatalf("write proceeded after capture failure: value=%x err=%v", got, err)
	}
}

// TestSequentialRevDiffUnwind proves that reverse-diffs from consecutive blocks
// COMPOSE: capturing blocks 1,2,3 over evolving warm state and then replaying
// diff3 + diff2 (newest-first) restores the warm tables to their exact
// post-block-1 state. This is the multi-block unwind path a deeper-than-1 reorg
// takes (one block per round); a composition bug here would silently corrupt
// minimal-mode state (no per-block root check to catch it).
func TestSequentialRevDiffUnwind(t *testing.T) {
	ctx := context.Background()
	db := mdbx.NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(journalTestCfg).MustOpen()
	defer db.Close()

	var A types.Address
	A[0] = 0xaa
	var S1, S2, S3 types.Hash
	S1[0], S2[0], S3[0] = 1, 2, 3
	sk := func(s types.Hash) []byte { return modules.PlainGenerateCompositeStorageKey(A.Bytes(), s.Bytes()) }
	u := func(n uint64) uint256.Int { return *uint256.NewInt(n) }

	// block0 seed (post-block-0 warm state)
	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	require.NoError(t, tx.Put(modules.Account, A[:], bal(1000).MarshalV2()))
	require.NoError(t, tx.Put(modules.Storage, sk(S1), []byte{0x01}))
	require.NoError(t, tx.Put(modules.Storage, sk(S2), []byte{0x02}))
	require.NoError(t, tx.Commit())

	// snapshot the full warm state at a given point for later comparison
	snapshot := func() map[string][]byte {
		m := map[string][]byte{}
		rtx, err := db.BeginRo(ctx)
		require.NoError(t, err)
		defer rtx.Rollback()
		for _, tbl := range []string{modules.Account, modules.Storage, modules.Code} {
			cur, err := rtx.Cursor(tbl)
			require.NoError(t, err)
			for k, v, err := cur.First(); k != nil; k, v, err = cur.Next() {
				require.NoError(t, err)
				m[tbl+"|"+string(k)] = append([]byte(nil), v...)
			}
			cur.Close()
		}
		return m
	}
	postBlock1 := map[string][]byte{}

	// execute a block through a capturing writer, returning its diff
	execBlock := func(fn func(w *OverlayStateWriter)) *BlockRevDiff {
		tx, err := db.BeginRw(ctx)
		require.NoError(t, err)
		diff := NewBlockRevDiff()
		w := NewOverlayStateWriter(NewCapturingTx(tx, diff))
		fn(w)
		require.NoError(t, tx.Commit())
		return diff
	}

	// block1: bump A, overwrite S1, create S3
	_ = execBlock(func(w *OverlayStateWriter) {
		require.NoError(t, w.UpdateAccountData(A, bal(1000), bal(1100)))
		require.NoError(t, w.WriteAccountStorage(A, S1, u(0x01), u(0x11)))
		require.NoError(t, w.WriteAccountStorage(A, S3, u(0), u(0x33)))
	})
	postBlock1 = snapshot() // <-- target state for the unwind

	// block2: bump A again, S2->0 tombstone, code
	diff2 := execBlock(func(w *OverlayStateWriter) {
		require.NoError(t, w.UpdateAccountData(A, bal(1100), bal(1200)))
		require.NoError(t, w.WriteAccountStorage(A, S2, u(0x02), u(0)))
		require.NoError(t, w.UpdateAccountCode(A, types.Hash{0xcc}, []byte{0x01, 0x02}))
	})

	// block3: bump A again, overwrite S3
	diff3 := execBlock(func(w *OverlayStateWriter) {
		require.NoError(t, w.UpdateAccountData(A, bal(1200), bal(1300)))
		require.NoError(t, w.WriteAccountStorage(A, S3, u(0x33), u(0x99)))
	})

	// unwind block3 then block2 (newest-first), each in its own tx (as the
	// coordinator does one-per-round)
	for _, d := range []*BlockRevDiff{diff3, diff2} {
		tx, err := db.BeginRw(ctx)
		require.NoError(t, err)
		require.NoError(t, d.Restore(tx))
		require.NoError(t, tx.Commit())
	}

	require.Equal(t, postBlock1, snapshot(),
		"warm state after unwinding blocks 3+2 must equal the post-block-1 snapshot")
}
