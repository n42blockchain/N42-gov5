// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"strings"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/params"
)

// A header whose extra-data QC does not decode must not be sealed: every
// follower rejects such a block, and the fleet aborts on it.
func TestSealRefusesUndecodableExtra(t *testing.T) {
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	h := New(nil, params.TestChainConfig)
	h.Authorize(types.Address{1}, sk)
	extra := make([]byte, extraMinLen)
	copy(extra[:extraMagicLen], extraMagic[:])
	// A QC region that claims more bytes than it has, then a seal slot.
	extra = append(extra, 0xff, 0xff, 0xff, 0xff, 0x01, 0x02)
	extra = append(extra, make([]byte, extraSealLen)...)
	hdr := &block.Header{Number: uint256.NewInt(7), Extra: extra}
	b := block.NewBlockFromReceipt(hdr, nil, nil, nil, nil)
	results := make(chan block.IBlock, 1)
	err = h.Seal(nil, b, results, nil)
	if err == nil || !strings.Contains(err.Error(), "extra-data does not decode") {
		t.Fatalf("Seal accepted an undecodable extra: %v", err)
	}
	select {
	case got := <-results:
		t.Fatalf("a block was sealed anyway: %v", got.Hash())
	default:
	}
}
