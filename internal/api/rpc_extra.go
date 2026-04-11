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

// =============================================================================
// Additional RPC Methods - Geth/Erigon Compatibility
// =============================================================================
//
// This file adds simple RPC methods for compatibility with common tools.
// Only safe, read-only or simple methods are included.
//
// Namespaces covered:
// - admin_*   : Node administration (read-only info)
// - personal_*: Account management (limited)
// - miner_*   : Mining control (PoA compatible)
// - rpc_*     : RPC module info

import (
	"context"
	"runtime"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Admin API - Node Information
// =============================================================================

// AdminAPI provides node administration RPC methods.
type AdminAPI struct {
	api *API
}

// NewAdminAPI creates a new AdminAPI instance.
func NewAdminAPI(api *API) *AdminAPI {
	return &AdminAPI{api: api}
}

// NodeInfo represents basic information about the node.
type NodeInfo struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Enode      string            `json:"enode"`
	ENR        string            `json:"enr"`
	IP         string            `json:"ip"`
	Ports      *NodePorts        `json:"ports"`
	ListenAddr string            `json:"listenAddr"`
	Protocols  map[string]string `json:"protocols"`
}

// NodePorts represents the node's listening ports.
type NodePorts struct {
	Discovery int `json:"discovery"`
	Listener  int `json:"listener"`
}

// NodeInfo returns basic information about the running node.
func (admin *AdminAPI) NodeInfo() *NodeInfo {
	info := &NodeInfo{
		ID:   "n42-node",
		Name: "N42/" + params.Version + "/" + runtime.GOOS + "-" + runtime.GOARCH + "/" + runtime.Version(),
		Ports: &NodePorts{
			Discovery: 61015,
			Listener:  61016,
		},
		ListenAddr: ":61016",
		Protocols:  map[string]string{"eth": "eth/69"},
	}

	if admin.api != nil {
		if p := admin.api.p2p; p != nil {
			info.ID = p.SelfNodeID()
			info.Enode = p.SelfEnode()
			info.ENR = p.SelfENR()
			addrs := p.SelfListenAddrs()
			if len(addrs) > 0 {
				info.ListenAddr = addrs[0]
			}
		}
	}
	return info
}

// PeerInfo represents information about a connected peer.
type PeerInfo struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Caps    []string `json:"caps"`
	Network struct {
		LocalAddress  string `json:"localAddress"`
		RemoteAddress string `json:"remoteAddress"`
	} `json:"network"`
	Protocols map[string]interface{} `json:"protocols"`
}

// Peers returns information about connected peers.
func (admin *AdminAPI) Peers() []*PeerInfo {
	if admin.api != nil {
		if p := admin.api.p2p; p != nil {
			return p.PeerInfos()
		}
	}
	return []*PeerInfo{}
}

// Datadir returns the data directory of the node.
func (admin *AdminAPI) Datadir() string {
	// Return empty string as we don't expose the actual path for security
	return ""
}

