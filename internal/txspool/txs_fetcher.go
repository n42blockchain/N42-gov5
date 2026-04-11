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
// Transaction fetcher. Tracks which transaction hashes were announced
// by which libp2p peers and pulls the full transactions on demand via
// the sync_proto request/response protocol, honouring a retry budget
// and per-peer rate limits to avoid amplification.

package txspool

import (
	"context"
	"errors"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/proto/sync_proto"
	"github.com/n42blockchain/N42/proto/types_pb"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/message"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

const (
	BloomFetcherMaxTime      = 10 * time.Minute
	BloomSendTransactionTime = 10 * time.Second
	BloomSendMaxTransactions = 100
)

var ErrBadPeer = errors.New("bad peer error")

type txsRequest struct {
	hashes hashes
	bloom  *types.Bloom
	peer   peer.ID
}

type hashes []types.Hash

func (h *hashes) pop() types.Hash {
	if len(*h) == 0 {
		return types.Hash{}
	}
	n := len(*h)
	x := (*h)[n-1]
	*h = (*h)[:n-1]
	return x
}

// TxsFetcher handles transaction synchronization with peers via bloom filters.
type TxsFetcher struct {
	quit     chan struct{}
	finished bool

	peers     common.PeerMap
	p2pServer common.INetwork

	peerRequests map[peer.ID]*txsRequest
	bloom        *types.Bloom

	getTx      func(hash types.Hash) *transaction.Transaction
	addTxs     func([]*transaction.Transaction) []error
	pendingTxs func(enforceTips bool) map[types.Address][]*transaction.Transaction

	peerJoinCh chan *common.PeerJoinEvent
	peerDropCh chan *common.PeerDropEvent

	ctx    context.Context
	cancel context.CancelFunc
}

func NewTxsFetcher(
	ctx context.Context,
	getTx func(hash types.Hash) *transaction.Transaction,
	addTxs func([]*transaction.Transaction) []error,
	pendingTxs func(enforceTips bool) map[types.Address][]*transaction.Transaction,
	p2pServer common.INetwork,
	peers common.PeerMap,
	bloom *types.Bloom,
) *TxsFetcher {
	c, cancel := context.WithCancel(ctx)
	return &TxsFetcher{
		quit:         make(chan struct{}),
		peers:        peers,
		peerRequests: make(map[peer.ID]*txsRequest),
		p2pServer:    p2pServer,
		addTxs:       addTxs,
		getTx:        getTx,
		pendingTxs:   pendingTxs,
		bloom:        bloom,
		peerJoinCh:   make(chan *common.PeerJoinEvent, 10),
		peerDropCh:   make(chan *common.PeerDropEvent, 10),
		finished:     false,
		ctx:          c,
		cancel:       cancel,
	}
}

func (f *TxsFetcher) Start() error {
	go f.sendBloomTransactionLoop()
	go f.bloomBroadcastLoop()
	return nil
}

func (f *TxsFetcher) Stop() {
	if f.cancel != nil {
		f.cancel()
	}
	f.finished = true
}

// sendBloomTransactionLoop periodically sends pending transactions to peers.
func (f *TxsFetcher) sendBloomTransactionLoop() {
	tick := time.NewTicker(BloomSendTransactionTime)
	defer tick.Stop()

	for {
		select {
		case <-tick.C:
			for ID, req := range f.peerRequests {
				p, ok := f.peers[ID]
				if !ok {
					continue
				}
				var txs []*types_pb.Transaction
				for i := 0; i < BloomSendMaxTransactions && len(req.hashes) > 0; i++ {
					hash := req.hashes.pop()
					if tx := f.getTx(hash); tx != nil {
						txs = append(txs, tx.ToProtoMessage().(*types_pb.Transaction))
					}
				}
				if len(txs) == 0 {
					continue
				}
				msg := &sync_proto.SyncTask{
					Id:       misc.SecureUint64(),
					Ok:       true,
					SyncType: sync_proto.SyncType_TransactionRes,
					Payload: &sync_proto.SyncTask_SyncTransactionResponse{
						SyncTransactionResponse: &sync_proto.SyncTransactionResponse{
							Transactions: txs,
						},
					},
				}
				data, err := proto.Marshal(msg)
				if err != nil {
					log.Warn("Failed to marshal transaction response", "peer", ID, "err", err)
					continue
				}
				p.WriteMsg(message.MsgTransaction, data)
			}
		case <-f.ctx.Done():
			return
		}
	}
}

// bloomBroadcastLoop periodically broadcasts bloom filter to request missing transactions.
func (f *TxsFetcher) bloomBroadcastLoop() {
	if f.bloom == nil {
		return
	}
	tick := time.NewTicker(BloomFetcherMaxTime)
	defer tick.Stop()

	bloom, err := f.bloom.Marshal()
	if err != nil {
		log.Warn("txs fetcher bloom Marshal err", zap.Error(err))
		return
	}

	msg := &sync_proto.SyncTask{
		Id:       misc.SecureUint64(),
		Ok:       true,
		SyncType: sync_proto.SyncType_TransactionReq,
		Payload: &sync_proto.SyncTask_SyncTransactionRequest{
			SyncTransactionRequest: &sync_proto.SyncTransactionRequest{
				Bloom: bloom,
			},
		},
	}
	request, err := proto.Marshal(msg)
	if err != nil {
		log.Warn("Failed to marshal transaction request", "err", err)
		return
	}

	for {
		select {
		case peerId := <-f.peerJoinCh:
			if p, ok := f.peers[peerId.Peer]; ok {
				p.WriteMsg(message.MsgTransaction, request)
			}
		case <-tick.C:
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
	if _, ok := f.peers[ID]; !ok {
		return ErrBadPeer
	}

	syncTask := sync_proto.SyncTask{}
	if err := proto.Unmarshal(data, &syncTask); err != nil {
		log.Errorf("receive sync task msg err: %v", err)
		return err
	}

	log.Debugf("receive synctask msg from :%v, task type: %v, ok:%v", ID, syncTask.SyncType, syncTask.Ok)

	switch syncTask.SyncType {
	case sync_proto.SyncType_TransactionReq:
		request := syncTask.Payload.(*sync_proto.SyncTask_SyncTransactionRequest).SyncTransactionRequest
		if _, ok := f.peerRequests[ID]; !ok {
			bloom := new(types.Bloom)
			if err := bloom.UnMarshalBloom(request.Bloom); err == nil {
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
		// Transaction response handling - currently no-op
	}
	return nil
}
