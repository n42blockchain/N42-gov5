// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// EthHandler processes eth/68 and eth/69 wire messages for the ETH EL
// profile. BlockProvider abstracts chain access (CurrentHead, lookup
// by number and hash) so the handler can answer GetBlockHeaders and
// related queries using the N42 chain state with the configured genesis.

package devp2p

import (
	"errors"
	"fmt"
	"io"
	"math/big"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"

	n42block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/params"
)

// BlockProvider reads chain data for serving to peers.
type BlockProvider interface {
	CurrentHead() (*n42block.Header, types.Hash, error)
	GetHeaderByNumber(number uint64) (*n42block.Header, error)
	GetHeaderByHash(hash types.Hash) (*n42block.Header, error)
}

// ResponseHandler receives wire-message responses from peers. A nil
// ResponseHandler means the EthHandler operates passively (only serves
// incoming queries, never expects responses). The eldevp2p downloader
// registers a real implementation to drive active block fetching.
type ResponseHandler interface {
	// OnPeerHandshake fires once per peer after eth/69 Status exchange
	// succeeds. Gives the downloader the peer's latest head so it can
	// decide whether to fetch from this peer.
	OnPeerHandshake(peerID string, rw gethp2p.MsgReadWriter, peerHead uint64, peerHash types.Hash)
	// OnPeerDisconnect fires when a peer's runPeer returns. Lets the
	// downloader cancel any in-flight requests on this peer.
	OnPeerDisconnect(peerID string)
	// OnBlockHeaders delivers a BlockHeaders (msg code 4) response.
	// Headers are raw RLP bytes — the consumer must decode each with
	// ethel.DecodeUncleHeader (or any other fork-aware decoder).
	OnBlockHeaders(peerID string, reqID uint64, headers [][]byte)
	// OnBlockBodies delivers a BlockBodies (msg code 6) response.
	OnBlockBodies(peerID string, reqID uint64, bodies []BlockBody)
	// OnReceipts delivers a Receipts (msg code 16) response. receipts is
	// per requested block → its raw receipts (rlp.RawValue, mixed shapes).
	OnReceipts(peerID string, reqID uint64, receipts [][]rlp.RawValue)
	// OnNewBlock delivers a NewBlock (msg code 7) push — peer announces
	// they've produced/learned a new tip.
	OnNewBlock(peerID string, hdr *n42block.Header, txs [][]byte)
}

// balServer is the optional out-of-band BAL capability a BlockProvider may also
// implement: return a block's stored raw EIP-7928 BAL (canonical RLP), or nil if
// none is held. Kept as an optional interface so existing BlockProviders need not
// change.
type balServer interface {
	BlockAccessList(hash types.Hash) []byte
}

// balResponseHandler is the optional capability a ResponseHandler may also
// implement to receive BlockAccessLists (0x13) responses. The eldevp2p downloader
// implements it to route full-BAL replies to the matching in-flight request.
type balResponseHandler interface {
	OnBlockAccessLists(peerID string, reqID uint64, bals [][]byte)
}

// EthHandler processes eth/68-69 protocol messages.
type EthHandler struct {
	chainConfig *params.ChainConfig
	provider    BlockProvider
	networkID   uint64
	genesis     types.Hash
	genesisTime uint64

	// rh is the optional active-download callback. Nil = passive mode.
	rh ResponseHandler
}

// SetResponseHandler swaps in (or out) the active-download callback.
// Must be called BEFORE Start so the peer loop reads it consistently.
func (h *EthHandler) SetResponseHandler(rh ResponseHandler) { h.rh = rh }

// NewEthHandler creates a new eth protocol message handler.
func NewEthHandler(cfg *params.ChainConfig, genesisHash types.Hash, genesisTime uint64, provider BlockProvider) (*EthHandler, error) {
	if genesisHash == (types.Hash{}) {
		return nil, fmt.Errorf("genesis hash unavailable")
	}
	return &EthHandler{
		chainConfig: cfg,
		provider:    provider,
		networkID:   networkID(cfg),
		genesis:     genesisHash,
		genesisTime: genesisTime,
	}, nil
}

