// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"testing"

	"github.com/n42blockchain/N42/common/hexutil"
)

func TestAdminAPI_NoopMethods(t *testing.T) {
	admin := NewAdminAPI(&API{})

	ok, err := admin.AddPeer("/ip4/127.0.0.1/tcp/1/p2p/x")
	if err != nil || ok {
		t.Fatalf("AddPeer(no p2p) = %v, %v, want false, nil", ok, err)
	}
	ok, err = admin.RemovePeer("peer-id")
	if err != nil || ok {
		t.Fatalf("RemovePeer(no p2p) = %v, %v, want false, nil", ok, err)
	}
	if ok, err := admin.AddTrustedPeer("x"); err != nil || ok {
		t.Fatalf("AddTrustedPeer() = %v, %v, want false, nil", ok, err)
	}
	if ok, err := admin.RemoveTrustedPeer("x"); err != nil || ok {
		t.Fatalf("RemoveTrustedPeer() = %v, %v, want false, nil", ok, err)
	}
	if ok, err := admin.StartHTTP("127.0.0.1", 0, "", ""); err != nil || ok {
		t.Fatalf("StartHTTP() = %v, %v, want false, nil", ok, err)
	}
	if ok, err := admin.StopHTTP(); err != nil || ok {
		t.Fatalf("StopHTTP() = %v, %v, want false, nil", ok, err)
	}
	if ok, err := admin.StartWS("127.0.0.1", 0, "", ""); err != nil || ok {
		t.Fatalf("StartWS() = %v, %v, want false, nil", ok, err)
	}
	if ok, err := admin.StopWS(); err != nil || ok {
		t.Fatalf("StopWS() = %v, %v, want false, nil", ok, err)
	}
}

func TestPersonalAPI_EmptyAccountManager(t *testing.T) {
	p := NewPersonalAPI(&API{})
	if got := p.ListAccounts(); len(got) != 0 {
		t.Fatalf("ListAccounts(no manager) = %v, want empty", got)
	}
	if got := p.ListWallets(); len(got) != 0 {
		t.Fatalf("ListWallets(no manager) = %v, want empty", got)
	}
}

func TestMinerAPI_NoopMethods(t *testing.T) {
	m := NewMinerAPI(&API{})
	if err := m.Start(nil); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	m.Stop()
	if m.Mining() {
		t.Fatal("Mining(no miner) = true, want false")
	}
	if m.SetEtherbase([20]byte{}) {
		t.Fatal("SetEtherbase(no miner) = true, want false")
	}
	if m.SetGasPrice(hexutil.Big{}) {
		t.Fatal("SetGasPrice() = true, want false")
	}
	if m.SetGasLimit(0) {
		t.Fatal("SetGasLimit() = true, want false")
	}
}

func TestRPCAPI_Modules(t *testing.T) {
	r := NewRPCAPI(&API{})
	mods := r.Modules()
	if mods["eth"] != "1.0" {
		t.Fatalf("Modules()[eth] = %q, want 1.0", mods["eth"])
	}
}

func TestBlockChainAPI_ProtocolVersion(t *testing.T) {
	bca := &BlockChainAPI{api: &API{}}
	if got := bca.ProtocolVersion(); got != hexutil.Uint(69) {
		t.Fatalf("ProtocolVersion() = %d, want 69", got)
	}
}

func TestWeb3API_Version(t *testing.T) {
	w := &Web3API{}
	if got := w.Version(); got == "" {
		t.Fatal("Version() returned empty string")
	}
}

func TestDebugAPI_MiscNoops(t *testing.T) {
	d := &DebugAPI{api: &API{}}

	if s, err := d.ChaindbProperty("x"); err != nil || s != "" {
		t.Fatalf("ChaindbProperty() = %q, %v, want empty, nil", s, err)
	}
	if err := d.ChaindbCompact(); err != nil {
		t.Fatalf("ChaindbCompact() error = %v", err)
	}
	d.FreeOSMemory()

	stats := d.MemStats()
	if stats == nil {
		t.Fatal("MemStats() = nil")
	}

	gc := d.GcStats()
	if gc == nil {
		t.Fatal("GcStats() = nil")
	}

	if s := d.Stacks(); s == "" {
		t.Fatal("Stacks() returned empty string")
	}

	if err := d.CPUProfile("x", 1); err != nil {
		t.Fatalf("CPUProfile() error = %v", err)
	}
	if err := d.BlockProfile("x", 1); err != nil {
		t.Fatalf("BlockProfile() error = %v", err)
	}
	d.SetBlockProfileRate(0)
	d.SetMutexProfileFraction(0)
}
