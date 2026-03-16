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

package download

import (
	"context"
	"errors"
	"fmt"
	"hash"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/api/protocol/sync_proto"
	"github.com/n42blockchain/N42/api/protocol/types_pb"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/utils"
)

var (
	ErrBusy                   = errors.New("busy")
	ErrCanceled               = errors.New("syncing canceled (requested)")
	ErrSyncBlock              = errors.New("err sync block")
	ErrTimeout                = errors.New("timeout")
	ErrBadPeer                = errors.New("bad peer error")
	ErrNoPeers                = errors.New("no peers to download")
	ErrInvalidNetwork         = errors.New("network is nil")
	ErrInvalidPubSub          = errors.New("PubSub is nil")
	ErrInvalidSyncTaskPayload = errors.New("invalid sync task payload")
)

const (
	maxHeaderFetch          = 192  // Maximum number of headers per request
	maxBodiesFetch          = 128  // Maximum number of bodies per request
	maxResultsProcess       = 2048 // Maximum number of results to import at once
	syncPeerCount           = 6
	syncTimeTick            = 10 * time.Second
	syncTimeOutPerRequest   = 1 * time.Minute
	syncPeerIntervalRequest = 3 * time.Second
)

type headerResponse struct {
	taskID  uint64
	ok      bool
	headers []*types_pb.Header
}

type bodyResponse struct {
	taskID uint64
	ok     bool
	bodies []*types_pb.Block
}

type blockTask struct {
	taskID uint64
	ok     bool
	number []uint256.Int
}

type Task struct {
	taskID     uint64
	Id         peer.ID
	H          hash.Hash
	TimeBegin  time.Time
	IsSync     bool
	IndexBegin uint256.Int
	IndexEnd   uint256.Int
}

type Downloader struct {
	mode uint32 // sync mode; use d.getMode() to get the SyncMode

	bc            common.IBlockChain
	network       common.INetwork
	isDownloading int32

	highestNumber uint256.Int
	highestMu     sync.RWMutex

	ctx        context.Context
	cancel     context.CancelFunc
	cancelLock sync.RWMutex
	cancelWg   sync.WaitGroup
	once       sync.Once

	errorCh chan error

	pubsub    common.IPubSub
	peersInfo *peersInfo

	headerTasks           []Task
	headerProcessingTasks map[uint64]Task
	headerResultStore     map[uint256.Int]*types_pb.Header
	headerTaskLock        sync.Mutex
	headerProcCh          chan *headerResponse

	blockProcCh chan *bodyResponse

	bodyTaskPoolLock    sync.Mutex
	bodyTaskPool        []*blockTask
	bodyProcessingTasks map[uint64]*blockTask
	bodyResultStore     map[uint256.Int]*types_pb.Block
}

func NewDownloader(ctx context.Context, bc common.IBlockChain, network common.INetwork, pubsub common.IPubSub, peers common.PeerMap) common.IDownloader {
	c, cancel := context.WithCancel(ctx)

	highestNumber := cloneCurrentBlockNumberOrZero(bc)
	for _, peer := range peers {
		if peer.CurrentHeight != nil && highestNumber.Uint64() < peer.CurrentHeight.Uint64() {
			highestNumber = peer.CurrentHeight.Clone()
		}
	}

	return &Downloader{
		mode:                  uint32(FullSync),
		bc:                    bc,
		network:               network,
		ctx:                   c,
		cancel:                cancel,
		isDownloading:         0,
		pubsub:                pubsub,
		errorCh:               make(chan error, 10),
		headerTasks:           make([]Task, 0),
		headerProcessingTasks: make(map[uint64]Task),
		headerResultStore:     make(map[uint256.Int]*types_pb.Header),
		headerProcCh:          make(chan *headerResponse, 10),
		blockProcCh:           make(chan *bodyResponse, 10),
		bodyTaskPool:          make([]*blockTask, 0),
		bodyProcessingTasks:   make(map[uint64]*blockTask),
		bodyResultStore:       make(map[uint256.Int]*types_pb.Block),
		highestNumber:         *highestNumber,
		peersInfo:             newPeersInfo(c, peers),
	}
}

