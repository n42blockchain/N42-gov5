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
	"bytes"
	"fmt"
	"math"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/lib/common/assert"
	"github.com/n42blockchain/N42/lib/common/cmp"
	"github.com/n42blockchain/N42/lib/common/u256"
	"github.com/n42blockchain/N42/lib/gointerfaces"
	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/kv/kvcache"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/txpool/txpoolcfg"
	"github.com/n42blockchain/N42/lib/types"
)

func (p *TxPool) addLocked(mt *metaTx, announcements *types.Announcements) txpoolcfg.DiscardReason {
	found := p.all.get(mt.Tx.SenderID, mt.Tx.Nonce)
	if found != nil {
		if found.Tx.Type == types.BlobTxType && mt.Tx.Type != types.BlobTxType {
			return txpoolcfg.BlobTxReplace
		}
		priceBump := p.cfg.PriceBump

		// Blob txn threshold checks for replace
		if mt.Tx.Type == types.BlobTxType {
			priceBump = p.cfg.BlobPriceBump
			blobFeeThreshold, overflow := (&uint256.Int{}).MulDivOverflow(
				&found.Tx.BlobFeeCap,
				uint256.NewInt(100+priceBump),
				uint256.NewInt(100),
			)
			if mt.Tx.BlobFeeCap.Lt(blobFeeThreshold) && !overflow {
				if bytes.Equal(found.Tx.IDHash[:], mt.Tx.IDHash[:]) {
					return txpoolcfg.NotSet
				}
				return txpoolcfg.ReplaceUnderpriced // TODO: This is the same as NotReplaced
			}
		}

		// Regular txn threshold checks
		tipThreshold := uint256.NewInt(0)
		tipThreshold = tipThreshold.Mul(&found.Tx.Tip, uint256.NewInt(100+priceBump))
		tipThreshold.Div(tipThreshold, u256.N100)
		feecapThreshold := uint256.NewInt(0)
		feecapThreshold.Mul(&found.Tx.FeeCap, uint256.NewInt(100+priceBump))
		feecapThreshold.Div(feecapThreshold, u256.N100)
		if mt.Tx.Tip.Cmp(tipThreshold) < 0 || mt.Tx.FeeCap.Cmp(feecapThreshold) < 0 {
			// Both tip and feecap need to be larger than previously to replace the transaction
			// In case if the transition is stuck, "poke" it to rebroadcast
			if mt.subPool&IsLocal != 0 && (found.currentSubPool == PendingSubPool || found.currentSubPool == BaseFeeSubPool) {
				announcements.Append(found.Tx.Type, found.Tx.Size, found.Tx.IDHash[:])
			}
			if bytes.Equal(found.Tx.IDHash[:], mt.Tx.IDHash[:]) {
				return txpoolcfg.NotSet
			}
			return txpoolcfg.NotReplaced
		}

		switch found.currentSubPool {
		case PendingSubPool:
			p.pending.Remove(found, "add", p.logger)
		case BaseFeeSubPool:
			p.baseFee.Remove(found, "add", p.logger)
		case QueuedSubPool:
			p.queued.Remove(found, "add", p.logger)
		default:
			//already removed
		}

		p.discardLocked(found, txpoolcfg.ReplacedByHigherTip)
	}

	// Don't add blob tx to queued if it's less than current pending blob base fee
	if mt.Tx.Type == types.BlobTxType && mt.Tx.BlobFeeCap.LtUint64(p.pendingBlobFee.Load()) {
		return txpoolcfg.FeeTooLow
	}

	// Check if we have txn with same authorization in the pool
	if mt.Tx.Type == types.SetCodeTxType {
		numAuths := len(mt.Tx.AuthRaw)
		foundDuplicate := false
		for i := range numAuths {
			signature := mt.Tx.Authorizations[i]
			signer, err := RecoverSignerFromRLP(mt.Tx.AuthRaw[i], uint8(signature.V.Uint64()), signature.R, signature.S)
			if err != nil {
				continue
			}

			if _, ok := p.auths[*signer]; ok {
				foundDuplicate = true
				break
			}

			p.auths[*signer] = mt
		}

		if foundDuplicate {
			return txpoolcfg.ErrAuthorityReserved
		}
	}

	hashStr := string(mt.Tx.IDHash[:])
	p.byHash[hashStr] = mt

	if replaced := p.all.replaceOrInsert(mt, p.logger); replaced != nil {
		if assert.Enable {
			panic("must never happen")
		}
	}

	if mt.subPool&IsLocal != 0 {
		p.isLocalLRU.Add(hashStr, struct{}{})
	}
	// All transactions are first added to the queued pool and then immediately promoted from there if required
	p.queued.Add(mt, "addLocked", p.logger)
	if mt.Tx.Type == types.BlobTxType {
		p.totalBlobsInPool.Store(p.totalBlobsInPool.Load() + uint64(len(mt.Tx.BlobHashes)))
	}

	p.deleteMinedBlobTxn(hashStr)
	return txpoolcfg.NotSet
}

