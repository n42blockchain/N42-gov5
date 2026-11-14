// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Downloader wire requests to peers.

//go:build n42el

package eldevp2p

import (
	"context"
	"errors"
	"fmt"
	"time"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/devp2p"
	"github.com/n42blockchain/N42/internal/network/eth69"
	"github.com/n42blockchain/N42/log"
)

func (d *Downloader) requestHeaders(ctx context.Context, peerID string, rw gethp2p.MsgReadWriter, from, amount uint64) ([][]byte, error) {
	return d.requestHeadersQuery(ctx, peerID, rw, eth69.HashOrNumber{Number: from}, amount, false)
}

func (d *Downloader) requestHeadersByHash(ctx context.Context, peerID string, rw gethp2p.MsgReadWriter, hash types.Hash, amount uint64, reverse bool) ([][]byte, error) {
	return d.requestHeadersQuery(ctx, peerID, rw, eth69.HashOrNumber{Hash: hash}, amount, reverse)
}

func (d *Downloader) requestHeadersQuery(ctx context.Context, peerID string, rw gethp2p.MsgReadWriter, origin eth69.HashOrNumber, amount uint64, reverse bool) ([][]byte, error) {
	reqID := d.reqSeq.Add(1)
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
		GetBlockHeadersQuery: &eth69.GetBlockHeadersQuery{
			Origin:  origin,
			Amount:  amount,
			Skip:    0,
			Reverse: reverse,
		},
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
	reqID := d.reqSeq.Add(1)
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

func (d *Downloader) requestReceipts(ctx context.Context, peerID string, rw gethp2p.MsgReadWriter, hashes []types.Hash) ([][]rlp.RawValue, error) {
	reqID := d.reqSeq.Add(1)
	key := fmt.Sprintf("%s:r:%d", peerID, reqID)
	ch := make(chan inflightResp, 1)
	d.mu.Lock()
	d.inflight[key] = ch
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.inflight, key)
		d.mu.Unlock()
	}()

	req := eth69.GetReceiptsPacket{RequestID: reqID, Hashes: hashes}
	if err := gethp2p.Send(rw, eth69.GetReceiptsMsg, &req); err != nil {
		return nil, fmt.Errorf("send GetReceipts: %w", err)
	}
	select {
	case resp := <-ch:
		return resp.receipts, nil
	case <-time.After(requestTimeout):
		return nil, errors.New("receipts timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// requestBlockAccessLists fetches the out-of-band EIP-7928 BALs for the given
// block hashes from a peer (eth/71-style GetBlockAccessLists 0x12). Returns the
// raw BAL RLP per requested hash, in request order (nil where the peer has none),
// so a consumer can decode + verify against the header hash and drive prefetch.
func (d *Downloader) requestBlockAccessLists(ctx context.Context, peerID string, rw gethp2p.MsgReadWriter, hashes []types.Hash) ([][]byte, error) {
	if len(hashes) > maxBlockAccessListsRequest {
		return nil, fmt.Errorf("too many block access lists requested: %d > %d", len(hashes), maxBlockAccessListsRequest)
	}
	reqID := d.reqSeq.Add(1)
	key := fmt.Sprintf("%s:a:%d", peerID, reqID)
	ch := make(chan inflightResp, 1)
	d.mu.Lock()
	d.inflight[key] = ch
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.inflight, key)
		d.mu.Unlock()
	}()

	req := getBlockAccessListsRequest{RequestID: reqID, Hashes: hashes}
	if err := gethp2p.Send(rw, eth69.GetBlockAccessListsMsg, &req); err != nil {
		return nil, fmt.Errorf("send GetBlockAccessLists: %w", err)
	}
	select {
	case resp := <-ch:
		if len(resp.bals) > len(hashes) {
			return nil, fmt.Errorf("block access lists response has %d entries for %d requested hashes", len(resp.bals), len(hashes))
		}
		out := make([][]byte, len(resp.bals))
		for i, b := range resp.bals {
			out[i] = []byte(b)
		}
		return out, nil
	case <-time.After(requestTimeout):
		return nil, errors.New("block access lists timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// FetchBlockAccessLists fetches the out-of-band EIP-7928 BALs for the given block
// hashes from any available peer at atHeight. Public entry for the BAL
// prefetch/verify path: the caller decodes each raw BAL (bal.DecodeBAL), verifies
// it against the header's BlockAccessListHash, and can prewarm state from it.
// It needs an eth/71 peer; N42 does not advertise eth/71 (its BAL format is
// not the spec's), so this finds no peer until spec eth/71 lands.
func (d *Downloader) FetchBlockAccessLists(ctx context.Context, atHeight uint64, hashes []types.Hash) ([][]byte, error) {
	pp := d.pickPeerWithMinVersion(atHeight, eth69.ETH71)
	if pp == nil {
		return nil, errors.New("no peer for block access lists")
	}
	defer d.releasePeer(pp)
	return d.requestBlockAccessLists(ctx, pp.id, pp.rw, hashes)
}

// dumpMainnetReceipts (N42_RCPTDIFF diag) fetches block n's canonical receipts
// from a peer and logs each tx's status + cumulativeGasUsed, so it can be diffed
// against N42's per-tx gas (GAS161 logs) to pin the first divergent tx. eth/69
// receipt wire form is [txType, status, cumulativeGasUsed, logs]; cumGas is the
// second-to-last field (logs is last), which also handles a 3-field legacy form.
func (d *Downloader) dumpMainnetReceipts(ctx context.Context, n uint64, blockHash types.Hash) {
	pp := d.pickPeer(n)
	if pp == nil {
		log.Warn("RCPTDIFF no peer", "block", n)
		return
	}
	defer d.releasePeer(pp)
	res, err := d.requestReceipts(ctx, pp.id, pp.rw, []types.Hash{blockHash})
	if err != nil {
		log.Warn("RCPTDIFF fetch err", "block", n, "err", err)
		return
	}
	if len(res) == 0 {
		log.Warn("RCPTDIFF empty response", "block", n)
		return
	}
	for i, raw := range res[0] {
		var fields []rlp.RawValue
		if err := rlp.DecodeBytes(raw, &fields); err != nil || len(fields) < 2 {
			log.Warn("RCPTDIFF decode err", "block", n, "i", i, "err", err, "nfields", len(fields))
			continue
		}
		var status, cum uint64
		_ = rlp.DecodeBytes(fields[len(fields)-3], &status)
		_ = rlp.DecodeBytes(fields[len(fields)-2], &cum)
		log.Warn("RCPTDIFF mainnet tx", "block", n, "i", i, "status", status, "cumGas", cum)
	}
}
