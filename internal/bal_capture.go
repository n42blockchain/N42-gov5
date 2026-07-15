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
//
// BALCapture is the shared EIP-7928 harvest seam used by both the miner (block
// generation) and the importer (block verification). IntraBlockState.FinalizeTx
// emits each transaction's post-execution writes to the StateWriter it is given;
// wrapping that writer lets us record the per-tx access changes with their post
// values without any re-execution or extra state reads. It is a pure observer:
// every call is delegated to the inner writer unchanged, so execution behaviour
// is identical whether or not the fork is active. Only constructed when
// Rules.IsBAL is set.

package internal

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/bal"
	"github.com/n42blockchain/N42/modules/state"
)

// blockTxHashes returns the block's transaction hashes in block order.
func blockTxHashes(b *block.Block) []types.Hash {
	txs := b.Transactions()
	out := make([]types.Hash, len(txs))
	for i, tx := range txs {
		out[i] = tx.Hash()
	}
	return out
}

// txHashes returns the hashes of txs in order (miner side: env.txs).
func TxHashes(txs []*transaction.Transaction) []types.Hash {
	out := make([]types.Hash, len(txs))
	for i, tx := range txs {
		out[i] = tx.Hash()
	}
	return out
}

// BALCapture wraps a StateWriter and accumulates per-tx access records for the
// block access list. beginTx must be called (with the executing tx's hash) before
// each transaction, so FinalizeTx's post-value writes land in that tx's bucket.
//
// The final BAL is assembled by HashFor(orderedHashes), which filters the
// captured buckets to the block's finally-included transactions in block order
// and assigns EIP-7928 tx indexes (1..n; 0 / n+1 are reserved for pre/post-block
// system calls). Both the miner and the importer feed the same executed writes
// and call HashFor with the same ordered tx hashes, so they compute an identical
// hash by construction — reverted/dropped tx buckets (miner build) are excluded.
type BALCapture struct {
	inner  state.StateWriter
	txs    []bal.TxAccess
	hashes []types.Hash
	cur    *bal.TxAccess
}

func NewBALCapture(inner state.StateWriter) *BALCapture {
	return &BALCapture{inner: inner, txs: make([]bal.TxAccess, 0, 8), hashes: make([]types.Hash, 0, 8)}
}

// beginTx starts a new capture bucket for the transaction with hash txHash.
func (c *BALCapture) BeginTx(txHash types.Hash) {
	c.txs = append(c.txs, bal.TxAccess{})
	c.hashes = append(c.hashes, txHash)
	c.cur = &c.txs[len(c.txs)-1]
}

// CommitTx closes the current capture bucket after a successful transaction.
func (c *BALCapture) CommitTx() {
	c.cur = nil
}

// DiscardTx drops the current bucket after a failed transaction. This matters
// when the same transaction hash was already included through a bundle: a later
// nonce-too-low retry from the public pool must not replace the successful
// capture with an empty bucket.
func (c *BALCapture) DiscardTx() {
	if c.cur == nil || len(c.txs) == 0 {
		return
	}
	c.txs = c.txs[:len(c.txs)-1]
	c.hashes = c.hashes[:len(c.hashes)-1]
	c.cur = nil
}

// Snapshot returns a capture checkpoint for an atomic group such as an MEV
// bundle. RevertToSnapshot removes all buckets added after it.
func (c *BALCapture) Snapshot() int {
	return len(c.txs)
}

func (c *BALCapture) RevertToSnapshot(snapshot int) {
	if snapshot < 0 || snapshot > len(c.txs) {
		return
	}
	c.txs = c.txs[:snapshot]
	c.hashes = c.hashes[:snapshot]
	c.cur = nil
}

// BALFor builds the canonical block access list for the finally-included
// transactions, in block order. Buckets whose tx is not in order (reverted or
// dropped during block building) are excluded; when a tx hash was captured more
// than once, the last capture wins.
func (c *BALCapture) BALFor(order []types.Hash) *bal.BlockAccessList {
	byHash := make(map[types.Hash]int, len(c.txs))
	for i := range c.hashes {
		byHash[c.hashes[i]] = i // last capture wins
	}
	ordered := make([]bal.TxAccess, 0, len(order))
	for pos, h := range order {
		if idx, ok := byHash[h]; ok {
			t := c.txs[idx]
			t.TxIndex = uint16(pos + 1) // index 0 reserved for pre-block system calls
			ordered = append(ordered, t)
		}
	}
	return bal.BuildBAL(ordered)
}

// HashFor returns the block_access_list_hash for the given block-ordered txs.
func (c *BALCapture) HashFor(order []types.Hash) (types.Hash, error) {
	return c.BALFor(order).Hash()
}

// --- state.StateWriter (record post-values, delegate unchanged) -------------

func (c *BALCapture) UpdateAccountData(address types.Address, original, acct *account.StateAccount) error {
	if c.cur != nil && acct != nil {
		c.cur.BalanceChanges = append(c.cur.BalanceChanges, bal.AccountBalance{Address: address, PostBalance: acct.Balance})
		c.cur.NonceChanges = append(c.cur.NonceChanges, bal.AccountNonce{Address: address, NewNonce: acct.Nonce})
	}
	return c.inner.UpdateAccountData(address, original, acct)
}

func (c *BALCapture) UpdateAccountCode(address types.Address, codeHash types.Hash, code []byte) error {
	if c.cur != nil && len(code) > 0 {
		c.cur.CodeChanges = append(c.cur.CodeChanges, bal.AccountCode{Address: address, NewCode: code})
	}
	return c.inner.UpdateAccountCode(address, codeHash, code)
}

func (c *BALCapture) WriteAccountStorage(address types.Address, key types.Hash, original, value uint256.Int) error {
	if c.cur != nil {
		c.cur.StorageWrites = append(c.cur.StorageWrites, bal.SlotWrite{
			Address:  address,
			Slot:     key,
			NewValue: types.Hash(value.Bytes32()),
		})
	}
	return c.inner.WriteAccountStorage(address, key, original, value)
}

func (c *BALCapture) DeleteAccount(address types.Address, original *account.StateAccount) error {
	// Account deletion is represented by absence in the phase-1 BAL model; no
	// balance/nonce change is emitted here.
	return c.inner.DeleteAccount(address, original)
}

func (c *BALCapture) CreateContract(address types.Address) error {
	return c.inner.CreateContract(address)
}
