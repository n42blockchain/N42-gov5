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

package guest

import (
	"fmt"

	"github.com/n42blockchain/N42/api/protocol/types_pb"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/witness"
	"google.golang.org/protobuf/proto"
)

// Execute runs the guest program logic: decode inputs, replay transactions
// using the witness-backed state, and produce a GuestOutput with the resulting
// state root and gas usage.
//
// This function is designed to be compiled to RISC-V64 for execution inside
// the ZISK zkVM. It depends only on pure Go packages (no CGO, no MDBX, no P2P).
func Execute(input *GuestInput) *GuestOutput {
	output := &GuestOutput{}

	if input == nil {
		output.Error = "nil guest input"
		return output
	}

	// 1. Decode witness.
	bw, err := witness.DecodeBinaryWitness(input.Witness)
	if err != nil {
		output.Error = fmt.Sprintf("failed to decode witness: %v", err)
		return output
	}

	// 2. Verify witness Merkle proofs.
	var parentRoot types.Hash
	copy(parentRoot[:], bw.ParentRoot[:])
	if err := witness.VerifyWitness(parentRoot, bw); err != nil {
		output.Error = fmt.Sprintf("witness verification failed: %v", err)
		return output
	}

	// 3. Create state reader from verified witness.
	stateReader, err := witness.NewWitnessStateReader(bw)
	if err != nil {
		output.Error = fmt.Sprintf("failed to create witness state reader: %v", err)
		return output
	}

	// 4. Decode block header.
	var pbHeader types_pb.Header
	if err := proto.Unmarshal(input.BlockHeader, &pbHeader); err != nil {
		output.Error = fmt.Sprintf("failed to decode block header: %v", err)
		return output
	}
	header := &block.Header{}
	if err := header.FromProtoMessage(&pbHeader); err != nil {
		output.Error = fmt.Sprintf("failed to convert block header: %v", err)
		return output
	}

	// 5. Decode transactions.
	txs := make([]*transaction.Transaction, len(input.Transactions))
	for i, raw := range input.Transactions {
		var pbTx types_pb.Transaction
		if err := proto.Unmarshal(raw, &pbTx); err != nil {
			output.Error = fmt.Sprintf("failed to decode transaction %d: %v", i, err)
			return output
		}
		tx, err := transaction.FromProtoMessage(&pbTx)
		if err != nil {
			output.Error = fmt.Sprintf("failed to convert transaction %d: %v", i, err)
			return output
		}
		txs[i] = tx
	}

	// 6. Create IntraBlockState from witness-backed reader.
	ibs := state.New(stateReader)

	// 7. Decode parent header for block hash lookups.
	var parentHeader *block.Header
	if len(input.ParentHeader) > 0 {
		var pbParent types_pb.Header
		if err := proto.Unmarshal(input.ParentHeader, &pbParent); err != nil {
			output.Error = fmt.Sprintf("failed to decode parent header: %v", err)
			return output
		}
		parentHeader = &block.Header{}
		if err := parentHeader.FromProtoMessage(&pbParent); err != nil {
			output.Error = fmt.Sprintf("failed to convert parent header: %v", err)
			return output
		}
	}

	// Build a GetHash function that returns the parent block's hash.
	// In the zkVM guest context, we only have access to the parent header,
	// so BLOCKHASH is limited to the parent block only.
	getHash := func(n uint64) types.Hash {
		if parentHeader != nil && parentHeader.Number != nil && parentHeader.Number.Uint64() == n {
			return parentHeader.Hash()
		}
		return types.Hash{}
	}

	// 8. Execute transactions with gas limit enforcement.
	// Use a shared GasPool across all transactions to enforce the block gas limit.
	gp := new(common.GasPool).AddGas(header.GasLimit)
	var totalGasUsed uint64
	receipts := make(block.Receipts, 0, len(txs))

	for i, tx := range txs {
		txResult, err := ApplyTx(input.ForkConfig, input.ChainID, header, ibs, tx, i, gp, getHash, totalGasUsed)
		if err != nil {
			output.Error = fmt.Sprintf("tx %d execution failed: %v", i, err)
			return output
		}
		totalGasUsed += txResult.GasUsed
		if totalGasUsed > header.GasLimit {
			output.Error = fmt.Sprintf("cumulative gas %d exceeds block gas limit %d after tx %d", totalGasUsed, header.GasLimit, i)
			return output
		}

		// Build receipt for receipts root computation.
		receipt := &block.Receipt{
			Type:              tx.Type(),
			CumulativeGasUsed: txResult.CumulativeGas,
			Logs:              txResult.Logs,
			TxHash:            tx.Hash(),
			ContractAddress:   txResult.ContractAddress,
			GasUsed:           txResult.GasUsed,
		}
		if txResult.Failed {
			receipt.Status = 0 // ReceiptStatusFailed
		} else {
			receipt.Status = 1 // ReceiptStatusSuccessful
		}
		receipt.Bloom = block.CreateBloom(block.Receipts{receipt})
		receipts = append(receipts, receipt)
	}

	// 9. Compute post-state root.
	// Note: IntermediateRoot uses the RootComputer if set (e.g., JMT+Blake3),
	// otherwise falls back to legacy incremental Keccak. In the guest context
	// with WitnessStateReader, no RootComputer is set, so the legacy Keccak
	// path is used. The prover/verifier must ensure the same root computation
	// method is used on both sides.
	postRoot := ibs.IntermediateRoot()
	copy(output.PostStateRoot[:], postRoot[:])

	// 10. Compute receipts root.
	receiptsRoot := hash.DeriveSha(receipts)
	copy(output.ReceiptsRoot[:], receiptsRoot[:])

	output.GasUsed = totalGasUsed
	output.Success = true

	return output
}
