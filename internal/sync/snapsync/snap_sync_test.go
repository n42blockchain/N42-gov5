// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package snapsync

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// Simulation parameters: 300 blocks × 100 tx/block, 20% ERC-20.
// Produces ~2000 EOA accounts, 1 contract, ~2000 storage slots.
const (
	simEOACount     = 2000
	simStorageSlots = 2000
)

// TestSnapSyncIntegration simulates a mining node with 5 minutes of blocks
// (300 blocks, 100 tx each, 20% ERC-20 USDT transfers), then runs snap sync
// to replicate the state to a second node's database.
func TestSnapSyncIntegration(t *testing.T) {
	sourceDB := testDB(t)
	targetDB := testDB(t)
	ctx := context.Background()

	// Step 1: Populate source DB with simulated state.
	usdtAddr, usdtCodeHash := populateSourceState(t, sourceDB)
	t.Logf("Source state: %d EOAs, 1 contract (%x), %d storage slots",
		simEOACount, usdtAddr[:8], simStorageSlots)

	srcAccounts := countTableEntries(t, sourceDB, modules.Account)
	srcStorage := countTableEntries(t, sourceDB, modules.Storage)
	srcCode := countTableEntries(t, sourceDB, modules.Code)
	if srcAccounts != simEOACount+1 {
		t.Fatalf("source accounts: want %d, got %d", simEOACount+1, srcAccounts)
	}
	if srcStorage != simStorageSlots {
		t.Fatalf("source storage: want %d, got %d", simStorageSlots, srcStorage)
	}
	if srcCode != 1 {
		t.Fatalf("source code: want 1, got %d", srcCode)
	}

	// Step 2: Override send functions to serve from source DB.
	origSendAccount := sendGetAccountRange
	origSendStorage := sendGetStorageRange
	origSendCode := sendGetCode
	t.Cleanup(func() {
		sendGetAccountRange = origSendAccount
		sendGetStorageRange = origSendStorage
		sendGetCode = origSendCode
	})

	maxReqBytes := uint64(32 * 1024) // force pagination
	sendGetAccountRange = makeAccountRangeMock(sourceDB, maxReqBytes)
	sendGetStorageRange = makeStorageRangeMock(sourceDB, maxReqBytes)
	sendGetCode = makeCodeMock(sourceDB)

	// Step 3: Create manager with target DB (no real P2P needed).
	mgr := &Manager{
		cfg:    &conf.SnapSyncConfig{MaxBytesPerReq: maxReqBytes},
		db:     targetDB,
		dbSem:  semaphore.NewWeighted(maxConcurrentDBWrites),
		scorer: newPeerScorer(),
	}
	mgr.initFresh()

	// Step 4: Single-threaded sync loop.
	start := time.Now()
	rounds := 0
	for {
		task := mgr.pickTask()
		if task == nil {
			break
		}
		task.Assigned = "test-peer"
		task.AssignedAt = time.Now()
		mgr.executeTask(ctx, task)
		rounds++
		if rounds > 10000 {
			t.Fatal("sync loop exceeded 10000 rounds — possible infinite loop")
		}
	}
	elapsed := time.Since(start)

	// Step 5: Verify progress counters.
	p := mgr.GetProgress()
	t.Logf("Sync done in %v (%d rounds): accounts=%d storage=%d codes=%d bytes=%d",
		elapsed, rounds, p.AccountsDone, p.StoragesDone, p.CodesDone, p.BytesReceived)

	if p.AccountsDone != uint64(simEOACount+1) {
		t.Errorf("accounts synced: want %d, got %d", simEOACount+1, p.AccountsDone)
	}
	if p.StoragesDone != uint64(simStorageSlots) {
		t.Errorf("storage synced: want %d, got %d", simStorageSlots, p.StoragesDone)
	}
	if p.CodesDone != 1 {
		t.Errorf("codes synced: want 1, got %d", p.CodesDone)
	}
	if p.PendingTasks != 0 {
		t.Errorf("pending tasks: want 0, got %d", p.PendingTasks)
	}

	// Step 6: Verify target DB matches source DB exactly.
	verifyTableMatch(t, sourceDB, targetDB, modules.Account, "Account")
	verifyTableMatch(t, sourceDB, targetDB, modules.Storage, "Storage")
	verifyTableMatch(t, sourceDB, targetDB, modules.Code, "Code")

	// Step 7: Spot-check the USDT contract specifically.
	verifyContractState(t, targetDB, usdtAddr, usdtCodeHash)
}

// ---------------------------------------------------------------------------
// Source state population
// ---------------------------------------------------------------------------

