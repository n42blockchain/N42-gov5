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
// EVM execution metrics: per-opcode latency histograms, gas used
// distributions, precompile invocation counts and snapshot / journal
// bookkeeping timers used to diagnose execution hot spots in the
// intra-block state machine.

package metrics

import (
	prometheus "github.com/n42blockchain/N42/common/metrics"
)

// =============================================================================
// EVM Execution Metrics
// =============================================================================

var (
	// Block processing timing
	BlockProcessingSeconds   = prometheus.GetOrCreateSummary("n42_block_processing_seconds")
	BlockValidationSeconds   = prometheus.GetOrCreateSummary("n42_block_validation_seconds")
	BlockExecutionSeconds    = prometheus.GetOrCreateSummary("n42_block_execution_seconds")
	BlockFinalizationSeconds = prometheus.GetOrCreateSummary("n42_block_finalization_seconds")
	BlockCommitSeconds       = prometheus.GetOrCreateSummary("n42_block_commit_seconds")

	// Per-block gas metrics
	BlockGasUsed    = prometheus.GetOrCreateCounter("n42_block_gas_used_total", true)
	BlockGasLimit   = prometheus.GetOrCreateCounter("n42_block_gas_limit", true)
	BlockGasPercent = prometheus.GetOrCreateCounter("n42_block_gas_utilization_percent", true)
	BlockTxCount    = prometheus.GetOrCreateCounter("n42_block_tx_count_total", true)

	// EVM call metrics
	EVMCallCount      = prometheus.GetOrCreateCounter("n42_evm_call_total", true)
	EVMCreateCount    = prometheus.GetOrCreateCounter("n42_evm_create_total", true)
	EVMRevertCount    = prometheus.GetOrCreateCounter("n42_evm_revert_total", true)
	EVMPrecompileCall = prometheus.GetOrCreateCounter("n42_evm_precompile_call_total", true)

	// State metrics
	StateAccountReads   = prometheus.GetOrCreateCounter("n42_state_account_reads_total", true)
	StateStorageReads   = prometheus.GetOrCreateCounter("n42_state_storage_reads_total", true)
	StateAccountWrites  = prometheus.GetOrCreateCounter("n42_state_account_writes_total", true)
	StateStorageWrites  = prometheus.GetOrCreateCounter("n42_state_storage_writes_total", true)
	StateCommitDirty    = prometheus.GetOrCreateCounter("n42_state_commit_dirty_objects", true)
	StateSnapshotLayers = prometheus.GetOrCreateCounter("n42_state_snapshot_layers", true)
)

// =============================================================================
// Chain & Reorg Metrics
// =============================================================================

var (
	// Chain head tracking
	ChainHeadBlock     = prometheus.GetOrCreateCounter("n42_chain_head_block", true)
	ChainHeadTimestamp  = prometheus.GetOrCreateCounter("n42_chain_head_timestamp", true)
	ChainHeadGasUsed   = prometheus.GetOrCreateCounter("n42_chain_head_gas_used", true)

	// Reorg tracking
	ReorgCount      = prometheus.GetOrCreateCounter("n42_chain_reorg_total", true)
	ReorgMaxDepth   = prometheus.GetOrCreateCounter("n42_chain_reorg_max_depth", true)
	ReorgAddBlocks  = prometheus.GetOrCreateCounter("n42_chain_reorg_add_blocks_total", true)
	ReorgDropBlocks = prometheus.GetOrCreateCounter("n42_chain_reorg_drop_blocks_total", true)

	// Block propagation
	BlockPropagationSeconds = prometheus.GetOrCreateSummary("n42_block_propagation_seconds")
	BlockImportSeconds      = prometheus.GetOrCreateSummary("n42_block_import_seconds")
)

// =============================================================================
// Fee Market Metrics (EIP-1559)
// =============================================================================

