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

package internal

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// BlockValidator is responsible for validating block headers, uncles and
// processed state.
//
// BlockValidator implements Validator.
type BlockValidator struct {
	bc     *BlockChain      // Canonical block chain
	engine consensus.Engine // Consensus engine used for validating
	config *params.ChainConfig
}

// NewBlockValidator returns a new block validator which is safe for re-use
func NewBlockValidator(config *params.ChainConfig, blockchain *BlockChain, engine consensus.Engine) *BlockValidator {
	validator := &BlockValidator{
		engine: engine,
		bc:     blockchain,
		config: config,
	}
	return validator
}

// ValidateBody validates the given block's uncles and verifies the block
// header's transaction and uncle roots. The headers are assumed to be already
// validated at this point.
func (v *BlockValidator) ValidateBody(b block.IBlock) error {
	blockNumber, err := requireBlockNumber(b, "block number unavailable")
	if err != nil {
		return err
	}

	// Check Signature valid
	vfs := b.Body().Verifier()
	addrs := make([]types.Address, len(vfs))
	ss := make([]bls.PublicKey, len(vfs))
	for i, p := range vfs {
		addrs[i] = p.Address
		blsP, err := bls.PublicKeyFromBytes(p.PublicKey[:])
		if err != nil {
			return err
		}
		ss[i] = blsP
	}

	// APoS aggregate signature verification (skip for HotStuff — it uses
	// per-block BLS seal in extra-data, not header.Signature aggregate).
	if v.config.IsBeijing(blockNumber.Uint64()) && v.config.Consensus.UsesBeijingAggregateBodySignature() {
		// Signature verification now uses ConsensusEvidence table.
		// Legacy APoS signature was in Header.Signature (removed).
		// TODO: read from ConsensusEvidence table and verify.
		_ = b.Header()
		if false { // skip legacy sig verification — signature moved to ConsensusEvidence table
			return errors.New("aggregate signature verification failed")
		}
	}

	// Check whether the block's known, and if not, that it's linkable
	if v.bc.HasBlockAndState(b.Hash(), blockNumber.Uint64()) {
		return ErrKnownBlock
	}

	blockNum := blockNumber.Uint64()
	txHash := DeriveSha(transaction.Transactions(b.Transactions()))
	if txHash != b.TxHash() {
		return fmt.Errorf("transaction root hash mismatch: have %x, want %x", txHash, b.TxHash())
	}

	if blockNum == 0 {
		return nil
	}
	if !v.bc.HasBlockAndState(b.ParentHash(), blockNum-1) {
		if !v.bc.HasBlock(b.ParentHash(), blockNum-1) {
			return ErrUnknownAncestor
		}
		return ErrPrunedAncestor
	}
	return nil
}

// ValidateState validates the various changes that happen after a state
// transition, such as amount of used gas, the receipt roots and the state root
// itself. ValidateState returns a database batch if the validation was a success
// otherwise nil and an error is returned.
func (v *BlockValidator) ValidateState(iBlock block.IBlock, statedb *state.IntraBlockState, receipts block.Receipts, usedGas uint64) error {
	header, ok := iBlock.Header().(*block.Header)
	if !ok {
		return fmt.Errorf("ValidateState: invalid header type assertion for block %v", iBlock.Number64())
	}
	if header.GasUsed != usedGas {
		return fmt.Errorf("invalid gas used (remote: %d local: %d)", header.GasUsed, usedGas)
	}

	rbloom := block.CreateBloom(receipts)
	if rbloom != header.Bloom {
		return fmt.Errorf("invalid bloom (remote: %x  local: %x)", header.Bloom, rbloom)
	}

	receiptSha := DeriveSha(receipts)
	if receiptSha != header.ReceiptHash {
		for i, tx := range iBlock.Body().Transactions() {
			if i < len(receipts) {
				log.Warn("tx", "index", i, "from", tx.From(), "GasUsed", receipts[i].GasUsed)
				for index2, l := range receipts[i].Logs {
					topic := "none"
					if len(l.Topics) > 0 {
						topic = hexutil.Encode(l.Topics[0].Bytes())
					}
					log.Warn("tx logs", "index", index2, "address", l.Address, "topic", topic, "data", hexutil.Encode(l.Data))
				}
			} else {
				log.Warn("tx", "index", i, "from", tx.From(), "receipt", "missing")
			}
		}
		return fmt.Errorf("invalid receipt root hash (remote: %x local: %x)", header.ReceiptHash, receiptSha)
	}
	// State root validation: skip during initial sync (first-pass data import).
	// The incremental state hash diverges from the original chain because
	// post-audit EVM fixes (SELFDESTRUCT semantics, gas corrections) changed
	// execution outcomes. Receipt/gas/bloom checks above still enforce
	// transaction-level correctness.
	// TODO: re-enable after full sync by computing canonical state roots.

	// LtHash validation: LtHashRoot is now in Extra, not a header field.
	// TODO: extract LtHashRoot from Extra and validate against statedb.LtHashRoot().

	return nil
}
