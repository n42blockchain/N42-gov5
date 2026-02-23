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
	"sync/atomic"
	"time"

	gokzg4844 "github.com/crate-crypto/go-kzg-4844"
	"github.com/hashicorp/golang-lru/v2/simplelru"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/lib/chain"
	"github.com/n42blockchain/N42/lib/common/fixedgas"
	libkzg "github.com/n42blockchain/N42/lib/crypto/kzg"
	"github.com/n42blockchain/N42/lib/kv/kvcache"
	"github.com/n42blockchain/N42/lib/txpool/txpoolcfg"
	"github.com/n42blockchain/N42/lib/types"
)

func toBlobs(_blobs [][]byte) []gokzg4844.Blob {
	blobs := make([]gokzg4844.Blob, len(_blobs))
	for i, _blob := range _blobs {
		var b gokzg4844.Blob
		copy(b[:], _blob)
		blobs[i] = b
	}
	return blobs
}

func (p *TxPool) validateTx(txn *types.TxSlot, isLocal bool, stateCache kvcache.CacheView) txpoolcfg.DiscardReason {
	isShanghai := p.isShanghai() || p.isAgra()
	if isShanghai && txn.Creation && txn.DataLen > fixedgas.MaxInitCodeSize {
		return txpoolcfg.InitCodeTooLarge // EIP-3860
	}
	// EIP-7825: Transaction Gas Limit Cap (Osaka/Fusaka)
	if p.isOsaka() && txn.Gas > fixedgas.MaxTxnGasLimit {
		return txpoolcfg.GasLimitTooHigh
	}
	if txn.Type == types.BlobTxType {
		if !p.isCancun() {
			return txpoolcfg.TypeNotActivated
		}
		if txn.Creation {
			return txpoolcfg.InvalidCreateTxn
		}
		blobCount := uint64(len(txn.BlobHashes))
		if blobCount == 0 {
			return txpoolcfg.NoBlobs
		}
		if blobCount > p.GetMaxBlobsPerBlock() {
			return txpoolcfg.TooManyBlobs
		}
		equalNumber := len(txn.BlobHashes) == len(txn.Blobs) &&
			len(txn.Blobs) == len(txn.Commitments) &&
			len(txn.Commitments) == len(txn.Proofs)

		if !equalNumber {
			return txpoolcfg.UnequalBlobTxExt
		}

		for i := 0; i < len(txn.Commitments); i++ {
			if libkzg.KZGToVersionedHash(txn.Commitments[i]) != libkzg.VersionedHash(txn.BlobHashes[i]) {
				return txpoolcfg.BlobHashCheckFail
			}
		}

		// https://github.com/ethereum/consensus-specs/blob/017a8495f7671f5fff2075a9bfc9238c1a0982f8/specs/deneb/polynomial-commitments.md#verify_blob_kzg_proof_batch
		kzgCtx := libkzg.Ctx()
		err := kzgCtx.VerifyBlobKZGProofBatch(toBlobs(txn.Blobs), txn.Commitments, txn.Proofs)
		if err != nil {
			return txpoolcfg.UnmatchedBlobTxExt
		}

		if !isLocal && (p.all.blobCount(txn.SenderID)+uint64(len(txn.BlobHashes))) > p.cfg.BlobSlots {
			if txn.Traced {
				p.logger.Info(fmt.Sprintf("TX TRACING: validateTx marked as spamming (too many blobs) idHash=%x slots=%d, limit=%d", txn.IDHash, p.all.count(txn.SenderID), p.cfg.AccountSlots))
			}
			return txpoolcfg.Spammer
		}
		if p.totalBlobsInPool.Load() >= p.cfg.TotalBlobPoolLimit {
			if txn.Traced {
				p.logger.Info(fmt.Sprintf("TX TRACING: validateTx total blobs limit reached in pool limit=%x current blobs=%d", p.cfg.TotalBlobPoolLimit, p.totalBlobsInPool.Load()))
			}
			return txpoolcfg.BlobPoolOverflow
		}
	}

	authorizationLen := len(txn.Authorizations)
	if txn.Type == types.SetCodeTxType {
		if !p.isPrague() {
			return txpoolcfg.TypeNotActivated
		}
		if txn.Creation {
			return txpoolcfg.InvalidCreateTxn
		}
		if authorizationLen == 0 {
			return txpoolcfg.NoAuthorizations
		}
	}

	// Drop non-local transactions under our own minimal accepted gas price or tip
	if !isLocal && uint256.NewInt(p.cfg.MinFeeCap).Cmp(&txn.FeeCap) == 1 {
		if txn.Traced {
			p.logger.Info(fmt.Sprintf("TX TRACING: validateTx underpriced idHash=%x local=%t, feeCap=%d, cfg.MinFeeCap=%d", txn.IDHash, isLocal, txn.FeeCap, p.cfg.MinFeeCap))
		}
		return txpoolcfg.UnderPriced
	}
	gas, floorGas, reason := txpoolcfg.CalcIntrinsicGas(uint64(txn.DataLen), uint64(txn.DataNonZeroLen), uint64(authorizationLen), uint64(txn.AlAddrCount), uint64(txn.AlStorCount), txn.Creation, true, true, isShanghai, p.isPrague())
	if p.isPrague() && floorGas > gas {
		gas = floorGas
	}

	if txn.Traced {
		p.logger.Info(fmt.Sprintf("TX TRACING: validateTx intrinsic gas idHash=%x gas=%d", txn.IDHash, gas))
	}
	if reason != txpoolcfg.Success {
		if txn.Traced {
			p.logger.Info(fmt.Sprintf("TX TRACING: validateTx intrinsic gas calculated failed idHash=%x reason=%s", txn.IDHash, reason))
		}
		return reason
	}
	if gas > txn.Gas {
		if txn.Traced {
			p.logger.Info(fmt.Sprintf("TX TRACING: validateTx intrinsic gas > txn.gas idHash=%x gas=%d, txn.gas=%d", txn.IDHash, gas, txn.Gas))
		}
		return txpoolcfg.IntrinsicGas
	}
	if !isLocal && uint64(p.all.count(txn.SenderID)) > p.cfg.AccountSlots {
		if txn.Traced {
			p.logger.Info(fmt.Sprintf("TX TRACING: validateTx marked as spamming idHash=%x slots=%d, limit=%d", txn.IDHash, p.all.count(txn.SenderID), p.cfg.AccountSlots))
		}
		return txpoolcfg.Spammer
	}

	// Check nonce and balance
	senderNonce, senderBalance, _ := p.senders.info(stateCache, txn.SenderID)
	if senderNonce > txn.Nonce {
		if txn.Traced {
			p.logger.Info(fmt.Sprintf("TX TRACING: validateTx nonce too low idHash=%x nonce in state=%d, txn.nonce=%d", txn.IDHash, senderNonce, txn.Nonce))
		}
		return txpoolcfg.NonceTooLow
	}
	// Transactor should have enough funds to cover the costs
	total := requiredBalance(txn)
	if senderBalance.Cmp(total) < 0 {
		if txn.Traced {
			p.logger.Info(fmt.Sprintf("TX TRACING: validateTx insufficient funds idHash=%x balance in state=%d, txn.gas*txn.tip=%d", txn.IDHash, senderBalance, total))
		}
		return txpoolcfg.InsufficientFunds
	}
	return txpoolcfg.Success
}

