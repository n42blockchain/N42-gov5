// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package state

import "github.com/n42blockchain/N42/internal/cl/depshim/common"

func (b *CachingBeaconState) ValidatorIndexByPubkey(key [48]byte) (uint64, bool) {
	return b.publicKeyIndicies.Get(key[:])
}

// PreviousStateRoot gets the previously saved state root and then deletes it.
func (b *CachingBeaconState) PreviousStateRoot() common.Hash {
	ret := b.previousStateRoot
	b.previousStateRoot = common.Hash{}
	return ret
}
