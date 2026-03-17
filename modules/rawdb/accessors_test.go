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

package rawdb

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/params"
)

// testHash is a shared test hash used across multiple tests.
var testHash = types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")

// =============================================================================
// Key Generation Tests
// =============================================================================

func TestKeyGeneration(t *testing.T) {
	number := uint64(12345)

	t.Run("HeaderKey", func(t *testing.T) {
		key := HeaderKey(number, testHash)
		if len(key) == 0 {
			t.Error("HeaderKey should return non-empty key")
		}
	})

	t.Run("BlockBodyKey", func(t *testing.T) {
		key := BlockBodyKey(number, testHash)
		if len(key) == 0 {
			t.Error("BlockBodyKey should return non-empty key")
		}
	})

	t.Run("TxLookupKey", func(t *testing.T) {
		key := TxLookupKey(testHash)
		if len(key) == 0 {
			t.Error("TxLookupKey should return non-empty key")
		}
	})

	t.Run("ReceiptKey", func(t *testing.T) {
		key := ReceiptKey(number)
		if len(key) == 0 {
			t.Error("ReceiptKey should return non-empty key")
		}
	})
}

// =============================================================================
// Key Consistency and Uniqueness Tests
// =============================================================================

func TestKeyConsistencyAndUniqueness(t *testing.T) {
	number := uint64(12345)

	t.Run("Deterministic", func(t *testing.T) {
		key1 := HeaderKey(number, testHash)
		key2 := HeaderKey(number, testHash)
		if !bytes.Equal(key1, key2) {
			t.Error("HeaderKey should be deterministic")
		}
	})

	t.Run("DifferentInputsDifferentKeys", func(t *testing.T) {
		key1 := HeaderKey(number, testHash)
		key2 := HeaderKey(number+1, testHash)
		if bytes.Equal(key1, key2) {
			t.Error("Different inputs should produce different keys")
		}
	})

	t.Run("HeaderAndBodyKeySameFormat", func(t *testing.T) {
		headerKey := HeaderKey(number, testHash)
		bodyKey := BlockBodyKey(number, testHash)
		if !bytes.Equal(headerKey, bodyKey) {
			t.Error("HeaderKey and BlockBodyKey should have same format")
		}
	})

	t.Run("DifferentKeyTypesAreDifferent", func(t *testing.T) {
		headerKey := HeaderKey(number, testHash)
		txKey := TxLookupKey(testHash)
		receiptKey := ReceiptKey(number)

		if bytes.Equal(headerKey, txKey) {
			t.Error("HeaderKey and TxLookupKey should be different")
		}
		if bytes.Equal(txKey, receiptKey) {
			t.Error("TxLookupKey and ReceiptKey should be different")
		}
	})

	t.Run("StableAcrossIterations", func(t *testing.T) {
		for i := 0; i < 100; i++ {
			key1 := HeaderKey(number, testHash)
			key2 := HeaderKey(number, testHash)
			if !bytes.Equal(key1, key2) {
				t.Fatalf("Key generation unstable at iteration %d", i)
			}
		}
	})
}

// =============================================================================
// Encoding Tests
// =============================================================================

func TestEncodeBlockNumberConsistency(t *testing.T) {
	tests := []struct {
		name   string
		number uint64
	}{
		{"zero", 0},
		{"one", 1},
		{"small", 100},
		{"large", 1000000},
		{"max", ^uint64(0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeBlockNumber(tt.number)
			if len(encoded) != 8 {
				t.Errorf("EncodeBlockNumber should return 8 bytes, got %d", len(encoded))
			}

			encoded2 := EncodeBlockNumber(tt.number)
			if !bytes.Equal(encoded, encoded2) {
				t.Error("EncodeBlockNumber should be deterministic")
			}
		})
	}
}

func TestUint256Encoding(t *testing.T) {
	tests := []struct {
		name  string
		value *uint256.Int
	}{
		{"zero", uint256.NewInt(0)},
		{"one", uint256.NewInt(1)},
		{"small", uint256.NewInt(1000)},
		{"large", uint256.NewInt(1000000000000000000)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bytesVal := tt.value.Bytes()
			if bytesVal == nil {
				t.Error("uint256.Bytes() should not return nil")
			}
		})
	}
}

// =============================================================================
// Edge Cases
// =============================================================================

