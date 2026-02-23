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
	"encoding/hex"
	"time"

	"github.com/n42blockchain/N42/lib/gointerfaces/grpcutil"
	txpoolproto "github.com/n42blockchain/N42/lib/gointerfaces/txpool"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/types"
)

// txMaxBroadcastSize is the max size of a transaction that will be broadcasted.
// All transactions with a higher size will be announced and need to be fetched
// by the peer.
const txMaxBroadcastSize = 4 * 1024

// MainLoop - does:
// send pending byHash to p2p:
//   - new byHash
//   - all pooled byHash to recently connected peers
//   - all local pooled byHash to random peers periodically
//
// promote/demote transactions
// reorgs
func MainLoop(ctx context.Context, db kv.RwDB, p *TxPool, newTxs chan types.Announcements, send *Send, newSlotsStreams *NewSlotsStreams, notifyMiningAboutNewSlots func()) {
	syncToNewPeersEvery := time.NewTicker(p.cfg.SyncToNewPeersEvery)
	defer syncToNewPeersEvery.Stop()
	processRemoteTxsEvery := time.NewTicker(p.cfg.ProcessRemoteTxsEvery)
	defer processRemoteTxsEvery.Stop()
	commitEvery := time.NewTicker(p.cfg.CommitEvery)
	defer commitEvery.Stop()
	logEvery := time.NewTicker(p.cfg.LogEvery)
	defer logEvery.Stop()

	err := p.Start(ctx, db)

	if err != nil {
		p.logger.Error("[txpool] Failed to start", "err", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			_, _ = p.flush(ctx, db)
			return
		case <-logEvery.C:
			p.logStats()
		case <-processRemoteTxsEvery.C:
			if !p.Started() {
				continue
			}

			if err := p.processRemoteTxs(ctx); err != nil {
				if grpcutil.IsRetryLater(err) || grpcutil.IsEndOfStream(err) {
					time.Sleep(3 * time.Second)
					continue
				}

				p.logger.Error("[txpool] process batch remote txs", "err", err)
			}
		case <-commitEvery.C:
			if db != nil && p.Started() {
				t := time.Now()
				written, err := p.flush(ctx, db)
				if err != nil {
					p.logger.Error("[txpool] flush is local history", "err", err)
					continue
				}
				writeToDBBytesCounter.SetUint64(written)
				p.logger.Debug("[txpool] Commit", "written_kb", written/1024, "in", time.Since(t))
			}
		case announcements := <-newTxs:
			go func() {
				for i := 0; i < 16; i++ { // drain more events from channel, then merge and dedup them
					select {
					case a := <-newTxs:
						announcements.AppendOther(a)
						continue
					default:
					}
					break
				}
				if announcements.Len() == 0 {
					return
				}
				defer propagateNewTxsTimer.ObserveDuration(time.Now())

				announcements = announcements.DedupCopy()

				notifyMiningAboutNewSlots()

				if p.cfg.NoGossip {
					// drain newTxs for emptying newTx channel
					// newTx channel will be filled only with local transactions
					// early return to avoid outbound transaction propagation
					log.Debug("[txpool] tx gossip disabled", "state", "drain new transactions")
					return
				}

				var localTxTypes []byte
				var localTxSizes []uint32
				var localTxHashes types.Hashes
				var localTxRlps [][]byte
				var remoteTxTypes []byte
				var remoteTxSizes []uint32
				var remoteTxHashes types.Hashes
				var remoteTxRlps [][]byte
				var broadcastHashes types.Hashes
				slotsRlp := make([][]byte, 0, announcements.Len())

				if err := db.View(ctx, func(tx kv.Tx) error {
					for i := 0; i < announcements.Len(); i++ {
						t, size, hash := announcements.At(i)
						slotRlp, err := p.GetRlp(tx, hash)
						if err != nil {
							return err
						}
						if len(slotRlp) == 0 {
							continue
						}
						// Strip away blob wrapper, if applicable
						slotRlp, err2 := types.UnwrapTxPlayloadRlp(slotRlp)
						if err2 != nil {
							continue
						}

						// Empty rlp can happen if a transaction we want to broadcast has just been mined, for example
						slotsRlp = append(slotsRlp, slotRlp)
						if p.IsLocal(hash) {
							localTxTypes = append(localTxTypes, t)
							localTxSizes = append(localTxSizes, size)
							localTxHashes = append(localTxHashes, hash...)

							// "Nodes MUST NOT automatically broadcast blob transactions to their peers" - EIP-4844
							if t != types.BlobTxType {
								localTxRlps = append(localTxRlps, slotRlp)
								broadcastHashes = append(broadcastHashes, hash...)
							}
						} else {
							remoteTxTypes = append(remoteTxTypes, t)
							remoteTxSizes = append(remoteTxSizes, size)
							remoteTxHashes = append(remoteTxHashes, hash...)

							// "Nodes MUST NOT automatically broadcast blob transactions to their peers" - EIP-4844
							if t != types.BlobTxType && len(slotRlp) < txMaxBroadcastSize {
								remoteTxRlps = append(remoteTxRlps, slotRlp)
							}
						}
					}
					return nil
				}); err != nil {
					p.logger.Error("[txpool] collect info to propagate", "err", err)
					return
				}
				if newSlotsStreams != nil {
					newSlotsStreams.Broadcast(&txpoolproto.OnAddReply{RplTxs: slotsRlp}, p.logger)
				}

				// broadcast local transactions
				const localTxsBroadcastMaxPeers uint64 = 10
				txSentTo := send.BroadcastPooledTxs(localTxRlps, localTxsBroadcastMaxPeers)
				for i, peer := range txSentTo {
					p.logger.Trace("Local tx broadcast", "txHash", hex.EncodeToString(broadcastHashes.At(i)), "to peer", peer)
				}
				hashSentTo := send.AnnouncePooledTxs(localTxTypes, localTxSizes, localTxHashes, localTxsBroadcastMaxPeers*2)
				for i := 0; i < localTxHashes.Len(); i++ {
					hash := localTxHashes.At(i)
					p.logger.Trace("Local tx announced", "txHash", hex.EncodeToString(hash), "to peer", hashSentTo[i], "baseFee", p.pendingBaseFee.Load())
				}

				// broadcast remote transactions
				const remoteTxsBroadcastMaxPeers uint64 = 3
				send.BroadcastPooledTxs(remoteTxRlps, remoteTxsBroadcastMaxPeers)
				send.AnnouncePooledTxs(remoteTxTypes, remoteTxSizes, remoteTxHashes, remoteTxsBroadcastMaxPeers*2)
			}()
		case <-syncToNewPeersEvery.C: // new peer
			newPeers := p.recentlyConnectedPeers.GetAndClean()
			if len(newPeers) == 0 {
				continue
			}
			if p.cfg.NoGossip {
				// avoid transaction gossiping for new peers
				log.Debug("[txpool] tx gossip disabled", "state", "sync new peers")
				continue
			}
			t := time.Now()
			var hashes types.Hashes
			var types []byte
			var sizes []uint32
			types, sizes, hashes = p.AppendAllAnnouncements(types, sizes, hashes[:0])
			go send.PropagatePooledTxsToPeersList(newPeers, types, sizes, hashes)
			propagateToNewPeerTimer.ObserveDuration(t)
		}
	}
}