func (d *Downloader) getMode() SyncMode {
	return SyncMode(atomic.LoadUint32(&d.mode))
}

func (d *Downloader) FindBlock(number uint64, peerID peer.ID) (uint64, error) {
	return 0, nil
}

func (d *Downloader) waitAvailablePeer() {
	timer := time.NewTicker(1 * time.Second)
	defer timer.Stop()

	timeOutTimer := time.NewTicker(60 * time.Second)
	defer timeOutTimer.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-timer.C:
			peers := d.peersInfo.findPeers(uint256.NewInt(currentBlockNumberOrZero(d.bc)+1), 10)
			if len(peers) > 0 {
				return
			}
		case <-timeOutTimer.C:
			log.Warn("Can not find Peers")
		}
	}
}

// Start starts the Downloader service.
func (d *Downloader) Start() error {
	if d.network == nil {
		return ErrInvalidNetwork
	}

	go d.pubSubLoop()

	// If this is a bootstrap node, signal completion immediately
	if d.network.Bootstrapped() {
		event.GlobalEvent.Send(common.DownloaderFinishEvent{})
		return nil
	}

	go d.synchronise()
	return nil
}

// doSync performs the actual synchronization using the specified mode.
func (d *Downloader) doSync(mode SyncMode) error {
	log.Info("do sync", zap.Int("SyncMode", int(mode)))
	if !atomic.CompareAndSwapInt32(&d.isDownloading, 0, 1) {
		return ErrBusy
	}
	defer atomic.StoreInt32(&d.isDownloading, 0)

	origin, err := d.findAncestor()
	if err != nil {
		return err
	}
	latest, err := d.findHead()
	if err != nil {
		return err
	}

	var fetchers []func() error
	if mode != HeaderSync {
		fetchers = append(fetchers,
			func() error { return d.fetchHeaders(origin, latest) },
			func() error { return d.fetchBodies(latest) },
			func() error { return d.processHeaders() },
		)
	}
	fetchers = append(fetchers,
		func() error { return d.processBodies() },
		func() error { return d.processChain() },
	)

	return d.spawnSync(fetchers)
}

// spawnSync launches all fetcher goroutines and waits for completion or error.
func (d *Downloader) spawnSync(fetchers []func() error) error {
	errc := make(chan error, len(fetchers))
	d.cancelWg.Add(len(fetchers))
	for _, fn := range fetchers {
		fn := fn
		go func() {
			defer d.cancelWg.Done()
			defer func() {
				if r := recover(); r != nil {
					errc <- fmt.Errorf("panic in downloader: %v", r)
				}
			}()
			errc <- fn()
		}()
	}
	var err error
	for i := 0; i < len(fetchers); i++ {
		if err = <-errc; err != nil {
			break
		}
	}
	d.Close()
	return err
}

func (d *Downloader) SyncHeader() error {
	return d.doSync(HeaderSync)
}

func (d *Downloader) SyncBody() error {
	return nil
}

func (d *Downloader) SyncTx() error {
	return nil
}

func (d *Downloader) IsDownloading() bool {
	return atomic.LoadInt32(&d.isDownloading) != 0
}

func (d *Downloader) findAncestor() (uint256.Int, error) {
	number, err := requireCurrentBlockNumber(d.bc, "current block number unavailable")
	if err != nil {
		return uint256.Int{}, err
	}
	return *number.Clone(), nil
}

func (d *Downloader) findHead() (uint256.Int, error) {
	d.highestMu.RLock()
	defer d.highestMu.RUnlock()
	return d.highestNumber, nil
}