func (h *EthHandler) currentForkID(head *n42block.Header) forkID {
	if head == nil {
		return forkID{}
	}
	return newForkID(h.chainConfig, h.genesis, h.genesisTime, head.Number64().Uint64(), head.Time)
}

// runPeer is called by the p2p server for each new peer connection. version is
// the negotiated eth protocol version (68 or 69) — it selects the Status wire
// layout for the handshake; all other messages (GetBlockHeaders/Bodies etc.) are
// identical across eth/68-69 since eth/66's request-id wrapping, so they need no
// branching.
func (h *EthHandler) runPeer(peer *gethp2p.Peer, rw gethp2p.MsgReadWriter, version uint) error {
	log.Info("devp2p peer connected", "id", peer.ID().String()[:16], "name", peer.Name(), "eth", version)

	// cleanExit translates any error our handler would return into a
	// clean nil if the underlying cause is a TCP/RLPx shutdown (peer
	// went away on their own terms). Returning the original error
	// instead would make p2p.Server attribute the disconnect to OUR
	// subprotocol — see commit message for the long version. Any
	// non-shutdown error is still surfaced (network ID mismatch,
	// genesis mismatch, malformed packet, etc.).
	cleanExit := func(err error) error {
		if err == nil {
			return nil
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
			return nil
		}
		return err
	}

	// Handshake: send our status in the negotiated version's wire layout.
	head, headHash, err := h.provider.CurrentHead()
	if err != nil {
		return cleanExit(fmt.Errorf("current head: %w", err))
	}
	fork := h.currentForkID(head)
	if version >= 69 {
		status := statusPacket{
			ProtocolVersion: uint32(version),
			NetworkID:       h.networkID,
			Genesis:         h.genesis,
			ForkID:          fork,
			EarliestBlock:   0,
			LatestBlock:     head.Number64().Uint64(),
			LatestBlockHash: headHash,
		}
		log.Info("devp2p sending status", "peer", peer.ID().String()[:16], "eth", version,
			"network", h.networkID, "head", status.LatestBlock,
			"forkHash", fmt.Sprintf("%x", fork.Hash), "forkNext", fork.Next)
		if err := gethp2p.Send(rw, 0, &status); err != nil {
			return cleanExit(fmt.Errorf("send status: %w", err))
		}
	} else {
		// eth/68: TD is informational post-merge (geth doesn't reject on it), so a
		// zero placeholder is accepted; Head carries our head hash.
		status := statusPacket68{
			ProtocolVersion: uint32(version),
			NetworkID:       h.networkID,
			TD:              big.NewInt(0),
			Head:            headHash,
			Genesis:         h.genesis,
			ForkID:          fork,
		}
		log.Info("devp2p sending status", "peer", peer.ID().String()[:16], "eth", version,
			"network", h.networkID, "head", head.Number64().Uint64(),
			"forkHash", fmt.Sprintf("%x", fork.Hash), "forkNext", fork.Next)
		if err := gethp2p.Send(rw, 0, &status); err != nil {
			return cleanExit(fmt.Errorf("send status: %w", err))
		}
	}

	// Read peer's status. The handshake-phase ReadMsg is where ~all our
	// peers were dying — same EOF-returns-as-subprotocol-error trap as
	// the message loop below. The clean treatment is: if peer closed
	// before completing the handshake, exit cleanly so the drop reason
	// reflects geth's real cause (full mesh, useless peer, etc).
	msg, err := rw.ReadMsg()
	if err != nil {
		return cleanExit(err)
	}
	if msg.Code != 0 {
		return fmt.Errorf("expected status message, got %d", msg.Code)
	}
	// Decode into the layout matching the negotiated version. peerHead is the
	// peer's head block NUMBER; eth/68 Status carries only the head hash (no
	// number), so it stays 0 — the downloader treats head==0 as "unknown, still
	// pickable" and learns the real number from the first header response.
	var (
		peerNetwork uint64
		peerGenesis types.Hash
		peerHead    uint64
		peerHash    types.Hash
		peerFork    forkID
	)
	if version >= 69 {
		var ps statusPacket
		if err := msg.Decode(&ps); err != nil {
			return fmt.Errorf("decode peer status (eth/69): %w", err)
		}
		peerNetwork, peerGenesis, peerHead, peerHash, peerFork =
			ps.NetworkID, ps.Genesis, ps.LatestBlock, ps.LatestBlockHash, ps.ForkID
	} else {
		var ps statusPacket68
		if err := msg.Decode(&ps); err != nil {
			return fmt.Errorf("decode peer status (eth/68): %w", err)
		}
		peerNetwork, peerGenesis, peerHead, peerHash, peerFork =
			ps.NetworkID, ps.Genesis, 0, ps.Head, ps.ForkID
	}
	if peerNetwork != h.networkID {
		return fmt.Errorf("network ID mismatch: ours=%d theirs=%d", h.networkID, peerNetwork)
	}
	if peerGenesis != h.genesis {
		return fmt.Errorf("genesis mismatch: ours=%s theirs=%s", h.genesis.Hex()[:10], peerGenesis.Hex()[:10])
	}

	log.Info("devp2p handshake complete",
		"peer", peer.ID().String()[:16], "eth", version,
		"head", peerHead,
		"peerForkHash", fmt.Sprintf("%x", peerFork.Hash),
		"peerForkNext", peerFork.Next)

	// IMPORTANT: do NOT send BlockRangeUpdate (msg 0x11) right after
	// Status. The eth/69 spec sends BRU only on range CHANGE — the
	// initial range is already carried by the Status itself. Geth
	// reads an unsolicited BRU as spam and replies with a
	// DiscProtocolError ("breach of protocol") Disconnect frame.
	// We were doing exactly that and getting EOF'd; the fix is to
	// stay quiet until our range actually moves.

	peerID := peer.ID().String()
	if h.rh != nil {
		h.rh.OnPeerHandshake(peerID, rw, peerHead, peerHash)
		defer h.rh.OnPeerDisconnect(peerID)
	}

	// Message loop.
	for {
		msg, err := rw.ReadMsg()
		if err != nil {
			return cleanExit(err)
		}
		if err := h.handleMessage(peer, rw, msg); err != nil {
			log.Warn("devp2p message error", "peer", peer.ID().String()[:16],
				"msg", msg.Code, "err", err)
			return cleanExit(err)
		}
	}
}