var (
	BaseFeeGwei           = prometheus.GetOrCreateCounter("n42_basefee_gwei", true)
	BlobBaseFeeGwei       = prometheus.GetOrCreateCounter("n42_blob_basefee_gwei", true)
	ExcessBlobGas         = prometheus.GetOrCreateCounter("n42_excess_blob_gas", true)
	BlobGasUsed           = prometheus.GetOrCreateCounter("n42_blob_gas_used", true)
	BlobCount             = prometheus.GetOrCreateCounter("n42_blob_count_total", true)
	TxPriorityFeeAvgGwei  = prometheus.GetOrCreateCounter("n42_tx_priority_fee_avg_gwei", true)
)

// =============================================================================
// Transaction Lifecycle Metrics
// =============================================================================

var (
	TxReceived     = prometheus.GetOrCreateCounter("n42_tx_received_total", true)
	TxValidated    = prometheus.GetOrCreateCounter("n42_tx_validated_total", true)
	TxIncluded     = prometheus.GetOrCreateCounter("n42_tx_included_total", true)
	TxFailed       = prometheus.GetOrCreateCounter("n42_tx_failed_total", true)
	TxPendingTime  = prometheus.GetOrCreateSummary("n42_tx_pending_seconds")

	// By type
	TxLegacyCount     = prometheus.GetOrCreateCounter("n42_tx_type_legacy_total", true)
	TxAccessListCount = prometheus.GetOrCreateCounter("n42_tx_type_access_list_total", true)
	TxDynamicFeeCount = prometheus.GetOrCreateCounter("n42_tx_type_dynamic_fee_total", true)
	TxBlobCount       = prometheus.GetOrCreateCounter("n42_tx_type_blob_total", true)
	TxSetCodeCount    = prometheus.GetOrCreateCounter("n42_tx_type_set_code_total", true)
)

// =============================================================================
// Engine API Metrics
// =============================================================================

var (
	EngineNewPayloadCount  = prometheus.GetOrCreateCounter("n42_engine_new_payload_total", true)
	EngineNewPayloadValid  = prometheus.GetOrCreateCounter("n42_engine_new_payload_valid_total", true)
	EngineNewPayloadInvalid = prometheus.GetOrCreateCounter("n42_engine_new_payload_invalid_total", true)
	EngineForkchoiceCount  = prometheus.GetOrCreateCounter("n42_engine_forkchoice_total", true)
	EngineGetPayloadCount  = prometheus.GetOrCreateCounter("n42_engine_get_payload_total", true)
	EngineNewPayloadSeconds = prometheus.GetOrCreateSummary("n42_engine_new_payload_seconds")
	EngineForkchoiceSeconds = prometheus.GetOrCreateSummary("n42_engine_forkchoice_seconds")
)

// =============================================================================
// RPC Metrics
// =============================================================================

var (
	RPCRequestsTotal   = prometheus.GetOrCreateCounter("n42_rpc_requests_total", true)
	RPCErrorsTotal     = prometheus.GetOrCreateCounter("n42_rpc_errors_total", true)
	RPCRequestSeconds  = prometheus.GetOrCreateSummary("n42_rpc_request_seconds")
	RPCWebSocketConns  = prometheus.GetOrCreateCounter("n42_rpc_ws_connections", true)
	RPCBatchSize       = prometheus.GetOrCreateSummary("n42_rpc_batch_size")
)

// =============================================================================
// JMT State Commitment Metrics
// =============================================================================

var (
	JMTUpdateSeconds   = prometheus.GetOrCreateSummary("n42_jmt_update_seconds")
	JMTFlushSeconds    = prometheus.GetOrCreateSummary("n42_jmt_flush_seconds")
	JMTDirtyNodes      = prometheus.GetOrCreateCounter("n42_jmt_dirty_nodes", true)
	JMTProofSeconds    = prometheus.GetOrCreateSummary("n42_jmt_proof_seconds")
	JMTTreeHeight      = prometheus.GetOrCreateCounter("n42_jmt_tree_height", true)
	JMTCacheHits       = prometheus.GetOrCreateCounter("n42_jmt_cache_hits_total", true)
	JMTCacheMisses     = prometheus.GetOrCreateCounter("n42_jmt_cache_misses_total", true)
	JMTCacheSize       = prometheus.GetOrCreateCounter("n42_jmt_cache_size", true)
	JMTGCDeletedNodes  = prometheus.GetOrCreateCounter("n42_jmt_gc_deleted_total", true)
	JMTGCPendingNodes  = prometheus.GetOrCreateCounter("n42_jmt_gc_pending", true)
)

