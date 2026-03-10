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

// Command stresstest generates sustained transaction load against an N42 node.
//
// Features:
//   - 30 TPS default (90% native transfers, 10% ERC-20 transfers)
//   - Deterministic test accounts using project's own crypto package
//   - ERC-20 USDT mock contract auto-deployed
//   - 3-layer nonce management
//   - Periodic health checks and final report
//
// Usage:
//
//	go run ./cmd/stresstest --rpc http://127.0.0.1:28545 --tps 30 --duration 3600
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/rlp"
)

// =============================================================================
// Configuration
// =============================================================================

const (
	numAccounts    = 50
	fundAmountWei  = "0x8AC7230489E80000" // 10 ETH
	nativeGasLimit = 21_000
	erc20GasLimit  = 100_000
	deployGasLimit = 3_000_000
	defaultGasWei  = 1_000_000_000 // 1 Gwei
)

// Minimal ERC-20 bytecode (Solidity compiled):
//
//	mapping(address => uint256) public balanceOf;
//	constructor() { balanceOf[msg.sender] = 10**27; }
//	function transfer(address to, uint256 v) external returns(bool) {
//	    balanceOf[msg.sender] -= v; balanceOf[to] += v; return true;
//	}
//
// transfer(address,uint256) selector: 0xa9059cbb
// balanceOf(address)        selector: 0x70a08231
var erc20Bytecode []byte

func init() {
	// Minimal ERC-20 init code (hand-verified, Solidity 0.8 compatible output)
	// Constructor mints 10^27 tokens to msg.sender
	hexCode := "608060405234801561001057600080fd5b50" +
		"6c0c9f2c9cd04674edea40000000" + // 10**27
		"600080336001600160a01b03166001600160a01b0316815260200190815260200160002081905550" +
		"610196806100456000396000f3fe" + // runtime code size + offset
		"608060405234801561001057600080fd5b506004361061003657" +
		"60003560e01c8063a9059cbb1461003b57806370a082311461006b575b600080fd5b" +
		// transfer(address,uint256) -> returns bool
		"61005560048036036040811015610051576000" +
		"80fd5b50604435906001600160a01b03602435166100a1565b604080519115158252519081" +
		"900360200190f35b" +
		// balanceOf(address) -> returns uint256
		"61008f60048036036020811015610081576000" +
		"80fd5b50356001600160a01b03166100ea565b60408051918252519081900360200190f35b" +
		// transfer implementation
		"336000908152602081905260408120805483900390556001600160a01b03831660009081526020819052604090208054820190556001" +
		"92915050565b" +
		// balanceOf implementation
		"6001600160a01b0316600090815260208190526040902054905600" +
		"fea164736f6c6343"
	erc20Bytecode, _ = hex.DecodeString(hexCode)
}

// =============================================================================
// JSON-RPC Client
// =============================================================================

