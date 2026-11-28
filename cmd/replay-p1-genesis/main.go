// Command replay-p1-genesis writes a profile-1 genesis for a chain-94 replay.
//
// Without --validator-secrets-file it uses four published TEST validator
// keys (and writes them to --keys); without --pool-seed-file it keeps the
// chain-94 committee-pool seed, which is public. With the secret files the
// output genesis embeds the secret pool seed (committeePool.seedHex) and is
// itself secret: it is written mode 0600 and nothing secret is printed.
//
// --source sums every source block's body rewards (historic APoS rewards)
// per address into the genesis alloc as a pre-fund: profile 1 executes all
// history under one rule set, so the rewards cannot be credited per block.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/replay"
	"github.com/n42blockchain/N42/params"
)

func main() {
	std := flag.String("std", "internal/testdata/profile1/standard_genesis.json", "standard profile-1 genesis")
	out := flag.String("out", "p1-genesis.json", "genesis out")
	keysOut := flag.String("keys", "p1-keys.json", "keys out")
	gasLimit := flag.Uint64("gaslimit", 30_000_000, "hotstuff.gasLimit")
	reward := flag.String("reward", "0", "devBlockReward (wei, decimal string)")
	seedFile := flag.String("pool-seed-file", "", "file holding the committee-pool seed (32-byte hex); the genesis becomes secret")
	secretsFile := flag.String("validator-secrets-file", "", "JSON list of the validators' BLS secret keys (0x-hex, 32 B)")
	source := flag.String("source", "", "source chain datadir (read-only): pre-fund the summed body rewards")
	sourceTo := flag.Uint64("source-to", 0, "last source block for the reward pre-fund")
	prefundOut := flag.String("prefund-out", "", "write the per-address pre-fund table (public data) here")
	var extraAlloc stringList
	flag.Var(&extraAlloc, "extra-alloc", "alloc delta of a replay-v2 --p1-repair-out pass (public data); its credits are added to the genesis alloc; repeatable, deltas add up")
	flag.Parse()
	secret := *seedFile != "" || *secretsFile != ""

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
	var sks []blsSecret
	if *secretsFile != "" {
		sks, err = readValidatorSecrets(*secretsFile)
		must(err)
	} else {
		for i := 0; i < 4; i++ {
			seed := sha256.Sum256([]byte(fmt.Sprintf("n42-replay-p1-test-validator-%d", i)))
			seed[0] = 0
			sk, err := bls.SecretKeyFromBytes(seed[:])
			must(err)
			sks = append(sks, sk)
			keys = append(keys, hexutil.Encode(sk.Marshal()))
		}
	}
	var vals []map[string]string
	for _, sk := range sks {
		pk := sk.PublicKey().Marshal()
		addr := types.BytesToAddress(crypto.Keccak256(pk)[12:])
		vals = append(vals, map[string]string{"address": addr.Hex(), "blsKey": hexutil.Encode(pk)})
	}
	hsm["validators"] = vals
	hsm["blockIntervalMs"] = 8000
	hsm["gasLimit"] = *gasLimit
	hsm["devBlockReward"] = *reward
	pool := params.MainnetQMDBStaggeredChainConfig.HotStuff.CommitteePool
	seedHex := pool.SeedHex
	if *seedFile != "" {
		seedHex, err = readPoolSeed(*seedFile)
		must(err)
	}
	hsm["committeePool"] = map[string]any{"seedHex": seedHex, "poolSize": pool.PoolSize, "committeeSize": pool.CommitteeSize, "rampBlocks": pool.RampBlocks}
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
	if *source != "" {
		prefund, entries, err := sourceRewards(*source, *sourceTo)
		must(err)
		total := new(big.Int)
		for a, amt := range prefund {
			acc := alloc[a]
			bal, ok := new(big.Int).SetString(strings.TrimSpace(acc.Balance), 0)
			if acc.Balance == "" {
				bal, ok = new(big.Int), true
			}
			if !ok {
				must(fmt.Errorf("alloc %s: balance %q", a.Hex(), acc.Balance))
			}
			acc.Balance = bal.Add(bal, amt).String()
			alloc[a] = acc
			total.Add(total, amt)
		}
		fmt.Printf("reward pre-fund: %d entries, %d addresses, %s wei\n", entries, len(prefund), total)
		if *prefundOut != "" {
			type row struct {
				Address string `json:"address"`
				Wei     string `json:"wei"`
			}
			rows := make([]row, 0, len(prefund))
			for a, amt := range prefund {
				rows = append(rows, row{a.Hex(), amt.String()})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Address < rows[j].Address })
			enc, _ := json.MarshalIndent(rows, "", "  ")
			must(os.WriteFile(*prefundOut, enc, 0o644))
		}
	}
	for _, path := range extraAlloc {
		d, err := replay.ReadP1AllocDelta(path)
		must(err)
		must(addAllocDelta(alloc, d))
		fmt.Printf("extra alloc %s: %d addresses, %d precreated, %d predeployed, %s wei\n", path, len(d.Entries), countPrecreates(d), d.Predeploys(), d.Total().Dec())
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
	mode := os.FileMode(0o644)
	if secret {
		mode = 0o600
	}
	must(writeFileMode(*out, enc, mode))
	if len(keys) > 0 {
		kenc, _ := json.Marshal(keys)
		must(writeFileMode(*keysOut, kenc, 0o600))
	}
	fmt.Println("alloc", len(alloc), "chainId", back.Config.ChainID, "validators", len(back.Config.HotStuff.Validators), "time", back.Timestamp, "secret", secret)
	for _, v := range vals {
		fmt.Println("validator", v["address"], v["blsKey"])
	}
}

