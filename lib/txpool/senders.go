/*
   Copyright 2022 The Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package txpool

import (
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/gointerfaces"
	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/kv/kvcache"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/types"
)

type sender struct {
	balance uint256.Int
	nonce   uint64
}

func newSender(nonce uint64, balance uint256.Int) *sender {
	return &sender{nonce: nonce, balance: balance}
}

var emptySender = newSender(0, *uint256.NewInt(0))

func SortByNonceLess(a, b *metaTx) bool {
	if a.Tx.SenderID != b.Tx.SenderID {
		return a.Tx.SenderID < b.Tx.SenderID
	}
	return a.Tx.Nonce < b.Tx.Nonce
}

// sendersBatch stores in-memory senders-related objects. Not thread-safe.
type sendersBatch struct {
	senderIDs     map[common.Address]uint64
	senderID2Addr map[uint64]common.Address
	tracedSenders map[common.Address]struct{}
	senderID      uint64
}

func newSendersCache(tracedSenders map[common.Address]struct{}) *sendersBatch {
	return &sendersBatch{senderIDs: map[common.Address]uint64{}, senderID2Addr: map[uint64]common.Address{}, tracedSenders: tracedSenders}
}

func (sc *sendersBatch) getID(addr common.Address) (uint64, bool) {
	id, ok := sc.senderIDs[addr]
	return id, ok
}
func (sc *sendersBatch) getOrCreateID(addr common.Address, logger log.Logger) (uint64, bool) {
	_, traced := sc.tracedSenders[addr]

	if !traced {
		traced = TraceAll
	}

	id, ok := sc.senderIDs[addr]
	if !ok {
		sc.senderID++
		id = sc.senderID
		sc.senderIDs[addr] = id
		sc.senderID2Addr[id] = addr
		if traced {
			logger.Info(fmt.Sprintf("TX TRACING: allocated senderID %d to sender %x", id, addr))
		}
	}
	return id, traced
}
func (sc *sendersBatch) info(cacheView kvcache.CacheView, id uint64) (nonce uint64, balance uint256.Int, err error) {
	addr, ok := sc.senderID2Addr[id]
	if !ok {
		panic("must not happen")
	}
	encoded, err := cacheView.Get(addr.Bytes())
	if err != nil {
		return 0, emptySender.balance, err
	}
	if len(encoded) == 0 {
		return emptySender.nonce, emptySender.balance, nil
	}
	nonce, balance, err = types.DecodeSender(encoded)
	if err != nil {
		return 0, emptySender.balance, err
	}
	return nonce, balance, nil
}

func (sc *sendersBatch) registerNewSenders(newTxs *types.TxSlots, logger log.Logger) (err error) {
	for i, txn := range newTxs.Txs {
		txn.SenderID, txn.Traced = sc.getOrCreateID(newTxs.Senders.AddressAt(i), logger)
	}
	return nil
}
func (sc *sendersBatch) onNewBlock(stateChanges *remote.StateChangeBatch, unwindTxs, minedTxs types.TxSlots, logger log.Logger) error {
	for _, diff := range stateChanges.ChangeBatch {
		for _, change := range diff.Changes { // merge state changes
			addrB := gointerfaces.ConvertH160toAddress(change.Address)
			sc.getOrCreateID(addrB, logger)
		}

		for i, txn := range unwindTxs.Txs {
			txn.SenderID, txn.Traced = sc.getOrCreateID(unwindTxs.Senders.AddressAt(i), logger)
		}

		for i, txn := range minedTxs.Txs {
			txn.SenderID, txn.Traced = sc.getOrCreateID(minedTxs.Senders.AddressAt(i), logger)
		}
	}
	return nil
}

// nolint
func (sc *sendersBatch) printDebug(prefix string) {
	fmt.Printf("%s.sendersBatch.sender\n", prefix)
	//for i, j := range sc.senderInfo {
	//	fmt.Printf("\tid=%d,nonce=%d,balance=%d\n", i, j.nonce, j.balance.Uint64())
	//}
}