// handleMessage routes an incoming message to the appropriate handler.
func (h *EthHandler) handleMessage(peer *gethp2p.Peer, rw gethp2p.MsgReadWriter, msg gethp2p.Msg) error {
	defer msg.Discard()
	peerID := peer.ID().String()

	switch msg.Code {
	case 1:
		// Peer announces new block hashes — log and ignore for now.
		log.Debug("devp2p: new block hashes", "peer", peerID[:16])
		return nil

	case 2:
		// Peer broadcasts transactions — forward to mempool (TODO).
		return nil

	case 3:
		return h.handleGetBlockHeaders(rw, msg)

	case 4:
		var resp blockHeadersPacket
		if err := msg.Decode(&resp); err != nil {
			return fmt.Errorf("decode BlockHeaders: %w", err)
		}
		if h.rh != nil {
			raw := make([][]byte, len(resp.Headers))
			for i, h := range resp.Headers {
				raw[i] = []byte(h)
			}
			h.rh.OnBlockHeaders(peerID, resp.RequestID, raw)
		}
		return nil

	case 5:
		return h.handleGetBlockBodies(rw, msg)

	case 6:
		var resp blockBodiesPacket
		if err := msg.Decode(&resp); err != nil {
			return fmt.Errorf("decode BlockBodies: %w", err)
		}
		if h.rh != nil {
			h.rh.OnBlockBodies(peerID, resp.RequestID, resp.Bodies)
		}
		return nil

	case 7:
		// NewBlock push — peer found / produced a block.
		log.Debug("devp2p: new block", "peer", peerID[:16])
		return nil

	case 8:
		return nil // ignore

	case 9:
		return nil // TODO: serve from txpool

	case 15:
		return nil // TODO: serve from freezer

	case 10:
		// 0x0a PooledTransactions response — we never asked, ignore.
		return nil

	case 16:
		var resp blockReceiptsPacket
		if err := msg.Decode(&resp); err != nil {
			return fmt.Errorf("decode Receipts: %w", err)
		}
		if h.rh != nil {
			h.rh.OnReceipts(peerID, resp.RequestID, resp.Receipts)
		}
		return nil

	case 17:
		return nil // eth/69 range update, log only

	case 18:
		// 0x12 GetBlockAccessLists — serve the out-of-band EIP-7928 BALs.
		return h.handleGetBlockAccessLists(rw, msg)

	case 19:
		// 0x13 BlockAccessLists — full-BAL response; route to the downloader.
		var resp blockAccessListsPacket
		if err := msg.Decode(&resp); err != nil {
			return fmt.Errorf("decode BlockAccessLists: %w", err)
		}
		if bh, ok := h.rh.(balResponseHandler); ok {
			bh.OnBlockAccessLists(peerID, resp.RequestID, resp.BALs)
		}
		return nil

	default:
		// Unknown / not-yet-handled codes must NOT disconnect — that's
		// what was happening with geth right after handshake. Log so we
		// can extend handling later, but keep the peer.
		log.Debug("devp2p: unhandled msg", "peer", peerID[:16], "code", msg.Code)
		return nil
	}
}

