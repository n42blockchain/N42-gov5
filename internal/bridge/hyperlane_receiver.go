// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/n42blockchain/N42/accounts/abi"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// HyperlaneReceiver monitors the Hyperlane Mailbox contract on N42 for
// incoming processed messages and updates the bridge router's transfer records.
type HyperlaneReceiver struct {
	client       *jsonrpc.Client
	mailboxAddr  types.Address
	mailboxABI   abi.ABI
	router       *ZKRouter
	pollInterval time.Duration
	lastBlock    uint64
}

// NewHyperlaneReceiver creates a new inbound message receiver.
func NewHyperlaneReceiver(
	endpoint string,
	mailboxAddr types.Address,
	router *ZKRouter,
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
		router:       router,
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

// pollEvents queries for new Process events from the Mailbox contract.
func (r *HyperlaneReceiver) pollEvents(ctx context.Context) error {
	// Get current block number
	var blockHex string
	if err := r.client.CallContext(ctx, &blockHex, "eth_blockNumber"); err != nil {
		return fmt.Errorf("get block number: %w", err)
	}
	currentBlock := hexToUint64(blockHex)

	if r.lastBlock == 0 {
		// Start from recent blocks on first run
		if currentBlock > 100 {
			r.lastBlock = currentBlock - 100
		}
	}

	if currentBlock <= r.lastBlock {
		return nil
	}

	// Query Process event logs
	// Process event topic: keccak256("Process(uint32,bytes32,address)")
	processEventTopic := "0x" + fmt.Sprintf("%x", processEventSignatureHash())

	filter := map[string]interface{}{
		"fromBlock": fmt.Sprintf("0x%x", r.lastBlock+1),
		"toBlock":   fmt.Sprintf("0x%x", currentBlock),
		"address":   r.mailboxAddr.Hex(),
		"topics":    []string{processEventTopic},
	}

	var logs []map[string]interface{}
	if err := r.client.CallContext(ctx, &logs, "eth_getLogs", filter); err != nil {
		return fmt.Errorf("get logs: %w", err)
	}

	for _, logEntry := range logs {
		r.handleProcessEvent(logEntry)
	}

	r.lastBlock = currentBlock
	return nil
}

// handleProcessEvent processes a single Mailbox Process event.
func (r *HyperlaneReceiver) handleProcessEvent(logEntry map[string]interface{}) {
	topics, ok := logEntry["topics"].([]interface{})
	if !ok || len(topics) < 4 {
		return
	}

	// topics[1] = origin (uint32, left-padded to 32 bytes)
	// topics[2] = sender (bytes32)
	// topics[3] = recipient (address, left-padded to 32 bytes)
	originHex, _ := topics[1].(string)
	origin := hexToUint64(originHex)

	txHash, _ := logEntry["transactionHash"].(string)

	hyperlaneReceiveTotal.Inc()
	log.Info("Hyperlane message received on N42",
		"origin", origin,
		"txHash", txHash,
	)
}

// processEventSignatureHash returns keccak256("Process(uint32,bytes32,address)").
func processEventSignatureHash() [32]byte {
	return crypto.Keccak256Hash([]byte("Process(uint32,bytes32,address)"))
}

func hexToUint64(hex string) uint64 {
	if len(hex) >= 2 && hex[:2] == "0x" {
		hex = hex[2:]
	}
	var n uint64
	for _, c := range hex {
		n <<= 4
		switch {
		case c >= '0' && c <= '9':
			n |= uint64(c - '0')
		case c >= 'a' && c <= 'f':
			n |= uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			n |= uint64(c-'A') + 10
		}
	}
	return n
}
