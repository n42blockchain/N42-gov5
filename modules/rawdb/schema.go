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

// Package rawdb provides low-level database access for blockchain data.
//
// # Database Schema Documentation
//
// This file documents the database schema used by N42 blockchain.
// All bucket names and key formats are defined in modules/table.go.
//
// # Bucket Categories
//
// ## 1. State Buckets (modules/state/ access only)
//
//	Account          : address(20) -> account_proto
//	Storage          : address(20) + incarnation(2) + key(32) -> value(32)
//	Code             : code_hash(32) -> code_bytes
//	PlainContractCode: address(20) + incarnation(2) -> code_hash(32)
//	IncarnationMap   : address(20) -> incarnation(2)
//
// ## 2. History Buckets (modules/state/ access only)
//
//	AccountChangeSet : block_num(8) -> address(20) + account_proto
//	AccountsHistory  : address(20) + shard_id(8) -> roaring_bitmap
//	StorageChangeSet : block_num(8) + address(20) + incarnation(2) -> key(32) + value(32)
//	StorageHistory   : address(20) + key(32) + shard_id(8) -> roaring_bitmap
//
// ## 3. Chain Buckets (modules/rawdb/ access)
//
//	Headers          : block_num(8) + hash(32) -> header_proto
//	HeaderNumber     : hash(32) -> block_num(8)
//	HeaderTD         : block_num(8) + hash(32) -> td_bytes
//	HeaderCanonical  : block_num(8) -> hash(32)
//	HeadBlockKey     : "LastBlock" -> hash(32) + block_num(8)
//	HeadHeaderKey    : "LastHeader" -> hash(32) + block_num(8)
//
// ## 4. Block Buckets (modules/rawdb/ access)
//
//	BlockBody        : block_num(8) + hash(32) -> body_proto
//	BlockTx          : sequence(8) -> tx_proto
//	NonCanonicalTxs  : sequence(8) -> tx_rlp
//	MaxTxNum         : block_num(8) -> max_tx_num(8)
//	TxLookup         : tx_hash(32) -> block_num(8) + tx_index(4)
//	Senders          : block_num(8) + hash(32) -> [address(20)...]
//
// ## 5. Receipt/Log Buckets (modules/rawdb/ access)
//
//	Receipts         : block_num(8) -> receipts_proto
//	Log              : block_num(8) + tx_id(4) -> logs_proto
//	LogTopicIndex    : topic(32) + shard(2) -> roaring_bitmap
//	LogAddressIndex  : address(20) + shard(2) -> roaring_bitmap
//
// ## 6. Trace Buckets (modules/rawdb/ access)
//
//	CallTraceSet     : block_num(8) -> [address(20) + flags(1)...]
//	CallFromIndex    : address(20) + shard(2) -> roaring_bitmap
//	CallToIndex      : address(20) + shard(2) -> roaring_bitmap
//
// ## 7. Consensus Buckets (internal/consensus/ access)
//
//	SignersDB        : key -> signers_data
//	PoaSnapshot      : hash(32) -> snapshot_proto
//
// ## 8. Metadata Buckets
//
//	DatabaseInfo     : key -> value
//	ChainConfig      : "config" -> chain_config_json
//	Sequence         : table_name -> sequence(8)
//
// ## 9. Application Buckets
//
//	Reward           : key -> reward_data
//	Deposit          : key -> deposit_data
//	BlockVerify      : key -> verify_data
//	BlockRewards     : key -> rewards_data
//	Stake            : key -> stake_data
//
// # Key Encoding Conventions
//
// - Block numbers: 8 bytes, big-endian
// - Hashes: 32 bytes, raw bytes
// - Addresses: 20 bytes, raw bytes
// - Incarnation: 2 bytes, big-endian
// - Storage keys: 32 bytes, raw bytes
//
// # Access Patterns
//
// The following access patterns should be followed:
//
// 1. modules/state/ should only access State and History buckets
// 2. modules/rawdb/ should only access Chain, Block, Receipt, Log, and Trace buckets
// 3. internal/consensus/ should only access Consensus buckets
// 4. Application code should use the appropriate accessor functions
//
// # Migration Notes
//
// When modifying the schema:
// 1. Increment DatabaseInfo version
// 2. Add migration logic in database initialization
// 3. Update this documentation
// 4. Test backward compatibility
package rawdb

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules"
)

// =============================================================================
// Key Encoding Functions
// =============================================================================

// EncodeBlockNumber encodes a block number as 8 bytes big-endian
func EncodeBlockNumber(number uint64) []byte {
	return modules.EncodeBlockNumber(number)
}

// DecodeBlockNumber decodes a block number from 8 bytes big-endian
func DecodeBlockNumber(data []byte) uint64 {
	if len(data) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(data)
}

// HeaderKey returns the database key for a header (block_num + hash)
func HeaderKey(number uint64, hash types.Hash) []byte {
	key := make([]byte, 8+32)
	binary.BigEndian.PutUint64(key[:8], number)
	copy(key[8:], hash.Bytes())
	return key
}

// BlockBodyKey returns the database key for a block body (block_num + hash)
func BlockBodyKey(number uint64, hash types.Hash) []byte {
	return HeaderKey(number, hash) // Same format
}

// TxLookupKey returns the database key for transaction lookup
func TxLookupKey(txHash types.Hash) []byte {
	return txHash.Bytes()
}

// ReceiptKey returns the database key for receipts
func ReceiptKey(number uint64) []byte {
	return EncodeBlockNumber(number)
}

// =============================================================================
// Schema Version
// =============================================================================

const (
	// SchemaVersion is the current database schema version
	SchemaVersion = 1

	// SchemaVersionKey is the key for storing schema version
	SchemaVersionKey = "schema_version"
)

// =============================================================================
// Bucket Access Control (documentation purposes)
// =============================================================================

// StateBuckets lists buckets that should only be accessed by modules/state
var StateBuckets = []string{
	modules.Account,
	modules.Storage,
	modules.Code,
	modules.PlainContractCode,
	modules.IncarnationMap,
	modules.AccountChangeSet,
	modules.AccountsHistory,
	modules.StorageChangeSet,
	modules.StorageHistory,
}

// ChainBuckets lists buckets that should only be accessed by modules/rawdb
// Note: Some buckets (NonCanonicalTxs, MaxTxNum, LogTopicIndex, LogAddressIndex,
// CallTraceSet, CallFromIndex, CallToIndex) are documented but not yet in AstTableCfg
var ChainBuckets = []string{
	modules.Headers,
	modules.HeaderNumber,
	modules.HeaderTD,
	modules.HeaderCanonical,
	modules.HeadBlockKey,
	modules.HeadHeaderKey,
	modules.BlockBody,
	modules.BlockTx,
	modules.TxLookup,
	modules.Senders,
	modules.Receipts,
	modules.Log,
}

// ConsensusBuckets lists buckets that should only be accessed by internal/consensus
var ConsensusBuckets = []string{
	modules.SignersDB,
	modules.PoaSnapshot,
}

// MetadataBuckets lists metadata buckets
var MetadataBuckets = []string{
	modules.DatabaseInfo,
	modules.ChainConfig,
	modules.Sequence,
}

// ApplicationBuckets lists application-specific buckets
var ApplicationBuckets = []string{
	modules.Reward,
	modules.Deposit,
	modules.BlockVerify,
	modules.BlockRewards,
}