// handleGetBlockHeaders serves block headers to a requesting peer.
//
// Today the eth-el chaindata is state-only — there are no Ethereum
// headers stored, so the only honest answer is the empty response.
// (Returning empty is a valid eth/69 reply meaning "no data in
// range"; it does not cause a disconnect.) Once the body/header
// columnar store is wired into BlockProvider, this handler will
// encode each fetched *block.Header via the same per-fork RLP layout
// that ethel.DecodeUncleHeader expects. See header_compact.go for the
// canonical encoder we'd reuse.
func (h *EthHandler) handleGetBlockHeaders(rw gethp2p.MsgReadWriter, msg gethp2p.Msg) error {
	var req getBlockHeadersPacket
	if err := msg.Decode(&req); err != nil {
		return err
	}
	return gethp2p.Send(rw, 4, &blockHeadersPacket{RequestID: req.RequestID})
}

// handleGetBlockBodies serves block bodies to a requesting peer.
func (h *EthHandler) handleGetBlockBodies(rw gethp2p.MsgReadWriter, msg gethp2p.Msg) error {
	var req getBlockBodiesPacket
	if err := msg.Decode(&req); err != nil {
		return err
	}

	resp := blockBodiesPacket{
		RequestID: req.RequestID,
		Bodies:    nil,
	}
	return gethp2p.Send(rw, 6, &resp)
}

// handleGetBlockAccessLists serves the out-of-band EIP-7928 BALs a peer requested
// by block hash. Answers with the stored raw BAL per hash (empty when the provider
// does not implement BAL serving or does not hold that block's BAL), preserving
// request order so the requester can pair by index.
func (h *EthHandler) handleGetBlockAccessLists(rw gethp2p.MsgReadWriter, msg gethp2p.Msg) error {
	var req getBlockAccessListsPacket
	if err := msg.Decode(&req); err != nil {
		return err
	}
	n := len(req.Hashes)
	if n > maxBlockAccessListsRequest {
		n = maxBlockAccessListsRequest
	}
	resp := blockAccessListsPacket{RequestID: req.RequestID, BALs: make([][]byte, n)}
	if srv, ok := h.provider.(balServer); ok {
		for i, hash := range req.Hashes[:n] {
			resp.BALs[i] = srv.BlockAccessList(hash) // nil -> empty entry
		}
	}
	return gethp2p.Send(rw, 19, &resp)
}
