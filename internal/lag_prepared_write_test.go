// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// dumpTables digests every chaindata table of db: table -> rows and a hash
// of its rows in cursor order (duplicates included).
func dumpTables(t *testing.T, db kv.RoDB) map[string]string {
	t.Helper()
	out := map[string]string{}
	names := make([]string, 0, len(modules.N42TableCfg))
	for name := range modules.N42TableCfg {
		names = append(names, name)
	}
	sort.Strings(names)
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		for _, name := range names {
			h := sha256.New()
			n := 0
			c, err := tx.Cursor(name)
			if err != nil {
				continue // not created in this database
			}
			for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
				if err != nil {
					c.Close()
					return err
				}
				var l [8]byte
				l[0], l[1] = byte(len(k)>>8), byte(len(k))
				l[2], l[3], l[4], l[5] = byte(len(v)>>24), byte(len(v)>>16), byte(len(v)>>8), byte(len(v))
				h.Write(l[:6])
				h.Write(k)
				h.Write(v)
				n++
			}
			c.Close()
			if n > 0 {
				out[name] = hex.EncodeToString(h.Sum(nil)[:8]) + "/" + itoa(n)
			}
		}
		return nil
	}))
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// requireSameTables fails on the first table whose rows differ.
func requireSameTables(t *testing.T, want, got map[string]string, what string) {
	t.Helper()
	for name, w := range want {
		require.Equal(t, w, got[name], "%s: table %s", what, name)
	}
	for name, g := range got {
		require.Equal(t, want[name], g, "%s: table %s", what, name)
	}
}

// preparedWriteBlocks builds n blocks on lc, a depth-2 chain, importing
// each one alone and digesting the tables after its write (dumps[i] is the
// store after block i+1). The blocks carry every kind of state write the
// prepared path must reproduce: transfers between funded accounts and to
// fresh ones, a zero-value touch of an empty account (EIP-161 removal), a
// contract creation with storage (CreateContract of an address without
// stored slots: prepared rows), later calls that rewrite its slot (storage
// rows and storage changesets) and its SELFDESTRUCT (a wipe of stored
// slots: the replayed path).
func preparedWriteBlocks(t *testing.T, lc *lagChain, n int) (blks []*block.Block, dumps []map[string]string) {
	t.Helper()
	// runtime: SSTORE(0, CALLVALUE), or SELFDESTRUCT(CALLER) on a zero-value
	// call; init: SSTORE(0, 1), return the runtime.
	runtime := []byte{0x34, 0x80, 0x60, 0x08, 0x57, 0x50, 0x33, 0xff, 0x5b, 0x60, 0x00, 0x55, 0x00}
	initCode := append([]byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x6c}, runtime...)
	initCode = append(initCode, 0x60, 0x00, 0x52, 0x60, 0x0d, 0x60, 0x13, 0xf3)
	contract := crypto.CreateAddress(lc.addrs[2], 0)
	nonces := make([]uint64, len(lc.keys))
	send := func(from int, to *types.Address, value uint64, gas uint64, data []byte) *transaction.Transaction {
		tx, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
			ChainID: uint256.MustFromBig(lc.cfg.ChainID), Nonce: nonces[from], GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(2), Gas: gas, To: to, Value: uint256.NewInt(value), Data: data,
		}), lc.signer, lc.keys[from])
		require.NoError(t, err)
		nonces[from]++
		return tx
	}
	imported := 0
	importNext := func() {
		lc.bc.EnqueueLagImport(blks[imported])
		imported++
		waitLagWritten(t, lc.bc, uint64(imported))
		lc.bc.lock.Lock()
		require.NoError(t, lc.bc.joinLagWrite())
		lc.bc.lock.Unlock()
		dumps = append(dumps, dumpTables(t, lc.db))
	}
	parent := lc.bc.genesisBlock.Header().(*block.Header)
	for i := 0; i < n; i++ {
		fresh := types.BytesToAddress(crypto.Keccak256([]byte("p11-fresh"), []byte{byte(i)}))
		a0, a1 := lc.addrs[0], lc.addrs[1]
		txs := []*transaction.Transaction{
			send(0, &a1, 1000+uint64(i), 21000, nil),
			send(0, &fresh, 1+uint64(i), 21000, nil),
			send(1, &a0, 7, 21000, nil),
		}
		switch i {
		case 1:
			txs = append(txs, send(2, nil, 0, 200000, initCode))
		case 3, 5:
			txs = append(txs, send(2, &contract, 5+uint64(i), 100000, nil))
		case 6:
			// SELFDESTRUCT of a contract with a stored slot: the wipe reads
			// the store, so this block's prepared write is replayed.
			txs = append(txs, send(2, &contract, 0, 100000, nil))
		case 4:
			empty := types.BytesToAddress(crypto.Keccak256([]byte("p11-empty")))
			txs = append(txs, send(1, &empty, 0, 21000, nil))
		}
		b := lc.build(parent, 0, txs...)
		blks = append(blks, b)
		parent = b.Header().(*block.Header)
		// The next build reads the result of the block two below.
		if i >= 1 {
			importNext()
		}
	}
	importNext()
	return blks, dumps
}