type blsSecret = blscommon.SecretKey

// writeFileMode writes data with exactly mode (an existing file's mode is
// reset too).
func writeFileMode(path string, data []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// readPoolSeed reads a 32-byte hex seed. Errors never quote the content.
func readPoolSeed(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("pool seed file: %w", err)
	}
	b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(string(raw)), "0x"))
	if err != nil || len(b) != 32 {
		return "", errors.New("pool seed file: not 32 bytes of hex")
	}
	return hexutil.Encode(b), nil
}

// readValidatorSecrets reads a JSON list of hex BLS secret keys. Errors
// never quote the content.
func readValidatorSecrets(path string) ([]blsSecret, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("validator secrets file: %w", err)
	}
	var hexKeys []string
	if json.Unmarshal(raw, &hexKeys) != nil || len(hexKeys) == 0 {
		return nil, errors.New("validator secrets file: not a JSON list of hex keys")
	}
	out := make([]blsSecret, 0, len(hexKeys))
	for i, h := range hexKeys {
		b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(h), "0x"))
		if err != nil || len(b) != 32 {
			return nil, fmt.Errorf("validator secrets file: key %d is not 32 bytes of hex", i)
		}
		sk, err := bls.SecretKeyFromBytes(b)
		if err != nil {
			return nil, fmt.Errorf("validator secrets file: key %d is not a valid BLS secret key", i)
		}
		out = append(out, sk)
	}
	return out, nil
}

// sourceRewards sums every source block's body rewards per address.
func sourceRewards(dir string, to uint64) (map[types.Address]*big.Int, uint64, error) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbx.NewMDBX(log.New()).Path(dir + "/chaindata").Label(kv.ChainDB).
		MapSize(2 * datasize.TB).Readonly().Accede().Open(context.Background())
	if err != nil {
		return nil, 0, err
	}
	defer db.Close()
	sums := map[types.Address]*big.Int{}
	var entries uint64
	err = db.View(context.Background(), func(tx kv.Tx) error {
		if to == 0 {
			return errors.New("--source-to is required")
		}
		for n := uint64(1); n <= to; n++ {
			blk, _ := rawdb.ReadBlockByNumber(tx, n)
			if blk == nil {
				return fmt.Errorf("source block %d missing", n)
			}
			bd, ok := blk.Body().(*block.Body)
			if !ok || bd == nil {
				continue
			}
			for _, r := range bd.Rewards {
				if r.Amount == nil || r.Amount.IsZero() {
					continue
				}
				entries++
				s := sums[r.Address]
				if s == nil {
					s = new(big.Int)
					sums[r.Address] = s
				}
				s.Add(s, r.Amount.ToBig())
			}
		}
		return nil
	})
	return sums, entries, err
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// addAllocDelta adds a repair-pass alloc delta to alloc, creating the
// accounts it does not hold yet: balance credits add up; a predeployed
// contract sets code, storage and nonce, and must agree with what alloc (or
// an earlier delta) already holds for that account.
func addAllocDelta(alloc conf.GenesisAlloc, d *replay.P1AllocDelta) error {
	for a, e := range d.Entries {
		acc := alloc[a]
		bal := new(big.Int)
		if strings.TrimSpace(acc.Balance) != "" {
			var ok bool
			if bal, ok = new(big.Int).SetString(strings.TrimSpace(acc.Balance), 0); !ok {
				return fmt.Errorf("alloc %s: balance %q", a.Hex(), acc.Balance)
			}
		}
		acc.Balance = bal.Add(bal, e.Wei.ToBig()).String()
		if e.Predeploy {
			if len(acc.Code) > 0 && !bytes.Equal(acc.Code, e.Code) {
				return fmt.Errorf("alloc %s: predeploy code differs from the alloc's", a.Hex())
			}
			if acc.Nonce != 0 && acc.Nonce != e.Nonce {
				return fmt.Errorf("alloc %s: predeploy nonce %d, alloc has %d", a.Hex(), e.Nonce, acc.Nonce)
			}
			acc.Code, acc.Nonce = append([]byte(nil), e.Code...), e.Nonce
			if len(e.Storage) > 0 {
				st := make(map[types.Hash]types.Hash, len(acc.Storage)+len(e.Storage))
				for k, v := range acc.Storage {
					st[k] = v
				}
				for k, v := range e.Storage {
					if old, ok := st[k]; ok && old != v {
						return fmt.Errorf("alloc %s: predeploy slot %s differs from the alloc's", a.Hex(), k.Hex())
					}
					st[k] = v
				}
				acc.Storage = st
			}
		}
		alloc[a] = acc
	}
	return nil
}

func countPrecreates(d *replay.P1AllocDelta) int {
	n := 0
	for _, e := range d.Entries {
		if e.Precreate {
			n++
		}
	}
	return n
}
