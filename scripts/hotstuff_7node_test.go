// +build ignore

// hotstuff_7node_test generates 7 BLS keypairs, creates a HotStuff genesis,
// and prints the shell commands to start 7 nodes on localhost.
//
// Usage: go run scripts/hotstuff_7node_test.go
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/n42blockchain/N42/common/crypto/bls"
)

type validator struct {
	Address string `json:"address"`
	BLSKey  string `json:"blsKey"`
}

func main() {
	baseDir := "hotstuff_testnet"
	os.MkdirAll(baseDir, 0755)

	validators := make([]validator, 7)
	secretHexes := make([]string, 7)

	for i := 0; i < 7; i++ {
		// Generate BLS secret key
		var seed [32]byte
		rand.Read(seed[:])
		sk, err := bls.SecretKeyFromBytes(seed[:])
		if err != nil {
			// Fallback: use RandKey
			sk2, err2 := bls.RandKey()
			if err2 != nil {
				panic(err2)
			}
			sk = sk2
		}

		pk := sk.PublicKey()
		// Use a deterministic address from the public key
		pkBytes := pk.Marshal()
		var addr [20]byte
		copy(addr[:], pkBytes[:20])

		validators[i] = validator{
			Address: "0x" + hex.EncodeToString(addr[:]),
			BLSKey:  "0x" + hex.EncodeToString(pkBytes),
		}
		secretHexes[i] = hex.EncodeToString(sk.Marshal())

		// Write secret key to keystore dir
		nodeDir := filepath.Join(baseDir, fmt.Sprintf("node%d", i))
		keystoreDir := filepath.Join(nodeDir, "keystore")
		os.MkdirAll(keystoreDir, 0700)
		keyFile := filepath.Join(keystoreDir, fmt.Sprintf("bls_%s.key", hex.EncodeToString(addr[:])))
		os.WriteFile(keyFile, []byte(secretHexes[i]), 0600)
	}

	// Build genesis JSON
	genesis := map[string]interface{}{
		"chainId":             1143,
		"homesteadBlock":      0,
		"eip150Block":         0,
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
	}

	genesisWrapper := map[string]interface{}{
		"config":   genesis,
		"gasLimit": 30000000,
		"alloc": map[string]interface{}{
			validators[0].Address: map[string]string{
				"balance": "1000000000000000000000000000",
			},
		},
	}

	genesisFile := filepath.Join(baseDir, "genesis.json")
	data, _ := json.MarshalIndent(genesisWrapper, "", "  ")
	os.WriteFile(genesisFile, data, 0644)

	fmt.Println("=== HotStuff 7-Node Testnet Generated ===")
	fmt.Printf("Genesis: %s\n\n", genesisFile)

	for i := 0; i < 7; i++ {
		fmt.Printf("Node %d: addr=%s\n", i, validators[i].Address)
	}

	fmt.Println("\n=== Start Commands (run each in a separate terminal) ===\n")

	baseP2P := 30300
	baseRPC := 28500

	// Build static peers list (all nodes connect to all others)
	for i := 0; i < 7; i++ {
		nodeDir := filepath.Join(baseDir, fmt.Sprintf("node%d", i))
		p2pPort := baseP2P + i
		rpcPort := baseRPC + i

		cmd := fmt.Sprintf(
			".\\build\\bin\\n42.exe --data.dir %s --chain hotstuff_testnet --p2p.listen.tcp %d --http.port %d --miner.etherbase %s --log.level info",
			nodeDir, p2pPort, rpcPort, validators[i].Address,
		)
		fmt.Printf("# Node %d\n%s\n\n", i, cmd)
	}

	fmt.Println("=== Or use --dev mode for single-node test ===")
	fmt.Printf(".\\build\\bin\\n42.exe --dev --data.dir %s --miner.etherbase %s --log.level info\n",
		filepath.Join(baseDir, "node0"), validators[0].Address)
}
