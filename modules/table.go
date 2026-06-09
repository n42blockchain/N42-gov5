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
//
// Canonical MDBX bucket name registry for the N42 data model.
// Groups table constants into PlainState (Account, Storage, Code,
// Reward, Deposit, ContractCode, IncarnationMap), HistoryState
// (AccountChangeSet, AccountsHistory, StorageChangeSet), and
// DatabaseInfo/ChainConfig metadata tables. These names are the
// authoritative identifiers used by all rawdb/state accessors.

package modules

import (
	"sort"
	"strings"

	"github.com/n42blockchain/N42/lib/kv"
)

//const PlainState = "PlainState"

// StateInfo
const (
	// DatabaseInfo is used to store information about data layout.
	DatabaseInfo = "DbInfo"
	ChainConfig  = "ChainConfig"
)

// PlainState
const (
	//key - contract code hash
	//value - contract code
	Code    = "Code"    // contract code hash -> contract code
	Account = "Account" // address(un hashed) -> account encoded
	Storage = "Storage" // address(20B) + slot(32B) -> storage value
	Reward  = "Reward"  // ...
	Deposit = "Deposit" // Deposit info

	//key - addressHash+incarnation
	//value - code hash
	ContractCode = "HashedCodeHash"
)

// HistoryState
const (
	AccountChangeSet = "AccountChangeSet" // blockNum_u64 ->  address + account(encoded)
	AccountsHistory  = "AccountHistory"   // address + shard_id_u64 -> roaring bitmap  - list of block where it changed

	StorageChangeSet = "StorageChangeSet" // blockNum_u64 + address + incarnation_u64 ->  plain_storage_key + value
	StorageHistory   = "StorageHistory"   // address + storage_key + shard_id_u64 -> roaring bitmap - list of block where it changed
)

// Changeset V1 dictionaries: replace 20B addr / 32B codeHash inside ethexec
// changeset blobs with 3B big-endian ids backed by these MDBX tables. Ids are
// monotonic (assigned at intern time, never recycled). Both forward and
// reverse maps are stored so encoding can re-use existing ids and decoding can
// resolve ids back to raw values.
const (
	// CodeHashDict: codeHashID(3B BE) -> codeHash(32B). id=0 reserved for "no code".
	CodeHashDict = "CodeHashDict"
	// CodeHashIndex: codeHash(32B) -> codeHashID(3B BE). Reverse lookup for intern.
	CodeHashIndex = "CodeHashIndex"
	// AddrDict: addrID(3B BE) -> address(20B). id=0 reserved (never assigned).
	AddrDict = "AddrDict"
	// AddrIndex: address(20B) -> addrID(3B BE). Reverse lookup for intern.
	AddrIndex = "AddrIndex"
	// DictMeta: counter_key(string) -> uint32 BE. Holds "addr_next" / "codehash_next".
	DictMeta = "DictMeta"
)

