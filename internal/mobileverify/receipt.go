// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package mobileverify is the server side of the unified mobile
// attestation pipeline (docs/mobile-attestation-design.md): the BLS
// registry with proof-of-possession, the per-block receipt collector,
// and the aggregation into MobileAttestationCert with a sparse signer
// mask. Mobile attestations are bypass evidence — nothing in this
// package feeds consensus.
//
// The receipt wire format is owned by the mobile SDK
// (cmd/evmsdk/verification_receipt.go, mirrored byte-exactly by the
// Rust reference): this file re-states the canonical 72-byte signing
// message so the server does not import the SDK command package; a
// cross-package test pins the two encodings together.

package mobileverify

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
)

// SigningMessageLen is block_hash(32) + block_number(8 LE) + receipts_root(32).
const SigningMessageLen = 32 + 8 + 32

// Receipt is a mobile verifier's BLS attestation over its own
// re-execution of a block — the uplink half of the unified data plane.
// Field-for-field it matches evmsdk.VerificationReceipt.
type Receipt struct {
	BlockHash            types.Hash
	BlockNumber          uint64
	ComputedReceiptsRoot types.Hash
	VerifierPubkey       [48]byte // BLS12-381 G1 compressed
	Signature            [96]byte // BLS12-381 G2 compressed
	TimestampMs          uint64   // local clock at sign time; NOT covered by the signature
}

// SigningMessage builds the canonical 72-byte payload. All phones
// attesting to the same (block, root) sign identical bytes — the
// property that makes cohort-wide aggregate verification one pairing
// check instead of one per signer.
func SigningMessage(blockHash types.Hash, blockNumber uint64, receiptsRoot types.Hash) []byte {
	out := make([]byte, 0, SigningMessageLen)
	out = append(out, blockHash[:]...)
	var nbuf [8]byte
	binary.LittleEndian.PutUint64(nbuf[:], blockNumber)
	out = append(out, nbuf[:]...)
	out = append(out, receiptsRoot[:]...)
	return out
}

// ErrBadReceiptSignature reports a receipt whose BLS signature does not
// verify against its embedded pubkey.
var ErrBadReceiptSignature = errors.New("mobileverify: invalid receipt signature")

// VerifySignature checks the receipt against its own embedded pubkey.
// Registry membership is a separate check (Collector.Add does both).
func (r *Receipt) VerifySignature() error {
	pk, err := bls.PublicKeyFromBytes(r.VerifierPubkey[:])
	if err != nil {
		return fmt.Errorf("%w: pubkey decode: %v", ErrBadReceiptSignature, err)
	}
	sig, err := bls.SignatureFromBytes(r.Signature[:])
	if err != nil {
		return fmt.Errorf("%w: signature decode: %v", ErrBadReceiptSignature, err)
	}
	if !sig.Verify(pk, SigningMessage(r.BlockHash, r.BlockNumber, r.ComputedReceiptsRoot)) {
		return ErrBadReceiptSignature
	}
	return nil
}
