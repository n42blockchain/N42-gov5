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
	mapset "github.com/deckarep/golang-set/v2"

	"github.com/n42blockchain/N42/lib/common/cmp"
	"github.com/n42blockchain/N42/lib/common/fixedgas"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/txpool/txpoolcfg"
	"github.com/n42blockchain/N42/lib/types"
)

func (p *TxPool) best(n uint16, txs *types.TxsRlp, tx kv.Tx, onTopOf, availableGas, availableBlobGas uint64, yielded mapset.Set[[32]byte]) (bool, int, error) {
	p.lock.Lock()
	defer p.lock.Unlock()

	for last := p.lastSeenBlock.Load(); last < onTopOf; last = p.lastSeenBlock.Load() {
		p.logger.Debug("[txpool] Waiting for block", "expecting", onTopOf, "lastSeen", last, "txRequested", n, "pending", p.pending.Len(), "baseFee", p.baseFee.Len(), "queued", p.queued.Len())
		p.lastSeenCond.Wait()
	}

	best := p.pending.best

	isShanghai := p.isShanghai() || p.isAgra()
	isPrague := p.isPrague()

	txs.Resize(uint(cmp.Min(int(n), len(best.ms))))
	var toRemove []*metaTx
	count := 0
	i := 0

	defer func() {
		p.logger.Debug("[txpool] Processing best request", "last", onTopOf, "txRequested", n, "txAvailable", len(best.ms), "txProcessed", i, "txReturned", count)
	}()

	for ; count < int(n) && i < len(best.ms); i++ {
		// if we wouldn't have enough gas for a standard transaction then quit out early
		if availableGas < fixedgas.TxGas {
			break
		}

		mt := best.ms[i]

		if yielded.Contains(mt.Tx.IDHash) {
			continue
		}

		if mt.Tx.Gas >= p.blockGasLimit.Load() {
			// Skip transactions with very large gas limit
			continue
		}

		rlpTx, sender, isLocal, err := p.getRlpLocked(tx, mt.Tx.IDHash[:])
		if err != nil {
			return false, count, err
		}
		if len(rlpTx) == 0 {
			toRemove = append(toRemove, mt)
			continue
		}

		// Skip transactions that require more blob gas than is available
		blobCount := uint64(len(mt.Tx.BlobHashes))
		if blobCount*fixedgas.BlobGasPerBlob > availableBlobGas {
			continue
		}
		availableBlobGas -= blobCount * fixedgas.BlobGasPerBlob

		// make sure we have enough gas in the caller to add this transaction.
		// not an exact science using intrinsic gas but as close as we could hope for at
		// this stage
		authorizationLen := uint64(len(mt.Tx.Authorizations))
		intrinsicGas, floorGas, _ := txpoolcfg.CalcIntrinsicGas(uint64(mt.Tx.DataLen), uint64(mt.Tx.DataNonZeroLen), authorizationLen, uint64(mt.Tx.AlAddrCount), uint64(mt.Tx.AlStorCount), mt.Tx.Creation, true, true, isShanghai, isPrague)
		if isPrague && floorGas > intrinsicGas {
			intrinsicGas = floorGas
		}
		if intrinsicGas > availableGas {
			// we might find another TX with a low enough intrinsic gas to include so carry on
			continue
		}
		availableGas -= intrinsicGas

		txs.Txs[count] = rlpTx
		copy(txs.Senders.At(count), sender.Bytes())
		txs.IsLocal[count] = isLocal
		yielded.Add(mt.Tx.IDHash)
		count++
	}

	txs.Resize(uint(count))
	if len(toRemove) > 0 {
		for _, mt := range toRemove {
			p.pending.Remove(mt, "best", p.logger)
		}
	}
	return true, count, nil
}

func (p *TxPool) YieldBest(n uint16, txs *types.TxsRlp, tx kv.Tx, onTopOf, availableGas, availableBlobGas uint64, toSkip mapset.Set[[32]byte]) (bool, int, error) {
	return p.best(n, txs, tx, onTopOf, availableGas, availableBlobGas, toSkip)
}

func (p *TxPool) PeekBest(n uint16, txs *types.TxsRlp, tx kv.Tx, onTopOf, availableGas, availableBlobGas uint64) (bool, error) {
	set := mapset.NewThreadUnsafeSet[[32]byte]()
	onTime, _, err := p.YieldBest(n, txs, tx, onTopOf, availableGas, availableBlobGas, set)
	return onTime, err
}

func (p *TxPool) CountContent() (int, int, int) {
	p.lock.Lock()
	defer p.lock.Unlock()
	return p.pending.Len(), p.baseFee.Len(), p.queued.Len()
}
