// Command replay-p1-genesis writes a profile-1 genesis for a chain-94 replay
// (TEST validator keys, published) and the matching key file.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/params"
)

func main() {
	std := flag.String("std", "internal/testdata/profile1/standard_genesis.json", "standard profile-1 genesis")
	out := flag.String("out", "p1-genesis.json", "genesis out")
	keysOut := flag.String("keys", "p1-keys.json", "keys out")
	gasLimit := flag.Uint64("gaslimit", 30_000_000, "hotstuff.gasLimit")
	reward := flag.String("reward", "0", "devBlockReward (wei, decimal string)")
	flag.Parse()

	raw, err := os.ReadFile(*std)
	must(err)
	sg := new(conf.Genesis)
	must(json.Unmarshal(raw, sg))
	src := internal.GenesisByChainName("mainnet_qmdb_staggered")

	var doc map[string]any
	must(json.Unmarshal(raw, &doc))
	cfgm := doc["config"].(map[string]any)
	cfgm["chainId"] = params.MainnetQMDBStaggeredChainConfig.ChainID.Uint64()
	hsm := cfgm["hotstuff"].(map[string]any)
	var keys []string
	var vals []map[string]string
	for i := 0; i < 4; i++ {
		seed := sha256.Sum256([]byte(fmt.Sprintf("n42-replay-p1-test-validator-%d", i)))
		seed[0] = 0
		sk, err := bls.SecretKeyFromBytes(seed[:])
		must(err)
		pk := sk.PublicKey().Marshal()
		addr := types.BytesToAddress(crypto.Keccak256(pk)[12:])
		vals = append(vals, map[string]string{"address": addr.Hex(), "blsKey": hexutil.Encode(pk)})
		keys = append(keys, hexutil.Encode(sk.Marshal()))
	}
	hsm["validators"] = vals
	hsm["blockIntervalMs"] = 8000
	hsm["gasLimit"] = *gasLimit
	hsm["devBlockReward"] = *reward
	pool := params.MainnetQMDBStaggeredChainConfig.HotStuff.CommitteePool
	hsm["committeePool"] = map[string]any{"seedHex": pool.SeedHex, "poolSize": pool.PoolSize, "committeeSize": pool.CommitteeSize, "rampBlocks": pool.RampBlocks}
	doc["timestamp"] = fmt.Sprintf("0x%x", src.Timestamp)
	doc["gasLimit"] = fmt.Sprintf("0x%x", *gasLimit)
	alloc := conf.GenesisAlloc{}
	for a, acc := range src.Alloc {
		alloc[a] = acc
	}
	for a, acc := range sg.Alloc {
		if _, ok := alloc[a]; !ok {
			alloc[a] = acc
		}
	}
	am := map[string]map[string]any{}
	for a, acc := range alloc {
		e := map[string]any{"balance": acc.Balance}
		if len(acc.Code) > 0 {
			e["code"] = hexutil.Encode(acc.Code)
		}
		if len(acc.Storage) > 0 {
			e["storage"] = acc.Storage
		}
		if acc.Nonce != 0 {
			e["nonce"] = fmt.Sprintf("0x%x", acc.Nonce)
		}
		am[a.Hex()] = e
	}
	doc["alloc"] = am
	enc, err := json.MarshalIndent(doc, "", "  ")
	must(err)
	back := new(conf.Genesis)
	must(json.Unmarshal(enc, back))
	must(back.Config.ValidateProfile1())
	must(os.WriteFile(*out, enc, 0o644))
	kenc, _ := json.Marshal(keys)
	must(os.WriteFile(*keysOut, kenc, 0o600))
	fmt.Println("alloc", len(alloc), "chainId", back.Config.ChainID, "validators", len(back.Config.HotStuff.Validators), "time", back.Timestamp)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