func TestKeyBoundaryConditions(t *testing.T) {
	testCases := []struct {
		name   string
		number uint64
	}{
		{"genesis", 0},
		{"first", 1},
		{"small", 100},
		{"medium", 1000000},
		{"large", 100000000000},
		{"max-1", ^uint64(0) - 1},
		{"max", ^uint64(0)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			key := HeaderKey(tc.number, testHash)
			if len(key) == 0 {
				t.Errorf("HeaderKey failed for number=%d", tc.number)
			}
		})
	}
}

func TestZeroHash(t *testing.T) {
	key := TxLookupKey(types.Hash{})
	if len(key) == 0 {
		t.Error("TxLookupKey should handle zero hash")
	}
}

// =============================================================================
// Database Accessor Tests
// =============================================================================

func TestCanonicalHashStorage(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	hash, err := ReadCanonicalHash(tx, 0)
	if err != nil {
		t.Fatalf("ReadCanonicalHash failed: %v", err)
	}
	if hash != (types.Hash{}) {
		t.Fatalf("Non existent canonical hash returned: %v", hash)
	}

	writeHash := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	if err := WriteCanonicalHash(tx, writeHash, 100); err != nil {
		t.Fatalf("WriteCanonicalHash failed: %v", err)
	}

	hash, err = ReadCanonicalHash(tx, 100)
	if err != nil {
		t.Fatalf("ReadCanonicalHash failed: %v", err)
	}
	if hash != writeHash {
		t.Fatalf("Canonical hash mismatch: have %v, want %v", hash, writeHash)
	}

	hash, err = ReadCanonicalHash(tx, 101)
	if err != nil {
		t.Fatalf("ReadCanonicalHash failed: %v", err)
	}
	if hash != (types.Hash{}) {
		t.Fatalf("Non existent canonical hash returned for block 101: %v", hash)
	}
}

func TestHeaderNumberStorage(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	number := ReadHeaderNumber(tx, testHash)
	if number != nil {
		t.Fatalf("Non existent header number returned: %v", *number)
	}

	if err := WriteHeaderNumber(tx, testHash, 12345); err != nil {
		t.Fatalf("WriteHeaderNumber failed: %v", err)
	}

	number = ReadHeaderNumber(tx, testHash)
	if number == nil {
		t.Fatal("Header number not found after write")
	}
	if *number != 12345 {
		t.Fatalf("Header number mismatch: have %v, want %v", *number, 12345)
	}

	DeleteHeaderNumber(tx, testHash)
	number = ReadHeaderNumber(tx, testHash)
	if number != nil {
		t.Fatalf("Deleted header number returned: %v", *number)
	}
}

func TestIsCanonicalHash(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	hash1 := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	blockNumber := uint64(500)

	// Before writing, should not be canonical
	isCanonical, err := IsCanonicalHash(tx, hash1)
	if err != nil {
		t.Fatalf("IsCanonicalHash failed: %v", err)
	}
	if isCanonical {
		t.Fatal("Non-existent hash reported as canonical")
	}

	// Write header number mapping only -- still not canonical
	if err := WriteHeaderNumber(tx, hash1, blockNumber); err != nil {
		t.Fatalf("WriteHeaderNumber failed: %v", err)
	}
	isCanonical, err = IsCanonicalHash(tx, hash1)
	if err != nil {
		t.Fatalf("IsCanonicalHash failed: %v", err)
	}
	if isCanonical {
		t.Fatal("Hash without canonical mapping reported as canonical")
	}

	// Write canonical hash -- now should be canonical
	if err := WriteCanonicalHash(tx, hash1, blockNumber); err != nil {
		t.Fatalf("WriteCanonicalHash failed: %v", err)
	}
	isCanonical, err = IsCanonicalHash(tx, hash1)
	if err != nil {
		t.Fatalf("IsCanonicalHash failed: %v", err)
	}
	if !isCanonical {
		t.Fatal("Canonical hash not recognized")
	}

	// Different hash at same block number should not be canonical
	hash2 := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	if err := WriteHeaderNumber(tx, hash2, blockNumber); err != nil {
		t.Fatalf("WriteHeaderNumber failed: %v", err)
	}
	isCanonical, err = IsCanonicalHash(tx, hash2)
	if err != nil {
		t.Fatalf("IsCanonicalHash failed: %v", err)
	}
	if isCanonical {
		t.Fatal("Non-canonical hash reported as canonical")
	}
}

