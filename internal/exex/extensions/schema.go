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

package extensions

import (
	"github.com/n42blockchain/N42/common/types"
)

// MDBX table names for AI index data.
const (
	TableTokenTransfers = "ai_token_transfers"
	TableContractEvents = "ai_contract_events"
	TableAddressProfile = "ai_address_profiles"
	TableGasMetrics     = "ai_gas_metrics"
)

// TokenTransfer represents an ERC20/ERC721 transfer event extracted from
// on-chain logs. For ERC20 transfers Value is a decimal string of the
// token amount; for ERC721 transfers Value holds the tokenID and IsERC721
// is true.
type TokenTransfer struct {
	BlockNumber uint64
	TxHash      types.Hash
	LogIndex    uint
	Contract    types.Address
	From        types.Address
	To          types.Address
	Value       string // decimal string for ERC20, tokenID for ERC721
	IsERC721    bool
	Timestamp   uint64
}

// ContractEvent represents an indexed contract event log entry. EventSig
// is topic[0] (the keccak256 of the event signature) and Topics contains
// all indexed topics including topic[0].
type ContractEvent struct {
	BlockNumber uint64
	TxHash      types.Hash
	LogIndex    uint
	Contract    types.Address
	EventSig    types.Hash // topic[0]
	Topics      []types.Hash
	Data        []byte
	Timestamp   uint64
}

// AddressProfile tracks cumulative activity metrics for a single address.
// Counters are updated incrementally as blocks are committed and rolled
// back during chain reorganizations.
type AddressProfile struct {
	Address       types.Address
	TxCount       uint64
	LastActive    uint64 // block number
	TotalReceived string
	TotalSent     string
	ContractCalls uint64
}

// GasMetrics captures per-block gas usage statistics for trend analysis
// by AI agents.
type GasMetrics struct {
	BlockNumber uint64
	GasUsed     uint64
	GasLimit    uint64
	BaseFee     string
	TxCount     int
	Timestamp   uint64
	Utilization float64 // gasUsed / gasLimit
}