// =============================================================================
// P2P Network Detailed Metrics
// =============================================================================

var (
	P2PPeersConnected     = prometheus.GetOrCreateCounter("n42_p2p_peers_connected", true)
	P2PPeersDisconnected  = prometheus.GetOrCreateCounter("n42_p2p_peers_disconnected_total", true)
	P2PBytesReceived      = prometheus.GetOrCreateCounter("n42_p2p_bytes_received_total", true)
	P2PBytesSent          = prometheus.GetOrCreateCounter("n42_p2p_bytes_sent_total", true)
	P2PMessagesReceived   = prometheus.GetOrCreateCounter("n42_p2p_messages_received_total", true)
	P2PMessagesSent       = prometheus.GetOrCreateCounter("n42_p2p_messages_sent_total", true)
	P2PDiscoveryLookups   = prometheus.GetOrCreateCounter("n42_p2p_discovery_lookups_total", true)
	P2PDiscoverySeeded    = prometheus.GetOrCreateCounter("n42_p2p_discovery_seeded_total", true)
	P2PDialAttempts       = prometheus.GetOrCreateCounter("n42_p2p_dial_attempts_total", true)
	P2PDialErrors         = prometheus.GetOrCreateCounter("n42_p2p_dial_errors_total", true)
	P2PHandshakeSeconds   = prometheus.GetOrCreateSummary("n42_p2p_handshake_seconds")
	P2PBadPeers           = prometheus.GetOrCreateCounter("n42_p2p_bad_peers_total", true)
)

// =============================================================================
// Database MDBX Detailed Metrics
// =============================================================================

var (
	DBTableCount        = prometheus.GetOrCreateCounter("n42_db_table_count", true)
	DBDiskSizeBytes     = prometheus.GetOrCreateCounter("n42_db_disk_size_bytes", true)
	DBFreelistPages     = prometheus.GetOrCreateCounter("n42_db_freelist_pages", true)
	DBPageSize          = prometheus.GetOrCreateCounter("n42_db_page_size_bytes", true)
	DBReadTxActive      = prometheus.GetOrCreateCounter("n42_db_read_tx_active", true)
	DBWriteTxActive     = prometheus.GetOrCreateCounter("n42_db_write_tx_active", true)
	DBReadTxSeconds     = prometheus.GetOrCreateSummary("n42_db_read_tx_seconds")
	DBWriteTxSeconds    = prometheus.GetOrCreateSummary("n42_db_write_tx_seconds")
	DBGetOps            = prometheus.GetOrCreateCounter("n42_db_get_ops_total", true)
	DBPutOps            = prometheus.GetOrCreateCounter("n42_db_put_ops_total", true)
	DBDeleteOps         = prometheus.GetOrCreateCounter("n42_db_delete_ops_total", true)
	DBCursorOps         = prometheus.GetOrCreateCounter("n42_db_cursor_ops_total", true)
)

// =============================================================================
// Consensus Detailed Metrics
// =============================================================================

