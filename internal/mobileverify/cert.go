// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
)

// MobileAttestationCert is the aggregated evidence object one
// collection window produces per (block, receipts-root) cohort: every
// cohort member signed the identical 72-byte message, so the aggregate
// verifies with one aggregated-pubkey pairing check regardless of
// cohort size. Shaped like a QC on purpose (aggregate + signer set) —
// and deliberately NOT one: it never enters consensus (design §1, §6).
type MobileAttestationCert struct {
	BlockHash      types.Hash
	BlockNumber    uint64
	ReceiptsRoot   types.Hash // the root this cohort attested to
	AggregateSig   [96]byte
	SignerMask     []byte // sparse mask over registry indices (mask.go)
	WindowClosedAt uint64 // unix ms; metadata only, not signed
}

// ErrBadCert reports an aggregate that fails verification.
var ErrBadCert = errors.New("mobileverify: certificate verification failed")

// Verify checks the certificate against the registry: decode the mask,
// resolve the cohort's pubkeys, and verify the aggregate over the
// canonical signing message. Same-message aggregation means the check
// is aggregate-pubkey + one signature verify — cohort-size-independent
// pairing cost (the FastAggregateVerify identity, expressed through
// Verify because the canonical message is 72 bytes, not a 32-byte
// digest).
func (c *MobileAttestationCert) Verify(reg *Registry) ([]MobileIndex, error) {
	indices, err := DecodeMask(c.SignerMask, reg.Count())
	if err != nil {
		return nil, err
	}
	if len(indices) == 0 {
		return nil, fmt.Errorf("%w: empty cohort", ErrBadCert)
	}
	pks, err := reg.PublicKeys(indices)
	if err != nil {
		return nil, err
	}
	sig, err := bls.SignatureFromBytes(c.AggregateSig[:])
	if err != nil {
		return nil, fmt.Errorf("%w: aggregate decode: %v", ErrBadCert, err)
	}
	aggPk := aggregatePubkeys(pks)
	if !sig.Verify(aggPk, SigningMessage(c.BlockHash, c.BlockNumber, c.ReceiptsRoot)) {
		return nil, ErrBadCert
	}
	return indices, nil
}

// aggregatePubkeys sums the cohort's G1 points.
func aggregatePubkeys(pks []common.PublicKey) common.PublicKey {
	return bls.AggregateMultiplePubkeys(pks)
}