func (d *Downloader) pubSubLoop() {
	defer close(d.errorCh)
	defer d.cancel()

	highestBlockCh := make(chan common.ChainHighestBlock)
	defer close(highestBlockCh)
	highestSub, err := event.GlobalEvent.Subscribe(highestBlockCh)
	if err != nil || highestSub == nil {
		log.Errorf("failed to subscribe to highest block events: %v", err)
		return
	}
	defer highestSub.Unsubscribe()

	for {
		select {
		case <-d.ctx.Done():
			return
		case err := <-highestSub.Err():
			log.Debugf("receive a err from highestSub %v", err)
			return
		case highestBlock, ok := <-highestBlockCh:
			if !ok {
				continue
			}
			blockNumber, err := requireBlockNumber(&highestBlock.Block, "highest block number unavailable")
			if err != nil {
				log.Warn("ignoring highest block event", "err", err)
				continue
			}
			d.highestMu.RLock()
			curHighest := d.highestNumber.Uint64()
			d.highestMu.RUnlock()
			if blockNumber.Uint64() > curHighest {
				log.Debugf("receive a new highestBlock block number: %d", blockNumber.Uint64())
				d.highestMu.Lock()
				d.highestNumber = *blockNumber.Clone()
				d.highestMu.Unlock()
				if highestBlock.Inserted {
					d.peersInfo.peerInfoBroadcast(blockNumber)
				}
			}
		}
	}
}

func (d *Downloader) synchronise() {
	log.Info("start downloader")
	defer log.Info("downloader finished")

	d.waitAvailablePeer()

	event.GlobalEvent.Send(common.DownloaderStartEvent{})
	defer event.GlobalEvent.Send(common.DownloaderFinishEvent{})
	defer d.cancel()

	tick := time.NewTicker(syncTimeTick)
	defer tick.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case err, ok := <-d.errorCh:
			if ok {
				log.Errorf("failed to running downloader, err:%v", err)
			}
			return
		case <-tick.C:
			d.highestMu.RLock()
			highest := d.highestNumber.Clone()
			d.highestMu.RUnlock()
			currentNumber, err := requireCurrentBlockNumber(d.bc, "current block number unavailable")
			if err != nil {
				log.Warn("downloader skipped sync check", "err", err)
				tick.Reset(syncTimeTick)
				continue
			}
			difference := new(uint256.Int).Sub(highest, currentNumber)
			log.Tracef("highest: %d, current: %d", highest.Uint64(), currentNumber.Uint64())
			if difference.Uint64() > 1 {
				log.Infof("start downloader Compare Loop remote highestNumber: %d, current number: %d, difference: %d", highest.Uint64(), currentNumber.Uint64(), difference.Uint64())
				if err := d.doSync(d.getMode()); err != nil {
					log.Errorf("failed to running downloader, err:%v", err)
				}
				return
			}
			tick.Reset(syncTimeTick)
		}
	}
}

