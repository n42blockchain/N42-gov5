// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package node

import (
	"fmt"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	n42block "github.com/n42blockchain/N42/common/block"
	n42types "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/devp2p"
)

type devp2pBlockProvider struct {
	node *Node
}

func blockHeaderFromBlock(blk n42block.IBlock) (*n42block.Header, error) {
	if blk == nil {
		return nil, fmt.Errorf("block unavailable")
	}
	header, ok := blk.Header().(*n42block.Header)
	if !ok {
		return nil, fmt.Errorf("unexpected current header type %T", blk.Header())
	}
	return n42block.CopyHeader(header), nil
}

func (p *devp2pBlockProvider) CurrentHead() (*n42block.Header, n42types.Hash, error) {
	if p == nil || p.node == nil || p.node.blockChain == nil {
		return nil, n42types.Hash{}, fmt.Errorf("blockchain unavailable")
	}
	if p.node.api != nil {
		if current := p.node.api.ForkchoiceHeadBlock(); current != nil {
			header, err := blockHeaderFromBlock(current)
			if err != nil {
				return nil, n42types.Hash{}, err
			}
			return header, p.node.api.ForkchoiceBlockHash(current), nil
		}
	}
	current := p.node.blockChain.CurrentBlock()
	if current == nil {
		return nil, n42types.Hash{}, fmt.Errorf("current block unavailable")
	}
	header, err := blockHeaderFromBlock(current)
	if err != nil {
		return nil, n42types.Hash{}, err
	}
	return header, current.Hash(), nil
}

func (p *devp2pBlockProvider) GetHeaderByNumber(number uint64) (*n42block.Header, error) {
	if p == nil || p.node == nil || p.node.blockChain == nil {
		return nil, fmt.Errorf("blockchain unavailable")
	}
	if p.node.api != nil {
		if blk := p.node.api.ForkchoiceBlockByNumber(number); blk != nil {
			return blockHeaderFromBlock(blk)
		}
	}
	header := p.node.blockChain.GetHeaderByNumber(uint256.NewInt(number))
	if header == nil {
		return nil, nil
	}
	concrete, ok := header.(*n42block.Header)
	if !ok {
		return nil, fmt.Errorf("unexpected header type %T", header)
	}
	return n42block.CopyHeader(concrete), nil
}

func (p *devp2pBlockProvider) GetHeaderByHash(hash n42types.Hash) (*n42block.Header, error) {
	if p == nil || p.node == nil || p.node.blockChain == nil {
		return nil, fmt.Errorf("blockchain unavailable")
	}
	if p.node.api != nil {
		if blk := p.node.api.ForkchoiceBlockByHash(hash); blk != nil {
			return blockHeaderFromBlock(blk)
		}
	}
	header, err := p.node.blockChain.GetHeaderByHash(hash)
	if err != nil {
		return nil, err
	}
	if header == nil {
		return nil, nil
	}
	concrete, ok := header.(*n42block.Header)
	if !ok {
		return nil, fmt.Errorf("unexpected header type %T", header)
	}
	return n42block.CopyHeader(concrete), nil
}

func (n *Node) startEthereumDevP2P() error {
	if n == nil || !n.profile.IsEthereumEL() {
		return nil
	}
	if n.devp2pServer != nil {
		return nil
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		return fmt.Errorf("generate devp2p key: %w", err)
	}
	genesisHeader, err := blockHeaderFromBlock(n.genesisBlock)
	if err != nil {
		return fmt.Errorf("read genesis header: %w", err)
	}
	genesisHash := n.genesisBlock.Hash()
	if n.config != nil && n.config.NodeCfg.DataDir != "" {
		if persistedHash, ok, err := ReadEthereumGenesisHash(n.config.NodeCfg.DataDir); err == nil && ok {
			genesisHash = persistedHash
		}
	}
	if genesisHash == n.genesisBlock.Hash() && n.api != nil {
		genesisHash = n.api.ForkchoiceBlockHash(n.genesisBlock)
	}
	handler, err := devp2p.NewEthHandler(n.config.ChainCfg, genesisHash, genesisHeader.Time, &devp2pBlockProvider{node: n})
	if err != nil {
		return fmt.Errorf("create devp2p handler: %w", err)
	}
	listenPort := 30304
	maxPeers := 25
	if n.config != nil && n.config.P2PCfg != nil {
		if n.config.P2PCfg.TCPPort > 0 {
			listenPort = n.config.P2PCfg.TCPPort + 1
		}
		if n.config.P2PCfg.MaxPeers > 0 {
			maxPeers = n.config.P2PCfg.MaxPeers
		}
	}
	server := devp2p.NewServer(devp2p.ServerConfig{
		PrivateKey:  key,
		ListenAddr:  fmt.Sprintf(":%d", listenPort),
		MaxPeers:    maxPeers,
		ChainConfig: n.config.ChainCfg,
	}, handler)
	if err := server.Start(); err != nil {
		return fmt.Errorf("start devp2p server: %w", err)
	}
	n.devp2pServer = server
	return nil
}

func (n *Node) selfEnodeURL() string {
	if n == nil || n.devp2pServer == nil {
		return ""
	}
	self := n.devp2pServer.Self()
	if self == nil {
		return ""
	}
	return self.URLv4()
}
