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
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/types"
)

// Cache recently mined blobs in anticipation of reorg, delete finalized ones
func (p *TxPool) processMinedFinalizedBlobs(coreTx kv.Tx, minedTxs []*types.TxSlot, finalizedBlock uint64) error {
	p.lastFinalizedBlock.Store(finalizedBlock)
	// Remove blobs in the finalized block and older, loop through all entries
	for l := len(p.minedBlobTxsByBlock); l > 0 && finalizedBlock > 0; l-- {
		// delete individual hashes
		for _, mt := range p.minedBlobTxsByBlock[finalizedBlock] {
			delete(p.minedBlobTxsByHash, string(mt.Tx.IDHash[:]))
		}
		// delete the map entry for this block num
		delete(p.minedBlobTxsByBlock, finalizedBlock)
		// move on to older blocks, if present
		finalizedBlock--
	}

	// Add mined blobs
	minedBlock := p.lastSeenBlock.Load()
	p.minedBlobTxsByBlock[minedBlock] = make([]*metaTx, 0)
	for _, txn := range minedTxs {
		if txn.Type == types.BlobTxType {
			mt := &metaTx{Tx: txn, minedBlockNum: minedBlock}
			p.minedBlobTxsByBlock[minedBlock] = append(p.minedBlobTxsByBlock[minedBlock], mt)
			mt.bestIndex = len(p.minedBlobTxsByBlock[minedBlock]) - 1
			p.minedBlobTxsByHash[string(txn.IDHash[:])] = mt
		}
	}
	return nil
}

// Delete individual hash entries from minedBlobTxs cache
func (p *TxPool) deleteMinedBlobTxn(hash string) {
	mt, exists := p.minedBlobTxsByHash[hash]
	if !exists {
		return
	}
	l := len(p.minedBlobTxsByBlock[mt.minedBlockNum])
	if l > 1 {
		p.minedBlobTxsByBlock[mt.minedBlockNum][mt.bestIndex] = p.minedBlobTxsByBlock[mt.minedBlockNum][l-1]
	}
	p.minedBlobTxsByBlock[mt.minedBlockNum] = p.minedBlobTxsByBlock[mt.minedBlockNum][:l-1]
	delete(p.minedBlobTxsByHash, hash)
}