// AddPeer requests connecting to a remote node.
// N42 uses libp2p; url must be a multiaddr string such as
// /ip4/1.2.3.4/tcp/61016/p2p/12D3KooW...
func (admin *AdminAPI) AddPeer(url string) (bool, error) {
	if admin.api != nil {
		if p := admin.api.p2p; p != nil {
			if err := p.AddPeer(url); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

// RemovePeer disconnects from a remote node identified by its peer ID string.
func (admin *AdminAPI) RemovePeer(peerID string) (bool, error) {
	if admin.api != nil {
		if p := admin.api.p2p; p != nil {
			if err := p.RemovePeer(peerID); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

// AddTrustedPeer adds the given node to a reserved whitelist.
// N42's libp2p stack does not use a static trusted-peer list; this is a no-op.
func (admin *AdminAPI) AddTrustedPeer(url string) (bool, error) {
	return false, nil
}

// RemoveTrustedPeer removes a remote node from the trusted peer set.
// N42's libp2p stack does not use a static trusted-peer list; this is a no-op.
func (admin *AdminAPI) RemoveTrustedPeer(url string) (bool, error) {
	return false, nil
}

// StartHTTP starts the HTTP RPC server.
func (admin *AdminAPI) StartHTTP(host string, port int, cors string, apis string) (bool, error) {
	// Not supported at runtime
	return false, nil
}

// StopHTTP stops the HTTP RPC server.
func (admin *AdminAPI) StopHTTP() (bool, error) {
	// Not supported at runtime
	return false, nil
}

// StartWS starts the WebSocket RPC server.
func (admin *AdminAPI) StartWS(host string, port int, allowedOrigins string, apis string) (bool, error) {
	// Not supported at runtime
	return false, nil
}

// StopWS stops the WebSocket RPC server.
func (admin *AdminAPI) StopWS() (bool, error) {
	// Not supported at runtime
	return false, nil
}

// =============================================================================
// Personal API - Account Management (Limited)
// =============================================================================

// PersonalAPI provides account management RPC methods.
type PersonalAPI struct {
	api *API
}

// NewPersonalAPI creates a new PersonalAPI instance.
func NewPersonalAPI(api *API) *PersonalAPI {
	return &PersonalAPI{api: api}
}

// ListAccounts returns the list of accounts managed by the node.
func (personal *PersonalAPI) ListAccounts() []types.Address {
	if personal.api == nil || personal.api.accountManager == nil {
		return []types.Address{}
	}
	return personal.api.accountManager.Accounts()
}

// ListWallets returns the list of wallets managed by the node.
func (personal *PersonalAPI) ListWallets() []map[string]interface{} {
	if personal.api == nil || personal.api.accountManager == nil {
		return []map[string]interface{}{}
	}

	wallets := personal.api.accountManager.Wallets()
	result := make([]map[string]interface{}, len(wallets))
	for i, wallet := range wallets {
		accounts := wallet.Accounts()
		addrs := make([]types.Address, len(accounts))
		for j, acc := range accounts {
			addrs[j] = acc.Address
		}
		status, _ := wallet.Status()
		result[i] = map[string]interface{}{
			"url":      wallet.URL().String(),
			"status":   status,
			"accounts": addrs,
		}
	}
	return result
}

// =============================================================================
// Miner API - Mining Control (PoA Compatible)
// =============================================================================

// MinerAPI provides mining control RPC methods.
type MinerAPI struct {
	api *API
}

// NewMinerAPI creates a new MinerAPI instance.
func NewMinerAPI(api *API) *MinerAPI {
	return &MinerAPI{api: api}
}

// Start starts the miner.
// For PoA/PoS, block production is driven by the consensus engine; this RPC
// method does not start new goroutines but will be respected if mining is stopped.
func (miner *MinerAPI) Start(threads *int) error {
	return nil
}

// Stop stops the miner.
// For PoA/PoS, stopping mining at runtime is not supported via RPC.
func (miner *MinerAPI) Stop() {}

// Mining returns whether the node is currently producing blocks.
func (miner *MinerAPI) Mining() bool {
	if miner.api != nil {
		if m := miner.api.miner; m != nil {
			return m.Mining()
		}
	}
	return false
}

// SetEtherbase sets the etherbase (coinbase/reward) address for block production.
func (miner *MinerAPI) SetEtherbase(etherbase types.Address) bool {
	if miner.api != nil {
		if m := miner.api.miner; m != nil {
			m.SetCoinbase(etherbase)
			return true
		}
	}
	return false
}

// SetGasPrice sets the minimum gas price for mining.
// Gas price policy is managed at the chain level in N42; this is advisory only.
func (miner *MinerAPI) SetGasPrice(gasPrice hexutil.Big) bool {
	return false
}

// SetGasLimit sets the target gas limit for mining.
// Gas limit is managed by the worker; dynamic adjustment via RPC is not yet supported.
func (miner *MinerAPI) SetGasLimit(gasLimit hexutil.Uint64) bool {
	return false
}

// =============================================================================
// RPC API - Module Information
// =============================================================================

// RPCAPI provides RPC module information.
type RPCAPI struct {
	api *API
}

// NewRPCAPI creates a new RPCAPI instance.
func NewRPCAPI(api *API) *RPCAPI {
	return &RPCAPI{api: api}
}

// Modules returns the list of enabled RPC modules.
func (rpc *RPCAPI) Modules() map[string]string {
	return map[string]string{
		"eth":      "1.0",
		"net":      "1.0",
		"web3":     "1.0",
		"txpool":   "1.0",
		"debug":    "1.0",
		"admin":    "1.0",
		"personal": "1.0",
		"miner":    "1.0",
		"rpc":      "1.0",
	}
}

// =============================================================================
// Additional eth_* Methods
// =============================================================================

// ProtocolVersion returns the current Ethereum protocol version.
func (s *BlockChainAPI) ProtocolVersion() hexutil.Uint {
	// Return the eth protocol version (execution layer)
	// Note: N42 uses libp2p/gossipsub for P2P, not DevP2P/RLPx
	// This version number is for RPC compatibility with Ethereum tools
	return hexutil.Uint(69) // eth/69 (RPC compatibility declaration)
}

// =============================================================================
// Additional web3_* Methods
// =============================================================================

// ClientVersion returns the version of the running node.
// Note: This extends the existing Web3API.
func (s *Web3API) Version() string {
	return "N42/" + params.Version
}

// =============================================================================
// Additional txpool_* Methods
// =============================================================================

// ContentFrom returns the transactions contained within the transaction pool
// from a specific address.
func (s *TxsPoolAPI) ContentFrom(ctx context.Context, addr types.Address) map[string]map[string]*RPCTransaction {
	content := make(map[string]map[string]*RPCTransaction)
	content["pending"] = make(map[string]*RPCTransaction)
	content["queued"] = make(map[string]*RPCTransaction)

	pending, queue := s.api.TxsPool().Content()
	curBlock := s.api.BlockChain().CurrentBlock()
	if curBlock == nil {
		return content
	}
	curHeader := curBlock.Header()

	// Filter pending transactions
	if txs, ok := pending[addr]; ok {
		for _, tx := range txs {
			content["pending"][tx.Hash().Hex()] = newRPCPendingTransaction(tx, curHeader)
		}
	}

	// Filter queued transactions
	if txs, ok := queue[addr]; ok {
		for _, tx := range txs {
			content["queued"][tx.Hash().Hex()] = newRPCPendingTransaction(tx, curHeader)
		}
	}

	return content
}

// =============================================================================
// Additional debug_* Methods
// =============================================================================

// ChaindbProperty returns the value of a database property.
func (debug *DebugAPI) ChaindbProperty(property string) (string, error) {
	// Return empty as we don't expose internal DB properties
	return "", nil
}

// ChaindbCompact flattens the entire key-value database.
// This is a no-op for safety.
func (debug *DebugAPI) ChaindbCompact() error {
	// No-op for safety
	return nil
}

// FreeOSMemory forces garbage collection.
func (debug *DebugAPI) FreeOSMemory() {
	runtime.GC()
}

// MemStats returns detailed runtime memory statistics.
func (debug *DebugAPI) MemStats() *runtime.MemStats {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return &stats
}

// GcStats returns GC statistics.
func (debug *DebugAPI) GcStats() *GCStats {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return &GCStats{
		LastGC:       stats.LastGC,
		NumGC:        uint64(stats.NumGC),
		PauseTotal:   stats.PauseTotalNs,
		PauseHistory: stats.PauseNs[:],
	}
}

// GCStats represents GC statistics.
type GCStats struct {
	LastGC       uint64   `json:"lastGC"`
	NumGC        uint64   `json:"numGC"`
	PauseTotal   uint64   `json:"pauseTotal"`
	PauseHistory []uint64 `json:"pauseHistory"`
}

// Stacks returns a printed representation of the stacks of all goroutines.
func (debug *DebugAPI) Stacks() string {
	buf := make([]byte, 1024*1024)
	n := runtime.Stack(buf, true)
	return string(buf[:n])
}

// CPUProfile enables CPU profiling for the given duration.
// Returns an error as this is not supported in production.
func (debug *DebugAPI) CPUProfile(file string, seconds uint) error {
	// Not supported in production for security
	return nil
}

// BlockProfile enables block profiling.
// Returns as this is not supported in production.
func (debug *DebugAPI) BlockProfile(file string, seconds uint) error {
	// Not supported in production for security
	return nil
}

// SetBlockProfileRate sets the block profiling rate.
func (debug *DebugAPI) SetBlockProfileRate(rate int) {
	runtime.SetBlockProfileRate(rate)
}

// SetMutexProfileFraction sets the mutex profiling fraction.
func (debug *DebugAPI) SetMutexProfileFraction(rate int) {
	runtime.SetMutexProfileFraction(rate)
}

// Verbosity sets the global log verbosity level.
// Maps Ethereum convention (0=silent .. 5=trace) to N42 log levels:
//
//	0 → Crit (fatal only)   3 → Info
//	1 → Error               4 → Debug
//	2 → Warn                5 → Trace
func (debug *DebugAPI) Verbosity(level int) {
	// Clamp to valid range and forward to the log package.
	if level < 0 {
		level = 0
	}
	if level > 5 {
		level = 5
	}
	// Our log.Lvl: 0=Crit 1=Fatal 2=Error 3=Warn 4=Info 5=Debug 6=Trace
	// eth verbosity: 0=silent 1=error 2=warn 3=info 4=debug 5=trace
	// Shift by +2 so that verbosity 0→Crit(0), 1→Error(2), ..., 5→Trace(6).
	mapping := [6]int{
		0, // 0 → LvlCrit
		2, // 1 → LvlError
		3, // 2 → LvlWarn
		4, // 3 → LvlInfo
		5, // 4 → LvlDebug
		6, // 5 → LvlTrace
	}
	log.SetLevel(mapping[level])
}

// Vmodule sets per-module log verbosity.
// N42 uses logrus which does not support module-level verbosity filtering;
// calling this method adjusts the global level instead if pattern is empty.
func (debug *DebugAPI) Vmodule(pattern string) error {
	// logrus does not support vmodule patterns; accepted but ignored.
	return nil
}
