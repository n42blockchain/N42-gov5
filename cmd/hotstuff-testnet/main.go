// hotstuff-testnet generates a 7-node HotStuff BFT testnet:
// - 7 BLS keypairs
// - genesis.json with all validators
// - 7 data directories with keystores
// - PowerShell start script
//
// Usage: go run ./cmd/hotstuff-testnet
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/n42blockchain/N42/common/crypto/bls"
)

const numNodes = 7

func main() {
	baseDir := "hotstuff_testnet"
	os.RemoveAll(baseDir)
	os.MkdirAll(baseDir, 0755)

	type valInfo struct {
		Address string
		BLSPub  string
		secHex  string
	}

	nodes := make([]valInfo, numNodes)

	for i := 0; i < numNodes; i++ {
		sk, err := bls.RandKey()
		if err != nil {
			panic(fmt.Sprintf("generate BLS key %d: %v", i, err))
		}
		pk := sk.PublicKey()
		pkBytes := pk.Marshal()

		// Derive address from first 20 bytes of pubkey
		var addr [20]byte
		copy(addr[:], pkBytes[:20])
		addrHex := "0x" + hex.EncodeToString(addr[:])

		nodes[i] = valInfo{
			Address: addrHex,
			BLSPub:  "0x" + hex.EncodeToString(pkBytes),
			secHex:  hex.EncodeToString(sk.Marshal()),
		}

		// Write BLS key to keystore
		nodeDir := filepath.Join(baseDir, fmt.Sprintf("node%d", i))
		keystoreDir := filepath.Join(nodeDir, "keystore")
		os.MkdirAll(keystoreDir, 0700)
		keyFile := filepath.Join(keystoreDir, fmt.Sprintf("bls_%s.key", hex.EncodeToString(addr[:])))
		os.WriteFile(keyFile, []byte(nodes[i].secHex), 0600)

		fmt.Printf("Node %d: %s\n", i, addrHex)
	}

	// Build validators array for chainspec
	validators := make([]map[string]string, numNodes)
	for i, n := range nodes {
		validators[i] = map[string]string{
			"address": n.Address,
			"blsKey":  n.BLSPub,
		}
	}

	// Genesis alloc: fund first validator
	alloc := map[string]interface{}{
		nodes[0].Address: map[string]string{
			"balance": "1000000000000000000000000000",
		},
	}

	genesis := map[string]interface{}{
		"config": map[string]interface{}{
			"chainId":             1143,
			"homesteadBlock":      0,
			"daoForkBlock":        nil,
			"daoForkSupport":      false,
			"eip150Block":         0,
			"eip150Hash":          "0x0000000000000000000000000000000000000000000000000000000000000000",
			"eip155Block":         0,
			"eip158Block":         0,
			"byzantiumBlock":      0,
			"constantinopleBlock": 0,
			"petersburgBlock":     0,
			"istanbulBlock":       0,
			"muirGlacierBlock":    0,
			"berlinBlock":         0,
			"londonBlock":         0,
			"arrowGlacierBlock":   0,
			"beijingBlock":        0,
			"consensus":           "hotstuff",
			"hotstuff": map[string]interface{}{
				"period":            4,
				"baseTimeout":       30000,
				"maxTimeout":        120000,
				"epochLength":       1000,
				"fastPropose":       false,
				"minProposeDelayMs": 200,
				"validators":        validators,
			},
		},
		"gasLimit": 30000000,
		"alloc":    alloc,
	}

	genesisFile := filepath.Join(baseDir, "genesis.json")
	data, _ := json.MarshalIndent(genesis, "", "  ")
	os.WriteFile(genesisFile, data, 0644)
	fmt.Printf("\nGenesis: %s\n", genesisFile)

	// Init all nodes
	fmt.Println("\n=== Initializing all nodes ===")
	for i := 0; i < numNodes; i++ {
		nodeDir := filepath.Join(baseDir, fmt.Sprintf("node%d", i))
		fmt.Printf("  n42 init --data.dir %s %s\n", nodeDir, genesisFile)
	}

	// Generate PowerShell start script
	baseP2P := 30300
	baseHTTP := 28500

	script := "# HotStuff 7-Node Testnet Start Script\n"
	script += "# Run this from the N42-gov5 directory\n\n"
	script += fmt.Sprintf("$genesis = \"%s\"\n", genesisFile)
	script += "$binary = \".\\build\\bin\\n42.exe\"\n\n"

	// Init commands
	script += "# Step 1: Initialize all nodes\n"
	for i := 0; i < numNodes; i++ {
		nodeDir := filepath.Join(baseDir, fmt.Sprintf("node%d", i))
		script += fmt.Sprintf("& $binary init --data.dir %s $genesis\n", nodeDir)
	}
	script += "\n"

	// Build static peer args - each node connects to node0
	script += "# Step 2: Start all nodes (each in its own process)\n"
	script += "# Node 0 starts first, others connect to it\n\n"

	for i := 0; i < numNodes; i++ {
		nodeDir := filepath.Join(baseDir, fmt.Sprintf("node%d", i))
		p2pPort := baseP2P + i
		httpPort := baseHTTP + i
		udpPort := 31300 + i

		cmd := fmt.Sprintf(
			"Start-Process -FilePath $binary -ArgumentList "+
				"'--data.dir %s --chain %s --port %d --p2p.tcp-port %d --p2p.udp-port %d --http --http.port %d --mine --etherbase %s --log.level info --p2p.no-discovery --p2p.max-peers 10'",
			nodeDir, genesisFile, p2pPort, p2pPort, udpPort, httpPort, nodes[i].Address,
		)

		script += fmt.Sprintf("# Node %d (port %d)\n%s\n\n", i, p2pPort, cmd)
	}

	script += "Write-Host 'All 7 nodes started. Check logs in each node directory.'\n"
	script += "Write-Host 'Monitor: curl http://localhost:28500 -X POST -H \"Content-Type: application/json\" -d \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"method\\\":\\\"eth_blockNumber\\\",\\\"params\\\":[],\\\"id\\\":1}\"'\n"

	scriptFile := filepath.Join(baseDir, "start.ps1")
	os.WriteFile(scriptFile, []byte(script), 0755)
	fmt.Printf("Start script: %s\n", scriptFile)

	// Also generate a simpler batch for checking block number
	checkScript := fmt.Sprintf(`@echo off
echo Checking block numbers across all 7 nodes...
for /L %%%%i in (0,1,6) do (
    set /a port=28500+%%%%i
    echo Node %%%%i (port %%%%port%%%%):
    curl -s http://localhost:%%%%port%%%% -X POST -H "Content-Type: application/json" -d "{\"jsonrpc\":\"2.0\",\"method\":\"eth_blockNumber\",\"params\":[],\"id\":1}"
    echo.
)
`)
	checkFile := filepath.Join(baseDir, "check_blocks.bat")
	os.WriteFile(checkFile, []byte(checkScript), 0755)

	fmt.Println("\n=== Quick Start ===")
	fmt.Println("1. cd C:\\N42\\N42-gov5")
	fmt.Println("2. go run ./cmd/hotstuff-testnet")
	fmt.Println("3. Run init for each node:")
	for i := 0; i < numNodes; i++ {
		nodeDir := filepath.Join(baseDir, fmt.Sprintf("node%d", i))
		fmt.Printf("   .\\build\\bin\\n42.exe init --data.dir %s %s\n", nodeDir, genesisFile)
	}
	fmt.Println("4. Start nodes with: powershell -File hotstuff_testnet\\start.ps1")
	fmt.Println("5. Check blocks: hotstuff_testnet\\check_blocks.bat")
}
