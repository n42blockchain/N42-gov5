package mcp

import (
	"context"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/params"
)

func bigFromUint(v uint64) *big.Int { return new(big.Int).SetUint64(v) }

// resBackendStub is a configurable Backend implementation for resource tests.
type resBackendStub struct {
	chain     common.IBlockChain
	sync      *SyncStatus
	peerCount int
}

func (b *resBackendStub) BlockChain() common.IBlockChain { return b.chain }
func (b *resBackendStub) Database() kv.RwDB              { return nil }
func (b *resBackendStub) TxPool() common.ITxsPool        { return nil }
func (b *resBackendStub) SyncProgress() *SyncStatus      { return b.sync }
func (b *resBackendStub) PeerCount() int                 { return b.peerCount }

func TestResourceChainId(t *testing.T) {
	current := newTestBlock(1, nil)
	chain := newChainStub(current, map[uint64]block.IBlock{1: current})
	chain.cfg = &params.ChainConfig{ChainID: bigFromUint(7), ChainName: "n42-test"}
	s := &Server{backend: &resBackendStub{chain: chain}}

	res, err := s.resourceChainId(context.Background())
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	m := res.(map[string]interface{})
	if m["chain_id"] != uint64(7) {
		t.Errorf("chain_id = %v", m["chain_id"])
	}
	if m["chain_name"] != "n42-test" {
		t.Errorf("chain_name = %v", m["chain_name"])
	}

	// missing config
	chain2 := newChainStub(current, nil)
	s2 := &Server{backend: &resBackendStub{chain: chain2}}
	if _, err := s2.resourceChainId(context.Background()); err == nil {
		t.Error("expected error for missing chain config")
	}
}

func TestResourceLatestBlock(t *testing.T) {
	current := newTestBlock(42, nil)
	chain := newChainStub(current, map[uint64]block.IBlock{42: current})
	s := &Server{backend: &resBackendStub{chain: chain}}

	res, err := s.resourceLatestBlock(context.Background())
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	m := res.(map[string]interface{})
	if m["number"] != uint64(42) {
		t.Errorf("number = %v", m["number"])
	}

	// no current block
	chain2 := newChainStub(nil, nil)
	s2 := &Server{backend: &resBackendStub{chain: chain2}}
	if _, err := s2.resourceLatestBlock(context.Background()); err == nil {
		t.Error("expected error for missing current block")
	}
}

func TestResourceSyncStatus(t *testing.T) {
	// explicit sync status
	s := &Server{backend: &resBackendStub{sync: &SyncStatus{CurrentBlock: 1, HighestBlock: 10, Syncing: true}}}
	res, err := s.resourceSyncStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	m := res.(map[string]interface{})
	if m["syncing"] != true || m["current_block"] != uint64(1) || m["highest_block"] != uint64(10) {
		t.Errorf("got %v", m)
	}

	// no sync status, with current block
	current := newTestBlock(5, nil)
	chain := newChainStub(current, map[uint64]block.IBlock{5: current})
	s2 := &Server{backend: &resBackendStub{chain: chain}}
	res2, err2 := s2.resourceSyncStatus(context.Background())
	if err2 != nil {
		t.Fatalf("unexpected err %v", err2)
	}
	m2 := res2.(map[string]interface{})
	if m2["syncing"] != false || m2["current_block"] != uint64(5) {
		t.Errorf("got %v", m2)
	}

	// no sync status, no current block
	chain3 := newChainStub(nil, nil)
	s3 := &Server{backend: &resBackendStub{chain: chain3}}
	res3, err3 := s3.resourceSyncStatus(context.Background())
	if err3 != nil {
		t.Fatalf("unexpected err %v", err3)
	}
	m3 := res3.(map[string]interface{})
	if m3["current_block"] != 0 {
		t.Errorf("got %v", m3)
	}
}

func TestResourcePeerCount(t *testing.T) {
	s := &Server{backend: &resBackendStub{peerCount: 3}}
	res, err := s.resourcePeerCount(context.Background())
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	m := res.(map[string]interface{})
	if m["peer_count"] != 3 {
		t.Errorf("peer_count = %v", m["peer_count"])
	}
}

func TestRegisterDefaultResources(t *testing.T) {
	s := newTestServer()
	s.registerDefaultResources()
	for _, name := range []string{"chainId", "latestBlock", "syncStatus", "peerCount"} {
		if _, ok := s.resources[name]; !ok {
			t.Errorf("expected resource %q registered", name)
		}
	}
}
