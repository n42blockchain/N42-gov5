// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

//go:build n42el

package forkchoice

import (
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
)

// LatestMessage represents the latest message from a validator.
// [Modified in Gloas:EIP7732] Added Slot and PayloadPresent.
type LatestMessage struct {
	Epoch          uint64
	Slot           uint64 // [New in Gloas:EIP7732]
	Root           common.Hash
	PayloadPresent bool // [New in Gloas:EIP7732]
}

// ForkChoiceNode tracks the payload status for a block root in the fork choice store.
// [New in Gloas:EIP7732]
type ForkChoiceNode struct {
	Root          common.Hash
	PayloadStatus cltypes.PayloadStatus
}

// ForkNode is a struct that represents a node in the fork choice tree.
// Originally defined in forkchoice.go (Tier 3); hoisted here in the N42
// fork so Tier 0 (interface.go) compiles standalone before later tiers
// land.
type ForkNode struct {
	Slot           uint64      `json:"slot,string"`
	BlockRoot      common.Hash `json:"block_root"`
	ParentRoot     common.Hash `json:"parent_root"`
	JustifiedEpoch uint64      `json:"justified_epoch,string"`
	FinalizedEpoch uint64      `json:"finalized_epoch,string"`
	Weight         uint64      `json:"weight,string"`
	Validity       string      `json:"validity"`
	ExecutionBlock common.Hash `json:"execution_block_hash"`
}