// discardLocked drops a transaction from all sub-structures and marks it for DB deletion.
// Important: don't call it while iterating by all.
func (p *TxPool) discardLocked(mt *metaTx, reason txpoolcfg.DiscardReason) {
	hashStr := string(mt.Tx.IDHash[:])
	delete(p.byHash, hashStr)
	p.deletedTxs = append(p.deletedTxs, mt)
	p.all.delete(mt, reason, p.logger)
	p.discardReasonsLRU.Add(hashStr, reason)
	if mt.Tx.Type == types.BlobTxType {
		p.totalBlobsInPool.Store(p.totalBlobsInPool.Load() - uint64(len(mt.Tx.BlobHashes)))
	}
	if mt.Tx.Type == types.SetCodeTxType {
		numAuths := len(mt.Tx.AuthRaw)
		for i := range numAuths {
			signature := mt.Tx.Authorizations[i]
			signer, err := RecoverSignerFromRLP(mt.Tx.AuthRaw[i], uint8(signature.V.Uint64()), signature.R, signature.S)
			if err != nil {
				continue
			}

			delete(p.auths, *signer)
		}
	}
}

func (p *TxPool) addTxs(blockNum uint64, cacheView kvcache.CacheView, senders *sendersBatch,
	newTxs types.TxSlots, pendingBaseFee, pendingBlobFee, blockGasLimit uint64, collect bool, logger log.Logger) (types.Announcements, []txpoolcfg.DiscardReason, error) {
	if assert.Enable {
		for _, txn := range newTxs.Txs {
			if txn.SenderID == 0 {
				panic(fmt.Errorf("senderID can't be zero"))
			}
		}
	}

	sendersWithChangedState := map[uint64]struct{}{}
	discardReasons := make([]txpoolcfg.DiscardReason, len(newTxs.Txs))
	announcements := types.Announcements{}
	for i, txn := range newTxs.Txs {
		if found, ok := p.byHash[string(txn.IDHash[:])]; ok {
			discardReasons[i] = txpoolcfg.DuplicateHash
			// In case if the transition is stuck, "poke" it to rebroadcast
			if collect && newTxs.IsLocal[i] && (found.currentSubPool == PendingSubPool || found.currentSubPool == BaseFeeSubPool) {
				announcements.Append(found.Tx.Type, found.Tx.Size, found.Tx.IDHash[:])
			}
			continue
		}
		mt := newMetaTx(txn, newTxs.IsLocal[i], blockNum)
		if reason := p.addLocked(mt, &announcements); reason != txpoolcfg.NotSet {
			discardReasons[i] = reason
			continue
		}
		discardReasons[i] = txpoolcfg.NotSet
		if txn.Traced {
			logger.Info(fmt.Sprintf("TX TRACING: schedule sendersWithChangedState idHash=%x senderId=%d", txn.IDHash, mt.Tx.SenderID))
		}
		sendersWithChangedState[mt.Tx.SenderID] = struct{}{}
	}

	for senderID := range sendersWithChangedState {
		nonce, balance, err := senders.info(cacheView, senderID)
		if err != nil {
			return announcements, discardReasons, err
		}
		p.onSenderStateChange(senderID, nonce, balance, blockGasLimit, logger)
	}

	p.promote(pendingBaseFee, pendingBlobFee, &announcements, logger)
	p.pending.EnforceBestInvariants()

	return announcements, discardReasons, nil
}