func populateSourceState(t *testing.T, db kv.RwDB) (types.Address, types.Hash) {
	t.Helper()
	ctx := context.Background()

	// Deterministic USDT bytecode.
	usdtCode := make([]byte, 2048)
	for i := range usdtCode {
		usdtCode[i] = byte(i % 256)
	}
	usdtCodeHash := crypto.Keccak256Hash(usdtCode)
	emptyHash := crypto.Keccak256Hash(nil)

	var usdtAddr types.Address
	copy(usdtAddr[:], crypto.Keccak256([]byte("test-usdt"))[:20])

	err := db.Update(ctx, func(tx kv.RwTx) error {
		// EOA accounts.
		for i := 0; i < simEOACount; i++ {
			addr := makeTestAddress(i)
			acc := account.StateAccount{
				Initialised: true,
				Nonce:       uint64(i + 1),
				Balance:     *uint256.NewInt(uint64(1_000_000 + i*100)),
				CodeHash:    emptyHash,
			}
			data, err := acc.Marshal()
			if err != nil {
				return err
			}
			if err := tx.Put(modules.Account, addr[:], data); err != nil {
				return err
			}
		}

		// USDT contract account.
		usdtAcc := account.StateAccount{
			Initialised: true,
			Nonce:       1,
			Balance:     *uint256.NewInt(0),
			CodeHash:    usdtCodeHash,
			Incarnation: 1,
		}
		usdtData, err := usdtAcc.Marshal()
		if err != nil {
			return err
		}
		if err := tx.Put(modules.Account, usdtAddr[:], usdtData); err != nil {
			return err
		}

		// Storage slots (ERC-20 balance mapping).
		for i := 0; i < simStorageSlots; i++ {
			var buf [8]byte
			binary.BigEndian.PutUint64(buf[:], uint64(i))
			storageKey := crypto.Keccak256Hash(buf[:])
			compositeKey := modules.PlainGenerateCompositeStorageKey(
				usdtAddr[:], 1, storageKey[:],
			)
			value := uint256.NewInt(uint64(1000 * (i + 1)))
			if err := tx.Put(modules.Storage, compositeKey, value.Bytes()); err != nil {
				return err
			}
		}

		// Contract bytecode.
		if err := tx.Put(modules.Code, usdtCodeHash[:], usdtCode); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return usdtAddr, usdtCodeHash
}

func makeTestAddress(index int) types.Address {
	var addr types.Address
	binary.BigEndian.PutUint64(addr[12:], uint64(index))
	return addr
}

// ---------------------------------------------------------------------------
// Mock send functions (read from source DB, paginate like real RPC handlers)
// ---------------------------------------------------------------------------

func makeAccountRangeMock(
	sourceDB kv.RwDB, defaultMax uint64,
) func(context.Context, p2p.SenderEncoder, peer.ID, *sync_pb.GetAccountRangeRequest) (*sync_pb.AccountRangeResponse, error) {
	return func(ctx context.Context, _ p2p.SenderEncoder, _ peer.ID, req *sync_pb.GetAccountRangeRequest) (*sync_pb.AccountRangeResponse, error) {
		resp := &sync_pb.AccountRangeResponse{}
		maxBytes := req.MaxBytes
		if maxBytes == 0 || maxBytes > defaultMax {
			maxBytes = defaultMax
		}
		var total uint64

		if err := sourceDB.View(ctx, func(tx kv.Tx) error {
			c, err := tx.Cursor(modules.Account)
			if err != nil {
				return err
			}
			defer c.Close()

			var k, v []byte
			if len(req.Start) > 0 {
				k, v, err = c.Seek(req.Start)
			} else {
				k, v, err = c.First()
			}
			if err != nil {
				return err
			}

			for k != nil {
				if len(req.End) > 0 && bytes.Compare(k, req.End) >= 0 {
					resp.Completed = true
					break
				}
				sz := uint64(len(k) + len(v))
				if len(resp.Accounts) > 0 && total+sz > maxBytes {
					resp.NextStart = append([]byte{}, k...)
					break
				}
				resp.Accounts = append(resp.Accounts, &sync_pb.AccountEntry{
					Address: append([]byte{}, k...),
					Account: append([]byte{}, v...),
				})
				total += sz
				k, v, err = c.Next()
				if err != nil {
					return err
				}
			}
			if k == nil && !resp.Completed {
				resp.Completed = true
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return resp, nil
	}
}

func makeStorageRangeMock(
	sourceDB kv.RwDB, defaultMax uint64,
) func(context.Context, p2p.SenderEncoder, peer.ID, *sync_pb.GetStorageRangeRequest) (*sync_pb.StorageRangeResponse, error) {
	return func(ctx context.Context, _ p2p.SenderEncoder, _ peer.ID, req *sync_pb.GetStorageRangeRequest) (*sync_pb.StorageRangeResponse, error) {
		resp := &sync_pb.StorageRangeResponse{}
		maxBytes := req.MaxBytes
		if maxBytes == 0 || maxBytes > defaultMax {
			maxBytes = defaultMax
		}
		var total uint64

		if err := sourceDB.View(ctx, func(tx kv.Tx) error {
			c, err := tx.Cursor(modules.Storage)
			if err != nil {
				return err
			}
			defer c.Close()

			var k, v []byte
			if len(req.Start) > 0 {
				k, v, err = c.Seek(req.Start)
			} else {
				k, v, err = c.Seek(req.Account)
			}
			if err != nil {
				return err
			}

			for k != nil {
				if !bytes.HasPrefix(k, req.Account) {
					resp.Completed = true
					break
				}
				if len(req.End) > 0 && bytes.Compare(k, req.End) >= 0 {
					resp.Completed = true
					break
				}
				sz := uint64(len(k) + len(v))
				if len(resp.Slots) > 0 && total+sz > maxBytes {
					resp.NextStart = append([]byte{}, k...)
					break
				}
				resp.Slots = append(resp.Slots, &sync_pb.StorageEntry{
					Key:   append([]byte{}, k...),
					Value: append([]byte{}, v...),
				})
				total += sz
				k, v, err = c.Next()
				if err != nil {
					return err
				}
			}
			if k == nil && !resp.Completed {
				resp.Completed = true
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return resp, nil
	}
}

func makeCodeMock(
	sourceDB kv.RwDB,
) func(context.Context, p2p.SenderEncoder, peer.ID, *sync_pb.GetCodeRequest) (*sync_pb.CodeResponse, error) {
	return func(ctx context.Context, _ p2p.SenderEncoder, _ peer.ID, req *sync_pb.GetCodeRequest) (*sync_pb.CodeResponse, error) {
		resp := &sync_pb.CodeResponse{}
		if err := sourceDB.View(ctx, func(tx kv.Tx) error {
			for _, hash := range req.Hashes {
				code, err := tx.GetOne(modules.Code, hash)
				if err != nil {
					return err
				}
				if code == nil {
					continue
				}
				resp.Codes = append(resp.Codes, &sync_pb.CodeEntry{
					Hash: append([]byte{}, hash...),
					Code: append([]byte{}, code...),
				})
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return resp, nil
	}
}

// ---------------------------------------------------------------------------
// Verification helpers
// ---------------------------------------------------------------------------

func verifyTableMatch(t *testing.T, srcDB, dstDB kv.RwDB, table, name string) {
	t.Helper()
	ctx := context.Background()

	src := collectTable(t, srcDB, table, ctx)
	dst := collectTable(t, dstDB, table, ctx)

	if len(src) != len(dst) {
		t.Errorf("%s count mismatch: source=%d target=%d", name, len(src), len(dst))
	}
	for k, sv := range src {
		dv, ok := dst[k]
		if !ok {
			t.Errorf("%s: key %x missing in target", name, []byte(k))
			continue
		}
		if sv != dv {
			t.Errorf("%s: value mismatch for key %x", name, []byte(k))
		}
	}
	for k := range dst {
		if _, ok := src[k]; !ok {
			t.Errorf("%s: unexpected key %x in target", name, []byte(k))
		}
	}
}

func collectTable(t *testing.T, db kv.RwDB, table string, ctx context.Context) map[string]string {
	t.Helper()
	entries := make(map[string]string)
	if err := db.View(ctx, func(tx kv.Tx) error {
		c, err := tx.Cursor(table)
		if err != nil {
			return err
		}
		defer c.Close()
		k, v, err := c.First()
		if err != nil {
			return err
		}
		for k != nil {
			entries[string(k)] = string(v)
			k, v, err = c.Next()
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("collectTable(%s): %v", table, err)
	}
	return entries
}

func verifyContractState(t *testing.T, db kv.RwDB, addr types.Address, codeHash types.Hash) {
	t.Helper()
	ctx := context.Background()

	if err := db.View(ctx, func(tx kv.Tx) error {
		// Check contract account.
		data, err := tx.GetOne(modules.Account, addr[:])
		if err != nil {
			return err
		}
		if data == nil {
			t.Errorf("USDT contract account missing at %x", addr[:])
			return nil
		}
		var acc account.StateAccount
		if err := acc.Unmarshal(data); err != nil {
			t.Errorf("unmarshal USDT account: %v", err)
			return nil
		}
		if acc.Incarnation != 1 {
			t.Errorf("USDT incarnation: want 1, got %d", acc.Incarnation)
		}
		if acc.CodeHash != codeHash {
			t.Errorf("USDT codeHash mismatch")
		}

		// Check contract code.
		code, err := tx.GetOne(modules.Code, codeHash[:])
		if err != nil {
			return err
		}
		if code == nil {
			t.Errorf("USDT code missing")
		} else if len(code) != 2048 {
			t.Errorf("USDT code length: want 2048, got %d", len(code))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func countTableEntries(t *testing.T, db kv.RwDB, table string) int {
	t.Helper()
	ctx := context.Background()
	var count int
	if err := db.View(ctx, func(tx kv.Tx) error {
		c, err := tx.Cursor(table)
		if err != nil {
			return err
		}
		defer c.Close()
		k, _, err := c.First()
		if err != nil {
			return err
		}
		for k != nil {
			count++
			k, _, err = c.Next()
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return count
}
