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

package node

import (
	"context"
	"fmt"

	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/mcp"
	"github.com/n42blockchain/N42/internal/miner"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/lib/kv"
)

// p2pAdminAdapter bridges p2p.P2P to the api.P2PAdmin interface so that
// admin_* RPC methods can return real peer data without the api package
// importing the p2p package directly.
type p2pAdminAdapter struct {
	svc p2p.P2P
}

func (a *p2pAdminAdapter) PeerInfos() []*api.PeerInfo {
	status := a.svc.Peers()
	connected := status.Connected()
	result := make([]*api.PeerInfo, 0, len(connected))
	for _, pid := range connected {
		info := &api.PeerInfo{
			ID:   pid.String(),
			Name: "N42",
			Caps: []string{"eth/69"},
		}
		if addr, err := status.Address(pid); err == nil {
			info.Network.RemoteAddress = addr.String()
		}
		result = append(result, info)
	}
	return result
}

func (a *p2pAdminAdapter) SelfNodeID() string {
	return a.svc.PeerID().String()
}

func (a *p2pAdminAdapter) SelfENR() string {
	record := a.svc.ENR()
	if record == nil {
		return ""
	}
	s, err := p2p.SerializeENR(record)
	if err != nil {
		return ""
	}
	return s
}

func (a *p2pAdminAdapter) SelfListenAddrs() []string {
	h := a.svc.Host()
	if h == nil {
		return nil
	}
	addrs := h.Addrs()
	result := make([]string, len(addrs))
	for i, ma := range addrs {
		result[i] = ma.String()
	}
	return result
}

func (a *p2pAdminAdapter) AddPeer(addr string) error {
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return fmt.Errorf("admin_addPeer: invalid multiaddr %q: %w", addr, err)
	}
	info, err := libp2ppeer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		return fmt.Errorf("admin_addPeer: cannot derive peer info from %q: %w", addr, err)
	}
	type connector interface {
		Connect(libp2ppeer.AddrInfo) error
	}
	if c, ok := a.svc.(connector); ok {
		return c.Connect(*info)
	}
	return a.svc.Host().Connect(context.Background(), *info)
}

func (a *p2pAdminAdapter) RemovePeer(peerID string) error {
	pid, err := libp2ppeer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("admin_removePeer: invalid peer ID %q: %w", peerID, err)
	}
	return a.svc.Disconnect(pid)
}

func (a *p2pAdminAdapter) HighestPeerBlock() uint64 {
	return a.svc.Peers().HighestBlockNumber().Uint64()
}

// minerAdminAdapter bridges *miner.Miner to api.MinerAdmin.
type minerAdminAdapter struct {
	m *miner.Miner
}

func (a *minerAdminAdapter) Mining() bool {
	return a.m.Mining()
}

func (a *minerAdminAdapter) SetCoinbase(addr types.Address) {
	a.m.SetCoinbase(addr)
}

// nodeHealthProvider implements HealthProvider for the Node.
type nodeHealthProvider struct {
	node *Node
}

func (h *nodeHealthProvider) CurrentBlock() uint64 {
	if h.node.blockChain == nil {
		return 0
	}
	b := h.node.blockChain.CurrentBlock()
	if b == nil {
		return 0
	}
	return blockNumberOrZero(b)
}

func (h *nodeHealthProvider) HighestBlock() uint64 {
	if h.node.p2p == nil {
		return h.CurrentBlock()
	}
	highest := h.node.p2p.Peers().HighestBlockNumber().Uint64()
	if current := h.CurrentBlock(); current > highest {
		return current
	}
	return highest
}

func (h *nodeHealthProvider) IsSyncing() bool {
	if h.node.is == nil {
		return false
	}
	return h.node.is.Syncing()
}

func (h *nodeHealthProvider) PeerCount() int {
	if h.node.p2p == nil {
		return 0
	}
	return len(h.node.p2p.Peers().Connected())
}

// nodeBlockProvider implements snapshot.BlockProvider for the Node.
type nodeBlockProvider struct {
	node *Node
}

func (b *nodeBlockProvider) CurrentBlock() uint64 {
	if b.node.blockChain == nil {
		return 0
	}
	blk := b.node.blockChain.CurrentBlock()
	if blk == nil {
		return 0
	}
	return blockNumberOrZero(blk)
}

// peerdasBlockProvider implements peerdas.BlockProvider using the node's blockchain.
type peerdasBlockProvider struct {
	node *Node
}

func (p *peerdasBlockProvider) CurrentBlock() (types.Hash, uint64) {
	if p.node.blockChain == nil {
		return types.Hash{}, 0
	}
	blk := p.node.blockChain.CurrentBlock()
	if blk == nil {
		return types.Hash{}, 0
	}
	return blk.Hash(), blockNumberOrZero(blk)
}

// mcpNodeBackend implements mcp.Backend using the node's services.
type mcpNodeBackend struct {
	node *Node
}

func (b *mcpNodeBackend) BlockChain() common.IBlockChain { return b.node.blockChain }
func (b *mcpNodeBackend) Database() kv.RwDB              { return b.node.db }
func (b *mcpNodeBackend) TxPool() common.ITxsPool        { return b.node.txspool }
func (b *mcpNodeBackend) PeerCount() int {
	if b.node.p2p != nil {
		return len(b.node.p2p.Peers().Connected())
	}
	return 0
}

func (b *mcpNodeBackend) SyncProgress() *mcp.SyncStatus {
	var current uint64
	if b.node.blockChain != nil && b.node.blockChain.CurrentBlock() != nil {
		current = blockNumberOrZero(b.node.blockChain.CurrentBlock())
	}
	return &mcp.SyncStatus{
		CurrentBlock: current,
		HighestBlock: current,
		Syncing:      false,
	}
}

// ingestPoolAdapter adapts common.ITxsPool to the ingest.TxPool interface.
type ingestPoolAdapter struct {
	pool common.ITxsPool
}

func (a *ingestPoolAdapter) AddLocal(tx *transaction.Transaction) error {
	return a.pool.AddLocal(tx)
}

func (a *ingestPoolAdapter) Stats() (pending, pendingAddrs, queued, queuedAddrs int) {
	return a.pool.Stats()
}
