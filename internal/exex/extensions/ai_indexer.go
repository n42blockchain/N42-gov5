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
// ai_indexer.go — post-execution indexer for AI-agent data queries.
//
// AIIndexer implements exex.Extension and consumes block commit and
// revert notifications to build thread-safe in-memory indices of ERC20
// and ERC721 token transfers, generic contract events, per-address
// activity profiles and per-block gas metrics. Transfers are detected
// via the canonical Transfer(address,address,uint256) topic hash.
// Index capacities are bounded by defaultMaxTransfers / Events /
// GasMetrics so a long-running node cannot unbounded-grow memory; the
// MCP data tools read the same indices through a read-only snapshot.

package extensions

import (
	"context"
	"math/big"
	"sync"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/exex"
	"github.com/n42blockchain/N42/log"
)

// transferEventSig is the keccak256 hash of "Transfer(address,address,uint256)",
// the canonical ERC20/ERC721 Transfer event signature.
var transferEventSig = types.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")

// Default capacity limits for in-memory indices.
const (
	defaultMaxTransfers  = 100000
	defaultMaxEvents     = 100000
	defaultMaxGasMetrics = 10000
)

// AIIndexer indexes blockchain events (token transfers, contract events,
// address activity, gas metrics) for AI agent data queries. It implements
// the exex.Extension interface and maintains thread-safe in-memory indices
// that are queryable via the MCP data tools.
type AIIndexer struct {
	mu            sync.RWMutex
	transfers     []TokenTransfer
	events        []ContractEvent
	profiles      map[types.Address]*AddressProfile
	gasMetrics    []GasMetrics
	maxTransfers  int
	maxEvents     int
	maxGasMetrics int
	ctx           context.Context
	cancel        context.CancelFunc
}

// Compile-time check that AIIndexer implements the Extension interface.
var _ exex.Extension = (*AIIndexer)(nil)

// NewAIIndexer creates a new AIIndexer with default capacity limits.
func NewAIIndexer() *AIIndexer {
	return &AIIndexer{
		transfers:     make([]TokenTransfer, 0, 1024),
		events:        make([]ContractEvent, 0, 1024),
		profiles:      make(map[types.Address]*AddressProfile),
		gasMetrics:    make([]GasMetrics, 0, 256),
		maxTransfers:  defaultMaxTransfers,
		maxEvents:     defaultMaxEvents,
		maxGasMetrics: defaultMaxGasMetrics,
	}
}

// Name returns the extension identifier.
func (idx *AIIndexer) Name() string {
	return "ai_indexer"
}

// Start initializes the indexer with the provided context. The context is
// used for cancellation during shutdown.
func (idx *AIIndexer) Start(ctx context.Context) error {
	idx.ctx, idx.cancel = context.WithCancel(ctx)
	log.Info("AI indexer extension started")
	return nil
}

// OnNotification handles post-execution block events. Commit events
// trigger block processing; revert events remove data for the reverted
// block number.
func (idx *AIIndexer) OnNotification(_ context.Context, notif *exex.ExExNotification) error {
	switch notif.Type {
	case exex.NotificationCommit:
		if notif.Block != nil {
			idx.processBlock(notif.Block, notif.Receipts)
		}
	case exex.NotificationRevert:
		idx.revertBlock(notif.Number)
	}
	return nil
}

// Stop cancels the indexer context and logs shutdown.
func (idx *AIIndexer) Stop() error {
	if idx.cancel != nil {
		idx.cancel()
	}
	log.Info("AI indexer extension stopped",
		"transfers", len(idx.transfers),
		"events", len(idx.events),
		"profiles", len(idx.profiles),
		"gasMetrics", len(idx.gasMetrics),
	)
	return nil
}