var (
	ConsensusRoundDuration    = prometheus.GetOrCreateSummary("n42_consensus_round_seconds")
	ConsensusProposalLatency  = prometheus.GetOrCreateSummary("n42_consensus_proposal_latency_seconds")
	ConsensusVoteLatency      = prometheus.GetOrCreateSummary("n42_consensus_vote_latency_seconds")
	ConsensusCommitLatency    = prometheus.GetOrCreateSummary("n42_consensus_commit_latency_seconds")
	ConsensusTimeouts         = prometheus.GetOrCreateCounter("n42_consensus_timeouts_total", true)
	ConsensusViewChanges      = prometheus.GetOrCreateCounter("n42_consensus_view_changes_total", true)
	ConsensusBlocksCommitted  = prometheus.GetOrCreateCounter("n42_consensus_blocks_committed_total", true)
	ConsensusValidators       = prometheus.GetOrCreateCounter("n42_consensus_validators_active", true)
	ConsensusQuorumSize       = prometheus.GetOrCreateCounter("n42_consensus_quorum_size", true)
	ConsensusForkChoice       = prometheus.GetOrCreateCounter("n42_consensus_fork_choice_total", true)
)

// =============================================================================
// Memory & Cache Metrics
// =============================================================================

var (
	CacheShardedHits      = prometheus.GetOrCreateCounter("n42_cache_sharded_hits_total", true)
	CacheShardedMisses    = prometheus.GetOrCreateCounter("n42_cache_sharded_misses_total", true)
	CacheShardedSize      = prometheus.GetOrCreateCounter("n42_cache_sharded_size", true)
	CacheShardedEvictions = prometheus.GetOrCreateCounter("n42_cache_sharded_evictions_total", true)
	CacheBlockHits        = prometheus.GetOrCreateCounter("n42_cache_block_hits_total", true)
	CacheBlockMisses      = prometheus.GetOrCreateCounter("n42_cache_block_misses_total", true)
	CacheHeaderHits       = prometheus.GetOrCreateCounter("n42_cache_header_hits_total", true)
	CacheHeaderMisses     = prometheus.GetOrCreateCounter("n42_cache_header_misses_total", true)
	CacheTDHits           = prometheus.GetOrCreateCounter("n42_cache_td_hits_total", true)
	CacheTDMisses         = prometheus.GetOrCreateCounter("n42_cache_td_misses_total", true)
)

// =============================================================================
// Sync Detailed Metrics
// =============================================================================

var (
	SyncBlocksImported     = prometheus.GetOrCreateCounter("n42_sync_blocks_imported_total", true)
	SyncBlockImportSeconds = prometheus.GetOrCreateSummary("n42_sync_block_import_seconds")
	SyncHeadersDownloaded  = prometheus.GetOrCreateCounter("n42_sync_headers_downloaded_total", true)
	SyncBodiesDownloaded   = prometheus.GetOrCreateCounter("n42_sync_bodies_downloaded_total", true)
	SyncReceiptsDownloaded = prometheus.GetOrCreateCounter("n42_sync_receipts_downloaded_total", true)
	SyncPeersUsed          = prometheus.GetOrCreateCounter("n42_sync_peers_used", true)
	SyncBackfillProgress   = prometheus.GetOrCreateCounter("n42_sync_backfill_progress", true)
	SyncBackfillRemaining  = prometheus.GetOrCreateCounter("n42_sync_backfill_remaining", true)
	SyncStageProgress      = prometheus.GetOrCreateCounter("n42_sync_stage_progress", true)
)

// =============================================================================
// ZK Prover Detailed Metrics
// =============================================================================

var (
	ZKJobsSubmitted  = prometheus.GetOrCreateCounter("n42_zk_jobs_submitted_total", true)
	ZKJobsCompleted  = prometheus.GetOrCreateCounter("n42_zk_jobs_completed_total", true)
	ZKJobsFailed     = prometheus.GetOrCreateCounter("n42_zk_jobs_failed_total", true)
	ZKProvingSeconds = prometheus.GetOrCreateSummary("n42_zk_proving_seconds")
	ZKVerifySeconds  = prometheus.GetOrCreateSummary("n42_zk_verify_seconds")
	ZKProofSizeBytes = prometheus.GetOrCreateSummary("n42_zk_proof_size_bytes")
	ZKInputSizeBytes = prometheus.GetOrCreateSummary("n42_zk_input_size_bytes")
)
