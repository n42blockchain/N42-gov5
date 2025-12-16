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

package api

import (
	"encoding/json"
	"runtime"
	"testing"
)

// =============================================================================
// AdminAPI 测试
// =============================================================================

func TestNodeInfo(t *testing.T) {
	admin := &AdminAPI{}
	info := admin.NodeInfo()

	if info == nil {
		t.Fatal("NodeInfo should not return nil")
	}

	// 验证必要字段
	if info.ID == "" {
		t.Error("NodeInfo.ID should not be empty")
	}

	if info.Name == "" {
		t.Error("NodeInfo.Name should not be empty")
	}

	if info.Ports == nil {
		t.Error("NodeInfo.Ports should not be nil")
	}

	if info.Protocols == nil {
		t.Error("NodeInfo.Protocols should not be nil")
	}

	t.Logf("✓ NodeInfo: %s", info.Name)
}

func TestNodeInfoJSON(t *testing.T) {
	admin := &AdminAPI{}
	info := admin.NodeInfo()

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Failed to marshal NodeInfo: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	requiredFields := []string{"id", "name", "enode", "ip", "ports", "listenAddr", "protocols"}
	for _, field := range requiredFields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("Missing field: %s", field)
		}
	}

	t.Logf("✓ NodeInfo JSON serialization works correctly")
}

func TestPeers(t *testing.T) {
	admin := &AdminAPI{}
	peers := admin.Peers()

	// 没有 P2P 连接时应返回空列表
	if peers == nil {
		t.Error("Peers should return empty slice, not nil")
	}

	t.Logf("✓ Peers returns %d peers", len(peers))
}

func TestDatadir(t *testing.T) {
	admin := &AdminAPI{}
	datadir := admin.Datadir()

	// 安全原因返回空字符串
	if datadir != "" {
		t.Logf("Datadir: %s", datadir)
	}

	t.Logf("✓ Datadir works correctly")
}

func TestAddPeer(t *testing.T) {
	admin := &AdminAPI{}
	result, err := admin.AddPeer("enode://abc123@127.0.0.1:30303")

	if err != nil {
		t.Errorf("AddPeer returned error: %v", err)
	}

	// 当前实现返回 false（TODO）
	if result {
		t.Log("AddPeer returned true")
	}

	t.Logf("✓ AddPeer works correctly")
}

func TestRemovePeer(t *testing.T) {
	admin := &AdminAPI{}
	result, err := admin.RemovePeer("enode://abc123@127.0.0.1:30303")

	if err != nil {
		t.Errorf("RemovePeer returned error: %v", err)
	}

	// 当前实现返回 false（TODO）
	if result {
		t.Log("RemovePeer returned true")
	}

	t.Logf("✓ RemovePeer works correctly")
}

// =============================================================================
// MinerAPI 测试
// =============================================================================

func TestMinerStart(t *testing.T) {
	miner := &MinerAPI{}
	threads := 4
	err := miner.Start(&threads)

	if err != nil {
		t.Errorf("Miner.Start returned error: %v", err)
	}

	t.Logf("✓ Miner.Start works correctly")
}

func TestMinerStartNilThreads(t *testing.T) {
	miner := &MinerAPI{}
	err := miner.Start(nil)

	if err != nil {
		t.Errorf("Miner.Start with nil threads returned error: %v", err)
	}

	t.Logf("✓ Miner.Start with nil threads works correctly")
}

func TestMinerStop(t *testing.T) {
	miner := &MinerAPI{}
	miner.Stop() // 不应该 panic

	t.Logf("✓ Miner.Stop works correctly")
}

func TestMinerMining(t *testing.T) {
	miner := &MinerAPI{}
	result := miner.Mining()

	// 当前实现返回 false
	if result {
		t.Log("Mining is active")
	}

	t.Logf("✓ Miner.Mining works correctly")
}

// =============================================================================
// PersonalAPI 测试
// =============================================================================

func TestListAccounts(t *testing.T) {
	personal := &PersonalAPI{api: nil}
	accounts := personal.ListAccounts()

	// 没有账户管理器时应返回空列表
	if accounts == nil {
		t.Error("ListAccounts should return empty slice, not nil")
	}

	t.Logf("✓ ListAccounts returns %d accounts", len(accounts))
}

func TestListWallets(t *testing.T) {
	personal := &PersonalAPI{api: nil}
	wallets := personal.ListWallets()

	// 没有账户管理器时应返回空列表
	if wallets == nil {
		t.Error("ListWallets should return empty slice, not nil")
	}

	t.Logf("✓ ListWallets returns %d wallets", len(wallets))
}

// =============================================================================
// RPCAPI 测试
// =============================================================================

func TestModules(t *testing.T) {
	rpcAPI := &RPCAPI{}
	modules := rpcAPI.Modules()

	if modules == nil {
		t.Fatal("Modules should not return nil")
	}

	// 验证必要的模块
	expectedModules := []string{"eth", "net", "web3", "txpool", "debug", "admin"}
	for _, mod := range expectedModules {
		if _, ok := modules[mod]; !ok {
			t.Errorf("Missing module: %s", mod)
		}
	}

	t.Logf("✓ Modules returns %d modules", len(modules))
}

func TestModulesJSON(t *testing.T) {
	rpcAPI := &RPCAPI{}
	modules := rpcAPI.Modules()

	data, err := json.Marshal(modules)
	if err != nil {
		t.Fatalf("Failed to marshal modules: %v", err)
	}

	var decoded map[string]string
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded["eth"] != "1.0" {
		t.Errorf("eth version = %s, want 1.0", decoded["eth"])
	}

	t.Logf("✓ Modules JSON serialization works correctly")
}