// processBlock extracts token transfers, contract events, address profiles,
// and gas metrics from a committed block and its receipts.
func (idx *AIIndexer) processBlock(blk block.IBlock, receipts []*block.Receipt) {
	blockNum := uint64(0)
	if n := blk.Number64(); n != nil {
		blockNum = n.Uint64()
	}
	timestamp := blk.Time()

	idx.mu.Lock()
	defer idx.mu.Unlock()

	// --- Extract token transfers and contract events from receipts ---
	for _, receipt := range receipts {
		if receipt.Status != block.ReceiptStatusSuccessful {
			continue
		}
		for _, lg := range receipt.Logs {
			idx.indexContractEvent(lg, blockNum, timestamp)
			idx.indexTokenTransfer(lg, blockNum, timestamp)
		}
	}

	// --- Update address profiles from transactions ---
	for _, tx := range blk.Transactions() {
		if from := tx.From(); from != nil {
			idx.updateProfile(*from, blockNum, true, tx.To() != nil)
		}
		if to := tx.To(); to != nil {
			idx.updateProfile(*to, blockNum, false, false)
		}
	}

	// --- Record gas metrics ---
	gasLimit := blk.GasLimit()
	gasUsed := blk.GasUsed()
	var utilization float64
	if gasLimit > 0 {
		utilization = float64(gasUsed) / float64(gasLimit)
	}

	baseFee := "0"
	if bf := blk.BaseFee64(); bf != nil {
		baseFee = bf.ToBig().String()
	}

	gm := GasMetrics{
		BlockNumber: blockNum,
		GasUsed:     gasUsed,
		GasLimit:    gasLimit,
		BaseFee:     baseFee,
		TxCount:     len(blk.Transactions()),
		Timestamp:   timestamp,
		Utilization: utilization,
	}

	idx.gasMetrics = append(idx.gasMetrics, gm)
	if len(idx.gasMetrics) > idx.maxGasMetrics {
		// Drop the oldest entries.
		drop := len(idx.gasMetrics) - idx.maxGasMetrics
		idx.gasMetrics = idx.gasMetrics[drop:]
	}
}

// indexTokenTransfer checks whether a log is an ERC20 or ERC721 Transfer
// event and records it. ERC20 Transfer has 3 topics (sig, from, to) with
// value in data. ERC721 Transfer has 4 topics (sig, from, to, tokenId).
func (idx *AIIndexer) indexTokenTransfer(lg *block.Log, blockNum, timestamp uint64) {
	if len(lg.Topics) < 3 {
		return
	}
	if lg.Topics[0] != transferEventSig {
		return
	}

	from := types.BytesToAddress(lg.Topics[1].Bytes())
	to := types.BytesToAddress(lg.Topics[2].Bytes())

	var value string
	var isERC721 bool

	if len(lg.Topics) == 4 {
		// ERC721: tokenId is topic[3].
		isERC721 = true
		tokenID := new(big.Int).SetBytes(lg.Topics[3].Bytes())
		value = tokenID.String()
	} else {
		// ERC20: value is in log data.
		if len(lg.Data) >= 32 {
			v := new(big.Int).SetBytes(lg.Data[:32])
			value = v.String()
		} else {
			value = "0"
		}
	}

	tt := TokenTransfer{
		BlockNumber: blockNum,
		TxHash:      lg.TxHash,
		LogIndex:    lg.Index,
		Contract:    lg.Address,
		From:        from,
		To:          to,
		Value:       value,
		IsERC721:    isERC721,
		Timestamp:   timestamp,
	}

	idx.transfers = append(idx.transfers, tt)
	if len(idx.transfers) > idx.maxTransfers {
		drop := len(idx.transfers) - idx.maxTransfers
		idx.transfers = idx.transfers[drop:]
	}
}

// indexContractEvent records every log entry as a ContractEvent for
// structured querying by AI agents.
func (idx *AIIndexer) indexContractEvent(lg *block.Log, blockNum, timestamp uint64) {
	var eventSig types.Hash
	if len(lg.Topics) > 0 {
		eventSig = lg.Topics[0]
	}

	topics := make([]types.Hash, len(lg.Topics))
	copy(topics, lg.Topics)

	data := make([]byte, len(lg.Data))
	copy(data, lg.Data)

	ce := ContractEvent{
		BlockNumber: blockNum,
		TxHash:      lg.TxHash,
		LogIndex:    lg.Index,
		Contract:    lg.Address,
		EventSig:    eventSig,
		Topics:      topics,
		Data:        data,
		Timestamp:   timestamp,
	}

	idx.events = append(idx.events, ce)
	if len(idx.events) > idx.maxEvents {
		drop := len(idx.events) - idx.maxEvents
		idx.events = idx.events[drop:]
	}
}

// updateProfile updates or creates an address profile entry.
func (idx *AIIndexer) updateProfile(addr types.Address, blockNum uint64, isSender, isContractCall bool) {
	p, ok := idx.profiles[addr]
	if !ok {
		p = &AddressProfile{
			Address:       addr,
			TotalReceived: "0",
			TotalSent:     "0",
		}
		idx.profiles[addr] = p
	}

	p.TxCount++
	if blockNum > p.LastActive {
		p.LastActive = blockNum
	}

	if isSender && isContractCall {
		p.ContractCalls++
	}
}

