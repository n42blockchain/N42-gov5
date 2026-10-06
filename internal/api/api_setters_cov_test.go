// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func TestAPI_SimpleSetters(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	stub := newPreciseChainStub(cfg, nil, genesis)
	api := NewAPI(stub, nil, nil, &fakeTxsPool{}, nil, cfg)

	if api.RPCGasCap() == 0 {
		t.Fatal("RPCGasCap() = 0, want the configured default")
	}

	api.SetGpo(&Oracle{})
	if api.gpo == nil {
		t.Fatal("SetGpo() did not install the oracle")
	}

	miner := &fakeMinerAdmin{mining: true}
	api.SetMiner(miner)
	if !api.miner.Mining() {
		t.Fatal("SetMiner() did not install the miner backend")
	}

	p2p := &fakePeerInfoAdmin{highest: 5}
	api.SetP2P(p2p)
	if api.p2p.HighestPeerBlock() != 5 {
		t.Fatal("SetP2P() did not install the p2p backend")
	}

	var reader func(tx kv.Tx, blockNum uint64) (state.StateReader, error)
	reader = func(tx kv.Tx, blockNum uint64) (state.StateReader, error) { return nil, nil }
	api.SetStateReaderProvider(reader)
	if api.StateReaderProvider() == nil {
		t.Fatal("SetStateReaderProvider()/StateReaderProvider() round-trip failed")
	}

	api.SetStateReaderProvider(nil)
	if api.StateReaderProvider() != nil {
		t.Fatal("SetStateReaderProvider(nil) did not clear the provider")
	}

	if api.Engine() != nil {
		t.Fatal("Engine() = non-nil, want nil (none configured)")
	}
}

func TestAPI_Apis(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	stub := newPreciseChainStub(cfg, nil, genesis)
	api := NewAPI(stub, nil, nil, &fakeTxsPool{}, nil, cfg)

	apis := api.Apis()
	if len(apis) == 0 {
		t.Fatal("Apis() returned no services")
	}
	seenNamespaces := map[string]bool{}
	for _, a := range apis {
		seenNamespaces[a.Namespace] = true
		if a.Service == nil {
			t.Fatalf("Apis() entry %+v has a nil Service", a)
		}
	}
	for _, want := range []string{"eth", "web3", "net", "debug", "txpool", "n42"} {
		if !seenNamespaces[want] {
			t.Fatalf("Apis() missing namespace %q; got %v", want, seenNamespaces)
		}
	}
}
