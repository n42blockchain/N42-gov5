package sync

import (
	"context"
	"fmt"
	gosync "sync"
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/p2p/peers"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

// This file is an in-process multi-peer catch-up harness: a chain stub that
// enforces parent links and branch-switch authority, and fake peers that
// serve bodies-by-range from their own chains (honest, lying, malformed).

// harnessBlock builds a consensus block (N42H extra) at number on parent;
// salt separates sibling branches.
func harnessBlock(parent comtypes.Hash, number uint64, salt byte) block.IBlock {
	h := &block.Header{
		ParentHash:  parent,
		UncleHash:   comtypes.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
		Coinbase:    comtypes.BytesToAddress([]byte{salt}),
		Root:        comtypes.HexToHash("0x03"),
		TxHash:      comtypes.HexToHash("0x04"),
		ReceiptHash: comtypes.HexToHash("0x05"),
		Difficulty:  uint256.NewInt(1),
		Number:      uint256.NewInt(number),
		GasLimit:    1000,
		Time:        1700000000 + number,
		Extra:       []byte("N42H"),
		MixDigest:   comtypes.HexToHash("0x06"),
		BaseFee:     uint256.NewInt(1),
	}
	return block.NewBlock(h, nil)
}

// harnessExtend returns blocks parent+1..to on parent.
func harnessExtend(parent block.IBlock, to uint64, salt byte) []block.IBlock {
	out := []block.IBlock{}
	prev := parent
	for n := parent.Number64().Uint64() + 1; n <= to; n++ {
		b := harnessBlock(prev.Hash(), n, salt)
		out = append(out, b)
		prev = b
	}
	return out
}

// harnessChain is a chain stub: blocks import only onto the applied head; an
// authorized insert may switch from the head onto a sibling branch whose
// parent is known. Unknown parents fail with consensus.ErrUnknownAncestor.
type harnessChain struct {
	common.IBlockChain
	mu       gosync.Mutex
	head     block.IBlock
	known    map[comtypes.Hash]block.IBlock
	switches int
	inserts  int
}

func newHarnessChain(history ...block.IBlock) *harnessChain {
	c := &harnessChain{known: map[comtypes.Hash]block.IBlock{}}
	for _, b := range history {
		c.known[b.Hash()] = b
		c.head = b
	}
	return c
}

func (c *harnessChain) CurrentBlock() block.IBlock {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.head
}

func (c *harnessChain) headNumber() uint64 { return c.CurrentBlock().Number64().Uint64() }

func (c *harnessChain) GenesisBlock() block.IBlock { return (&syncTBlockByHashChain{}).GenesisBlock() }

func (c *harnessChain) GetBlockByHash(h comtypes.Hash) (block.IBlock, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.known[h], nil
}

// HasAppliedBlock reports whether h is on the applied head's lineage.
func (c *harnessChain) HasAppliedBlock(h comtypes.Hash, _ uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.appliedLocked(h)
}

func (c *harnessChain) appliedLocked(h comtypes.Hash) bool {
	for cur := c.head; cur != nil; cur = c.known[cur.ParentHash()] {
		if cur.Hash() == h {
			return true
		}
	}
	return false
}

func (c *harnessChain) switchCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.switches
}

// store records b as a stored (not applied) block.
func (c *harnessChain) store(b block.IBlock) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.known[b.Hash()] = b
}

func (c *harnessChain) insert(blocks []block.IBlock, authorized bool) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, b := range blocks {
		if c.appliedLocked(b.Hash()) {
			continue // already applied: a known-block skip
		}
		if b.ParentHash() == c.head.Hash() {
			c.known[b.Hash()] = b
			c.head = b
			c.inserts++
			continue
		}
		if _, ok := c.known[b.ParentHash()]; ok {
			if !authorized {
				return i, fmt.Errorf("sibling parent not applied")
			}
			c.known[b.Hash()] = b
			c.head = b
			c.switches++
			c.inserts++
			continue
		}
		return i, consensus.ErrUnknownAncestor
	}
	return len(blocks), nil
}

func (c *harnessChain) InsertChain(blocks []block.IBlock) (int, error) {
	return c.insert(blocks, false)
}
func (c *harnessChain) InsertChainAuthorized(blocks []block.IBlock) (int, error) {
	return c.insert(blocks, true)
}

type harnessMode int

const (
	harnessHonest    harnessMode = iota // serve the stored prefix of the range
	harnessEmpty                        // answer with nothing
	harnessMalformed                    // answer with a block outside the range
	harnessRefuse                       // answer with an error code ("server busy")
)

