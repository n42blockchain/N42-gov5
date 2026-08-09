// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"bytes"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

// ethereumWithdrawalList adapts decoded Ethereum withdrawals to the
// canonical MPT list-hash interface.
type ethereumWithdrawalList []*Withdrawal

func (w ethereumWithdrawalList) Len() int {
	return len(w)
}

func (w ethereumWithdrawalList) EncodeIndex(i int, buf *bytes.Buffer) {
	buf.Reset()
	if i < 0 || i >= len(w) || w[i] == nil {
		return
	}
	// EIP-4895 withdrawal leaves are RLP([index, validatorIndex, address,
	// amount]), where amount is denominated in Gwei.
	_ = rlp.Encode(buf, []interface{}{
		w[i].Index,
		w[i].Validator,
		w[i].Address,
		w[i].Amount,
	})
}

// EthWithdrawalsRoot computes the Ethereum withdrawalsRoot for decoded RLP
// withdrawals. Ethereum uses the standard MPT root, including the empty-trie
// root for an empty list.
func EthWithdrawalsRoot(withdrawals []*Withdrawal) types.Hash {
	return hash.DeriveShaErigon(ethereumWithdrawalList(withdrawals))
}