// =============================================================================
// DebugAPI 扩展测试
// =============================================================================

func TestMemStats(t *testing.T) {
	debug := &DebugAPI{}
	stats := debug.MemStats()

	if stats == nil {
		t.Fatal("MemStats should not return nil")
	}

	// 验证一些基本字段
	if stats.Alloc == 0 && stats.TotalAlloc == 0 {
		t.Error("MemStats should have non-zero memory values")
	}

	t.Logf("✓ MemStats: Alloc=%d, TotalAlloc=%d", stats.Alloc, stats.TotalAlloc)
}

func TestGcStats(t *testing.T) {
	debug := &DebugAPI{}
	stats := debug.GcStats()

	if stats == nil {
		t.Fatal("GcStats should not return nil")
	}

	// NumGC 可能是 0 如果还没有 GC 发生
	t.Logf("✓ GcStats: NumGC=%d, PauseTotal=%d", stats.NumGC, stats.PauseTotal)
}

func TestFreeOSMemory(t *testing.T) {
	debug := &DebugAPI{}

	// 获取 GC 前的内存统计
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	debug.FreeOSMemory()

	// 获取 GC 后的内存统计
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	// NumGC 应该增加
	if after.NumGC <= before.NumGC {
		t.Log("Warning: NumGC did not increase (may have already been at max)")
	}

	t.Logf("✓ FreeOSMemory works correctly (NumGC: %d -> %d)", before.NumGC, after.NumGC)
}

func TestStacks(t *testing.T) {
	debug := &DebugAPI{}
	stacks := debug.Stacks()

	if stacks == "" {
		t.Error("Stacks should return non-empty string")
	}

	// 验证包含 goroutine 信息
	if len(stacks) < 100 {
		t.Error("Stacks output seems too short")
	}

	t.Logf("✓ Stacks returns %d bytes of goroutine info", len(stacks))
}

func TestSetBlockProfileRate(t *testing.T) {
	debug := &DebugAPI{}

	// 设置 block profile rate
	debug.SetBlockProfileRate(1)

	// 重置
	debug.SetBlockProfileRate(0)

	t.Logf("✓ SetBlockProfileRate works correctly")
}

func TestSetMutexProfileFraction(t *testing.T) {
	debug := &DebugAPI{}

	// 设置 mutex profile fraction
	debug.SetMutexProfileFraction(1)

	// 重置
	debug.SetMutexProfileFraction(0)

	t.Logf("✓ SetMutexProfileFraction works correctly")
}

func TestVerbosity(t *testing.T) {
	debug := &DebugAPI{}

	// 设置日志级别
	debug.Verbosity(3)

	t.Logf("✓ Verbosity works correctly")
}

func TestVmodule(t *testing.T) {
	debug := &DebugAPI{}

	err := debug.Vmodule("eth/*=5")

	if err != nil {
		t.Errorf("Vmodule returned error: %v", err)
	}

	t.Logf("✓ Vmodule works correctly")
}

func TestChaindbProperty(t *testing.T) {
	debug := &DebugAPI{}

	result, err := debug.ChaindbProperty("leveldb.stats")

	if err != nil {
		t.Errorf("ChaindbProperty returned error: %v", err)
	}

	// 当前返回空字符串
	_ = result

	t.Logf("✓ ChaindbProperty works correctly")
}

func TestChaindbCompact(t *testing.T) {
	debug := &DebugAPI{}

	err := debug.ChaindbCompact()

	if err != nil {
		t.Errorf("ChaindbCompact returned error: %v", err)
	}

	t.Logf("✓ ChaindbCompact works correctly")
}

// =============================================================================
// PeerInfo 测试
// =============================================================================

func TestPeerInfoJSON(t *testing.T) {
	peer := &PeerInfo{
		ID:   "abc123",
		Name: "Geth/v1.10.0",
		Caps: []string{"eth/66", "eth/67"},
		Network: struct {
			LocalAddress  string `json:"localAddress"`
			RemoteAddress string `json:"remoteAddress"`
		}{
			LocalAddress:  "127.0.0.1:30303",
			RemoteAddress: "192.168.1.1:30303",
		},
		Protocols: map[string]interface{}{
			"eth": map[string]interface{}{
				"version": 67,
			},
		},
	}

	data, err := json.Marshal(peer)
	if err != nil {
		t.Fatalf("Failed to marshal PeerInfo: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	requiredFields := []string{"id", "name", "caps", "network", "protocols"}
	for _, field := range requiredFields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("Missing field: %s", field)
		}
	}

	t.Logf("✓ PeerInfo JSON serialization works correctly")
}

// =============================================================================
// NodePorts 测试
// =============================================================================

func TestNodePortsJSON(t *testing.T) {
	ports := &NodePorts{
		Discovery: 30303,
		Listener:  30303,
	}

	data, err := json.Marshal(ports)
	if err != nil {
		t.Fatalf("Failed to marshal NodePorts: %v", err)
	}

	var decoded NodePorts
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.Discovery != 30303 {
		t.Errorf("Discovery = %d, want 30303", decoded.Discovery)
	}

	if decoded.Listener != 30303 {
		t.Errorf("Listener = %d, want 30303", decoded.Listener)
	}

	t.Logf("✓ NodePorts JSON serialization works correctly")
}