var maxUint256 = new(uint256.Int).SetAllOne()

// Sender should have enough balance for: gasLimit x feeCap + blobGas x blobFeeCap + transferred_value
// See YP, Eq (61) in Section 6.2 "Execution"
func requiredBalance(txn *types.TxSlot) *uint256.Int {
	// See https://github.com/ethereum/EIPs/pull/3594
	total := uint256.NewInt(txn.Gas)
	_, overflow := total.MulOverflow(total, &txn.FeeCap)
	if overflow {
		return maxUint256
	}
	// and https://eips.ethereum.org/EIPS/eip-4844#gas-accounting
	blobCount := uint64(len(txn.BlobHashes))
	if blobCount != 0 {
		maxBlobGasCost := uint256.NewInt(fixedgas.BlobGasPerBlob)
		maxBlobGasCost.Mul(maxBlobGasCost, uint256.NewInt(blobCount))
		_, overflow = maxBlobGasCost.MulOverflow(maxBlobGasCost, &txn.BlobFeeCap)
		if overflow {
			return maxUint256
		}
		_, overflow = total.AddOverflow(total, maxBlobGasCost)
		if overflow {
			return maxUint256
		}
	}

	_, overflow = total.AddOverflow(total, &txn.Value)
	if overflow {
		return maxUint256
	}
	return total
}

func isTimeBasedForkActivated(isPostFlag *atomic.Bool, forkTime *uint64) bool {
	// once this flag has been set for the first time we no longer need to check the timestamp
	set := isPostFlag.Load()
	if set {
		return true
	}
	if forkTime == nil { // the fork is not enabled
		return false
	}

	// a zero here means the fork is always active
	if *forkTime == 0 {
		isPostFlag.Swap(true)
		return true
	}

	now := time.Now().Unix()
	activated := uint64(now) >= *forkTime
	if activated {
		isPostFlag.Swap(true)
	}
	return activated
}

func (p *TxPool) isShanghai() bool {
	return isTimeBasedForkActivated(&p.isPostShanghai, p.shanghaiTime)
}

func (p *TxPool) isAgra() bool {
	// once this flag has been set for the first time we no longer need to check the block
	set := p.isPostAgra.Load()
	if set {
		return true
	}
	if p.agraBlock == nil {
		return false
	}
	agraBlock := *p.agraBlock

	// a zero here means Agra is always active
	if agraBlock == 0 {
		p.isPostAgra.Swap(true)
		return true
	}

	tx, err := p._chainDB.BeginRo(context.Background())
	if err != nil {
		return false
	}
	defer tx.Rollback()

	head_block, err := chain.CurrentBlockNumber(tx)
	if head_block == nil || err != nil {
		return false
	}
	// A new block is built on top of the head block, so when the head is agraBlock-1,
	// the new block should use the Agra rules.
	activated := (*head_block + 1) >= agraBlock
	if activated {
		p.isPostAgra.Swap(true)
	}
	return activated
}

