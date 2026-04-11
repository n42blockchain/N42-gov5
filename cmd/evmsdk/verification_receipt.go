// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// VerificationReceipt is the per-block attestation a mobile parallel
// verifier returns to the IDC node after re-executing the block. The
// signing message is byte-identical to D:\N42\n42-26\crates\n42-mobile\src\receipt.rs's
// build_signing_message so receipts produced by either implementation
// can be verified by either side.

package evmsdk

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
)

// signingMessageLen is the canonical signing-message length:
// block_hash(32) + block_number(8 LE) + computed_receipts_root(32).
const signingMessageLen = 32 + 8 + 32

// VerificationReceipt is the BLS-signed attestation a mobile verifier
// produces after successfully re-executing a block.
type VerificationReceipt struct {
	BlockHash            types.Hash
	BlockNumber          uint64
	ComputedReceiptsRoot types.Hash
	VerifierPubkey       [48]byte // BLS12-381 G1 compressed
	Signature            [96]byte // BLS12-381 G2 compressed
	TimestampMs          uint64   // local clock at sign time; NOT covered by signature
}

// BuildSigningMessage builds the canonical 72-byte signing payload.
//
// Excludes timestamp deliberately so that all phones attesting to the
// same (block_hash, number, receipts_root) sign the IDENTICAL bytes.
// This enables fast_aggregate_verify (2 pairings regardless of signer
// count) on the IDC side, mirroring Ethereum beacon-chain attestations.
func BuildSigningMessage(blockHash types.Hash, blockNumber uint64, receiptsRoot types.Hash) []byte {
	out := make([]byte, 0, signingMessageLen)
	out = append(out, blockHash[:]...)
	var nbuf [8]byte
	binary.LittleEndian.PutUint64(nbuf[:], blockNumber)
	out = append(out, nbuf[:]...)
	out = append(out, receiptsRoot[:]...)
	return out
}

// SigningMessage returns the canonical signing payload for this receipt.
func (r *VerificationReceipt) SigningMessage() []byte {
	return BuildSigningMessage(r.BlockHash, r.BlockNumber, r.ComputedReceiptsRoot)
}

// ErrInvalidReceiptSignature is returned by VerifySignature on an
// unrecoverable BLS verification failure.
var ErrInvalidReceiptSignature = errors.New("verification_receipt: invalid BLS signature")

// VerifySignature checks that this receipt was signed by VerifierPubkey
// over the canonical signing message.
func (r *VerificationReceipt) VerifySignature() error {
	pk, err := bls.PublicKeyFromBytes(r.VerifierPubkey[:])
	if err != nil {
		return fmt.Errorf("%w: pubkey decode: %v", ErrInvalidReceiptSignature, err)
	}
	sig, err := bls.SignatureFromBytes(r.Signature[:])
	if err != nil {
		return fmt.Errorf("%w: signature decode: %v", ErrInvalidReceiptSignature, err)
	}
	if !sig.Verify(pk, r.SigningMessage()) {
		return ErrInvalidReceiptSignature
	}
	return nil
}
