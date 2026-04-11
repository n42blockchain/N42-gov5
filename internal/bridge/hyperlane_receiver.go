// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// HyperlaneReceiver monitors the Hyperlane Mailbox contract on N42 for
// incoming Process(uint32,bytes32,address) events and updates bridge
// metrics accordingly. Paginates eth_getLogs queries using
// maxLogsBlockRange (2000) to stay within RPC provider limits, caches
// the keccak256 event topic, and tracks lastBlock atomically to
// resume polling after restarts.

package bridge

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/accounts/abi"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// maxLogsBlockRange caps the block range per eth_getLogs query
// to avoid RPC provider limits (typically 2000-10000 blocks).
const maxLogsBlockRange = 2000

// processEventTopic is cached keccak256("Process(uint32,bytes32,address)").
var processEventTopic = "0x" + fmt.Sprintf("%x", crypto.Keccak256Hash([]byte("Process(uint32,bytes32,address)")))

// HyperlaneReceiver monitors the Hyperlane Mailbox contract on N42 for
// incoming processed messages and updates metrics.
type HyperlaneReceiver struct {
	client       *jsonrpc.Client
	mailboxAddr  types.Address
	mailboxABI   abi.ABI
	pollInterval time.Duration
	lastBlock    atomic.Uint64
}

// NewHyperlaneReceiver creates a new inbound message receiver.
func NewHyperlaneReceiver(
	endpoint string,
	mailboxAddr types.Address,
	pollInterval time.Duration,
) (*HyperlaneReceiver, error) {
	client, err := jsonrpc.DialHTTP(endpoint)
	if err != nil {
		return nil, fmt.Errorf("dial N42 RPC: %w", err)
	}

	parsed, err := abi.JSON(strings.NewReader(hyperlaneMailboxABI))
	if err != nil {
		return nil, fmt.Errorf("parse Mailbox ABI: %w", err)
	}

	if pollInterval == 0 {
		pollInterval = 12 * time.Second
	}

	return &HyperlaneReceiver{
		client:       client,
		mailboxAddr:  mailboxAddr,
		mailboxABI:   parsed,
		pollInterval: pollInterval,
	}, nil
}

// Run starts the receiver main loop, polling for Process events.
func (r *HyperlaneReceiver) Run(ctx context.Context) error {
	log.Info("Hyperlane receiver started",
		"mailbox", r.mailboxAddr,
		"pollInterval", r.pollInterval,
	)

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("Hyperlane receiver stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := r.pollEvents(ctx); err != nil {
				log.Warn("Hyperlane receiver poll failed", "err", err)
			}
		}
	}
}

// pollEvents queries for new Process events, chunking by maxLogsBlockRange.
func (r *HyperlaneReceiver) pollEvents(ctx context.Context) error {
	var blockHex string
	if err := r.client.CallContext(ctx, &blockHex, "eth_blockNumber"); err != nil {
		return fmt.Errorf("get block number: %w", err)
	}
	currentBlock, err := hexutil.DecodeUint64(blockHex)
	if err != nil {
		return fmt.Errorf("decode block number: %w", err)
	}

	last := r.lastBlock.Load()
	if last == 0 {
		if currentBlock > 100 {
			last = currentBlock - 100
		}
		r.lastBlock.Store(last)
	}

	if currentBlock <= last {
		return nil
	}

	// Chunk through blocks to avoid exceeding RPC provider limits
	for fromBlock := last + 1; fromBlock <= currentBlock; fromBlock += maxLogsBlockRange {
		toBlock := fromBlock + maxLogsBlockRange - 1
		if toBlock > currentBlock {
			toBlock = currentBlock
		}

		filter := map[string]interface{}{
			"fromBlock": fmt.Sprintf("0x%x", fromBlock),
			"toBlock":   fmt.Sprintf("0x%x", toBlock),
			"address":   r.mailboxAddr.Hex(),
			"topics":    []string{processEventTopic},
		}

		var logs []map[string]interface{}
		if err := r.client.CallContext(ctx, &logs, "eth_getLogs", filter); err != nil {
			return fmt.Errorf("get logs %d-%d: %w", fromBlock, toBlock, err)
		}

		for _, logEntry := range logs {
			r.handleProcessEvent(logEntry)
		}

		r.lastBlock.Store(toBlock)
	}

	return nil
}

func (r *HyperlaneReceiver) handleProcessEvent(logEntry map[string]interface{}) {
	topics, ok := logEntry["topics"].([]interface{})
	if !ok || len(topics) < 4 {
		return
	}

	txHash, _ := logEntry["transactionHash"].(string)
	hyperlaneReceiveTotal.Inc()
	log.Info("Hyperlane message received on N42", "txHash", txHash)
}