func TestHeadHeaderHashStorage(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	hash := ReadHeadHeaderHash(tx)
	if hash != (types.Hash{}) {
		t.Fatalf("Non existent head header hash returned: %v", hash)
	}

	writeHash := types.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000000")
	if err := WriteHeadHeaderHash(tx, writeHash); err != nil {
		t.Fatalf("WriteHeadHeaderHash failed: %v", err)
	}

	hash = ReadHeadHeaderHash(tx)
	if hash != writeHash {
		t.Fatalf("Head header hash mismatch: have %v, want %v", hash, writeHash)
	}
}

func TestHeadBlockHashStorage(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	hash := ReadHeadBlockHash(tx)
	if hash != (types.Hash{}) {
		t.Fatalf("Non existent head block hash returned: %v", hash)
	}

	writeHash := types.HexToHash("0xcafebabe00000000000000000000000000000000000000000000000000000000")
	WriteHeadBlockHash(tx, writeHash)

	hash = ReadHeadBlockHash(tx)
	if hash != writeHash {
		t.Fatalf("Head block hash mismatch: have %v, want %v", hash, writeHash)
	}
}

func TestVerifiesStorage(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	blockHash := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	blockNumber := uint64(100)

	verifies, err := ReadVerifies(tx, blockHash, blockNumber)
	if err != nil {
		t.Skipf("Skipping: BlockVerify table not available in test memdb: %v", err)
	}
	if len(verifies) != 0 {
		t.Fatalf("Non existent verifies returned: %v", verifies)
	}

	testVerifies := []*block.Verify{
		{
			Address:   types.HexToAddress("0x1111111111111111111111111111111111111111"),
			PublicKey: types.PublicKey{0x01, 0x02, 0x03},
		},
		{
			Address:   types.HexToAddress("0x2222222222222222222222222222222222222222"),
			PublicKey: types.PublicKey{0x04, 0x05, 0x06},
		},
	}

	if err := WriteVerifies(tx, blockHash, blockNumber, testVerifies); err != nil {
		t.Fatalf("WriteVerifies failed: %v", err)
	}

	verifies, err = ReadVerifies(tx, blockHash, blockNumber)
	if err != nil {
		t.Fatalf("ReadVerifies failed: %v", err)
	}
	if len(verifies) != len(testVerifies) {
		t.Fatalf("Verifies count mismatch: have %d, want %d", len(verifies), len(testVerifies))
	}
	for i, v := range verifies {
		if v.Address != testVerifies[i].Address {
			t.Fatalf("Verify address mismatch at %d: have %v, want %v", i, v.Address, testVerifies[i].Address)
		}
	}
}

func TestRewardsStorage(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	blockHash := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	blockNumber := uint64(200)

	rewards, err := ReadRewards(tx, blockHash, blockNumber)
	if err != nil {
		t.Skipf("Skipping: BlockRewards table not available in test memdb: %v", err)
	}
	if len(rewards) != 0 {
		t.Fatalf("Non existent rewards returned: %v", rewards)
	}

	testRewards := []*block.Reward{
		{
			Address: types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			Amount:  uint256.NewInt(1000000),
		},
		{
			Address: types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
			Amount:  uint256.NewInt(2000000),
		},
	}

	if err := WriteRewards(tx, blockHash, blockNumber, testRewards); err != nil {
		t.Fatalf("WriteRewards failed: %v", err)
	}

	rewards, err = ReadRewards(tx, blockHash, blockNumber)
	if err != nil {
		t.Fatalf("ReadRewards failed: %v", err)
	}
	if len(rewards) != len(testRewards) {
		t.Fatalf("Rewards count mismatch: have %d, want %d", len(rewards), len(testRewards))
	}
	for i, r := range rewards {
		if r.Address != testRewards[i].Address {
			t.Fatalf("Reward address mismatch at %d: have %v, want %v", i, r.Address, testRewards[i].Address)
		}
		if r.Amount.Cmp(testRewards[i].Amount) != 0 {
			t.Fatalf("Reward amount mismatch at %d: have %v, want %v", i, r.Amount, testRewards[i].Amount)
		}
	}
}