// Block
const (
	Headers         = "Header"                 // block_num_u64 + hash -> header
	HeaderNumber    = "HeaderNumber"           // header_hash -> num_u64
	HeaderTD        = "HeadersTotalDifficulty" // block_num_u64 + hash -> td
	HeaderCanonical = "CanonicalHeader"        // block_num_u64 -> header hash

	// headBlockKey tracks the latest know full block's hash.
	HeadBlockKey = "LastBlock"

	HeadHeaderKey = "LastHeader"

	BlockBody       = "BlockBody"               // block_num_u64 + hash -> block body
	BlockTx         = "BlockTransaction"        // tbl_sequence_u64 -> (tx)
	NonCanonicalTxs = "NonCanonicalTransaction" // tbl_sequence_u64 -> rlp(tx)
	MaxTxNum        = "MaxTxNum"                // block_number_u64 -> max_tx_num_in_block_u64
	TxLookup        = "BlockTransactionLookup"  // hash -> transaction/receipt lookup metadata

	BlockVerify  = "BlockVerify"
	BlockRewards = "BlockRewards"

	// Transaction senders - stored separately from the block bodies
	Senders = "TxSender" // block_num_u64 + blockHash -> sendersList (no serialization format, every 20 bytes is new sender)

	Receipts = "Receipt"        // block_num_u64 -> canonical block receipts (non-canonical are not stored)
	Log      = "TransactionLog" // block_num_u64 + txId -> logs of transaction

	// Stores bitmap indices - in which block numbers saw logs of given 'address' or 'topic'
	// [addr or topic] + [4 bytes shard number] -> bitmap(blockN)
	// indices are sharded - because some bitmaps are >1Mb and when new incoming blocks process it
	//	 updates ~300 of bitmaps - by append small amount new values. It cause much big writes (MDBX does copy-on-write).
	//
	// if last existing shard size merge it with delta
	// if serialized size of delta > ShardLimit - break down to multiple shards
	// shard number - it's biggest value in bitmap
	LogTopicIndex   = "LogTopicIndex"
	LogAddressIndex = "LogAddressIndex"

	// CallTraceSet is the name of the table that contain the mapping of block number to the set (sorted) of all accounts
	// touched by call traces. It is DupSort-ed table
	// 8-byte BE block number -> account address -> two bits (one for "from", another for "to")
	CallTraceSet = "CallTraceSet"
	// Indices for call traces - have the same format as LogTopicIndex and LogAddressIndex
	// Store bitmap indices - in which block number we saw calls from (CallFromIndex) or to (CallToIndex) some addresses
	CallFromIndex = "CallFromIndex"
	CallToIndex   = "CallToIndex"

	Sequence = "Sequence" // tbl_name -> seq_u64

	Stake = "Stake" // stakes   ast_stake -> bytes

	// SnapSyncProgress stores snap sync download progress for crash recovery.
	// key: "pivot" -> pivot block number + hash
	// key: "account_cursor" -> last downloaded account address
	// key: "state" -> "running" | "completed"
	SnapSyncProgress = "SnapSyncProgress"

	// BlobSidecars stores blob sidecars for EIP-4844 blob transactions.
	// key: block_num(8) + block_hash(32) = 40 bytes
	// value: protobuf-encoded list of BlobSidecar
	BlobSidecars = "BlobSidecars"

	// SnapshotIndex stores metadata for periodic state snapshots.
	// key: block_number (8 bytes big-endian)
	// value: JSON-encoded SnapshotMeta (creation time, counts, etc.)
	// The pruner uses the oldest retained snapshot height as the lower
	// bound for changeset retention.
	SnapshotIndex = "SnapshotIndex"

	// DataColumns stores PeerDAS (EIP-7594) data columns.
	// key: block_hash(32) + col_index(8) = 40 bytes
	// value: encoded DataColumn (block number, KZG proof, column data)
	DataColumns = "DataColumns"

	// HotStuffState stores HotStuff-2 BFT consensus state for crash recovery.
	// key: "state" (fixed)
	// value: view(8) + consecutiveTimeouts(4) + lockedQC(var) + lastCommittedQC(var)
	HotStuffState = "HotStuffState"

	// SnapshotAccount stores the flat snapshot of account state.
	// key: address (20 bytes)
	// value: protobuf-encoded StateAccount (same format as Account table)
	SnapshotAccount = "SnapshotAccount"

	// SnapshotStorage stores the flat snapshot of contract storage.
	// key: address(20) + incarnation(2) + storageKey(32) = 54 bytes
	// value: storage value (DupSort, same format as Storage table)
	SnapshotStorage = "SnapshotStorage"

	// SnapshotMeta stores snapshot disk layer metadata for crash recovery.
	// key: "disk_root" -> root_hash(32) + block_num(8) = 40 bytes
	// key: "gen_marker" -> address(20) — generation cursor
	// key: "gen_complete" -> 0x01 — flag indicating generation is done
	SnapshotMeta = "SnapshotMeta"

	// SnapshotJournal stores serialized diff layers for crash recovery.
	// key: block_num (8 bytes big-endian)
	// value: serialized diff layer (accounts, deletions, storage)
	SnapshotJournal = "SnapshotJournal"

	// TxPoolJournal stores internal/txspool crash recovery data.
	// key: transaction hash (32 bytes)
	// value: protobuf-encoded transaction
	TxPoolJournal = "TxPoolJournal"

	// JMTNode stores Jellyfish Merkle Tree nodes, content-addressed by Blake3 hash.
	// key: blake3_hash (32 bytes)
	// value: serialized JMT node (internal/leaf/extension)
	JMTNode = "JMTNode"

	// JMTRoot stores the latest JMT root hash for crash recovery.
	// key: "root" (fixed)
	// value: blake3_hash (32 bytes)
	JMTRoot = "JMTRoot"

	// BMTNode stores content-addressed Binary Merkle Tree nodes.
	// key: blake3_hash (32 bytes)
	// value: internal(65B) or leaf(33+B)
	BMTNode = "BMTNode"

	// BMTRoot stores the latest BMT root hash and version for recovery.
	BMTRoot = "BMTRoot"

	// QMDB-twig store (append-only, position-addressed). Keys are sequential.
	// QMDBEntries: BE8(slot) -> keyHash(32) || value
	QMDBEntries = "qmdbEntries"
	// QMDBTwigs: BE8(twigID) -> twigRoot(32) || activeBits(256)
	QMDBTwigs = "qmdbTwigs"
	// QMDBMeta: "root"|"version"|"nextSlot"|"replay_chain_head"|"replay_src_height"
	QMDBMeta = "qmdbMeta"

	// VerkleNode stores content-addressed Verkle nodes.
	// key: commitment_bytes (64 bytes)   value: serialized_node (97B internal / 288B+ leaf)
	VerkleNode = "VerkleNode"

	// VerkleRoot stores the latest Verkle root commitment and version for recovery.
	VerkleRoot = "VerkleRoot"

	// HashedAccounts stores accounts keyed by keccak256(address).
	// Used by CalcTrieRoot (erigon2.7 trie) for standard Ethereum state root.
	// key: keccak256(address) [32B]
	// value: account encoded (V2 format)
	HashedAccounts = "HashedAccount"

	// HashedStorage stores storage keyed by keccak256(address) + incarnation.
	// DupSort: key = keccak256(address)[32B] + incarnation[8B], value = keccak256(slot)[32B] + value
	HashedStorage = "HashedStorage"

	// TrieOfAccounts stores intermediate trie node hashes for the account trie.
	// key: nibble prefix (variable length)
	// value: MarshalTrieNode(hasState, hasTree, hasHash, hashes)
	TrieOfAccounts = "TrieAccount"

	// TrieOfStorage stores intermediate trie node hashes for storage tries.
	// DupSort: key = accountHash[32B] + incarnation[8B] + nibble prefix
	TrieOfStorage = "TrieStorage"

	// MPTBranch stores Ethereum MPT (HexPatriciaHashed) branch nodes.
	// key: nibble prefix (variable length)
	// value: [afterMap:2B][cell encodings...]
	MPTBranch = "MPTBranch"

	// MPTRoot stores the latest MPT state root hash for crash recovery.
	// key: "root" (fixed)
	// value: state root hash (32 bytes)
	MPTRoot = "MPTRoot"

	// LtHashDigest stores the 2048-byte running LtHash state digest for crash recovery.
	// key: "digest" (fixed)
	// value: 2048 bytes (lattice hash digest)
	LtHashDigest = "LtHashDigest"

	// BlockWitness stores per-block execution witness (input data stream).
	// A stateless client (mobile SDK) can verify/execute a block using only
	// the block header + transactions + this witness, without full state.
	// key: block number (8 bytes, big-endian)
	// value: serialized witness (accounts + storage + code accessed), nil for empty blocks
	BlockWitness = "BlockWitness"

	// ConsensusEvidence stores per-block consensus evidence (QC + mobile BLS).
	// Moved out of Header.Extra to keep extraData ≤32 bytes (ETH standard).
	// key: block_number (8 bytes, big-endian)
	// value: ConsensusEvidence (compact binary, 140-1600 bytes)
	ConsensusEvidence = "ConsensusEvidence"

	// ContentStore stores content-addressed blobs for the CAS precompile.
	// key: keccak256(data) (32 bytes)
	// value: raw data (up to 24KB)
	ContentStore = "ContentStore"

	// TorrentHashMap maps keccak256 content hashes to BitTorrent infohashes.
	// key: keccak256(data) (32 bytes)
	// value: infohash (20 bytes) + metainfo length (4 bytes) + metainfo
	TorrentHashMap = "TorrentHashMap"

	// Ed2kHashMap maps keccak256 content hashes to ed2k/eDonkey hashes.
	// key: keccak256(data) (32 bytes)
	// value: ed2k hash (16 bytes, MD4)
	Ed2kHashMap = "Ed2kHashMap"

	// MagnetURIStore stores magnet URIs for content discovery.
	// key: keccak256(data) (32 bytes)
	// value: magnet URI string (UTF-8)
	MagnetURIStore = "MagnetURIStore"
)

