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
	"context"
	"fmt"
	"time"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/types"
)

func (p *TxPool) getRlpLocked(tx kv.Tx, hash []byte) (rlpTxn []byte, sender common.Address, isLocal bool, err error) {
	txn, ok := p.byHash[string(hash)]
	if ok && txn.Tx.Rlp != nil {
		return txn.Tx.Rlp, p.senders.senderID2Addr[txn.Tx.SenderID], txn.subPool&IsLocal > 0, nil
	}
	v, err := tx.GetOne(kv.PoolTransaction, hash)
	if err != nil {
		return nil, common.Address{}, false, err
	}
	if v == nil {
		return nil, common.Address{}, false, nil
	}
	isLocal = txn != nil && txn.subPool&IsLocal > 0
	return v[20:], *(*[20]byte)(v[:20]), isLocal, nil
}

func (p *TxPool) GetRlp(tx kv.Tx, hash []byte) ([]byte, error) {
	p.lock.Lock()
	defer p.lock.Unlock()
	rlpTx, _, _, err := p.getRlpLocked(tx, hash)
	return common.Copy(rlpTx), err
}

func (p *TxPool) AppendLocalAnnouncements(types []byte, sizes []uint32, hashes []byte) ([]byte, []uint32, []byte) {
	p.lock.Lock()
	defer p.lock.Unlock()
	for hash, txn := range p.byHash {
		if txn.subPool&IsLocal == 0 {
			continue
		}
		types = append(types, txn.Tx.Type)
		sizes = append(sizes, txn.Tx.Size)
		hashes = append(hashes, hash...)
	}
	return types, sizes, hashes
}
func (p *TxPool) AppendRemoteAnnouncements(types []byte, sizes []uint32, hashes []byte) ([]byte, []uint32, []byte) {
	p.lock.Lock()
	defer p.lock.Unlock()

	for hash, txn := range p.byHash {
		if txn.subPool&IsLocal != 0 {
			continue
		}
		types = append(types, txn.Tx.Type)
		sizes = append(sizes, txn.Tx.Size)
		hashes = append(hashes, hash...)
	}
	for hash, txIdx := range p.unprocessedRemoteByHash {
		txSlot := p.unprocessedRemoteTxs.Txs[txIdx]
		types = append(types, txSlot.Type)
		sizes = append(sizes, txSlot.Size)
		hashes = append(hashes, hash...)
	}
	return types, sizes, hashes
}
func (p *TxPool) AppendAllAnnouncements(types []byte, sizes []uint32, hashes []byte) ([]byte, []uint32, []byte) {
	types, sizes, hashes = p.AppendLocalAnnouncements(types, sizes, hashes)
	types, sizes, hashes = p.AppendRemoteAnnouncements(types, sizes, hashes)
	return types, sizes, hashes
}
func (p *TxPool) idHashKnown(tx kv.Tx, hash []byte, hashS string) (bool, error) {
	if _, ok := p.unprocessedRemoteByHash[hashS]; ok {
		return true, nil
	}
	if _, ok := p.discardReasonsLRU.Get(hashS); ok {
		return true, nil
	}
	if _, ok := p.byHash[hashS]; ok {
		return true, nil
	}
	if _, ok := p.minedBlobTxsByHash[hashS]; ok {
		return true, nil
	}
	return tx.Has(kv.PoolTransaction, hash)
}
func (p *TxPool) IdHashKnown(tx kv.Tx, hash []byte) (bool, error) {
	hashS := string(hash)
	p.lock.Lock()
	defer p.lock.Unlock()
	return p.idHashKnown(tx, hash, hashS)
}
func (p *TxPool) FilterKnownIdHashes(tx kv.Tx, hashes types.Hashes) (unknownHashes types.Hashes, err error) {
	p.lock.Lock()
	defer p.lock.Unlock()
	for i := 0; i < len(hashes); i += 32 {
		known, err := p.idHashKnown(tx, hashes[i:i+32], string(hashes[i:i+32]))
		if err != nil {
			return unknownHashes, err
		}
		if !known {
			unknownHashes = append(unknownHashes, hashes[i:i+32]...)
		}
	}
	return unknownHashes, err
}

func (p *TxPool) getUnprocessedTxn(hashS string) (*types.TxSlot, bool) {
	if i, ok := p.unprocessedRemoteByHash[hashS]; ok {
		return p.unprocessedRemoteTxs.Txs[i], true
	}
	return nil, false
}

func (p *TxPool) getCachedBlobTxnLocked(tx kv.Tx, hash []byte) (*metaTx, error) {
	hashS := string(hash)
	if mt, ok := p.minedBlobTxsByHash[hashS]; ok {
		return mt, nil
	}
	if txn, ok := p.getUnprocessedTxn(hashS); ok {
		return newMetaTx(txn, false, 0), nil
	}
	if mt, ok := p.byHash[hashS]; ok {
		return mt, nil
	}
	has, err := tx.Has(kv.PoolTransaction, hash)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, nil
	}

	v, err := tx.GetOne(kv.PoolTransaction, hash)
	if err != nil {
		return nil, err
	}
	txRlp := common.Copy(v[20:])
	parseCtx := types.NewTxParseContext(p.chainID)
	parseCtx.WithSender(false)
	txSlot := &types.TxSlot{}
	parseCtx.ParseTransaction(txRlp, 0, txSlot, nil, false, true, nil)
	return newMetaTx(txSlot, false, 0), nil
}

func (p *TxPool) AddRemoteTxs(_ context.Context, newTxs types.TxSlots) {
	if p.cfg.NoGossip {
		return
	}

	defer addRemoteTxsTimer.ObserveDuration(time.Now())
	p.lock.Lock()
	defer p.lock.Unlock()
	for i, txn := range newTxs.Txs {
		hashS := string(txn.IDHash[:])
		_, ok := p.unprocessedRemoteByHash[hashS]
		if ok {
			continue
		}
		p.unprocessedRemoteByHash[hashS] = len(p.unprocessedRemoteTxs.Txs)
		p.unprocessedRemoteTxs.Append(txn, newTxs.Senders.At(i), false)
	}
}

func (p *TxPool) processRemoteTxs(ctx context.Context) error {
	if !p.Started() {
		return fmt.Errorf("txpool not started yet")
	}

	defer processBatchTxsTimer.ObserveDuration(time.Now())
	coreDB, cache := p.coreDBWithCache()
	coreTx, err := coreDB.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer coreTx.Rollback()
	cacheView, err := cache.View(ctx, coreTx)
	if err != nil {
		return err
	}

	p.lock.Lock()
	defer p.lock.Unlock()

	l := len(p.unprocessedRemoteTxs.Txs)
	if l == 0 {
		return nil
	}

	err = p.senders.registerNewSenders(p.unprocessedRemoteTxs, p.logger)
	if err != nil {
		return err
	}

	_, newTxs, err := p.validateTxs(p.unprocessedRemoteTxs, cacheView)
	if err != nil {
		return err
	}

	announcements, _, err := p.addTxs(p.lastSeenBlock.Load(), cacheView, p.senders, newTxs,
		p.pendingBaseFee.Load(), p.pendingBlobFee.Load(), p.blockGasLimit.Load(), true, p.logger)
	if err != nil {
		return err
	}
	p.promoted.Reset()
	p.promoted.AppendOther(announcements)

	if p.promoted.Len() > 0 {
		select {
		case <-ctx.Done():
			return nil
		case p.newPendingTxs <- p.promoted.Copy():
		default:
		}
	}

	p.unprocessedRemoteTxs.Resize(0)
	p.unprocessedRemoteByHash = map[string]int{}

	return nil
}