func (d *Downloader) ConnHandler(data []byte, ID peer.ID) error {
	p, ok := d.peersInfo.get(ID)
	if !ok {
		return ErrBadPeer
	}

	var syncTask sync_proto.SyncTask
	if err := proto.Unmarshal(data, &syncTask); err != nil {
		log.Errorf("receive sync task msg unmarshal err: %v", err)
		return err
	}

	taskID := syncTask.Id
	params := []interface{}{"peerID", ID, "taskType", syncTask.SyncType, "taskID", taskID, "isOK", syncTask.Ok}

	switch syncTask.SyncType {

	case sync_proto.SyncType_HeaderRes:
		headersResponse := syncTask.GetSyncHeaderResponse()
		if headersResponse == nil {
			return fmt.Errorf("%w: %s", ErrInvalidSyncTaskPayload, syncTask.SyncType.String())
		}
		// Security: bounds check before accessing slice elements
		if len(headersResponse.Headers) > 0 {
			firstNumber, err := requireProtoHeaderNumber(headersResponse.Headers[0], "header number unavailable")
			if err != nil {
				return err
			}
			lastNumber, err := requireProtoHeaderNumber(headersResponse.Headers[len(headersResponse.Headers)-1], "header number unavailable")
			if err != nil {
				return err
			}
			params = append(params, "headerCount", len(headersResponse.Headers), "headerNumberFrom", firstNumber.Uint64(), "headerNumberTo", lastNumber.Uint64())
		} else {
			params = append(params, "headerCount", 0)
		}
		d.headerProcCh <- &headerResponse{taskID: taskID, ok: syncTask.Ok, headers: headersResponse.Headers}

	case sync_proto.SyncType_HeaderReq:
		headerRequest := syncTask.GetSyncHeaderRequest()
		if headerRequest == nil {
			return fmt.Errorf("%w: %s", ErrInvalidSyncTaskPayload, syncTask.SyncType.String())
		}
		params = append(params, "Amount", utils.ConvertH256ToUint256Int(headerRequest.Amount).Uint64(), "headerNumberFrom", utils.ConvertH256ToUint256Int(headerRequest.Number).Uint64())
		go d.responseHeaders(taskID, p, headerRequest)

	case sync_proto.SyncType_BodyRes:
		bodiesResponse := syncTask.GetSyncBlockResponse()
		if bodiesResponse == nil {
			return fmt.Errorf("%w: %s", ErrInvalidSyncTaskPayload, syncTask.SyncType.String())
		}
		// Security: bounds check before accessing slice elements
		if len(bodiesResponse.Blocks) > 0 {
			firstNumber, err := requireProtoBlockNumber(bodiesResponse.Blocks[0], "block number unavailable")
			if err != nil {
				return err
			}
			lastNumber, err := requireProtoBlockNumber(bodiesResponse.Blocks[len(bodiesResponse.Blocks)-1], "block number unavailable")
			if err != nil {
				return err
			}
			params = append(params, "blocksCount", len(bodiesResponse.Blocks), "bodyNumberFrom", firstNumber.Uint64(), "bodyNumberTo", lastNumber.Uint64())
		} else {
			params = append(params, "blocksCount", 0)
		}
		d.blockProcCh <- &bodyResponse{taskID: taskID, ok: syncTask.Ok, bodies: bodiesResponse.Blocks}

	case sync_proto.SyncType_BodyReq:
		blockRequest := syncTask.GetSyncBlockRequest()
		if blockRequest == nil {
			return fmt.Errorf("%w: %s", ErrInvalidSyncTaskPayload, syncTask.SyncType.String())
		}
		// Security: bounds check before accessing slice elements
		if len(blockRequest.Number) > 0 {
			params = append(params, "bodyNumberFrom", utils.ConvertH256ToUint256Int(blockRequest.Number[0]).Uint64(), "bodyNumberTo", utils.ConvertH256ToUint256Int(blockRequest.Number[len(blockRequest.Number)-1]).Uint64())
		} else {
			params = append(params, "bodyCount", 0)
		}
		go d.responseBlocks(taskID, p, blockRequest)

	case sync_proto.SyncType_PeerInfoBroadcast:
		peerInfoBroadcast := syncTask.GetSyncPeerInfoBroadcast()
		if peerInfoBroadcast == nil {
			return fmt.Errorf("%w: %s", ErrInvalidSyncTaskPayload, syncTask.SyncType.String())
		}
		currentNumber := utils.ConvertH256ToUint256Int(peerInfoBroadcast.Number)
		currentDifficulty := utils.ConvertH256ToUint256Int(peerInfoBroadcast.Difficulty)
		params = append(params, "Number", currentNumber, "Difficulty", currentDifficulty)
		d.highestMu.Lock()
		if currentNumber.Uint64() > d.highestNumber.Uint64() {
			d.highestNumber.Set(currentNumber.Clone())
		}
		d.highestMu.Unlock()
		d.peersInfo.update(p.ID(), currentNumber, currentDifficulty)
	}

	log.Info("receive sync task msg", params...)

	return nil
}

func (d *Downloader) Close() error {
	d.cancelLock.Lock()
	defer d.cancelLock.Unlock()
	d.cancel()
	d.cancelWg.Wait()
	return nil
}
