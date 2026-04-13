// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// IDC-side StreamPacket producer for mobile parallel verification.
// BuildStreamPacket combines an executed block's final header, tx
// list and ReadLogRecorder output into an evmsdk.StreamPacket that
// mobile verifiers can replay and BLS-sign the resulting receipts root.

package streamverify

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
)

// BuildStreamPacket assembles a StreamPacket for distribution to mobile
// parallel verifiers, given a freshly-executed block and the recorder
// that captured the read log + uncached bytecodes during execution.
//
// The expected call site is right after a block has finished executing,
// while the recorder is still bound to that block's IntraBlockState.
// The header here is the FINAL header (with computed receipts root,
// state root, gas used, etc.) — what the IDC node would broadcast.
//
// Mobile verifiers receive this packet, replay the read log against the
// same Go EVM, and BLS-sign the (block_hash, number, computed_receipts_root)
// tuple iff the receipts root they compute equals header.ReceiptHash.
func BuildStreamPacket(header *block.Header, txs transaction.Transactions, recorder *ReadLogRecorder) (*evmsdk.StreamPacket, error) {
	if header == nil {
		return nil, errors.New("streamverify: nil header")
	}
	if recorder == nil {
		return nil, errors.New("streamverify: nil recorder")
	}

	headerBytes, err := header.Marshal()
	if err != nil {
		return nil, fmt.Errorf("streamverify: header marshal: %w", err)
	}

	txBytes := make([][]byte, 0, len(txs))
	for i, tx := range txs {
		b, err := tx.Marshal()
		if err != nil {
			return nil, fmt.Errorf("streamverify: tx %d marshal: %w", i, err)
		}
		txBytes = append(txBytes, b)
	}

	// Sort bytecodes by hash for deterministic packet bytes — makes
	// regression tests possible and lets the IDC dedup identical
	// payloads sent to multiple verifiers.
	captured := recorder.UncachedBytecodes()
	bytecodes := make([]evmsdk.CodeEntry, 0, len(captured))
	for hash, code := range captured {
		bytecodes = append(bytecodes, evmsdk.CodeEntry{Hash: hash, Code: code})
	}
	sort.Slice(bytecodes, func(i, j int) bool {
		return bytes.Compare(bytecodes[i].Hash[:], bytecodes[j].Hash[:]) < 0
	})

	return &evmsdk.StreamPacket{
		BlockHash:    header.Hash(),
		HeaderRLP:    headerBytes,
		Transactions: txBytes,
		ReadLogData:  evmsdk.EncodeReadLog(recorder.Log()),
		Bytecodes:    bytecodes,
	}, nil
}
