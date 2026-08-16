// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// EthHandler processes eth/68 through eth/71 wire messages for the ETH EL
// profile. BlockProvider abstracts chain access (CurrentHead, lookup
// by number and hash) so the handler can answer GetBlockHeaders and
// related queries using the N42 chain state with the configured genesis.

package devp2p

import (
	"errors"
	"fmt"
	"io"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/n42blockchain/N42/common"
	n42block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/network/eth69"
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
	OnPeerHandshake(peerID string, rw gethp2p.MsgReadWriter, peerHead uint64, peerHash types.Hash, version uint)
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

type newBlockHashesHandler interface {
	OnNewBlockHashes(peerID string, entries eth69.NewBlockHashesPacket)
}

// EthHandler processes eth/68-71 protocol messages.
type EthHandler struct {
	chainConfig *params.ChainConfig
	provider    BlockProvider
	networkID   uint64
	genesis     types.Hash
	genesisTime uint64
	txpool      common.ITxsPool
	requestID   atomic.Uint64

	// rh is the optional active-download callback. Nil = passive mode.
	rh ResponseHandler

	// markTrusted, when set, promotes a handshake-confirmed peer to the p2p
	// server's trusted set (immune to "too many peers" eviction). Wired by the
	// Server to srv.AddTrustedPeer. trustedIDs bounds how many we pin.
	markTrusted   func(*enode.Node)
	unmarkTrusted func(*enode.Node)
	trustedMu     sync.Mutex
	trustedIDs    map[enode.ID]int
}

// maxTrustedPeers caps how many confirmed mainnet peers we pin as trusted. Enough
// to survive the PulseChain inbound flood while keeping the trusted set (and its
// redial pressure) bounded.
const maxTrustedPeers = 64

// SetResponseHandler swaps in (or out) the active-download callback.
// Must be called BEFORE Start so the peer loop reads it consistently.
func (h *EthHandler) SetResponseHandler(rh ResponseHandler) { h.rh = rh }

// SetTxPool enables eth/68+ transaction announcements and pooled transaction
// request handling. It must be called before Start.
func (h *EthHandler) SetTxPool(pool common.ITxsPool) { h.txpool = pool }

// setTrustedCallbacks wires the callbacks before the server accepts peers.
func (h *EthHandler) setTrustedCallbacks(mark, unmark func(*enode.Node)) {
	h.trustedMu.Lock()
	defer h.trustedMu.Unlock()
	h.markTrusted = mark
	h.unmarkTrusted = unmark
}

// markPeerTrusted pins a handshake-confirmed (real network+genesis) peer as
// trusted so the p2p server never evicts it for "too many peers" — the scarce
// good mainnet peers must survive the PulseChain flood that shares our bootnodes
// and forkid. Returns true when this connection owns a reference that must be
// released with unmarkPeerTrusted.
func (h *EthHandler) markPeerTrusted(node *enode.Node) bool {
	if node == nil {
		return false
	}
	h.trustedMu.Lock()
	mark := h.markTrusted
	if mark == nil {
		h.trustedMu.Unlock()
		return false
	}
	if h.trustedIDs == nil {
		h.trustedIDs = make(map[enode.ID]int)
	}
	id := node.ID()
	if refs := h.trustedIDs[id]; refs > 0 {
		h.trustedIDs[id] = refs + 1
		h.trustedMu.Unlock()
		return true
	}
	if len(h.trustedIDs) >= maxTrustedPeers {
		h.trustedMu.Unlock()
		return false
	}
	h.trustedIDs[id] = 1
	n := len(h.trustedIDs)
	h.trustedMu.Unlock()
	mark(node)
	log.Info("devp2p: pinned confirmed mainnet peer as trusted (immune to too-many-peers eviction)",
		"peer", node.ID().String()[:16], "trustedCount", n)
	return true
}

func (h *EthHandler) unmarkPeerTrusted(node *enode.Node) {
	if node == nil {
		return
	}
	h.trustedMu.Lock()
	id := node.ID()
	refs := h.trustedIDs[id]
	if refs == 0 {
		h.trustedMu.Unlock()
		return
	}
	if refs > 1 {
		h.trustedIDs[id] = refs - 1
		h.trustedMu.Unlock()
		return
	}
	delete(h.trustedIDs, id)
	unmark := h.unmarkTrusted
	n := len(h.trustedIDs)
	h.trustedMu.Unlock()
	if unmark != nil {
		unmark(node)
	}
	log.Info("devp2p: unpinned disconnected mainnet peer", "peer", id.String()[:16], "trustedCount", n)
}

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
	// Decode the Status by what's actually on the wire, not just the negotiated
	// version. Some mainnet peers advertise eth/69 caps but send an eth/68-layout
	// Status (TD+head at fields 3-4 instead of the eth/69 32-byte genesis at field
	// 3); decoding that with the eth/69 struct fails ("input string too short for
	// Genesis") and needlessly drops an otherwise-good peer — the single largest
	// source of handshake drops observed in live-follow. The two layouts have
	// DIFFERENT element counts (eth/68=6, eth/69=7) and incompatible field-3 types
	// (big.Int TD vs 32-byte hash), so RLP's strict struct decode reliably rejects
	// the wrong layout: try the negotiated one first, fall back to the other.
	// peerHead is the peer's head block NUMBER; eth/68 Status has only the head
	// hash, so it stays 0 (the downloader treats head==0 as "unknown, pickable").
	payload, perr := io.ReadAll(msg.Payload)
	if perr != nil {
		return cleanExit(fmt.Errorf("read status payload: %w", perr))
	}
	var (
		peerNetwork uint64
		peerGenesis types.Hash
		peerHead    uint64
		peerHash    types.Hash
		peerFork    forkID
	)
	var ps69 statusPacket
	var ps68 statusPacket68
	take69 := func() {
		peerNetwork, peerGenesis, peerHead, peerHash, peerFork =
			ps69.NetworkID, ps69.Genesis, ps69.LatestBlock, ps69.LatestBlockHash, ps69.ForkID
	}
	take68 := func() {
		peerNetwork, peerGenesis, peerHead, peerHash, peerFork =
			ps68.NetworkID, ps68.Genesis, 0, ps68.Head, ps68.ForkID
	}
	switch {
	case version >= 69 && rlp.DecodeBytes(payload, &ps69) == nil:
		take69()
	case version >= 69 && rlp.DecodeBytes(payload, &ps68) == nil:
		take68()
		log.Debug("devp2p: peer negotiated eth/69 but sent eth/68 Status — accepted",
			"peer", peer.ID().String()[:16])
	case version < 69 && rlp.DecodeBytes(payload, &ps68) == nil:
		take68()
	case version < 69 && rlp.DecodeBytes(payload, &ps69) == nil:
		take69()
		log.Debug("devp2p: peer negotiated eth/68 but sent eth/69 Status — accepted",
			"peer", peer.ID().String()[:16])
	default:
		// Log the true reason: p2p.Server maps a returned handshake error to a
		// generic DiscSubprotocolError, which is what masked these as "subprotocol
		// error" drops. Make each rejection reason visible so the operator sees
		// WHY peers are lost, not just that they are.
		log.Info("devp2p handshake rejected — Status decode failed",
			"peer", peer.ID().String()[:16], "eth", version, "size", len(payload))
		return fmt.Errorf("decode peer status: neither eth/68 nor eth/69 layout matched (eth=%d size=%d)",
			version, len(payload))
	}
	if peerNetwork != h.networkID {
		log.Info("devp2p handshake rejected — network ID mismatch",
			"peer", peer.ID().String()[:16], "ours", h.networkID, "theirs", peerNetwork)
		return fmt.Errorf("network ID mismatch: ours=%d theirs=%d", h.networkID, peerNetwork)
	}
	if peerGenesis != h.genesis {
		log.Info("devp2p handshake rejected — genesis mismatch",
			"peer", peer.ID().String()[:16], "ours", h.genesis.Hex()[:10], "theirs", peerGenesis.Hex()[:10])
		return fmt.Errorf("genesis mismatch: ours=%s theirs=%s", h.genesis.Hex()[:10], peerGenesis.Hex()[:10])
	}

	// Confirmed real mainnet peer (passed network+genesis). Pin it trusted so the
	// p2p server never evicts it for "too many peers" — PulseChain (network=369)
	// shares our bootnodes+forkid and floods the inbound pool, and would otherwise
	// push the scarce good peers out right after handshake.
	if h.markPeerTrusted(peer.Node()) {
		defer h.unmarkPeerTrusted(peer.Node())
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
		h.rh.OnPeerHandshake(peerID, rw, peerHead, peerHash, version)
		defer h.rh.OnPeerDisconnect(peerID)
	}
	announceDone := make(chan struct{})
	defer close(announceDone)
	if h.txpool != nil {
		go h.announcePooledTransactions(rw, announceDone)
	}

	// Message loop. A ReadMsg error means the connection itself is gone → exit
	// (cleanly for a peer-initiated shutdown). But a per-message HANDLING error
	// (e.g. a response we couldn't decode, an unexpected request shape) must NOT
	// tear the connection down: we're a receive-only stub, so the robust behavior
	// is Discard+continue. Dropping the peer on message content was a needless
	// source of "subprotocol error" churn and starves the downloader of peers.
	for {
		msg, err := rw.ReadMsg()
		if err != nil {
			return cleanExit(err)
		}
		if err := h.handleMessage(peer, rw, msg); err != nil {
			log.Debug("devp2p message handling error (ignored — stub never disconnects on a message)",
				"peer", peer.ID().String()[:16], "msg", msg.Code, "err", err)
			// keep the peer
		}
	}
}

