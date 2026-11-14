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
// Sealed-block gossip, direct push and consensus-evidence access.

package internal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// ReadConsensusEvidence reads the stored consensus evidence for a block number,
// satisfying the hotstuff.CEReader interface (parent-CE lookup for the
// ParentBeaconRoot link).
func (bc *BlockChain) ReadConsensusEvidence(blockNum uint64) (*rawdb.ConsensusEvidence, error) {
	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return rawdb.ReadConsensusEvidence(tx, blockNum)
}

// =============================================================================
// Peer and P2P
// =============================================================================

func (bc *BlockChain) AddPeer(hash string, remoteBlock uint64, peerID peer.ID) error {
	if bc.genesisBlock.Hash().String() != hash {
		return errors.New("failed to addPeer, err: genesis block different")
	}
	bc.lock.Lock()
	defer bc.lock.Unlock()
	if _, ok := bc.peers[peerID]; ok {
		return errors.New("failed to addPeer, err: the peer already exists")
	}

	log.Debugf("local height:%d --> remote height: %d", bc.CurrentBlock().Number64(), remoteBlock)
	bc.peers[peerID] = true
	return nil
}

func (bc *BlockChain) SealedBlock(b block.IBlock) error {
	// Reliable direct push to each connected peer. Single-publisher gossip is
	// unreliable for HotStuff block delivery — when only the current leader
	// publishes the block topic, the gossipsub mesh doesn't form and followers
	// never receive the block (so they can't import/vote on the hash-only
	// Proposal). Mirrors the Rust node's send_block_direct_reliable.
	// Encode the block to ETH-standard RLP once and reuse the bytes for both the
	// direct push and the gossip fallback (the hash is keccak(rlp(header)), so the
	// same bytes are semantically identical on every path). Avoids re-encoding the
	// block twice per seal on the producer's hot path.
	data, err := rlp.EncodeToBytes(b)
	if err != nil {
		return err
	}
	// Defence in depth: the miner budgets the packed size against the same bound
	// (see miner/block_size.go), so this must never trigger. If it does, fail
	// loudly here instead of letting both delivery paths reject the block and
	// livelock import-gated voting with only a low-level encoder error to go on.
	if limit := encoder.MaxWireMessageSize(); uint64(len(data)) > limit {
		log.Error("sealed block exceeds p2p wire limit and cannot propagate",
			"number", b.Number64().Uint64(), "hash", b.Hash().Hex(),
			"txs", len(b.Transactions()), "size", len(data), "limit", limit)
		return fmt.Errorf("sealed block %d is %d bytes, above the %d byte p2p wire limit", b.Number64().Uint64(), len(data), limit)
	}
	// A frame-referenced block (P1b) encodes as its header plus the ordered
	// frame-hash list (block.blockRLP.FrameHashes, no transaction data):
	// tens of bytes per frame instead of the transactions, which every
	// validator already holds as frames or pulls over FRQ1. The HotStuff
	// proposal itself carries only the block hash and TxHash, so this small
	// push is how the hash list reaches the followers; the full body is never
	// sent. Ordinary blocks push their full body as before.
	if body, ok := b.Body().(*block.Body); ok && body.IsFrameBody() {
		log.Debug("sealed frame block push", "number", b.Number64().Uint64(), "frames", len(body.FrameHashes),
			"txs", len(body.Txs), "bytes", len(data))
	}
	peerCount := bc.directPushBlock(b, data)
	// Gossip as a best-effort fallback, off the seal path: the direct pushes
	// are already in flight, and compressing and publishing an ~18 MB block
	// here held the Proposal back (part of the leader's ~180 ms push phase).
	//
	// S15b (docs/QS_BLOCK_TIME_BUDGET.md 6cg): this same ~18-26 MB gossip
	// publish (and, on every OTHER node, the receive side's validation/
	// forwarding of it) rides the identical per-peer GossipSub streams the
	// consensus messages (Proposal/Vote/PrepareQC/CommitVote/Decide) use,
	// and 6cg's block profile shows the gossiped copy and the direct push of
	// the SAME block contending for bc.lock on InsertChain. Hypothesis G is
	// that this head-of-line-blocks the vote round-trip. N42_BLOCK_GOSSIP_FALLBACK=0
	// skips this publish once the direct push was actually dispatched to at
	// least one connected peer (peerCount>0) -- when there were zero peers,
	// gossip is the only path left and always runs, switch or no switch.
	// Default (unset or "1"): unchanged behaviour, always gossip.
	if bc.p2p != nil {
		number := b.Number64().Uint64()
		if shouldGossipBlock(blockGossipFallbackEnabled(), peerCount) {
			go func() {
				if err := bc.p2p.BroadcastBlock(bc.ctx, data); err != nil {
					log.Warn("sealed block gossip fallback failed", "number", number, "err", err)
				}
			}()
		} else {
			blockGossipFallbackSkipped.Add(1)
			log.Debug("block gossip fallback skipped: direct push dispatched", "number", number, "peers", peerCount)
		}
	}
	return nil
}

