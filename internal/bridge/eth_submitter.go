// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/n42blockchain/N42/accounts/abi"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// verifierABIJSON is the minimal ABI for N42Verifier.verifyHeaderChain.
const verifierABIJSON = `[{
	"name": "verifyHeaderChain",
	"type": "function",
	"inputs": [
		{"name": "proof", "type": "bytes"},
		{"name": "startBlock", "type": "uint64"},
		{"name": "endBlock", "type": "uint64"},
		{"name": "stateRoot", "type": "bytes32"}
	],
	"outputs": [{"name": "", "type": "bool"}]
}]`

// ETHSubmitter submits header chain proofs to the N42Verifier contract on Ethereum.
// Uses eth_sendTransaction with an unlocked account on the ETH RPC endpoint.
// For production with signed transactions, configure a signing middleware or
// use a custody service.
type ETHSubmitter struct {
	client       *jsonrpc.Client
	verifierAddr types.Address
	verifierABI  abi.ABI
	from         types.Address
}

// NewETHSubmitter creates a new Ethereum proof submitter.
// from is the account address that must be unlocked on the ETH RPC endpoint.
func NewETHSubmitter(
	endpoint string,
	verifierAddr types.Address,
	from types.Address,
) (*ETHSubmitter, error) {
	client, err := jsonrpc.DialHTTP(endpoint)
	if err != nil {
		return nil, fmt.Errorf("dial ETH RPC %s: %w", endpoint, err)
	}

	parsed, err := abi.JSON(strings.NewReader(verifierABIJSON))
	if err != nil {
		return nil, fmt.Errorf("parse verifier ABI: %w", err)
	}

	return &ETHSubmitter{
		client:       client,
		verifierAddr: verifierAddr,
		verifierABI:  parsed,
		from:         from,
	}, nil
}

// SubmitHeaderChainProof submits a ZK proof to the N42Verifier contract.
func (s *ETHSubmitter) SubmitHeaderChainProof(ctx context.Context, proof *HeaderChainProof) error {
	if proof == nil {
		return fmt.Errorf("nil proof")
	}

	proofData := proof.ProofData
	if proofData == nil {
		proofData = []byte{}
	}

	calldata, err := s.verifierABI.Pack("verifyHeaderChain",
		proofData,
		proof.StartBlock,
		proof.EndBlock,
		[32]byte(proof.StateRoot),
	)
	if err != nil {
		return fmt.Errorf("ABI encode: %w", err)
	}

	// Estimate gas
	callMsg := map[string]interface{}{
		"from": s.from.Hex(),
		"to":   s.verifierAddr.Hex(),
		"data": hexutil.Encode(calldata),
	}
	var gasHex string
	if err := s.client.CallContext(ctx, &gasHex, "eth_estimateGas", callMsg); err != nil {
		log.Warn("Gas estimate failed, using default 500K", "err", err)
		gasHex = "0x7A120"
	}

	// Submit via eth_sendTransaction (requires unlocked account on ETH RPC)
	txParams := map[string]interface{}{
		"from": s.from.Hex(),
		"to":   s.verifierAddr.Hex(),
		"data": hexutil.Encode(calldata),
		"gas":  gasHex,
	}

	var txHash string
	if err := s.client.CallContext(ctx, &txHash, "eth_sendTransaction", txParams); err != nil {
		return fmt.Errorf("send tx: %w", err)
	}

	log.Info("Header chain proof submitted to ETH",
		"txHash", txHash,
		"startBlock", proof.StartBlock,
		"endBlock", proof.EndBlock,
		"stateRoot", proof.StateRoot,
	)

	return s.waitForReceipt(ctx, txHash)
}

// Close closes the underlying RPC client.
func (s *ETHSubmitter) Close() {
	s.client.Close()
}

func (s *ETHSubmitter) waitForReceipt(ctx context.Context, txHash string) error {
	timeout := time.After(5 * time.Minute)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("receipt timeout for tx %s", txHash)
		case <-ticker.C:
			var receipt json.RawMessage
			err := s.client.CallContext(ctx, &receipt, "eth_getTransactionReceipt", txHash)
			if err != nil {
				continue
			}
			if receipt != nil && string(receipt) != "null" {
				log.Info("ETH proof tx confirmed", "txHash", txHash)
				return nil
			}
		}
	}
}