// TestLagWriterPreparedWriteSameRows (P11): a chain whose ordered writer
// prepares CommitBlock and the changeset rows off the write transaction
// stores, block by block, exactly the rows of a chain that runs them inside
// it -- every table -- with the history index deferred (prepared rows, a
// replay for the block whose SELFDESTRUCT wipes stored slots) and inline
// (replay), and
// a pipelined import of the same blocks ends with the same tables.
func TestLagWriterPreparedWriteSameRows(t *testing.T) {
	for _, deferred := range []bool{true, false} {
		t.Run(map[bool]string{true: "historyDeferred", false: "historyInline"}[deferred], func(t *testing.T) {
			testLagWriterPreparedWriteSameRows(t, deferred)
		})
	}
}

func testLagWriterPreparedWriteSameRows(t *testing.T, deferred bool) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagWriteAsyncForTest(true)()
	defer state.SetHistoryIndexDeferredForTest(deferred)()
	keys := frametest.Keys(t, 3, 71)
	const n = 8

	// Reference: CommitBlock inside the write.
	restore := SetLagWritePrepareForTest(false)
	ref := newLagChainEngine(t, keys, importBenchEngine{})
	blks, want := preparedWriteBlocks(t, ref, n)
	restore()
	// The creation and the calls ran (the contract's slot, code and storage
	// changesets after block 6) and the SELFDESTRUCT in block 7 wiped the
	// slot.
	for _, table := range []string{modules.Storage, modules.Code, modules.StorageChangeSet} {
		require.NotEmpty(t, want[5][table], "reference chain has no %s rows after block 6", table)
	}
	require.Empty(t, want[6][modules.Storage], "the SELFDESTRUCT left the contract's slot")

	var mu sync.Mutex
	prepared, fast := 0, 0
	blockWritePhasesHook = func(_ uint64, ph blockWritePhases) {
		mu.Lock()
		if ph.Prep > 0 {
			prepared++
		}
		if ph.PrepFast {
			fast++
		}
		mu.Unlock()
	}
	defer func() { blockWritePhasesHook = nil }()
	defer SetLagWritePrepareForTest(true)()

	// Block by block.
	seq := newLagChainEngine(t, keys, importBenchEngine{})
	for i, b := range blks {
		seq.bc.RememberAvailable(b)
		seq.bc.EnqueueLagImport(b)
		waitLagWritten(t, seq.bc, uint64(i+1))
		seq.bc.lock.Lock()
		require.NoError(t, seq.bc.joinLagWrite())
		seq.bc.lock.Unlock()
		requireSameTables(t, want[i], dumpTables(t, seq.db), "after block "+itoa(i+1))
	}
	mu.Lock()
	require.Equal(t, n, prepared, "writes that took the prepared path")
	if deferred {
		require.Equal(t, n-1, fast, "writes applied from prepared rows (the SELFDESTRUCT block replays)")
	} else {
		require.Zero(t, fast, "an inline history index replays")
	}
	mu.Unlock()

	// Pipelined: all blocks queued at once, each preparation overlapping the
	// previous block's write.
	pipe := newLagChainEngine(t, keys, importBenchEngine{})
	for _, b := range blks {
		pipe.bc.RememberAvailable(b)
		pipe.bc.EnqueueLagImport(b)
	}
	waitLagWritten(t, pipe.bc, n)
	pipe.bc.lock.Lock()
	require.NoError(t, pipe.bc.joinLagWrite())
	pipe.bc.lock.Unlock()
	requireSameTables(t, want[n-1], dumpTables(t, pipe.db), "pipelined import")
}
