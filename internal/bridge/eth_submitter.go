// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bridge

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/n42blockchain/N42/accounts/abi"
	"github.com/n42blockchain/N42/common/crypto"
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
// Implements the ProofSubmitter interface.
type ETHSubmitter struct {
	client       *jsonrpc.Client
	verifierAddr types.Address
	verifierABI  abi.ABI
	chainID      *big.Int
	privKey      *ecdsa.PrivateKey
	from         types.Address
}

// NewETHSubmitter creates a new Ethereum proof submitter.
func NewETHSubmitter(
	endpoint string,
	verifierAddr types.Address,
	chainID *big.Int,
	privKey *ecdsa.PrivateKey,
) (*ETHSubmitter, error) {
	client, err := jsonrpc.DialHTTP(endpoint)
	if err != nil {
		return nil, fmt.Errorf("dial ETH RPC %s: %w", endpoint, err)
	}

	parsed, err := abi.JSON(strings.NewReader(verifierABIJSON))
	if err != nil {
		return nil, fmt.Errorf("parse verifier ABI: %w", err)
	}

	pubKey := crypto.PubkeyToAddress(privKey.PublicKey)

	return &ETHSubmitter{
		client:       client,
		verifierAddr: verifierAddr,
		verifierABI:  parsed,
		chainID:      chainID,
		privKey:      privKey,
		from:         pubKey,
	}, nil
}

// SubmitHeaderChainProof submits a ZK proof to the N42Verifier contract on Ethereum.
func (s *ETHSubmitter) SubmitHeaderChainProof(ctx context.Context, proof *HeaderChainProof) error {
	if proof == nil {
		return fmt.Errorf("nil proof")
	}

	// ABI-encode the calldata
	proofData := proof.ProofData
	if proofData == nil {
		proofData = []byte{} // empty proof for simulation mode
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

	// Get nonce
	var nonceHex string
	if err := s.client.CallContext(ctx, &nonceHex, "eth_getTransactionCount",
		s.from.Hex(), "pending"); err != nil {
		return fmt.Errorf("get nonce: %w", err)
	}
	nonce := hexToBigInt(nonceHex)

	// Get gas price
	var gasPriceHex string
	if err := s.client.CallContext(ctx, &gasPriceHex, "eth_gasPrice"); err != nil {
		return fmt.Errorf("get gas price: %w", err)
	}
	gasPrice := hexToBigInt(gasPriceHex)

	// Estimate gas
	callMsg := map[string]interface{}{
		"from": s.from.Hex(),
		"to":   s.verifierAddr.Hex(),
		"data": fmt.Sprintf("0x%x", calldata),
	}
	var gasHex string
	if err := s.client.CallContext(ctx, &gasHex, "eth_estimateGas", callMsg); err != nil {
		log.Warn("Gas estimate failed, using default", "err", err)
		gasHex = "0x7A120" // 500K gas default
	}
	gasLimit := hexToBigInt(gasHex)

	// Build raw transaction (EIP-155)
	tx := buildLegacyTx(nonce.Uint64(), s.verifierAddr, big.NewInt(0), gasLimit.Uint64(), gasPrice, calldata, s.chainID)

	// Sign
	signedTx, err := signTx(tx, s.chainID, s.privKey)
	if err != nil {
		return fmt.Errorf("sign tx: %w", err)
	}

	// Send
	var txHash string
	if err := s.client.CallContext(ctx, &txHash, "eth_sendRawTransaction",
		fmt.Sprintf("0x%x", signedTx)); err != nil {
		return fmt.Errorf("send tx: %w", err)
	}

	log.Info("Header chain proof submitted to ETH",
		"txHash", txHash,
		"startBlock", proof.StartBlock,
		"endBlock", proof.EndBlock,
		"stateRoot", proof.StateRoot,
	)

	// Wait for receipt (with timeout)
	return s.waitForReceipt(ctx, txHash)
}

// Close closes the underlying RPC client.
func (s *ETHSubmitter) Close() {
	s.client.Close()
}

// waitForReceipt polls for a transaction receipt until confirmed or timeout.
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

// --- Transaction helpers (minimal, no go-ethereum dependency) ---

// buildLegacyTx builds a legacy (EIP-155) transaction for RLP encoding.
func buildLegacyTx(nonce uint64, to types.Address, value *big.Int, gasLimit uint64, gasPrice *big.Int, data []byte, chainID *big.Int) []interface{} {
	return []interface{}{
		nonce,
		gasPrice,
		gasLimit,
		to[:],
		value,
		data,
		chainID,
		uint(0),
		uint(0),
	}
}

// signTx signs a legacy transaction with EIP-155 replay protection.
// Returns the RLP-encoded signed transaction.
func signTx(tx []interface{}, chainID *big.Int, key *ecdsa.PrivateKey) ([]byte, error) {
	// For production, this should use proper RLP encoding + signing.
	// Minimal implementation: encode transaction fields, hash, sign.
	// TODO: use common/rlp.EncodeToBytes for proper RLP encoding
	_ = tx
	_ = chainID
	_ = key
	return nil, fmt.Errorf("raw transaction signing not yet implemented — use ETH RPC with unlocked account for testing")
}

func hexToBigInt(hex string) *big.Int {
	if len(hex) >= 2 && hex[:2] == "0x" {
		hex = hex[2:]
	}
	n := new(big.Int)
	n.SetString(hex, 16)
	return n
}