// handleMessage routes an incoming message to the appropriate handler.
func (h *EthHandler) handleMessage(peer *gethp2p.Peer, rw gethp2p.MsgReadWriter, msg gethp2p.Msg) error {
	defer msg.Discard()
	peerID := peer.ID().String()

	switch msg.Code {
	case 1:
		var packet eth69.NewBlockHashesPacket
		if err := msg.Decode(&packet); err != nil {
			return fmt.Errorf("decode NewBlockHashes: %w", err)
		}
		if nh, ok := h.rh.(newBlockHashesHandler); ok {
			nh.OnNewBlockHashes(peerID, packet)
		}
		log.Debug("devp2p: new block hashes", "peer", peerID[:16], "count", len(packet))
		return nil

	case 2:
		var packet []rlp.RawValue
		if err := msg.Decode(&packet); err != nil {
			return fmt.Errorf("decode Transactions: %w", err)
		}
		return h.acceptPooledTransactions(packet)

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
		var packet eth69.NewPooledTransactionHashesPacket
		if err := msg.Decode(&packet); err != nil {
			return fmt.Errorf("decode NewPooledTransactionHashes: %w", err)
		}
		if len(packet.Hashes) != len(packet.Types) || len(packet.Hashes) != len(packet.Sizes) {
			return fmt.Errorf("invalid pooled transaction announcement lengths")
		}
		if h.txpool == nil {
			return nil
		}
		hashes := make([]types.Hash, 0, len(packet.Hashes))
		for _, hash := range packet.Hashes {
			if !h.txpool.Has(hash) {
				hashes = append(hashes, hash)
			}
		}
		if len(hashes) == 0 {
			return nil
		}
		requestID := h.requestID.Add(1)
		return gethp2p.Send(rw, 9, &eth69.GetPooledTransactionsPacket{RequestID: requestID, Hashes: hashes})

	case 9:
		return h.handleGetPooledTransactions(rw, msg)

	case 15:
		return nil // TODO: serve from freezer

	case 10:
		var packet pooledTransactionsPacket
		if err := msg.Decode(&packet); err != nil {
			return fmt.Errorf("decode PooledTransactions: %w", err)
		}
		return h.acceptPooledTransactions(packet.Transactions)

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

	case eth69.GetBlockAccessListsMsg:
		// 0x12 GetBlockAccessLists — serve the out-of-band EIP-7928 BALs.
		return h.handleGetBlockAccessLists(rw, msg)

	case eth69.BlockAccessListsMsg:
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

type pooledTransactionsPacket struct {
	RequestID    uint64
	Transactions []rlp.RawValue
}

func (h *EthHandler) handleGetPooledTransactions(rw gethp2p.MsgReadWriter, msg gethp2p.Msg) error {
	var req eth69.GetPooledTransactionsPacket
	if err := msg.Decode(&req); err != nil {
		return fmt.Errorf("decode GetPooledTransactions: %w", err)
	}
	resp := pooledTransactionsPacket{RequestID: req.RequestID, Transactions: []rlp.RawValue{}}
	if h.txpool == nil {
		return gethp2p.Send(rw, 10, &resp)
	}
	for _, hash := range req.Hashes {
		tx := h.txpool.GetTx(hash)
		if tx == nil {
			continue
		}
		encoded, err := transaction.EncodeEthereumPooledTransaction(tx)
		if err != nil {
			continue
		}
		raw, err := pooledTransactionRawValue(encoded)
		if err != nil {
			continue
		}
		resp.Transactions = append(resp.Transactions, raw)
	}
	return gethp2p.Send(rw, 10, &resp)
}

func pooledTransactionRawValue(encoded []byte) (rlp.RawValue, error) {
	if len(encoded) == 0 {
		return nil, errors.New("empty pooled transaction")
	}
	if encoded[0] >= 0xc0 {
		return append(rlp.RawValue(nil), encoded...), nil
	}
	wrapped, err := rlp.EncodeToBytes(encoded)
	return rlp.RawValue(wrapped), err
}

func decodePooledTransactionRaw(raw rlp.RawValue) (*transaction.Transaction, error) {
	encoded := []byte(raw)
	if len(encoded) == 0 {
		return nil, errors.New("empty pooled transaction")
	}
	if encoded[0] < 0xc0 {
		var typed []byte
		if err := rlp.DecodeBytes(encoded, &typed); err != nil {
			return nil, err
		}
		encoded = typed
	}
	return transaction.DecodeEthereumTransaction(encoded)
}

func (h *EthHandler) acceptPooledTransactions(rawTxs []rlp.RawValue) error {
	if h.txpool == nil || len(rawTxs) == 0 {
		return nil
	}
	head, _, err := h.provider.CurrentHead()
	if err != nil || head == nil {
		return err
	}
	signer := transaction.MakeSignerWithTimestamp(h.chainConfig, head.Number64().ToBig(), head.Time)
	txs := make([]*transaction.Transaction, 0, len(rawTxs))
	for _, raw := range rawTxs {
		tx, err := decodePooledTransactionRaw(raw)
		if err != nil {
			continue
		}
		from, err := transaction.Sender(signer, tx)
		if err != nil {
			continue
		}
		tx.SetFrom(from)
		txs = append(txs, tx)
	}
	if len(txs) > 0 {
		h.txpool.AddRemotes(txs)
	}
	return nil
}

func (h *EthHandler) announcePooledTransactions(rw gethp2p.MsgReadWriter, done <-chan struct{}) {
	// Pace announcements so bidirectional peers do not burst several writes at
	// the same instant as their GetPooledTransactions request/response traffic.
	// Five Hive blob transactions still propagate in about 100ms, comfortably
	// inside the payload builder's collection window.
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	known := make(map[types.Hash]struct{})
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			txs, err := h.txpool.GetTransaction()
			if err != nil {
				continue
			}
			for _, tx := range txs {
				if tx == nil {
					continue
				}
				hash := tx.Hash()
				if _, ok := known[hash]; ok {
					continue
				}
				encoded, err := transaction.EncodeEthereumPooledTransaction(tx)
				if err != nil {
					continue
				}
				known[hash] = struct{}{}
				// Announce transactions individually. Besides keeping messages small,
				// this preserves the arrival granularity expected by peers that react
				// to each txpool insertion before issuing a pooled-tx request.
				packet := eth69.NewPooledTransactionHashesPacket{
					Types:  []byte{tx.Type()},
					Sizes:  []uint32{uint32(len(encoded))},
					Hashes: []types.Hash{hash},
				}
				if err := gethp2p.Send(rw, 8, &packet); err != nil {
					return
				}
				break
			}
		}
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
	// We serve an empty stub response, so only the request-id (echoed back) is
	// needed — decode leniently over the query tail so a peer whose GetBlockHeaders
	// query encoding differs by a field isn't dropped ("rlp: too many elements").
	var req requestIDTail
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