// blockGossipFallbackOnce/On/Skipped back blockGossipFallbackEnabled, S15b's
// experiment switch (see the comment in SealedBlock above). Read once at
// start-up like this codebase's other N42_* switches (e.g.
// internal/miner/push_order.go's PushBeforeWrite); Skipped is a lightweight
// counter, not logged per block, for whoever wants to confirm the switch
// actually fired without grepping Debug-level logs.
var (
	blockGossipFallbackOnce    sync.Once
	blockGossipFallbackOn      bool
	blockGossipFallbackSkipped atomic.Int64
)

// parseBlockGossipFallback is N42_BLOCK_GOSSIP_FALLBACK's parser: anything
// but the literal "0" (including unset, "") keeps today's behaviour.
func parseBlockGossipFallback(v string) bool {
	return v != "0"
}

// shouldGossipBlock is the pure decision blockGossipFallbackEnabled's switch
// feeds SealedBlock: gossip when the fallback is on, OR when the direct push
// reached no peers at all -- in that case gossip is the only delivery path
// left, switch or no switch.
func shouldGossipBlock(fallbackEnabled bool, peerCount int) bool {
	return fallbackEnabled || peerCount == 0
}

// blockGossipFallbackEnabled reports whether SealedBlock's gossip fallback
// runs unconditionally (true, the default/unset/"1" case) or only when the
// direct push reached zero peers ("0"). N42_BLOCK_GOSSIP_FALLBACK=0 is an
// experiment, not a production default: it trades the fallback's redundancy
// for removing its ~18-26 MB publish from the same GossipSub streams the
// vote round-trip uses (6cg).
func blockGossipFallbackEnabled() bool {
	blockGossipFallbackOnce.Do(func() {
		blockGossipFallbackOn = parseBlockGossipFallback(os.Getenv("N42_BLOCK_GOSSIP_FALLBACK"))
		if !blockGossipFallbackOn {
			log.Info("block gossip fallback disabled (N42_BLOCK_GOSSIP_FALLBACK=0)")
		}
	})
	return blockGossipFallbackOn
}

// directPushBlock opens a stream to each connected peer and writes the block as
// a single chunked response (status code + fork digest + encoded block) — the
// same wire format ReadChunkedBlock decodes in the sync block-push handler. The
// RLP-encoded block bytes are passed in (encoded once by SealedBlock).
//
// Returns the number of connected peers a push was DISPATCHED to (goroutines
// launched), not confirmed delivered -- each send is fire-and-forget on its
// own goroutine (that is the whole point: SealedBlock must not block on
// network I/O), so per-peer success is only known later, asynchronously, and
// cheap dispatch-time visibility is what S15b's gossip-fallback switch needs
// (see blockGossipFallbackEnabled). 0 when p2p is unavailable, the fork
// digest cannot be computed, or there are no connected peers.
func (bc *BlockChain) directPushBlock(b block.IBlock, data []byte) int {
	if bc.p2p == nil {
		return 0
	}
	digest, err := utils.CreateForkDigest(b.Number64(), bc.genesisBlock.Hash())
	if err != nil {
		return 0
	}
	protoID := protocol.ID(p2p.RPCBlockPushTopicV1 + bc.p2p.Encoding().ProtocolSuffix())
	peers := bc.p2p.Peers().Connected()
	log.Info("direct-push block", "number", b.Number64().Uint64(), "peers", len(peers))
	for _, pid := range peers {
		go func(pid peer.ID) {
			ctx, cancel := context.WithTimeout(bc.ctx, 5*time.Second)
			defer cancel()
			stream, err := bc.p2p.Host().NewStream(ctx, pid, protoID)
			if err != nil {
				log.Info("direct-push stream failed", "peer", pid.String()[:12], "err", err)
				return
			}
			defer func() { _ = stream.Close() }()
			if _, err := stream.Write([]byte{0x00}); err != nil { // success status code
				return
			}
			if _, err := stream.Write(digest[:]); err != nil {
				return
			}
			if _, err := encoder.EncodeWithMaxLengthLimit(stream, &rawBlockBytes{data: data}, encoder.MaxBlockChunkSize); err != nil {
				// Surfacing this matters: a silent failure here (e.g. an
				// oversized block) leaves the follower with an opaque
				// "failed to read varint: EOF" and no local explanation.
				log.Error("direct-push encode failed", "peer", pid.String()[:12],
					"number", b.Number64().Uint64(), "size", len(data), "err", err)
				return
			}
		}(pid)
	}
	return len(peers)
}

// rawBlockBytes carries pre-encoded RLP block bytes through the SSZ length/snappy
// framing of EncodeWithMaxLength without imposing an SSZ schema. Mirrors the sync
// package's rawSSZBytes; defined here to avoid an import cycle with internal/sync.
type rawBlockBytes struct{ data []byte }

func (r *rawBlockBytes) MarshalSSZ() ([]byte, error)             { return r.data, nil }
func (r *rawBlockBytes) MarshalSSZTo(buf []byte) ([]byte, error) { return append(buf, r.data...), nil }
func (r *rawBlockBytes) SizeSSZ() int                            { return len(r.data) }
func (r *rawBlockBytes) UnmarshalSSZ(buf []byte) error {
	r.data = append([]byte(nil), buf...)
	return nil
}