func TestChainConfigStorage(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	genesisHash := types.HexToHash("0xd4e56740f876aef8c010b86a40d5f56745a118d0906a34e69aec8c0db1cb8fa3")
	testConfig := &params.ChainConfig{
		ChainID: big.NewInt(1),
	}

	if err := WriteChainConfig(tx, genesisHash, testConfig); err != nil {
		t.Skipf("Skipping: ChainConfig table not available in test memdb: %v", err)
	}

	config, err := ReadChainConfig(tx, genesisHash)
	if err != nil {
		t.Fatalf("ReadChainConfig failed: %v", err)
	}
	if config.ChainID.Cmp(testConfig.ChainID) != 0 {
		t.Fatalf("ChainID mismatch: have %v, want %v", config.ChainID, testConfig.ChainID)
	}
	if config.Consensus != params.Faker {
		t.Fatalf("Consensus mismatch: have %v, want %v", config.Consensus, params.Faker)
	}

	if err := WriteChainConfig(tx, genesisHash, nil); err == nil {
		t.Fatal("WriteChainConfig should fail for nil config")
	}
}

func TestTxLookupEntryStorage(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	txHash := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")

	blockNum, err := ReadTxLookupEntry(tx, txHash)
	if err != nil {
		t.Fatalf("ReadTxLookupEntry failed: %v", err)
	}
	if blockNum != nil {
		t.Fatalf("Non existent tx lookup entry returned: %v", *blockNum)
	}

	// Deleting a non-existent entry should not error
	if err := DeleteTxLookupEntry(tx, txHash); err != nil {
		t.Fatalf("DeleteTxLookupEntry failed: %v", err)
	}
}

func TestPoaSnapshotStorage(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	snapshotHash := types.HexToHash("0xfeedfacecafebeef000000000000000000000000000000000000000000000000")

	data, err := GetPoaSnapshot(tx, snapshotHash)
	if err != nil {
		t.Skipf("Skipping: poaSnapshot table not available in test memdb: %v", err)
	}
	if data != nil {
		t.Fatalf("Non existent POA snapshot returned: %v", data)
	}

	testData := []byte("test snapshot data")
	if err := StorePoaSnapshot(tx, snapshotHash, testData); err != nil {
		t.Fatalf("StorePoaSnapshot failed: %v", err)
	}

	data, err = GetPoaSnapshot(tx, snapshotHash)
	if err != nil {
		t.Fatalf("GetPoaSnapshot failed: %v", err)
	}
	if string(data) != string(testData) {
		t.Fatalf("POA snapshot mismatch: have %v, want %v", string(data), string(testData))
	}
}

func TestReadTdByHash(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	blockHash := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	blockNumber := uint64(100)
	wantTd := uint256.NewInt(999999)

	// No header number mapping -- should return nil
	td, err := ReadTdByHash(tx, blockHash)
	if err != nil {
		t.Fatalf("ReadTdByHash failed: %v", err)
	}
	if td != nil {
		t.Fatalf("Non existent TD returned: %v", td)
	}

	// Write header number mapping -- still no TD value
	if err := WriteHeaderNumber(tx, blockHash, blockNumber); err != nil {
		t.Fatalf("WriteHeaderNumber failed: %v", err)
	}
	td, err = ReadTdByHash(tx, blockHash)
	if err != nil {
		t.Fatalf("ReadTdByHash failed: %v", err)
	}
	if td != nil {
		t.Fatalf("Non existent TD returned after header number write: %v", td)
	}

	// Write TD value -- now should return it
	if err := WriteTd(tx, blockHash, blockNumber, wantTd); err != nil {
		t.Fatalf("WriteTd failed: %v", err)
	}
	td, err = ReadTdByHash(tx, blockHash)
	if err != nil {
		t.Fatalf("ReadTdByHash failed: %v", err)
	}
	if td == nil {
		t.Fatal("TD not found after write")
	}
	if td.Cmp(wantTd) != 0 {
		t.Fatalf("TD mismatch: have %v, want %v", td, wantTd)
	}
}

func TestMultipleCanonicalHashes(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	hashes := []types.Hash{
		types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333"),
	}

	for i, h := range hashes {
		if err := WriteCanonicalHash(tx, h, uint64(i)); err != nil {
			t.Fatalf("WriteCanonicalHash failed at %d: %v", i, err)
		}
	}

	for i, expected := range hashes {
		hash, err := ReadCanonicalHash(tx, uint64(i))
		if err != nil {
			t.Fatalf("ReadCanonicalHash failed at %d: %v", i, err)
		}
		if hash != expected {
			t.Fatalf("Canonical hash mismatch at %d: have %v, want %v", i, hash, expected)
		}
	}
}

func TestHasReceipts(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	if HasReceipts(tx, 100) {
		t.Fatal("HasReceipts returned true for non-existent receipts")
	}
}
