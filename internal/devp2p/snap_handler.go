// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// snap/1 subprotocol stub for eth-el. Implements the wire types defined
// by EIP-2364 / snap.md so a Geth / Nethermind / Erigon / Besu peer
// sees us advertise both eth/69 AND snap/1 and does NOT classify us
// as a snapless leech (which causes immediate EOF after eth handshake
// on every modern client — observed 2026-05-24 across ~20 peers).
//
// The stub answers every GetXxx with an empty XxxResp carrying the
// matching request ID. Empty responses are valid in snap/1: they tell
// the requester "I don't have data in this range." Downloaders mark us
// as a low-priority source for that query but do NOT disconnect — that
// is the entire bar we need to clear to stay paired long enough for the
// eth/69 BlockHeaders / BlockBodies fetcher (Downloader in
// internal/ethel/eldevp2p) to drive the actual sync.
//
// When we later add a real snap server (serving from chaindata +
// freezer + reth-format MPT), the SnapBackend hook below is where the
// real implementation plugs in — it intentionally mirrors geth's
// Backend interface so a cut-over is a single field swap.

package devp2p

import (
	"fmt"

	gethp2p "github.com/ethereum/go-ethereum/p2p"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// snap/1 message codes from snap.md (EIP-2364).
const (
	snapGetAccountRangeMsg  = 0x00
	snapAccountRangeMsg     = 0x01
	snapGetStorageRangesMsg = 0x02
	snapStorageRangesMsg    = 0x03
	snapGetByteCodesMsg     = 0x04
	snapByteCodesMsg        = 0x05
	snapGetTrieNodesMsg     = 0x06
	snapTrieNodesMsg        = 0x07

	snapProtocolLength = 8
)

// --- snap/1 wire types (request side, decoded so we can echo back the ID) ---

type snapGetAccountRangePacket struct {
	ID     uint64
	Root   types.Hash
	Origin types.Hash
	Limit  types.Hash
	Bytes  uint64
}

type snapGetStorageRangesPacket struct {
	ID       uint64
	Root     types.Hash
	Accounts []types.Hash
	Origin   []byte
	Limit    []byte
	Bytes    uint64
}

type snapGetByteCodesPacket struct {
	ID     uint64
	Hashes []types.Hash
	Bytes  uint64
}

type snapGetTrieNodesPacket struct {
	ID    uint64
	Root  types.Hash
	Paths []snapTrieNodePathSet
	Bytes uint64
}

// snapTrieNodePathSet is a list of nibble-encoded trie paths. The outer
// slice picks an account by hashed address; the inner slice walks down
// that account's storage trie.
type snapTrieNodePathSet [][]byte

// --- snap/1 wire types (response side, all stubbed to empty) ---

type snapAccountRangePacket struct {
	ID       uint64
	Accounts []snapAccountData
	Proof    [][]byte
}

type snapAccountData struct {
	Hash types.Hash
	Body []byte
}

type snapStorageRangesPacket struct {
	ID    uint64
	Slots [][]snapStorageData
	Proof [][]byte
}

type snapStorageData struct {
	Hash types.Hash
	Body []byte
}

type snapByteCodesPacket struct {
	ID    uint64
	Codes [][]byte
}

type snapTrieNodesPacket struct {
	ID    uint64
	Nodes [][]byte
}

// SnapHandler runs the snap/1 subprotocol for one peer. It is wired in
// as a separate p2p.Protocol alongside eth/69 so the negotiation phase
// sees us offering both, which is all most peers need to keep us.
type SnapHandler struct{}

// NewSnapHandler builds the stub handler. No backend reference yet —
// when we add a real implementation it becomes a constructor arg.
func NewSnapHandler() *SnapHandler { return &SnapHandler{} }

// runPeer is plugged into p2p.Protocol.Run. Lifetime is owned by the
// p2p server; returning ends the snap channel for this peer but does
// NOT terminate the eth channel.
func (h *SnapHandler) runPeer(peer *gethp2p.Peer, rw gethp2p.MsgReadWriter) error {
	peerID := peer.ID().String()[:16]
	log.Info("snap/1 peer attached", "peer", peerID)
	defer log.Info("snap/1 peer detached", "peer", peerID)

	for {
		msg, err := rw.ReadMsg()
		if err != nil {
			return err
		}
		if err := h.handle(peerID, rw, msg); err != nil {
			log.Warn("snap/1 handler error",
				"peer", peerID, "code", msg.Code, "err", err)
			return err
		}
	}
}

// handle decodes one inbound message and (for requests) sends an empty
// response that preserves the request ID. Responses to our own queries
// would arrive here too once we run as a snap client, but the stub is
// receive-only — unknown / response codes are silently dropped.
func (h *SnapHandler) handle(peerID string, rw gethp2p.MsgReadWriter, msg gethp2p.Msg) error {
	defer msg.Discard()

	switch msg.Code {
	case snapGetAccountRangeMsg:
		var req snapGetAccountRangePacket
		if err := msg.Decode(&req); err != nil {
			return fmt.Errorf("decode GetAccountRange: %w", err)
		}
		return gethp2p.Send(rw, snapAccountRangeMsg, &snapAccountRangePacket{ID: req.ID})

	case snapGetStorageRangesMsg:
		var req snapGetStorageRangesPacket
		if err := msg.Decode(&req); err != nil {
			return fmt.Errorf("decode GetStorageRanges: %w", err)
		}
		return gethp2p.Send(rw, snapStorageRangesMsg, &snapStorageRangesPacket{ID: req.ID})

	case snapGetByteCodesMsg:
		var req snapGetByteCodesPacket
		if err := msg.Decode(&req); err != nil {
			return fmt.Errorf("decode GetByteCodes: %w", err)
		}
		return gethp2p.Send(rw, snapByteCodesMsg, &snapByteCodesPacket{ID: req.ID})

	case snapGetTrieNodesMsg:
		var req snapGetTrieNodesPacket
		if err := msg.Decode(&req); err != nil {
			return fmt.Errorf("decode GetTrieNodes: %w", err)
		}
		return gethp2p.Send(rw, snapTrieNodesMsg, &snapTrieNodesPacket{ID: req.ID})

	case snapAccountRangeMsg, snapStorageRangesMsg, snapByteCodesMsg, snapTrieNodesMsg:
		// Response codes — we never ask anything yet, so ignore.
		return nil

	default:
		// Future / unknown codes — non-fatal, log only.
		log.Debug("snap/1 unhandled msg", "peer", peerID, "code", msg.Code)
		return nil
	}
}
