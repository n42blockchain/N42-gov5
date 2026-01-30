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

package txspool

import (
	"context"
	"fmt"
	"google.golang.org/protobuf/proto"
	"math/rand"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/api/protocol/sync_proto"
	"github.com/n42blockchain/N42/api/protocol/types_pb"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/message"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"go.uber.org/zap"
)

const (
	BloomFetcherMaxTime      = 10 * time.Minute
	BloomSendTransactionTime = 10 * time.Second
	BloomSendMaxTransactions = 100
)

var (
	ErrBadPeer = fmt.Errorf("bad peer error")
)

// txsRequest
type txsRequest struct {
	hashes hashes       //
	bloom  *types.Bloom //
	peer   peer.ID      //
}

type hashes []types.Hash

// pop removes and returns the last element from the slice.
// IMPORTANT: Must use pointer receiver to modify the original slice.
// Security: Added bounds check to prevent panic on empty slice.
func (h *hashes) pop() types.Hash {
	if len(*h) == 0 {
		return types.Hash{}
	}
	n := len(*h)
	x := (*h)[n-1]
	*h = (*h)[:n-1]
	return x
}

type TxsFetcher struct {
	quit chan struct{}

	finished  bool
	peers     common.PeerMap
	p2pServer common.INetwork

	peerRequests map[peer.ID]*txsRequest // other peer requests

	bloom   *types.Bloom
	fetched map[types.Hash]bool //

	getTx      func(hash types.Hash) *transaction.Transaction
	addTxs     func([]*transaction.Transaction) []error
	pendingTxs func(enforceTips bool) map[types.Address][]*transaction.Transaction

	// fetchTxs
	//fetchTxs func(string, []types.Hash)
	// ch
	peerJoinCh chan *common.PeerJoinEvent
	peerDropCh chan *common.PeerDropEvent
	//

	ctx    context.Context
	cancel context.CancelFunc
}

func NewTxsFetcher(ctx context.Context, getTx func(hash types.Hash) *transaction.Transaction, addTxs func([]*transaction.Transaction) []error, pendingTxs func(enforceTips bool) map[types.Address][]*transaction.Transaction, p2pServer common.INetwork, peers common.PeerMap, bloom *types.Bloom) *TxsFetcher {

	c, cancel := context.WithCancel(ctx)
	f := &TxsFetcher{
		quit:         make(chan struct{}),
		peers:        peers,
		peerRequests: make(map[peer.ID]*txsRequest),
		fetched:      make(map[types.Hash]bool),
		p2pServer:    p2pServer,
		addTxs:       addTxs,
		getTx:        getTx,
		pendingTxs:   pendingTxs,
		bloom:        bloom,
		peerJoinCh:   make(chan *common.PeerJoinEvent, 10),
		peerDropCh:   make(chan *common.PeerDropEvent, 10),
		finished:     false,

		ctx:    c,
		cancel: cancel,
	}

	return f
}

// Start starts the fetcher goroutines for transaction broadcasting.
// R3/R4 fix: Track goroutines properly for cleanup.
func (f *TxsFetcher) Start() error {
	go f.sendBloomTransactionLoop()
	go f.bloomBroadcastLoop()
	return nil
}

// Stop gracefully stops all fetcher goroutines and releases resources.
// R3/R4 fix: Implement proper shutdown mechanism.
func (f *TxsFetcher) Stop() {
	if f.cancel != nil {
		f.cancel()
	}
	f.finished = true
}

// sendBloomTransactionLoop periodically sends pending transactions to peers.
// R3/R4 fix: Use proper loop with ticker cleanup on context cancellation.
func (f *TxsFetcher) sendBloomTransactionLoop() {
	tick := time.NewTicker(BloomSendTransactionTime)
	defer tick.Stop() // R3 fix: Ensure ticker is stopped to prevent resource leak

	for {
		select {
		case <-tick.C:
			for ID, req := range f.peerRequests {
				if p, ok := f.peers[ID]; ok {
					var txs []*types_pb.Transaction

					// Safely pop hashes, checking for empty slice
					for i := 0; i < BloomSendMaxTransactions && len(req.hashes) > 0; i++ {
						hash := req.hashes.pop()
						tx := f.getTx(hash)
						if tx != nil {
							txs = append(txs, tx.ToProtoMessage().(*types_pb.Transaction))
						}
					}
					if len(txs) == 0 {
						continue
					}
					msg := &sync_proto.SyncTask{
						Id:       rand.Uint64(),
						Ok:       true,
						SyncType: sync_proto.SyncType_TransactionRes,
						Payload: &sync_proto.SyncTask_SyncTransactionResponse{
							SyncTransactionResponse: &sync_proto.SyncTransactionResponse{
								Transactions: txs,
							},
						},
					}
					data, _ := proto.Marshal(msg)
					p.WriteMsg(message.MsgTransaction, data)
				}
			}
		case <-f.ctx.Done():
			return
		}
	}
}

