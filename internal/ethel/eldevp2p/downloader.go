// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Forward-direction block downloader for eth-el.
//
// Design (cf. reth ReverseHeadersDownloader + BodiesRequestFuture,
// geth eth/downloader, erigon stagedsync HeadersStage + BodiesStage):
//
//   - One goroutine per peer drives the request loop for that peer.
//   - State: local head (read from SyncStageProgress and advanced by us
//     as we import), best known peer head, requests in flight.
//   - Loop while local < peerHead:
//       1. RequestHeaders(local+1, batchSize, forward) → wait OnBlockHeaders
//          response keyed by request ID.
//       2. RequestBodies(headerHashes) → wait OnBlockBodies response.
//       3. Decode raw RLP txs + withdrawals, assemble *block.Block, run
//          api.EngineStateAdapter.ExecutePayloadFromWire — that path
//          computes the state root and verifies against header.Root.
//       4. On success: advance SyncStageProgress("ethel-last-block").
//          On failure: drop the batch (don't advance) and retry next
//          iteration, potentially from a different peer.
//
// This is a v1: serial per-peer, single inflight request, no header
// pre-validation, no parallel body fetch, no priority queue across
// peers. Sufficient to start catch-up; the request pipeline can be
// expanded later to match reth's parallel fetcher when needed.

//go:build n42el

package eldevp2p

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/devp2p"
	"github.com/n42blockchain/N42/internal/network/eth69"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// progressKey is the chaindata key the catch-up executor writes the
// per-batch head to; we read it on every iteration and advance it on
// every successful import so other Node services (engine API, RPC)
// see the new head immediately.
var progressKey = []byte("ethel-last-block")

const progressTable = kv.SyncStageProgress

// headersPerBatch is the GetBlockHeaders batch size. eth/69 caps at
// 1024; geth peers commonly throttle to 192. 192 stays friendly with
// every major impl.
const headersPerBatch = 192

// requestTimeout bounds the wait on a single peer response before we
// give up and try another iteration.
const requestTimeout = 30 * time.Second

// Downloader satisfies devp2p.ResponseHandler. One instance per
// eldevp2p.Service; it tracks every peer that completes handshake and
// drives the catch-up fetch loop.
type Downloader struct {
	exec executionProvider

	mu       sync.Mutex
	peers    map[string]*peerState        // peerID → state
	inflight map[string]chan inflightResp // reqKey → reply channel
	adapter  *api.EngineStateAdapter
}

// executionProvider is the surface the downloader needs from the
// surrounding eth-el Node to build the EngineStateAdapter and read
// chaindata for ongoing progress.
type executionProvider interface {
	RwDB() kv.RwDB
	ChainConfig() *params.ChainConfig
	Engine() consensus.Engine
	OutFreezer() *freezer.Freezer
}

// peerState tracks a connected peer.
type peerState struct {
	rw       gethp2p.MsgReadWriter
	head     uint64
	headHash types.Hash
	cancel   context.CancelFunc
}

// inflightResp carries either headers or bodies depending on which
// request the reply matches. exactly one of headers / bodies is
// non-nil on success.
type inflightResp struct {
	headers []*block.Header
	bodies  []devp2p.BlockBody
}

// NewDownloader builds the orchestrator without starting it.
func NewDownloader(exec executionProvider) *Downloader {
	return &Downloader{
		exec:     exec,
		peers:    make(map[string]*peerState),
		inflight: make(map[string]chan inflightResp),
	}
}

// initAdapter builds the EngineStateAdapter on first use. Same wiring
// as ethELBackend.initAPIs and engineapi.Service.Start (Phase 7.1.1.b).
func (d *Downloader) initAdapter() error {
	if d.adapter != nil {
		return nil
	}
	if d.exec == nil {
		return errors.New("eldevp2p downloader: no executionProvider")
	}
	rwdb := d.exec.RwDB()
	if rwdb == nil {
		return errors.New("eldevp2p downloader: RwDB not opened")
	}
	d.adapter = api.NewEngineStateAdapter(
		rwdb, d.exec.OutFreezer(), d.exec.ChainConfig(), d.exec.Engine(),
	)
	return nil
}