type rpcClient struct {
	url    string
	client *http.Client
	idSeq  atomic.Uint64
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      uint64        `json:"id"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newRPCClient(url string) *rpcClient {
	return &rpcClient{
		url: url,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *rpcClient) call(method string, params ...interface{}) (json.RawMessage, error) {
	if params == nil {
		params = []interface{}{}
	}
	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      c.idSeq.Add(1),
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Post(c.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

func (c *rpcClient) blockNumber() (uint64, error) {
	result, err := c.call("eth_blockNumber")
	if err != nil {
		return 0, err
	}
	return parseHexUint64(result)
}

func (c *rpcClient) chainID() (*big.Int, error) {
	result, err := c.call("eth_chainId")
	if err != nil {
		return nil, err
	}
	return parseHexBigInt(result)
}

func (c *rpcClient) getNonce(addr string) (uint64, error) {
	result, err := c.call("eth_getTransactionCount", addr, "pending")
	if err != nil {
		return 0, err
	}
	return parseHexUint64(result)
}

func (c *rpcClient) getBalance(addr string) (*big.Int, error) {
	result, err := c.call("eth_getBalance", addr, "latest")
	if err != nil {
		return nil, err
	}
	return parseHexBigInt(result)
}

func (c *rpcClient) getCoinbase() (string, error) {
	result, err := c.call("eth_coinbase")
	if err != nil {
		return "", err
	}
	var addr string
	if err := json.Unmarshal(result, &addr); err != nil {
		return "", err
	}
	return addr, nil
}

func (c *rpcClient) sendTransaction(from, to, value string) (string, error) {
	txObj := map[string]string{
		"from":  from,
		"to":    to,
		"value": value,
	}
	result, err := c.call("eth_sendTransaction", txObj)
	if err != nil {
		return "", err
	}
	var hash string
	if err := json.Unmarshal(result, &hash); err != nil {
		return "", err
	}
	return hash, nil
}

func (c *rpcClient) sendRawTx(rawHex string) (string, error) {
	result, err := c.call("eth_sendRawTransaction", rawHex)
	if err != nil {
		return "", err
	}
	var hash string
	if err := json.Unmarshal(result, &hash); err != nil {
		return "", err
	}
	return hash, nil
}

func (c *rpcClient) getReceipt(txHash string) (map[string]interface{}, error) {
	result, err := c.call("eth_getTransactionReceipt", txHash)
	if err != nil {
		return nil, err
	}
	if string(result) == "null" {
		return nil, nil
	}
	var receipt map[string]interface{}
	if err := json.Unmarshal(result, &receipt); err != nil {
		return nil, err
	}
	return receipt, nil
}

func (c *rpcClient) txpoolStatus() (pending, queued uint64) {
	result, err := c.call("txpool_status")
	if err != nil {
		return 0, 0
	}
	var status map[string]string
	if err := json.Unmarshal(result, &status); err != nil {
		return 0, 0
	}
	p, _ := parseHexUint64FromString(status["pending"])
	q, _ := parseHexUint64FromString(status["queued"])
	return p, q
}

// =============================================================================
// Account Management
// =============================================================================

type testAccount struct {
	key     *ecdsa.PrivateKey
	address avmutil.Address
	addrHex string
}

func generateAccounts(n int) []*testAccount {
	accounts := make([]*testAccount, n)
	for i := 0; i < n; i++ {
		seed := fmt.Sprintf("n42-stress-test-key-%d", i)
		hash := sha256.Sum256([]byte(seed))

		key, err := crypto.ToECDSA(hash[:])
		if err != nil {
			panic(fmt.Sprintf("failed to generate key %d: %v", i, err))
		}

		addr := crypto.PubkeyToAddress(key.PublicKey)
		// Convert N42 types.Address to avmutil.Address
		avmAddr := avmutil.BytesToAddress(addr.Bytes())

		accounts[i] = &testAccount{
			key:     key,
			address: avmAddr,
			addrHex: avmAddr.Hex(),
		}
	}
	return accounts
}

// =============================================================================
// Nonce Manager
// =============================================================================

type nonceManager struct {
	rpc    *rpcClient
	nonces sync.Map // address string -> *uint64
}

func newNonceManager(rpc *rpcClient) *nonceManager {
	return &nonceManager{rpc: rpc}
}

// preload fetches and caches the nonce for the given address without consuming it.
func (nm *nonceManager) preload(addr string) {
	if _, loaded := nm.nonces.Load(addr); loaded {
		return
	}
	nonce, err := nm.rpc.getNonce(addr)
	if err != nil {
		nonce = 0
	}
	ptr := &atomic.Uint64{}
	ptr.Store(nonce)
	nm.nonces.LoadOrStore(addr, ptr)
}

func (nm *nonceManager) getNonce(addr string) uint64 {
	val, loaded := nm.nonces.Load(addr)
	if loaded {
		ptr := val.(*atomic.Uint64)
		return ptr.Add(1) - 1
	}

	// First time: fetch from node
	nonce, err := nm.rpc.getNonce(addr)
	if err != nil {
		nonce = 0
	}
	ptr := &atomic.Uint64{}
	ptr.Store(nonce + 1)
	actual, loaded := nm.nonces.LoadOrStore(addr, ptr)
	if loaded {
		// Another goroutine beat us
		return actual.(*atomic.Uint64).Add(1) - 1
	}
	return nonce
}


// =============================================================================
// Transaction Builder
// =============================================================================

// signAndEncode signs a LegacyTx and returns its RLP encoding (suitable for eth_sendRawTransaction).
// The node's UnmarshalBinary does rlp.DecodeBytes(b, &LegacyTx{}), so we encode the signed LegacyTx directly.
func signAndEncode(inner *avmtypes.LegacyTx, key *ecdsa.PrivateKey, chainID *big.Int) ([]byte, error) {
	tx := avmtypes.NewTx(inner)
	signer := avmtypes.NewEIP155Signer(chainID)
	signedTx, err := avmtypes.SignTx(tx, signer, key)
	if err != nil {
		return nil, err
	}

	// Extract signature and build the signed LegacyTx for RLP encoding.
	v, r, s := signedTx.RawSignatureValues()
	signed := &avmtypes.LegacyTx{
		Nonce:    inner.Nonce,
		GasPrice: inner.GasPrice,
		Gas:      inner.Gas,
		To:       signedTx.To(),
		Value:    signedTx.Value(),
		Data:     signedTx.Data(),
		V:        v,
		R:        r,
		S:        s,
	}
	return rlp.EncodeToBytes(signed)
}

func buildNativeTransfer(key *ecdsa.PrivateKey, nonce uint64, to avmutil.Address, value *big.Int, gasPrice *big.Int, chainID *big.Int) ([]byte, error) {
	return signAndEncode(&avmtypes.LegacyTx{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      nativeGasLimit,
		To:       &to,
		Value:    value,
	}, key, chainID)
}

func buildERC20Transfer(key *ecdsa.PrivateKey, nonce uint64, contract avmutil.Address, to avmutil.Address, amount *big.Int, gasPrice *big.Int, chainID *big.Int) ([]byte, error) {
	// transfer(address,uint256) = 0xa9059cbb
	data := make([]byte, 4+32+32)
	copy(data[0:4], []byte{0xa9, 0x05, 0x9c, 0xbb})
	copy(data[4+12:4+32], to.Bytes())
	amountBytes := amount.Bytes()
	copy(data[4+32+(32-len(amountBytes)):4+64], amountBytes)

	return signAndEncode(&avmtypes.LegacyTx{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      erc20GasLimit,
		To:       &contract,
		Value:    big.NewInt(0),
		Data:     data,
	}, key, chainID)
}

func buildContractDeploy(key *ecdsa.PrivateKey, nonce uint64, code []byte, gasPrice *big.Int, chainID *big.Int) ([]byte, error) {
	return signAndEncode(&avmtypes.LegacyTx{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      deployGasLimit,
		Value:    big.NewInt(0),
		Data:     code,
	}, key, chainID)
}

// =============================================================================
// Stats
// =============================================================================

type stats struct {
	sent       atomic.Uint64
	failed     atomic.Uint64
	native     atomic.Uint64
	erc20      atomic.Uint64
	startTime  time.Time
}

func (s *stats) report(rpc *rpcClient) {
	elapsed := time.Since(s.startTime).Seconds()
	sent := s.sent.Load()
	failed := s.failed.Load()
	native := s.native.Load()
	erc20 := s.erc20.Load()
	actualTPS := float64(sent) / elapsed

	blockNum, _ := rpc.blockNumber()
	pending, queued := rpc.txpoolStatus()

	fmt.Printf("[%s] Sent: %d (native: %d, erc20: %d) | Failed: %d | TPS: %.1f | Block: #%d | Pool: p=%d q=%d\n",
		time.Now().Format("15:04:05"),
		sent, native, erc20, failed,
		actualTPS, blockNum, pending, queued)
}

func (s *stats) finalReport(rpc *rpcClient) {
	elapsed := time.Since(s.startTime).Seconds()
	sent := s.sent.Load()
	failed := s.failed.Load()
	native := s.native.Load()
	erc20 := s.erc20.Load()
	total := sent + failed

	blockNum, _ := rpc.blockNumber()

	fmt.Println()
	fmt.Println("==========================================================")
	fmt.Println("                    STRESS TEST REPORT")
	fmt.Println("==========================================================")
	fmt.Printf("Duration:       %.0fs\n", elapsed)
	fmt.Printf("Total sent:     %d\n", sent)
	fmt.Printf("  Native:       %d\n", native)
	fmt.Printf("  ERC-20:       %d\n", erc20)
	fmt.Printf("Total failed:   %d\n", failed)
	if total > 0 {
		fmt.Printf("Success rate:   %.1f%%\n", float64(sent)/float64(total)*100)
	}
	if elapsed > 0 {
		fmt.Printf("Actual TPS:     %.1f\n", float64(sent)/elapsed)
	}
	fmt.Printf("Final block:    #%d\n", blockNum)
	fmt.Println("==========================================================")
}

// =============================================================================
// Load Generator
// =============================================================================

type loadGenerator struct {
	rpc      *rpcClient
	nonces   *nonceManager
	accounts []*testAccount
	chainID  *big.Int
	gasPrice *big.Int

	tps      int
	duration time.Duration

	erc20Contract avmutil.Address
	hasERC20      bool

	st *stats
}

func (g *loadGenerator) deployERC20(ctx context.Context) {
	if len(erc20Bytecode) == 0 {
		fmt.Println("ERC-20 bytecode empty, skipping deployment")
		return
	}

	deployer := g.accounts[0]
	nonce := g.nonces.getNonce(deployer.addrHex)

	fmt.Printf("Deploying ERC-20 from %s (nonce=%d)...\n", deployer.addrHex, nonce)

	raw, err := buildContractDeploy(deployer.key, nonce, erc20Bytecode, g.gasPrice, g.chainID)
	if err != nil {
		fmt.Printf("Failed to build deploy tx: %v\n", err)
		return
	}

	txHash, err := g.rpc.sendRawTx("0x" + hex.EncodeToString(raw))
	if err != nil {
		fmt.Printf("Failed to send deploy tx: %v\n", err)
		return
	}

	fmt.Printf("Deploy tx: %s\n", txHash)

	// Wait for receipt
	for i := 0; i < 30; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}

		receipt, err := g.rpc.getReceipt(txHash)
		if err != nil || receipt == nil {
			continue
		}

		contractAddr, ok := receipt["contractAddress"].(string)
		if ok && contractAddr != "" {
			g.erc20Contract = avmutil.HexToAddress(contractAddr)
			g.hasERC20 = true
			fmt.Printf("ERC-20 deployed at: %s\n", contractAddr)

			// Distribute tokens to other accounts
			g.distributeTokens(ctx)
			return
		}

		// Check status
		status, _ := receipt["status"].(string)
		if status == "0x0" {
			fmt.Println("ERC-20 deploy reverted, skipping ERC-20 transfers")
			return
		}
	}

	fmt.Println("ERC-20 deploy tx not confirmed in 30s, skipping ERC-20")
}

func (g *loadGenerator) distributeTokens(ctx context.Context) {
	deployer := g.accounts[0]
	amount := new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil) // 10^24

	distributed := 0
	for i := 1; i < len(g.accounts); i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		nonce := g.nonces.getNonce(deployer.addrHex)
		raw, err := buildERC20Transfer(deployer.key, nonce, g.erc20Contract, g.accounts[i].address, amount, g.gasPrice, g.chainID)
		if err != nil {
			continue
		}

		_, err = g.rpc.sendRawTx("0x" + hex.EncodeToString(raw))
		if err == nil {
			distributed++
		}
	}
	fmt.Printf("Distributed tokens to %d accounts\n", distributed)
}

func (g *loadGenerator) warmupNonces() {
	fmt.Print("Warming up nonces...")
	for _, acc := range g.accounts {
		g.nonces.preload(acc.addrHex)
	}
	fmt.Println(" OK")
}

func (g *loadGenerator) run(ctx context.Context) {
	g.warmupNonces()
	fmt.Printf("Starting load: %d TPS for %s\n", g.tps, g.duration)
	fmt.Printf("  Native: %d/s, ERC-20: %d/s\n", g.tps*9/10, g.tps-g.tps*9/10)

	g.st = &stats{startTime: time.Now()}
	endTime := time.Now().Add(g.duration)

	nativePerSec := g.tps * 9 / 10
	erc20PerSec := g.tps - nativePerSec

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for time.Now().Before(endTime) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		batchStart := time.Now()

		// Native transfers
		for i := 0; i < nativePerSec; i++ {
			g.sendNativeTransfer()
		}

		// ERC-20 transfers
		for i := 0; i < erc20PerSec; i++ {
			g.sendERC20Transfer()
		}

		// Rate limiting
		elapsed := time.Since(batchStart)
		if elapsed < time.Second {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second - elapsed):
			}
		}

		// Periodic report
		select {
		case <-ticker.C:
			g.st.report(g.rpc)
		default:
		}
	}

	g.st.finalReport(g.rpc)
}

func (g *loadGenerator) sendNativeTransfer() {
	senderIdx := rand.Intn(len(g.accounts))
	receiverIdx := (senderIdx + 1 + rand.Intn(len(g.accounts)-1)) % len(g.accounts)

	sender := g.accounts[senderIdx]
	receiver := g.accounts[receiverIdx]

	nonce := g.nonces.getNonce(sender.addrHex)
	value := big.NewInt(int64(rand.Intn(1000) + 1))

	raw, err := buildNativeTransfer(sender.key, nonce, receiver.address, value, g.gasPrice, g.chainID)
	if err != nil {
		g.st.failed.Add(1)
		return
	}

	_, err = g.rpc.sendRawTx("0x" + hex.EncodeToString(raw))
	if err != nil {
		g.st.failed.Add(1)
		if g.st.failed.Load() <= 5 {
			fmt.Printf("  [DBG] native tx failed: nonce=%d from=%s err=%v\n", nonce, sender.addrHex, err)
		}
		return
	}

	g.st.sent.Add(1)
	g.st.native.Add(1)
}

func (g *loadGenerator) sendERC20Transfer() {
	if !g.hasERC20 {
		g.sendNativeTransfer()
		return
	}

	senderIdx := 1 + rand.Intn(len(g.accounts)-1)
	receiverIdx := (senderIdx + 1) % len(g.accounts)
	if receiverIdx == 0 {
		receiverIdx = 1
	}

	sender := g.accounts[senderIdx]
	receiver := g.accounts[receiverIdx]

	nonce := g.nonces.getNonce(sender.addrHex)
	amount := big.NewInt(int64(rand.Intn(1000)+1) * 1e6) // 1-1000 USDT (6 decimals)

	raw, err := buildERC20Transfer(sender.key, nonce, g.erc20Contract, receiver.address, amount, g.gasPrice, g.chainID)
	if err != nil {
		g.st.failed.Add(1)
		return
	}

	_, err = g.rpc.sendRawTx("0x" + hex.EncodeToString(raw))
	if err != nil {
		g.st.failed.Add(1)
		return
	}

	g.st.sent.Add(1)
	g.st.erc20.Add(1)
}

// =============================================================================
// Helpers
// =============================================================================

func parseHexUint64(raw json.RawMessage) (uint64, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, err
	}
	return parseHexUint64FromString(s)
}

func parseHexUint64FromString(s string) (uint64, error) {
	s = strings.TrimPrefix(s, "0x")
	if s == "" {
		return 0, nil
	}
	var val uint64
	for _, c := range s {
		val <<= 4
		switch {
		case c >= '0' && c <= '9':
			val |= uint64(c - '0')
		case c >= 'a' && c <= 'f':
			val |= uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			val |= uint64(c-'A') + 10
		default:
			return 0, fmt.Errorf("invalid hex char: %c", c)
		}
	}
	return val, nil
}

func parseHexBigInt(raw json.RawMessage) (*big.Int, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	s = strings.TrimPrefix(s, "0x")
	val := new(big.Int)
	val.SetString(s, 16)
	return val, nil
}

// =============================================================================
// Main
// =============================================================================

func main() {
	// Parse args (simple, no external flags package)
	rpcURL := "http://127.0.0.1:28545"
	tps := 30
	duration := 3600 // seconds
	noERC20 := false
	useAccounts := numAccounts

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--rpc", "-rpc":
			if i+1 < len(args) {
				rpcURL = args[i+1]
				i++
			}
		case "--tps", "-tps":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &tps)
				i++
			}
		case "--duration", "-duration":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &duration)
				i++
			}
		case "--accounts", "-accounts":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &useAccounts)
				i++
			}
		case "--no-erc20":
			noERC20 = true
		case "--help", "-h":
			fmt.Println("N42 Stress Test")
			fmt.Println()
			fmt.Println("Usage: stresstest [options]")
			fmt.Println()
			fmt.Println("Options:")
			fmt.Println("  --rpc URL        RPC endpoint (default: http://127.0.0.1:28545)")
			fmt.Println("  --tps N          Target TPS (default: 30)")
			fmt.Println("  --duration N     Duration in seconds (default: 3600)")
			fmt.Println("  --no-erc20       Skip ERC-20 deployment")
			fmt.Println("  --help           Show this help")
			os.Exit(0)
		}
	}

	fmt.Println("==========================================================")
	fmt.Println("                  N42 STRESS TEST")
	fmt.Println("==========================================================")
	fmt.Printf("RPC URL:    %s\n", rpcURL)
	fmt.Printf("Target TPS: %d (native: %d, ERC-20: %d)\n", tps, tps*9/10, tps-tps*9/10)
	fmt.Printf("Duration:   %ds (%dm)\n", duration, duration/60)
	fmt.Println("==========================================================")

	rpc := newRPCClient(rpcURL)

	// Check connectivity
	fmt.Print("Connecting to node... ")
	blockNum, err := rpc.blockNumber()
	if err != nil {
		fmt.Printf("FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("OK (block #%d)\n", blockNum)

	// Get chain ID
	chainID, err := rpc.chainID()
	if err != nil {
		fmt.Printf("Failed to get chain ID: %v\n", err)
		chainID = big.NewInt(94) // fallback to mainnet
	}
	fmt.Printf("Chain ID:   %s\n", chainID)

	// Generate accounts
	fmt.Printf("Generating %d test accounts...\n", numAccounts)
	accounts := generateAccounts(numAccounts)

	// Limit accounts if requested
	if useAccounts > 0 && useAccounts < len(accounts) {
		accounts = accounts[:useAccounts]
	}

	// Print first few addresses for reference
	for i := 0; i < 3 && i < len(accounts); i++ {
		fmt.Printf("  Account[%d]: %s\n", i, accounts[i].addrHex)
	}

	// Check balances
	funded := 0
	for i := 0; i < 5 && i < len(accounts); i++ {
		bal, err := rpc.getBalance(accounts[i].addrHex)
		if err == nil && bal.Sign() > 0 {
			funded++
		}
	}
	fmt.Printf("Funded accounts: %d/5 checked\n", funded)
	if funded == 0 {
		// Try to fund accounts from the node's coinbase (etherbase) via eth_sendTransaction.
		// This works when the etherbase account is unlocked (e.g., --dev mode).
		fmt.Println("No funded accounts. Attempting to fund from coinbase...")
		coinbase, err := rpc.getCoinbase()
		if err == nil && coinbase != "" {
			fundAmount := "0x" + new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1000)).Text(16) // 1000 ETH each
			fundCount := 10
			if fundCount > len(accounts) {
				fundCount = len(accounts)
			}
			for i := 0; i < fundCount; i++ {
				_, sendErr := rpc.sendTransaction(coinbase, accounts[i].addrHex, fundAmount)
				if sendErr != nil {
					fmt.Printf("  Fund account[%d] failed: %v\n", i, sendErr)
					break
				}
			}
			// Wait a few blocks for funding txs to be mined
			fmt.Print("Waiting for funding txs to be mined...")
			for attempt := 0; attempt < 15; attempt++ {
				time.Sleep(2 * time.Second)
				bal, balErr := rpc.getBalance(accounts[0].addrHex)
				if balErr == nil && bal.Sign() > 0 {
					fmt.Println(" OK")
					funded = fundCount
					break
				}
				fmt.Print(".")
			}
			if funded == 0 {
				fmt.Println(" TIMEOUT")
				fmt.Println("WARNING: Could not fund test accounts.")
			}
		} else {
			fmt.Println("WARNING: Could not get coinbase. Ensure genesis includes test accounts.")
		}
	}

	// Setup
	ctx, cancel := context.WithCancel(context.Background())

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nReceived signal, shutting down...")
		cancel()
	}()

	gen := &loadGenerator{
		rpc:      rpc,
		nonces:   newNonceManager(rpc),
		accounts: accounts,
		chainID:  chainID,
		gasPrice: big.NewInt(defaultGasWei),
		tps:      tps,
		duration: time.Duration(duration) * time.Second,
	}

	// Deploy ERC-20
	if !noERC20 {
		gen.deployERC20(ctx)
	}

	// Run
	gen.run(ctx)
}
