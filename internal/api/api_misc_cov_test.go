// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// fakePeerInfoAdmin is a minimal P2PAdmin stub for exercising NetAPI/DebugAPI
// peer-related paths without a real libp2p stack.
type fakePeerInfoAdmin struct {
	peers    []*PeerInfo
	highest  uint64
	selfID   string
	selfEnr  string
	selfNode string
	addrs    []string
}

func (f *fakePeerInfoAdmin) PeerInfos() []*PeerInfo        { return f.peers }
func (f *fakePeerInfoAdmin) SelfNodeID() string             { return f.selfID }
func (f *fakePeerInfoAdmin) SelfEnode() string               { return f.selfNode }
func (f *fakePeerInfoAdmin) SelfENR() string                 { return f.selfEnr }
func (f *fakePeerInfoAdmin) SelfListenAddrs() []string       { return f.addrs }
func (f *fakePeerInfoAdmin) AddPeer(addr string) error       { return nil }
func (f *fakePeerInfoAdmin) RemovePeer(peerID string) error  { return nil }
func (f *fakePeerInfoAdmin) HighestPeerBlock() uint64        { return f.highest }

func TestNetAPI_ListeningPeerCountVersion(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{
		bc:          newPreciseChainStub(cfg, nil, genesis),
		chainConfig: cfg,
	}
	net := NewNetAPI(api, 42)

	if !net.Listening() {
		t.Fatal("Listening() = false, want true")
	}
	if got := net.PeerCount(); got != 0 {
		t.Fatalf("PeerCount() with nil p2p = %v, want 0", got)
	}
	if got := net.Version(); got != "42" {
		t.Fatalf("Version() = %q, want %q", got, "42")
	}

	api.p2p = &fakePeerInfoAdmin{peers: []*PeerInfo{{}, {}}}
	if got := net.PeerCount(); got != 2 {
		t.Fatalf("PeerCount() with 2 peers = %v, want 2", got)
	}
}

func TestWeb3API_ClientVersionAndSha3(t *testing.T) {
	web3 := &Web3API{}
	if got := web3.ClientVersion(); got == "" {
		t.Fatal("ClientVersion() returned empty string")
	}
	out := web3.Sha3(hexutil.Bytes("hello"))
	if len(out) != 32 {
		t.Fatalf("Sha3() returned %d bytes, want 32", len(out))
	}
}

func TestAccountAPI_Accounts(t *testing.T) {
	acc := NewAccountAPI()
	if got := acc.Accounts(); got != nil {
		t.Fatalf("Accounts() = %v, want nil", got)
	}
}

func TestTxPoolAPI_ContentContentFromInspect(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{
		bc:          newPreciseChainStub(cfg, nil, genesis),
		chainConfig: cfg,
		txspool:     &fakeTxsPool{pending: 1, queued: 2},
	}
	tp := NewTxPoolAPI(api)

	content := tp.Content()
	if content["pending"] == nil || content["queued"] == nil {
		t.Fatal("Content() missing pending/queued keys")
	}

	from := tp.ContentFrom(types.Address{})
	if from == nil {
		t.Fatal("ContentFrom() returned nil map")
	}

	status := tp.Status()
	if status["pending"] != 1 || status["queued"] != 2 {
		t.Fatalf("Status() = %+v, want pending=1 queued=2", status)
	}

	insp := tp.Inspect()
	if insp["pending"] == nil || insp["queued"] == nil {
		t.Fatal("Inspect() missing pending/queued keys")
	}
}

func TestTxsPoolAPI_ContentStatusInspect(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	pool := &fakeTxsPool{}
	api := &API{
		bc:          newPreciseChainStub(cfg, nil, genesis),
		chainConfig: cfg,
		txspool:     pool,
	}
	ts := NewTxsPoolAPI(api)

	// Content/Status/Inspect call TxsPool().Content(); fakeTxsPool.Content
	// currently returns (nil, nil), so verify they don't panic and produce
	// the expected empty/flat shape.
	content := ts.Content()
	if content["pending"] == nil || content["queued"] == nil {
		t.Fatal("Content() missing pending/queued keys")
	}
	status := ts.Status()
	if status["pending"] != 0 || status["queued"] != 0 {
		t.Fatalf("Status() = %+v, want zero counts for empty pool", status)
	}
	insp := ts.Inspect()
	if insp["pending"] == nil || insp["queued"] == nil {
		t.Fatal("Inspect() missing pending/queued keys")
	}
}

func TestDebugAPI_SetHeadAndGetAccount(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	stub := newPreciseChainStub(cfg, nil, genesis)
	api := &API{bc: stub, chainConfig: cfg}
	dbg := NewDebugAPI(api)

	dbg.SetHead(hexutil.Uint64(0)) // must not panic
	dbg.GetAccount(context.Background(), types.Address{})
}

func TestDebugAPI_DbGetNoDatabase(t *testing.T) {
	dbg := NewDebugAPI(&API{})
	_, err := dbg.DbGet(context.Background(), "sometable", nil)
	if err == nil {
		t.Fatal("DbGet() with nil db: error = nil, want error")
	}
}

func TestDebugAPI_DbStatsNoDatabase(t *testing.T) {
	dbg := NewDebugAPI(&API{})
	_, err := dbg.DbStats(context.Background())
	if err == nil {
		t.Fatal("DbStats() with nil db: error = nil, want error")
	}
}

func TestDebugAPI_NodeStatusNoBackend(t *testing.T) {
	dbg := NewDebugAPI(nil)
	status, err := dbg.NodeStatus(context.Background())
	if err != nil {
		t.Fatalf("NodeStatus() error = %v", err)
	}
	if status.Version == "" {
		t.Fatal("NodeStatus().Version is empty")
	}
}

func TestDebugAPI_NodeStatusWithBackend(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1), ChainName: "n42-test"}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	stub := newPreciseChainStub(cfg, nil, genesis)
	api := &API{bc: stub, chainConfig: cfg, p2p: &fakePeerInfoAdmin{highest: 10}}
	dbg := NewDebugAPI(api)

	status, err := dbg.NodeStatus(context.Background())
	if err != nil {
		t.Fatalf("NodeStatus() error = %v", err)
	}
	if status.ChainID != "1" {
		t.Fatalf("NodeStatus().ChainID = %q, want %q", status.ChainID, "1")
	}
	if status.Network != "n42-test" {
		t.Fatalf("NodeStatus().Network = %q, want %q", status.Network, "n42-test")
	}
	if status.HighestBlock != 10 {
		t.Fatalf("NodeStatus().HighestBlock = %d, want 10", status.HighestBlock)
	}
	if !status.Syncing {
		t.Fatal("NodeStatus().Syncing = false, want true (current 0 < highest 10)")
	}
}