// addTxsOnNewBlock re-injects unwound transactions and collects senders whose state changed.
// Unlike addTxs, it also processes state change diffs and does not call promote (caller does that).
func (p *TxPool) addTxsOnNewBlock(blockNum uint64, cacheView kvcache.CacheView, stateChanges *remote.StateChangeBatch,
	senders *sendersBatch, newTxs types.TxSlots, pendingBaseFee uint64, blockGasLimit uint64, logger log.Logger) (types.Announcements, error) {
	if assert.Enable {
		for _, txn := range newTxs.Txs {
			if txn.SenderID == 0 {
				panic(fmt.Errorf("senderID can't be zero"))
			}
		}
	}

	sendersWithChangedState := map[uint64]struct{}{}
	announcements := types.Announcements{}
	for i, txn := range newTxs.Txs {
		if _, ok := p.byHash[string(txn.IDHash[:])]; ok {
			continue
		}
		mt := newMetaTx(txn, newTxs.IsLocal[i], blockNum)
		if reason := p.addLocked(mt, &announcements); reason != txpoolcfg.NotSet {
			p.discardLocked(mt, reason)
			continue
		}
		sendersWithChangedState[mt.Tx.SenderID] = struct{}{}
	}
	// add senders changed in state to `sendersWithChangedState` list
	for _, changesList := range stateChanges.ChangeBatch {
		for _, change := range changesList.Changes {
			switch change.Action {
			case remote.Action_UPSERT, remote.Action_UPSERT_CODE:
				addr := gointerfaces.ConvertH160toAddress(change.Address)
				id, ok := senders.getID(addr)
				if !ok {
					continue
				}
				sendersWithChangedState[id] = struct{}{}
			}
		}
	}

	for senderID := range sendersWithChangedState {
		nonce, balance, err := senders.info(cacheView, senderID)
		if err != nil {
			return announcements, err
		}
		p.onSenderStateChange(senderID, nonce, balance, blockGasLimit, logger)
	}

	return announcements, nil
}

func (p *TxPool) setBaseFee(baseFee uint64) (uint64, bool) {
	changed := false
	if baseFee > 0 {
		changed = baseFee != p.pendingBaseFee.Load()
		p.pendingBaseFee.Store(baseFee)
	}
	return p.pendingBaseFee.Load(), changed
}

func (p *TxPool) setBlobFee(blobFee uint64) {
	if blobFee > 0 {
		p.pendingBlobFee.Store(blobFee)
	}
}

// promote reasserts invariants of the subpool and returns the list of transactions that ended up
// being promoted to the pending or basefee pool, for re-broadcasting
func (p *TxPool) promote(pendingBaseFee uint64, pendingBlobFee uint64, announcements *types.Announcements, logger log.Logger) {
	// Demote worst transactions that do not qualify for pending sub pool anymore, to other sub pools, or discard
	for worst := p.pending.Worst(); p.pending.Len() > 0 && (worst.subPool < BaseFeePoolBits || worst.minFeeCap.LtUint64(pendingBaseFee) || (worst.Tx.Type == types.BlobTxType && worst.Tx.BlobFeeCap.LtUint64(pendingBlobFee))); worst = p.pending.Worst() {
		if worst.subPool >= BaseFeePoolBits {
			tx := p.pending.PopWorst()
			announcements.Append(tx.Tx.Type, tx.Tx.Size, tx.Tx.IDHash[:])
			p.baseFee.Add(tx, "demote-pending", logger)
		} else {
			p.queued.Add(p.pending.PopWorst(), "demote-pending", logger)
		}
	}

	// Promote best transactions from base fee pool to pending pool while they qualify
	for best := p.baseFee.Best(); p.baseFee.Len() > 0 && best.subPool >= BaseFeePoolBits && best.minFeeCap.CmpUint64(pendingBaseFee) >= 0 && (best.Tx.Type != types.BlobTxType || best.Tx.BlobFeeCap.CmpUint64(pendingBlobFee) >= 0); best = p.baseFee.Best() {
		tx := p.baseFee.PopBest()
		announcements.Append(tx.Tx.Type, tx.Tx.Size, tx.Tx.IDHash[:])
		p.pending.Add(tx, logger)
	}

	// Demote worst transactions that do not qualify for base fee pool anymore, to queued sub pool, or discard
	for worst := p.baseFee.Worst(); p.baseFee.Len() > 0 && worst.subPool < BaseFeePoolBits; worst = p.baseFee.Worst() {
		p.queued.Add(p.baseFee.PopWorst(), "demote-base", logger)
	}

	// Promote best transactions from the queued pool to either pending or base fee pool, while they qualify
	for best := p.queued.Best(); p.queued.Len() > 0 && best.subPool >= BaseFeePoolBits; best = p.queued.Best() {
		if best.minFeeCap.Cmp(uint256.NewInt(pendingBaseFee)) >= 0 {
			tx := p.queued.PopBest()
			announcements.Append(tx.Tx.Type, tx.Tx.Size, tx.Tx.IDHash[:])
			p.pending.Add(tx, logger)
		} else {
			p.baseFee.Add(p.queued.PopBest(), "promote-queued", logger)
		}
	}

	// Enforce capacity limits on all sub-pools
	for p.pending.Len() > p.pending.limit {
		p.discardLocked(p.pending.PopWorst(), txpoolcfg.PendingPoolOverflow)
	}
	for p.baseFee.Len() > p.baseFee.limit {
		p.discardLocked(p.baseFee.PopWorst(), txpoolcfg.BaseFeePoolOverflow)
	}
	for p.queued.Len() > p.queued.limit {
		p.discardLocked(p.queued.PopWorst(), txpoolcfg.QueuedPoolOverflow)
	}
}