// harnessPeer is one fake remote.
type harnessPeer struct {
	id     peer.ID
	claim  uint64
	mode   harnessMode
	chain  map[uint64]block.IBlock
	mu     gosync.Mutex
	asked  int
	ranges [][2]uint64
	// maxServe caps the blocks per reply (0 = no cap), like a byte budget.
	maxServe int
}

type harnessNet struct {
	t      *testing.T
	fp     *fakeP2P
	svc    *Service
	chain  *harnessChain
	peers  map[peer.ID]*harnessPeer
	nextIP int
}

func newHarnessNet(t *testing.T, chain *harnessChain) *harnessNet {
	t.Helper()
	fp := newFakeP2P(t)
	svc := &Service{ctx: context.Background(), cfg: &config{p2p: fp, chain: chain}}
	svc.rateLimiter = newRateLimiter(fp)
	if err := svc.initCaches(); err != nil {
		t.Fatalf("initCaches: %v", err)
	}
	n := &harnessNet{t: t, fp: fp, svc: svc, chain: chain, peers: map[peer.ID]*harnessPeer{}}
	fp.sendFn = n.send
	return n
}

// addPeer connects a peer advertising claim, serving blocks (by number).
func (n *harnessNet) addPeer(claim uint64, mode harnessMode, blocks ...block.IBlock) *harnessPeer {
	n.t.Helper()
	pid, err := test.RandPeerID()
	if err != nil {
		n.t.Fatalf("RandPeerID: %v", err)
	}
	n.nextIP++
	a, err := ma.NewMultiaddr(fmt.Sprintf("/ip4/10.0.%d.%d/tcp/30303", n.nextIP/250, n.nextIP%250+1))
	if err != nil {
		n.t.Fatalf("NewMultiaddr: %v", err)
	}
	n.fp.peerStatus.Add(nil, pid, a, network.DirOutbound)
	n.fp.peerStatus.SetConnectionState(pid, peers.PeerConnected)
	n.fp.peerStatus.SetChainState(pid, &sync_pb.Status{
		CurrentHeight: utils.ConvertUint256IntToH256(uint256.NewInt(claim)),
	})
	p := &harnessPeer{id: pid, claim: claim, mode: mode, chain: map[uint64]block.IBlock{}}
	for _, b := range blocks {
		p.chain[b.Number64().Uint64()] = b
	}
	n.peers[pid] = p
	return p
}

func (n *harnessNet) send(msg interface{}, pid peer.ID) (network.Stream, error) {
	p, ok := n.peers[pid]
	if !ok {
		return nil, fmt.Errorf("unknown peer")
	}
	req, ok := msg.(*sync_pb.BodiesByRangeRequest)
	if !ok {
		return nil, fmt.Errorf("unexpected request %T", msg)
	}
	start := utils.ConvertH256ToUint256Int(req.StartBlockNumber).Uint64()
	end := start + req.Count - 1
	p.mu.Lock()
	p.asked++
	p.ranges = append(p.ranges, [2]uint64{start, end})
	serve := make([]block.IBlock, 0, req.Count)
	for num := start; num <= end; num++ {
		b, ok := p.chain[num]
		if !ok || (p.maxServe > 0 && len(serve) >= p.maxServe) {
			break // stored chain ends (or reply cap): serve the prefix
		}
		serve = append(serve, b)
	}
	p.mu.Unlock()
	client, server := newFakeStreamPair(pid, n.fp.self)
	genesis := n.chain.GenesisBlock().Hash()
	go func() {
		defer server.Close()
		switch p.mode {
		case harnessEmpty:
			return
		case harnessMalformed:
			_ = writeBlockChunk(server, genesis, harnessBlock(comtypes.Hash{}, end+5, 0xee))
			return
		}
		for _, b := range serve {
			if err := writeBlockChunk(server, genesis, b); err != nil {
				return
			}
		}
	}()
	return client, nil
}

func (p *harnessPeer) askedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asked
}

func (n *harnessNet) badResponses(p *harnessPeer) int {
	c, err := n.fp.peerStatus.Scorers().BadResponsesScorer().Count(p.id)
	if err != nil {
		return 0
	}
	return c
}

// drain runs catch-up rounds the way the 8 s tick does until the head stops
// moving.
func (n *harnessNet) drain(maxRounds int) {
	for i := 0; i < maxRounds; i++ {
		before := n.chain.headNumber()
		n.svc.CatchUp()
		if n.chain.headNumber() == before {
			return
		}
	}
}