func (p *TxPool) isCancun() bool {
	return isTimeBasedForkActivated(&p.isPostCancun, p.cancunTime)
}

func (p *TxPool) isPrague() bool {
	return isTimeBasedForkActivated(&p.isPostPrague, p.pragueTime)
}

func (p *TxPool) isOsaka() bool {
	return isTimeBasedForkActivated(&p.isPostOsaka, p.osakaTime)
}

func (p *TxPool) GetMaxBlobsPerBlock() uint64 {
	return p.blobSchedule.MaxBlobsPerBlock(p.isPrague(), p.isOsaka())
}

// Check that the serialized txn should not exceed a certain max size
func (p *TxPool) ValidateSerializedTxn(serializedTxn []byte) error {
	const (
		// txSlotSize is used to calculate how many data slots a single transaction
		// takes up based on its size. The slots are used as DoS protection, ensuring
		// that validating a new transaction remains a constant operation (in reality
		// O(maxslots), where max slots are 4 currently).
		txSlotSize = 32 * 1024

		// txMaxSize is the maximum size a single transaction can have. This field has
		// non-trivial consequences: larger transactions are significantly harder and
		// more expensive to propagate; larger transactions also take more resources
		// to validate whether they fit into the pool or not.
		txMaxSize = 4 * txSlotSize // 128KB

		// Should be enough for a transaction with 6 blobs
		blobTxMaxSize = 800_000
	)
	txType, err := types.PeekTransactionType(serializedTxn)
	if err != nil {
		return err
	}
	maxSize := txMaxSize
	if txType == types.BlobTxType {
		maxSize = blobTxMaxSize
	}
	if len(serializedTxn) > maxSize {
		return types.ErrRlpTooBig
	}
	return nil
}

func (p *TxPool) validateTxs(txs *types.TxSlots, stateCache kvcache.CacheView) (reasons []txpoolcfg.DiscardReason, goodTxs types.TxSlots, err error) {
	// reasons is pre-sized for direct indexing, with the default zero
	// value DiscardReason of NotSet
	reasons = make([]txpoolcfg.DiscardReason, len(txs.Txs))

	if err := txs.Valid(); err != nil {
		return reasons, goodTxs, err
	}

	goodCount := 0
	for i, txn := range txs.Txs {
		reason := p.validateTx(txn, txs.IsLocal[i], stateCache)
		if reason == txpoolcfg.Success {
			goodCount++
			// Success here means no DiscardReason yet, so leave it NotSet
			continue
		}
		if reason == txpoolcfg.Spammer {
			p.punishSpammer(txn.SenderID)
		}
		reasons[i] = reason
	}

	goodTxs.Resize(uint(goodCount))

	j := 0
	for i, txn := range txs.Txs {
		if reasons[i] == txpoolcfg.NotSet {
			goodTxs.Txs[j] = txn
			goodTxs.IsLocal[j] = txs.IsLocal[i]
			copy(goodTxs.Senders.At(j), txs.Senders.At(i))
			j++
		}
	}
	return reasons, goodTxs, nil
}

// punishSpammer by drop half of it's transactions with high nonce
func (p *TxPool) punishSpammer(spammer uint64) {
	count := p.all.count(spammer) / 2
	if count > 0 {
		txsToDelete := make([]*metaTx, 0, count)
		p.all.descend(spammer, func(mt *metaTx) bool {
			txsToDelete = append(txsToDelete, mt)
			count--
			return count > 0
		})

		for _, mt := range txsToDelete {
			switch mt.currentSubPool {
			case PendingSubPool:
				p.pending.Remove(mt, "punishSpammer", p.logger)
			case BaseFeeSubPool:
				p.baseFee.Remove(mt, "punishSpammer", p.logger)
			case QueuedSubPool:
				p.queued.Remove(mt, "punishSpammer", p.logger)
			default:
				//already removed
			}

			p.discardLocked(mt, txpoolcfg.Spammer) // can't call it while iterating by all
		}
	}
}

func fillDiscardReasons(reasons []txpoolcfg.DiscardReason, newTxs types.TxSlots, discardReasonsLRU *simplelru.LRU[string, txpoolcfg.DiscardReason]) []txpoolcfg.DiscardReason {
	for i := range reasons {
		if reasons[i] != txpoolcfg.NotSet {
			continue
		}
		reason, ok := discardReasonsLRU.Get(string(newTxs.Txs[i].IDHash[:]))
		if ok {
			reasons[i] = reason
		} else {
			reasons[i] = txpoolcfg.Success
		}
	}
	return reasons
}