// onSenderStateChange is the function that recalculates ephemeral fields of transactions and determines
// which sub pool they will need to go to. Since this depends on other transactions from the same sender by with lower
// nonces, and also affect other transactions from the same sender with higher nonce, it loops through all transactions
// for a given senderID
func (p *TxPool) onSenderStateChange(senderID uint64, senderNonce uint64, senderBalance uint256.Int, blockGasLimit uint64, logger log.Logger) {
	noGapsNonce := senderNonce
	cumulativeRequiredBalance := uint256.NewInt(0)
	minFeeCap := uint256.NewInt(0).SetAllOne()
	minTip := uint64(math.MaxUint64)
	var toDel []*metaTx // can't delete items while iterate them

	p.all.ascend(senderID, func(mt *metaTx) bool {
		deleteAndContinueReasonLog := ""
		if senderNonce > mt.Tx.Nonce {
			deleteAndContinueReasonLog = "low nonce"
		} else if mt.Tx.Nonce != noGapsNonce && mt.Tx.Type == types.BlobTxType { // Discard nonce-gapped blob txns
			deleteAndContinueReasonLog = "nonce-gapped blob txn"
		}
		if deleteAndContinueReasonLog != "" {
			if mt.Tx.Traced {
				logger.Info("TX TRACING: onSenderStateChange loop iteration remove", "idHash", fmt.Sprintf("%x", mt.Tx.IDHash), "senderID", senderID, "senderNonce", senderNonce, "txn.nonce", mt.Tx.Nonce, "currentSubPool", mt.currentSubPool, "reason", deleteAndContinueReasonLog)
			}
			// del from sub-pool
			switch mt.currentSubPool {
			case PendingSubPool:
				p.pending.Remove(mt, deleteAndContinueReasonLog, p.logger)
			case BaseFeeSubPool:
				p.baseFee.Remove(mt, deleteAndContinueReasonLog, p.logger)
			case QueuedSubPool:
				p.queued.Remove(mt, deleteAndContinueReasonLog, p.logger)
			default:
				//already removed
			}
			toDel = append(toDel, mt)
			return true
		}

		if minFeeCap.Gt(&mt.Tx.FeeCap) {
			*minFeeCap = mt.Tx.FeeCap
		}
		mt.minFeeCap = *minFeeCap
		if mt.Tx.Tip.IsUint64() {
			minTip = cmp.Min(minTip, mt.Tx.Tip.Uint64())
		}
		mt.minTip = minTip

		mt.nonceDistance = 0
		if mt.Tx.Nonce > senderNonce { // no uint underflow
			mt.nonceDistance = mt.Tx.Nonce - senderNonce
		}

		needBalance := requiredBalance(mt.Tx)

		// Absence of nonce gaps
		mt.subPool &^= NoNonceGaps
		if noGapsNonce == mt.Tx.Nonce {
			mt.subPool |= NoNonceGaps
			noGapsNonce++
		}

		// Sufficient balance for gas
		mt.subPool &^= EnoughBalance
		mt.cumulativeBalanceDistance = math.MaxUint64
		if mt.Tx.Nonce >= senderNonce {
			cumulativeRequiredBalance = cumulativeRequiredBalance.Add(cumulativeRequiredBalance, needBalance)
			if senderBalance.Cmp(cumulativeRequiredBalance) >= 0 {
				mt.subPool |= EnoughBalance
			} else {
				if cumulativeRequiredBalance.IsUint64() && senderBalance.IsUint64() {
					mt.cumulativeBalanceDistance = cumulativeRequiredBalance.Uint64() - senderBalance.Uint64()
				}
			}
		}

		mt.subPool &^= NotTooMuchGas
		if mt.Tx.Gas < blockGasLimit {
			mt.subPool |= NotTooMuchGas
		}

		if mt.Tx.Traced {
			logger.Info("TX TRACING: onSenderStateChange loop iteration update", "idHash", fmt.Sprintf("%x", mt.Tx.IDHash), "senderId", mt.Tx.SenderID, "nonce", mt.Tx.Nonce, "subPool", mt.currentSubPool)
		}

		// Some fields of mt might have changed, need to fix the invariants in the subpool best and worst queues
		switch mt.currentSubPool {
		case PendingSubPool:
			p.pending.Updated(mt)
		case BaseFeeSubPool:
			p.baseFee.Updated(mt)
		case QueuedSubPool:
			p.queued.Updated(mt)
		}
		return true
	})

	for _, mt := range toDel {
		p.discardLocked(mt, txpoolcfg.NonceTooLow)
	}

	logger.Trace("[txpool] onSenderStateChange", "sender", senderID, "count", p.all.count(senderID), "pending", p.pending.Len(), "baseFee", p.baseFee.Len(), "queued", p.queued.Len())
}