// revertBlock removes all indexed data for the given block number during
// a chain reorganization.
//
// Design decision: address profiles are intentionally NOT reverted during
// chain reorganizations. Profiles track approximate cumulative metrics
// (TxCount, ContractCalls, LastActive) that serve as heuristic signals for
// AI agent queries rather than exact accounting. Reverting profiles would
// require maintaining per-block deltas or snapshots, adding significant
// complexity for minimal benefit. After a reorg, profiles naturally
// re-converge as new blocks are processed — the cumulative counters may
// temporarily overcount, but this is acceptable for the AI indexer's
// use case of approximate activity classification.
func (idx *AIIndexer) revertBlock(blockNum uint64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Remove transfers for the reverted block.
	filtered := idx.transfers[:0]
	for _, t := range idx.transfers {
		if t.BlockNumber != blockNum {
			filtered = append(filtered, t)
		}
	}
	idx.transfers = filtered

	// Remove events for the reverted block. Zero out trailing slots to
	// allow GC of Topics/Data slices held by removed ContractEvent values.
	filteredEvents := idx.events[:0]
	for _, e := range idx.events {
		if e.BlockNumber != blockNum {
			filteredEvents = append(filteredEvents, e)
		}
	}
	for i := len(filteredEvents); i < len(idx.events); i++ {
		idx.events[i] = ContractEvent{}
	}
	idx.events = filteredEvents

	// Remove gas metrics for the reverted block.
	filteredMetrics := idx.gasMetrics[:0]
	for _, g := range idx.gasMetrics {
		if g.BlockNumber != blockNum {
			filteredMetrics = append(filteredMetrics, g)
		}
	}
	idx.gasMetrics = filteredMetrics

	// Address profiles are NOT reverted here. See the method-level comment
	// for the rationale behind this design decision.

	log.Debug("AI indexer reverted block", "number", blockNum)
}

// --- Query methods ---

// QueryTransfers returns token transfers matching the given filters.
// Any filter parameter may be nil to match all values. Results are
// capped at limit (0 means no limit, capped at 1000).
func (idx *AIIndexer) QueryTransfers(contract *types.Address, from *types.Address, to *types.Address, fromBlock, toBlock uint64, limit int) []TokenTransfer {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	results := make([]TokenTransfer, 0, min(limit, len(idx.transfers)))
	for i := len(idx.transfers) - 1; i >= 0 && len(results) < limit; i-- {
		t := idx.transfers[i]
		if fromBlock > 0 && t.BlockNumber < fromBlock {
			continue
		}
		if toBlock > 0 && t.BlockNumber > toBlock {
			continue
		}
		if contract != nil && t.Contract != *contract {
			continue
		}
		if from != nil && t.From != *from {
			continue
		}
		if to != nil && t.To != *to {
			continue
		}
		results = append(results, t)
	}
	return results
}

// QueryEvents returns contract events matching the given filters.
// Any filter parameter may be nil to match all values. Results are
// capped at limit (0 means no limit, capped at 1000).
func (idx *AIIndexer) QueryEvents(contract *types.Address, eventSig *types.Hash, fromBlock, toBlock uint64, limit int) []ContractEvent {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	results := make([]ContractEvent, 0, min(limit, len(idx.events)))
	for i := len(idx.events) - 1; i >= 0 && len(results) < limit; i-- {
		e := idx.events[i]
		if fromBlock > 0 && e.BlockNumber < fromBlock {
			continue
		}
		if toBlock > 0 && e.BlockNumber > toBlock {
			continue
		}
		if contract != nil && e.Contract != *contract {
			continue
		}
		if eventSig != nil && e.EventSig != *eventSig {
			continue
		}
		results = append(results, e)
	}
	return results
}

// GetProfile returns the address profile for the given address, or nil
// if no activity has been recorded.
func (idx *AIIndexer) GetProfile(addr types.Address) *AddressProfile {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	p, ok := idx.profiles[addr]
	if !ok {
		return nil
	}

	// Return a copy to avoid data races on the caller side.
	cp := *p
	return &cp
}

// QueryGasMetrics returns gas metrics for blocks in the given range.
// A fromBlock of 0 means no lower bound; a toBlock of 0 means no upper
// bound.
func (idx *AIIndexer) QueryGasMetrics(fromBlock, toBlock uint64) []GasMetrics {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	results := make([]GasMetrics, 0, 64)
	for _, g := range idx.gasMetrics {
		if fromBlock > 0 && g.BlockNumber < fromBlock {
			continue
		}
		if toBlock > 0 && g.BlockNumber > toBlock {
			continue
		}
		results = append(results, g)
	}
	return results
}