const (
	SignersDB   = "signersDB"
	PoaSnapshot = "poaSnapshot"
)

var n42Tables = []string{
	Code,
	Account,
	Storage,

	DatabaseInfo,
	ChainConfig,

	AccountsHistory,
	AccountChangeSet,
	StorageHistory,
	StorageChangeSet,

	CodeHashDict,
	CodeHashIndex,
	AddrDict,
	AddrIndex,
	DictMeta,

	Headers,
	HeaderTD,
	HeaderCanonical,
	HeaderNumber,

	HeadBlockKey,
	HeadHeaderKey,

	BlockBody,
	BlockTx,

	TxLookup,
	Senders,
	Receipts,
	Log,

	LogTopicIndex,
	LogAddressIndex,

	SignersDB,
	PoaSnapshot,
	Sequence,

	Reward,
	Deposit,
	BlockVerify,
	BlockRewards,
	SnapSyncProgress,
	SnapshotIndex,
	BlobSidecars,
	DataColumns,
	HotStuffState,
	SnapshotAccount,
	SnapshotStorage,
	SnapshotMeta,
	SnapshotJournal,
	TxPoolJournal,
	JMTNode,
	JMTRoot,
	BMTNode,
	BMTRoot,
	QMDBEntries,
	QMDBTwigs,
	QMDBMeta,
	VerkleNode,
	VerkleRoot,
	ConsensusEvidence,
	HashedAccounts,
	HashedStorage,
	TrieOfAccounts,
	TrieOfStorage,
	MPTBranch,
	MPTRoot,
	LtHashDigest,
	BlockWitness,
	ContentStore,
	TorrentHashMap,
	Ed2kHashMap,
	MagnetURIStore,
}

var N42TableCfg = kv.TableCfg{
	AccountChangeSet: {Flags: kv.DupSort},
	StorageChangeSet: {Flags: kv.DupSort},
	Storage: {
		Flags:                     kv.DupSort,
		AutoDupSortKeysConversion: true,
		DupFromLen:                52, // addr(20)+slot(32), no incarnation
		DupToLen:                  20, // DupSort key = addr(20)
	},
	SnapshotStorage: {
		Flags:                     kv.DupSort,
		AutoDupSortKeysConversion: true,
		DupFromLen:                52,
		DupToLen:                  20,
	},
	HashedStorage: {
		Flags:                     kv.DupSort,
		AutoDupSortKeysConversion: true,
		DupFromLen:                64, // addrHash(32)+slotHash(32) (incarnation removed)
		DupToLen:                  32, // DupSort key = addrHash(32)
	},
	TrieOfStorage: {Flags: kv.DupSort},
}

func N42Init() {
	sort.SliceStable(n42Tables, func(i, j int) bool {
		return strings.Compare(n42Tables[i], n42Tables[j]) < 0
	})
	for _, name := range n42Tables {
		_, ok := N42TableCfg[name]
		if !ok {
			N42TableCfg[name] = kv.TableCfgItem{}
		}
	}
}