// removeMined removes transactions that have been included in mined blocks.
func (p *TxPool) removeMined(byNonce *BySenderAndNonce, minedTxs []*types.TxSlot) error {
	noncesToRemove := map[uint64]uint64{}
	for _, txn := range minedTxs {
		nonce, ok := noncesToRemove[txn.SenderID]
		if !ok || txn.Nonce > nonce {
			noncesToRemove[txn.SenderID] = txn.Nonce
		}
	}

	var toDel []*metaTx // can't delete items while iterate them

	discarded := 0
	pendingRemoved := 0
	baseFeeRemoved := 0
	queuedRemoved := 0

	for senderID, nonce := range noncesToRemove {
		byNonce.ascend(senderID, func(mt *metaTx) bool {
			if mt.Tx.Nonce > nonce {
				if mt.Tx.Traced {
					p.logger.Debug("[txpool] removing mined, cmp nonces", "tx.nonce", mt.Tx.Nonce, "sender.nonce", nonce)
				}

				return false
			}

			if mt.Tx.Traced {
				p.logger.Info("TX TRACING: removeMined", "idHash", fmt.Sprintf("%x", mt.Tx.IDHash), "senderId", mt.Tx.SenderID, "nonce", mt.Tx.Nonce, "currentSubPool", mt.currentSubPool)
			}

			toDel = append(toDel, mt)
			switch mt.currentSubPool {
			case PendingSubPool:
				pendingRemoved++
				p.pending.Remove(mt, "remove-mined", p.logger)
			case BaseFeeSubPool:
				baseFeeRemoved++
				p.baseFee.Remove(mt, "remove-mined", p.logger)
			case QueuedSubPool:
				queuedRemoved++
				p.queued.Remove(mt, "remove-mined", p.logger)
			default:
				//already removed
			}
			return true
		})

		discarded += len(toDel)

		for _, mt := range toDel {
			p.discardLocked(mt, txpoolcfg.Mined)
		}
		toDel = toDel[:0]
	}

	if discarded > 0 {
		p.logger.Debug("Discarded transactions", "count", discarded, "pending", pendingRemoved, "baseFee", baseFeeRemoved, "queued", queuedRemoved)
	}

	return nil
}
