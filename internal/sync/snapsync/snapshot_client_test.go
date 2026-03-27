package snapsync

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/snapshot"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestSnapshotSyncIntegration simulates a full snapshot-based sync:
// 1. Populate a source DB with accounts + storage + code
// 2. Override send functions to read from source and return compressed batches
// 3. Run SnapshotManager against target DB
// 4. Verify target matches source
func TestSnapshotSyncIntegration(t *testing.T) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	sourceDB := memdb.NewTestDB(t)
	targetDB := memdb.NewTestDB(t)

	emptyCodeHash := crypto.Keccak256Hash(nil)
	contractCodeHash := types.BytesHash(crypto.Keccak256([]byte("contract-code")))

	// Populate source DB.
	const numEOAs = 100
	const numStorageSlots = 50
	if err := sourceDB.Update(context.Background(), func(tx kv.RwTx) error {
		// Save snapshot meta so validation passes.
		if err := tx.Put(modules.SnapshotIndex, []byte{0, 0, 0, 0, 0, 0, 0x27, 0x10}, // block 10000
			[]byte(`{"block_number":10000,"created_at":1000000,"account_count":101,"storage_count":50,"code_count":1}`)); err != nil {
			return err
		}

		// EOAs.
		for i := 0; i < numEOAs; i++ {
			addr := make([]byte, 20)
			addr[0] = byte(i >> 8)
			addr[1] = byte(i)
			acc := account.StateAccount{
				Initialised: true,
				Nonce:       uint64(i),
				Balance:     *uint256.NewInt(uint64(1000 + i)),
				CodeHash:    emptyCodeHash,
			}
			data, err := acc.Marshal()
			if err != nil {
				return err
			}
			if err := tx.Put(modules.Account, addr, data); err != nil {
				return err
			}
		}

		// Contract with code + storage.
		contractAddr := make([]byte, 20)
		contractAddr[19] = 0xFF
		contract := account.StateAccount{
			Initialised: true,
			Nonce:       1,
			Balance:     *uint256.NewInt(0),
			CodeHash:    contractCodeHash,
			Incarnation: 1,
		}
		cData, err := contract.Marshal()
		if err != nil {
			return err
		}
		if err := tx.Put(modules.Account, contractAddr, cData); err != nil {
			return err
		}

		// Storage slots for contract.
		for i := 0; i < numStorageSlots; i++ {
			sKey := make([]byte, 32)
			sKey[31] = byte(i)
			fullKey := modules.PlainGenerateCompositeStorageKey(contractAddr, 1, sKey)
			val := make([]byte, 32)
			val[0] = byte(i + 1)
			if err := tx.Put(modules.Storage, fullKey, val); err != nil {
				return err
			}
		}

		// Code.
		return tx.Put(modules.Code, contractCodeHash[:], []byte("contract-code"))
	}); err != nil {
		t.Fatal(err)
	}

	// Also write snapshot meta to target DB (SnapshotManager validates it exists).
	if err := targetDB.Update(context.Background(), func(tx kv.RwTx) error {
		return tx.Put(modules.SnapshotIndex, []byte{0, 0, 0, 0, 0, 0, 0x27, 0x10},
			[]byte(`{"block_number":10000,"created_at":1000000,"account_count":101,"storage_count":50,"code_count":1}`))
	}); err != nil {
		t.Fatal(err)
	}

	// Override send functions to serve from source DB with compression.
	origAccountRange := sendGetSnapshotAccountRange
	origStorageRange := sendGetSnapshotStorageRange
	origCode := sendGetCode
	defer func() {
		sendGetSnapshotAccountRange = origAccountRange
		sendGetSnapshotStorageRange = origStorageRange
		sendGetCode = origCode
	}()

	sendGetSnapshotAccountRange = func(ctx context.Context, _ p2p.SenderEncoder, _ peer.ID, req *sync_pb.GetSnapshotAccountRangeRequest) (*sync_pb.CompressedBatchResponse, error) {
		resp := &sync_pb.CompressedBatchResponse{}
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

			var batch []snapshot.BatchEntry
			for k != nil && len(batch) < 4096 {
				key := make([]byte, len(k))
				copy(key, k)
				val := make([]byte, len(v))
				copy(val, v)
				batch = append(batch, snapshot.BatchEntry{Key: key, Value: val})
				k, v, err = c.Next()
				if err != nil {
					return err
				}
			}

			compressed, err := snapshot.EncodeBatch(batch)
			if err != nil {
				return err
			}
			resp.EntryCount = uint32(len(batch))
			resp.Data = compressed
			if k != nil {
				resp.NextStart = make([]byte, len(k))
				copy(resp.NextStart, k)
			} else {
				resp.Completed = true
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return resp, nil
	}

	sendGetSnapshotStorageRange = func(ctx context.Context, _ p2p.SenderEncoder, _ peer.ID, req *sync_pb.GetSnapshotStorageRangeRequest) (*sync_pb.CompressedBatchResponse, error) {
		resp := &sync_pb.CompressedBatchResponse{}
		if err := sourceDB.View(ctx, func(tx kv.Tx) error {
			c, err := tx.Cursor(modules.Storage)
			if err != nil {
				return err
			}
			defer c.Close()

			prefix := req.Account
			var k, v []byte
			if len(req.Start) > 0 {
				// Reconstruct full key: account(20) + incarnation(2) + start(32)
				seekKey := modules.PlainGenerateCompositeStorageKey(req.Account, req.Incarnation, req.Start)
				k, v, err = c.Seek(seekKey)
			} else {
				k, v, err = c.Seek(prefix)
			}
			if err != nil {
				return err
			}

			var batch []snapshot.BatchEntry
			for k != nil && bytes.HasPrefix(k, prefix) && len(batch) < 4096 {
				// Extract location key (last 32 bytes).
				if len(k) >= 54 {
					loc := make([]byte, 32)
					copy(loc, k[22:54])
					val := make([]byte, len(v))
					copy(val, v)
					batch = append(batch, snapshot.BatchEntry{Key: loc, Value: val})
				}
				k, v, err = c.Next()
				if err != nil {
					return err
				}
			}

			compressed, err := snapshot.EncodeBatch(batch)
			if err != nil {
				return err
			}
			resp.EntryCount = uint32(len(batch))
			resp.Data = compressed
			if k != nil && bytes.HasPrefix(k, prefix) {
				resp.NextStart = make([]byte, 32)
				copy(resp.NextStart, k[22:54])
			} else {
				resp.Completed = true
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return resp, nil
	}

	sendGetCode = func(ctx context.Context, _ p2p.SenderEncoder, _ peer.ID, req *sync_pb.GetCodeRequest) (*sync_pb.CodeResponse, error) {
		resp := &sync_pb.CodeResponse{}
		if err := sourceDB.View(ctx, func(tx kv.Tx) error {
			for _, hash := range req.Hashes {
				code, err := tx.GetOne(modules.Code, hash)
				if err != nil {
					return err
				}
				if code != nil {
					resp.Codes = append(resp.Codes, &sync_pb.CodeEntry{Hash: hash, Code: code})
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return resp, nil
	}

	// Run SnapshotManager (single-threaded: call pickTask/execute directly).
	cfg := &conf.SnapSyncConfig{
		Enable:         true,
		MaxBytesPerReq: 512 * 1024,
	}
	sm := NewSnapshotManager(cfg, nil, targetDB)
	sm.snapshotBlock = 10000

	sm.mu.Lock()
	sm.accountTasks = []*RangeTask{{Kind: TaskAccount}}
	sm.mu.Unlock()

	rounds := 0
	for {
		sm.mu.Lock()
		allDone := len(sm.accountTasks) == 0 && len(sm.storageTasks) == 0 && len(sm.codeTasks) == 0
		sm.mu.Unlock()
		if allDone {
			break
		}
		if rounds > 50 {
			t.Fatal("too many rounds")
		}

		task := sm.pickTask()
		if task == nil {
			break
		}
		task.Assigned = "test-peer"
		task.AssignedAt = time.Now()
		sm.executeTask(context.Background(), task)
		rounds++
	}

	sm.mu.Lock()
	t.Logf("Sync done in %d rounds: accounts=%d storage=%d codes=%d bytes=%d",
		rounds, sm.accountsDone, sm.storagesDone, sm.codesDone, sm.bytesReceived)
	sm.mu.Unlock()

	// Verify target matches source.
	verifyTablesMatch(t, sourceDB, targetDB, modules.Account)
	verifyTablesMatch(t, sourceDB, targetDB, modules.Storage)
	verifyTablesMatch(t, sourceDB, targetDB, modules.Code)
}

func verifyTablesMatch(t *testing.T, sourceDB, targetDB kv.RwDB, table string) {
	t.Helper()
	sourceEntries := readAllEntries(t, sourceDB, table)
	targetEntries := readAllEntries(t, targetDB, table)

	if len(sourceEntries) != len(targetEntries) {
		t.Errorf("table %s: source has %d entries, target has %d",
			table, len(sourceEntries), len(targetEntries))
		return
	}

	for i := range sourceEntries {
		if !bytes.Equal(sourceEntries[i].key, targetEntries[i].key) {
			t.Errorf("table %s entry %d: key mismatch", table, i)
		}
		if !bytes.Equal(sourceEntries[i].val, targetEntries[i].val) {
			t.Errorf("table %s entry %d: value mismatch", table, i)
		}
	}
}

type kvEntry struct {
	key, val []byte
}

func readAllEntries(t *testing.T, db kv.RwDB, table string) []kvEntry {
	t.Helper()
	var entries []kvEntry
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		c, err := tx.Cursor(table)
		if err != nil {
			return err
		}
		defer c.Close()
		for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
			if err != nil {
				return err
			}
			// Skip SnapshotIndex entries in Account table.
			if table == modules.Account && len(k) != 20 {
				continue
			}
			key := make([]byte, len(k))
			copy(key, k)
			val := make([]byte, len(v))
			copy(val, v)
			entries = append(entries, kvEntry{key, val})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return entries
}