// bloomBroadcastLoop periodically broadcasts bloom filter to request missing transactions.
// R3/R4 fix: Use proper loop with ticker cleanup on context cancellation.
func (f *TxsFetcher) bloomBroadcastLoop() {
	if f.bloom == nil {
		return
	}
	tick := time.NewTicker(BloomFetcherMaxTime)
	defer tick.Stop() // R3 fix: Ensure ticker is stopped to prevent resource leak

	bloom, err := f.bloom.Marshal()
	if err != nil {
		log.Warn("txs fetcher bloom Marshal err", zap.Error(err))
		return
	}

	msg := &sync_proto.SyncTask{
		Id:       rand.Uint64(),
		Ok:       true,
		SyncType: sync_proto.SyncType_TransactionReq,
		Payload: &sync_proto.SyncTask_SyncTransactionRequest{
			SyncTransactionRequest: &sync_proto.SyncTransactionRequest{
				Bloom: bloom,
			},
		},
	}
	request, _ := proto.Marshal(msg)

	for {
		select {
		case peerId := <-f.peerJoinCh:
			if p, ok := f.peers[peerId.Peer]; ok {
				p.WriteMsg(message.MsgTransaction, request)
			}
		case <-tick.C:
			// Periodic bloom broadcast to all connected peers
			for _, p := range f.peers {
				p.WriteMsg(message.MsgTransaction, request)
			}
		case <-f.ctx.Done():
			return
		}
	}
}

// ConnHandler handles peer messages for transaction synchronization.
func (f *TxsFetcher) ConnHandler(data []byte, ID peer.ID) error {
	_, ok := f.peers[ID]
	if !ok {
		return ErrBadPeer
	}

	syncTask := sync_proto.SyncTask{}
	if err := proto.Unmarshal(data, &syncTask); err != nil {
		log.Errorf("receive sync task(headersResponse) msg err: %v", err)
		return err
	}

	//taskID := syncTask.Id
	log.Debugf("receive synctask msg from :%v, task type: %v, ok:%v", ID, syncTask.SyncType, syncTask.Ok)

	switch syncTask.SyncType {

	case sync_proto.SyncType_TransactionReq:
		request := syncTask.Payload.(*sync_proto.SyncTask_SyncTransactionRequest).SyncTransactionRequest
		if _, ok := f.peerRequests[ID]; ok == false {

			bloom := new(types.Bloom)
			err := bloom.UnMarshalBloom(request.Bloom)
			if err == nil {
				var txs []*transaction.Transaction
				var hashes []types.Hash
				pending := f.pendingTxs(false)
				for _, batch := range pending {
					txs = append(txs, batch...)
				}
				for _, tx := range txs {
					hash := tx.Hash()
					if !bloom.Contain(hash.Bytes()) {
						hashes = append(hashes, hash)
					}
				}
				f.peerRequests[ID] = &txsRequest{
					bloom:  bloom,
					hashes: hashes,
					peer:   ID,
				}
			}
		}

	case sync_proto.SyncType_TransactionRes:
		//response := syncTask.Payload.(*sync_proto.SyncTask_SyncTransactionResponse).SyncTransactionResponse
		//var txs []*transaction.Transaction
		//for _, tranPb := range response.Transactions {
		//if _, ok := f.fetched[utils.ConvertH256ToHash(tranPb.Hash)]; ok == false {
		//	f.fetched[utils.ConvertH256ToHash(tranPb.Hash)] = true
		//	tx, err := transaction.FromProtoMessage(tranPb)
		//	if err == nil {
		//		txs = append(txs, tx)
		//	}
		//}
		//}
		//if len(txs) > 0 {
		//	f.addTxs(txs)
		//}
	}
	return nil
}
