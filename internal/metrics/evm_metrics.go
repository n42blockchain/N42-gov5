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
	JMTUpdateSeconds  = prometheus.GetOrCreateSummary("n42_jmt_update_seconds")
	JMTFlushSeconds   = prometheus.GetOrCreateSummary("n42_jmt_flush_seconds")
	JMTDirtyNodes     = prometheus.GetOrCreateCounter("n42_jmt_dirty_nodes", true)
	JMTProofSeconds   = prometheus.GetOrCreateSummary("n42_jmt_proof_seconds")
	JMTTreeHeight     = prometheus.GetOrCreateCounter("n42_jmt_tree_height", true)
)