func (d *Downloader) localHead(ctx context.Context) (uint64, error) {
	tx, err := d.exec.RwDB().BeginRo(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	v, _ := tx.GetOne(progressTable, progressKey)
	if len(v) != 8 {
		return 0, nil
	}
	return binary.BigEndian.Uint64(v), nil
}

func (d *Downloader) writeLocalHead(ctx context.Context, head uint64) error {
	tx, err := d.exec.RwDB().BeginRw(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v := make([]byte, 8)
	binary.BigEndian.PutUint64(v, head)
	if err := tx.Put(progressTable, progressKey, v); err != nil {
		return err
	}
	return tx.Commit()
}

// --- devp2p.ResponseHandler ----------------------------------------------

// OnPeerHandshake registers a peer and kicks off the download loop.
func (d *Downloader) OnPeerHandshake(peerID string, rw gethp2p.MsgReadWriter, peerHead uint64, peerHash types.Hash) {
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.peers[peerID] = &peerState{rw: rw, head: peerHead, headHash: peerHash, cancel: cancel}
	d.mu.Unlock()
	go d.runPeerLoop(ctx, peerID)
}

// OnPeerDisconnect tears down the per-peer loop.
func (d *Downloader) OnPeerDisconnect(peerID string) {
	d.mu.Lock()
	if p, ok := d.peers[peerID]; ok {
		p.cancel()
		delete(d.peers, peerID)
	}
	d.mu.Unlock()
}

// OnBlockHeaders forwards to the matching inflight request.
func (d *Downloader) OnBlockHeaders(peerID string, reqID uint64, headers []*block.Header) {
	d.dispatch(peerID, reqID, "h", inflightResp{headers: headers})
}

// OnBlockBodies forwards to the matching inflight request.
func (d *Downloader) OnBlockBodies(peerID string, reqID uint64, bodies []devp2p.BlockBody) {
	d.dispatch(peerID, reqID, "b", inflightResp{bodies: bodies})
}

// OnNewBlock is the live tip push from a peer. v1 ignores — we only
// catch up via batched fetch; live mode is a follow-up that wires
// NewBlock into the same import path.
func (d *Downloader) OnNewBlock(_ string, _ *block.Header, _ [][]byte) {}

func (d *Downloader) dispatch(peerID string, reqID uint64, kind string, resp inflightResp) {
	key := fmt.Sprintf("%s:%s:%d", peerID, kind, reqID)
	d.mu.Lock()
	ch, ok := d.inflight[key]
	if ok {
		delete(d.inflight, key)
	}
	d.mu.Unlock()
	if ok {
		select {
		case ch <- resp:
		default:
		}
	}
}

// --- per-peer loop -------------------------------------------------------

func (d *Downloader) runPeerLoop(ctx context.Context, peerID string) {
	if err := d.initAdapter(); err != nil {
		log.Error("eldevp2p: cannot init adapter", "err", err)
		return
	}

	d.mu.Lock()
	p := d.peers[peerID]
	d.mu.Unlock()
	if p == nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		local, err := d.localHead(ctx)
		if err != nil {
			log.Warn("eldevp2p: read local head", "err", err)
			time.Sleep(2 * time.Second)
			continue
		}

		if local >= p.head {
			// Caught up to this peer; wait a slot before re-checking
			// in case they push a new tip.
			log.Info("eldevp2p: caught up to peer", "peer", peerID[:16], "head", local)
			time.Sleep(12 * time.Second)
			continue
		}

		start := local + 1
		amount := uint64(headersPerBatch)
		if start+amount > p.head+1 {
			amount = p.head - start + 1
		}

		log.Info("eldevp2p: requesting headers", "peer", peerID[:16],
			"from", start, "amount", amount, "remaining", p.head-local)

		headers, err := d.requestHeaders(ctx, peerID, p.rw, start, amount)
		if err != nil {
			log.Warn("eldevp2p: header fetch failed", "peer", peerID[:16], "err", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if len(headers) == 0 {
			log.Warn("eldevp2p: empty header batch", "peer", peerID[:16], "from", start)
			time.Sleep(2 * time.Second)
			continue
		}

		hashes := make([]types.Hash, len(headers))
		for i, h := range headers {
			hashes[i] = h.Hash()
		}
		bodies, err := d.requestBodies(ctx, peerID, p.rw, hashes)
		if err != nil {
			log.Warn("eldevp2p: body fetch failed", "peer", peerID[:16], "err", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if len(bodies) != len(headers) {
			log.Warn("eldevp2p: body/header count mismatch",
				"headers", len(headers), "bodies", len(bodies))
			time.Sleep(2 * time.Second)
			continue
		}

		// Import each block sequentially. On any failure, abandon the
		// remainder of the batch — the next iteration will retry from
		// our updated head, possibly with a different peer.
		var imported uint64
		for i, hdr := range headers {
			blk, withdrawals, err := assembleBlock(hdr, bodies[i])
			if err != nil {
				log.Warn("eldevp2p: assemble block failed",
					"block", hdr.Number64().Uint64(), "err", err)
				break
			}
			ok, root, err := d.adapter.ExecutePayloadFromWire(blk, withdrawals)
			if err != nil {
				log.Warn("eldevp2p: ExecutePayloadFromWire error",
					"block", hdr.Number64().Uint64(), "err", err)
				break
			}
			if !ok {
				log.Warn("eldevp2p: payload invalid",
					"block", hdr.Number64().Uint64(),
					"computedRoot", root.Hex(),
					"declaredRoot", hdr.Root.Hex())
				break
			}
			imported++
		}

		if imported > 0 {
			newHead := start + imported - 1
			if err := d.writeLocalHead(ctx, newHead); err != nil {
				log.Error("eldevp2p: write head failed", "head", newHead, "err", err)
			} else {
				log.Info("eldevp2p: imported batch",
					"peer", peerID[:16],
					"from", start, "imported", imported,
					"newHead", newHead, "remaining", p.head-newHead)
			}
		} else {
			// Made no progress this iteration; back off briefly so we
			// don't hot-loop on a misbehaving peer.
			time.Sleep(5 * time.Second)
		}
	}
}

func (d *Downloader) requestHeaders(ctx context.Context, peerID string, rw gethp2p.MsgReadWriter, from, amount uint64) ([]*block.Header, error) {
	reqID := uint64(time.Now().UnixNano())
	key := fmt.Sprintf("%s:h:%d", peerID, reqID)
	ch := make(chan inflightResp, 1)
	d.mu.Lock()
	d.inflight[key] = ch
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.inflight, key)
		d.mu.Unlock()
	}()

	req := eth69.GetBlockHeadersPacket{
		RequestID: reqID,
		Origin:    eth69.HashOrNumber{Number: from},
		Amount:    amount,
		Skip:      0,
		Reverse:   false,
	}
	if err := gethp2p.Send(rw, eth69.GetBlockHeadersMsg, &req); err != nil {
		return nil, fmt.Errorf("send GetBlockHeaders: %w", err)
	}
	select {
	case resp := <-ch:
		return resp.headers, nil
	case <-time.After(requestTimeout):
		return nil, errors.New("headers timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *Downloader) requestBodies(ctx context.Context, peerID string, rw gethp2p.MsgReadWriter, hashes []types.Hash) ([]devp2p.BlockBody, error) {
	reqID := uint64(time.Now().UnixNano())
	key := fmt.Sprintf("%s:b:%d", peerID, reqID)
	ch := make(chan inflightResp, 1)
	d.mu.Lock()
	d.inflight[key] = ch
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.inflight, key)
		d.mu.Unlock()
	}()

	req := eth69.GetBlockBodiesPacket{
		RequestID: reqID,
		Hashes:    hashes,
	}
	if err := gethp2p.Send(rw, eth69.GetBlockBodiesMsg, &req); err != nil {
		return nil, fmt.Errorf("send GetBlockBodies: %w", err)
	}
	select {
	case resp := <-ch:
		return resp.bodies, nil
	case <-time.After(requestTimeout):
		return nil, errors.New("bodies timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// --- block assembly ------------------------------------------------------

// assembleBlock decodes the wire transactions + withdrawals and stitches
// them onto the wire header to produce a *block.Block ready for
// EngineStateAdapter.ExecutePayloadFromWire. Withdrawals are returned
// separately because the N42 Body type doesn't carry them.
func assembleBlock(hdr *block.Header, body devp2p.BlockBody) (*block.Block, []*api.Withdrawal, error) {
	if hdr == nil {
		return nil, nil, errors.New("nil header")
	}
	txs, err := decodeTxs(body.Transactions)
	if err != nil {
		return nil, nil, fmt.Errorf("decode txs: %w", err)
	}
	withdrawals, err := decodeWithdrawals(body.Withdrawals)
	if err != nil {
		return nil, nil, fmt.Errorf("decode withdrawals: %w", err)
	}
	ib := block.NewBlock(hdr, txs)
	blk, ok := ib.(*block.Block)
	if !ok {
		return nil, nil, fmt.Errorf("unexpected block type %T", ib)
	}
	return blk, withdrawals, nil
}

func decodeTxs(raw [][]byte) ([]*transaction.Transaction, error) {
	out := make([]*transaction.Transaction, 0, len(raw))
	for i, r := range raw {
		if len(r) == 0 {
			continue
		}
		tx, err := transaction.DecodeEthereumTransaction(r)
		if err != nil {
			return nil, fmt.Errorf("tx %d: %w", i, err)
		}
		out = append(out, tx)
	}
	return out, nil
}

// wireWithdrawal mirrors the eth/69 wire layout for withdrawals
// (EIP-4895): [index, validatorIndex, address, amount].
type wireWithdrawal struct {
	Index          uint64
	ValidatorIndex uint64
	Address        types.Address
	Amount         uint64
}

func decodeWithdrawals(raw [][]byte) ([]*api.Withdrawal, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]*api.Withdrawal, len(raw))
	for i, r := range raw {
		var w wireWithdrawal
		if err := rlp.DecodeBytes(r, &w); err != nil {
			return nil, fmt.Errorf("withdrawal %d: %w", i, err)
		}
		out[i] = &api.Withdrawal{
			Index:          hexutil.Uint64(w.Index),
			ValidatorIndex: hexutil.Uint64(w.ValidatorIndex),
			Address:        w.Address,
			Amount:         hexutil.Uint64(w.Amount),
		}
	}
	return out, nil
}
