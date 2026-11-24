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

package internal

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/params"
)

// The standard genesis of the mixed production standard v0.2 §5 ("N42 native
// chain, Standard Profile 1") as gov5 loads it, and the genesis files a
// Profile-1 client must refuse (vector V8 candidates).
const (
	profile1Dir             = "testdata/profile1"
	profile1StandardFile    = "standard_genesis.json"
	profile1RefusedDir      = "refused"
	profile1RefusedManifest = "expected_errors.json"

	// profile1Timestamp is the T0 placeholder of §5 (2026-11-25T00:00:00Z).
	profile1Timestamp = 1795564800
	// profile1FaucetBalance is the faucet's genesis balance, 1e27 wei.
	profile1FaucetBalance = "0x33b2e3c9fd0803ce8000000"
	profile1Faucet        = "0x42e9819036f61bF665D5f727E8C03121f12f586e"
	profile1SeedHex       = "0x0101010101010101010101010101010101010101010101010101010101010101"
)

// profile1StandardGenesisHash pins the gov5 genesis hash of the standard
// genesis fixture.
var profile1StandardGenesisHash = types.HexToHash("0x6f733745517957b421dcea37f6c879db89501aac0715f769fbc76ec54efe7e4f")

type p1Validator struct {
	Address string `json:"address"`
	BLSKey  string `json:"blsKey"`
}

type p1CommitteePool struct {
	SeedHex       string `json:"seedHex"`
	PoolSize      uint64 `json:"poolSize"`
	CommitteeSize uint64 `json:"committeeSize"`
	RampBlocks    uint64 `json:"rampBlocks"`
}

type p1HotStuff struct {
	Validators             []p1Validator   `json:"validators"`
	LeaderTenure           uint64          `json:"leaderTenure"`
	BaseTimeoutMs          uint64          `json:"baseTimeoutMs"`
	MaxTimeoutMs           uint64          `json:"maxTimeoutMs"`
	BlockIntervalMs        uint64          `json:"blockIntervalMs"`
	EpochLength            uint64          `json:"epochLength"`
	DeferredExecutionDepth uint64          `json:"deferredExecutionDepth"`
	GasLimit               uint64          `json:"gasLimit"`
	MaxBlockBytes          uint64          `json:"maxBlockBytes"`
	CommitteePool          p1CommitteePool `json:"committeePool"`
	DevBlockReward         string          `json:"devBlockReward"`
	DevFaucetAddress       string          `json:"devFaucetAddress"`
}

type p1BlobConfig struct {
	Target                uint64 `json:"target"`
	Max                   uint64 `json:"max"`
	BaseFeeUpdateFraction uint64 `json:"baseFeeUpdateFraction"`
}

type p1Config struct {
	ChainID                       uint64                  `json:"chainId"`
	N42Profile                    uint64                  `json:"n42Profile"`
	HomesteadBlock                uint64                  `json:"homesteadBlock"`
	EIP150Block                   uint64                  `json:"eip150Block"`
	EIP155Block                   uint64                  `json:"eip155Block"`
	EIP158Block                   uint64                  `json:"eip158Block"`
	ByzantiumBlock                uint64                  `json:"byzantiumBlock"`
	ConstantinopleBlock           uint64                  `json:"constantinopleBlock"`
	PetersburgBlock               uint64                  `json:"petersburgBlock"`
	IstanbulBlock                 uint64                  `json:"istanbulBlock"`
	BerlinBlock                   uint64                  `json:"berlinBlock"`
	LondonBlock                   uint64                  `json:"londonBlock"`
	MergeNetsplitBlock            uint64                  `json:"mergeNetsplitBlock"`
	TerminalTotalDifficulty       uint64                  `json:"terminalTotalDifficulty"`
	TerminalTotalDifficultyPassed bool                    `json:"terminalTotalDifficultyPassed"`
	ShanghaiTime                  uint64                  `json:"shanghaiTime"`
	CancunTime                    uint64                  `json:"cancunTime"`
	PragueTime                    uint64                  `json:"pragueTime"`
	OsakaTime                     uint64                  `json:"osakaTime"`
	AmsterdamTime                 uint64                  `json:"amsterdamTime"`
	BlobSchedule                  map[string]p1BlobConfig `json:"blobSchedule"`
	Consensus                     string                  `json:"consensus"`
	StateScheme                   string                  `json:"stateScheme"`
	HotStuff                      p1HotStuff              `json:"hotstuff"`
}

type p1Account struct {
	Balance string `json:"balance"`
	Nonce   string `json:"nonce,omitempty"`
	Code    string `json:"code,omitempty"`
}

type p1Genesis struct {
	Config     p1Config             `json:"config"`
	Nonce      string               `json:"nonce"`
	Timestamp  string               `json:"timestamp"`
	ExtraData  string               `json:"extraData"`
	GasLimit   string               `json:"gasLimit"`
	Difficulty string               `json:"difficulty"`
	MixHash    string               `json:"mixHash"`
	Coinbase   string               `json:"coinbase"`
	BaseFee    string               `json:"baseFeePerGas"`
	Alloc      map[string]p1Account `json:"alloc"`
}

// profile1TestValidators derives the four deterministic test validators:
// BLS secret i = 0x00 || sha256("n42-profile1-test-validator-<i>")[1:] (the
// zero top byte keeps it below the group order), address = the last 20 bytes
// of keccak256(blsPubKey).
func profile1TestValidators(t *testing.T) []p1Validator {
	t.Helper()
	out := make([]p1Validator, 0, 4)
	for i := 0; i < 4; i++ {
		seed := sha256.Sum256([]byte(fmt.Sprintf("n42-profile1-test-validator-%d", i)))
		seed[0] = 0
		sk, err := bls.SecretKeyFromBytes(seed[:])
		if err != nil {
			t.Fatalf("validator %d secret key: %v", i, err)
		}
		pk := sk.PublicKey().Marshal()
		addr := types.BytesToAddress(crypto.Keccak256(pk)[12:])
		out = append(out, p1Validator{Address: strings.ToLower(addr.Hex()), BLSKey: hexutil.Encode(pk)})
	}
	return out
}

// buildProfile1StandardGenesis renders the §5 standard genesis with the test
// validators and gov5's alloc: the Prague system contracts (EIP-7002,
// EIP-7251, EIP-2935), the EIP-4788 beacon roots contract and the faucet.
func buildProfile1StandardGenesis(t *testing.T) []byte {
	t.Helper()
	alloc := map[string]p1Account{
		profile1Faucet: {Balance: profile1FaucetBalance},
		params.BeaconRootsAddress.Hex(): {
			Balance: "0x0", Nonce: "0x1", Code: hexutil.Encode(params.BeaconRootsCode),
		},
	}
	for _, d := range PragueSystemContractDeployments() {
		alloc[d.Address.Hex()] = p1Account{
			Balance: d.Account.Balance, Nonce: fmt.Sprintf("0x%x", d.Account.Nonce), Code: hexutil.Encode(d.Account.Code),
		}
	}
	g := p1Genesis{
		Config: p1Config{
			ChainID:                       1144,
			N42Profile:                    1,
			TerminalTotalDifficultyPassed: true,
			BlobSchedule: map[string]p1BlobConfig{
				"cancun": {Target: 3, Max: 6, BaseFeeUpdateFraction: 3338477},
				"prague": {Target: 6, Max: 9, BaseFeeUpdateFraction: 5007716},
				"osaka":  {Target: 6, Max: 9, BaseFeeUpdateFraction: 5007716},
			},
			Consensus:   "hotstuff",
			StateScheme: "qmdb",
			HotStuff: p1HotStuff{
				Validators:             profile1TestValidators(t),
				LeaderTenure:           8,
				BaseTimeoutMs:          6000,
				MaxTimeoutMs:           30000,
				BlockIntervalMs:        500,
				EpochLength:            200,
				DeferredExecutionDepth: 1,
				GasLimit:               480000000,
				MaxBlockBytes:          67108864,
				CommitteePool: p1CommitteePool{
					SeedHex: profile1SeedHex, PoolSize: 200000, CommitteeSize: 512, RampBlocks: 1000000,
				},
				DevBlockReward:   "1000000000000000000",
				DevFaucetAddress: profile1Faucet,
			},
		},
		Nonce:      "0x0",
		Timestamp:  fmt.Sprintf("0x%x", profile1Timestamp),
		ExtraData:  "0x",
		GasLimit:   "0x1c9c3800",
		Difficulty: "0x0",
		MixHash:    types.Hash{}.Hex(),
		Coinbase:   types.Address{}.Hex(),
		BaseFee:    "0x3b9aca00",
		Alloc:      alloc,
	}
	out, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// TestProfile1StandardGenesisFixture keeps the fixture equal to its
// derivation; N42_UPDATE_PROFILE1_FIXTURE=1 rewrites it.
func TestProfile1StandardGenesisFixture(t *testing.T) {
	want := buildProfile1StandardGenesis(t)
	path := filepath.Join(profile1Dir, profile1StandardFile)
	if os.Getenv("N42_UPDATE_PROFILE1_FIXTURE") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from its derivation; rerun with N42_UPDATE_PROFILE1_FIXTURE=1 if intended", path)
	}
}

func loadProfile1Genesis(t *testing.T, path string) (*conf.Genesis, error) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	g := new(conf.Genesis)
	return g, json.Unmarshal(raw, g)
}

// TestProfile1StandardGenesisLoads loads the standard genesis, checks that
// every Profile-1 predicate holds from block 0 and pins the genesis hash.
func TestProfile1StandardGenesisLoads(t *testing.T) {
	g, err := loadProfile1Genesis(t, filepath.Join(profile1Dir, profile1StandardFile))
	if err != nil {
		t.Fatalf("standard genesis refused: %v", err)
	}
	cfg := g.Config
	if !cfg.IsProfile1() {
		t.Fatal("standard genesis is not profile 1")
	}
	for name, on := range map[string]bool{
		"deferred execution":   cfg.IsDeferredExecution(0),
		"unchanged no-op":      cfg.IsUnchangedAccountNoOp(0),
		"storage net change":   cfg.IsStorageNetChange(0),
		"mobile anchor":        cfg.IsMobileAnchor(0),
		"amsterdam":            cfg.IsGlamsterdam(0),
		"registry root header": cfg.IsMobileRegistryRootInHeader(0),
		"two-phase vote gate":  cfg.HotStuffTwoPhaseVoteGate(),
		"H2-v4":                cfg.HotStuffInteropV4(),
		"committee pool":       cfg.HotStuffCommitteePoolEnabled(),
		"prague":               cfg.Rules(0).IsPrague,
		"osaka":                cfg.Rules(0).IsOsaka,
	} {
		if !on {
			t.Errorf("%s is off at genesis on profile 1", name)
		}
	}
	for name, on := range map[string]bool{
		"eof": cfg.IsEOF(0), "bal": cfg.IsBAL(0), "fusaka": cfg.IsFusaka(0),
		"pq precompiles": cfg.IsPQPrecompiles(0), "blake3 tx root": cfg.IsTxRootBlake3(0),
		"lthash": cfg.IsLtHash(0),
	} {
		if on {
			t.Errorf("%s is on at genesis on profile 1", name)
		}
	}
	if on, from := cfg.HotStuffRotateOnTimeout(); !on || from != 0 {
		t.Errorf("rotate-on-timeout = %v from %d, want on from view 0", on, from)
	}
	if base, max := cfg.HotStuffTimeoutsMs(); base != 6000 || max != 30000 {
		t.Errorf("timeouts = %d/%d ms, want 6000/30000", base, max)
	}
	if got := cfg.HotStuff.DevBlockReward.String(); got != "1000000000000000000" {
		t.Errorf("devBlockReward = %s", got)
	}

	blk, _, err := (&GenesisBlock{GenesisConfig: g}).ToBlock()
	if err != nil {
		t.Fatalf("build standard genesis block: %v", err)
	}
	head := blk.Header().(*block.Header)
	if head.UncleHash != (types.Hash{}) {
		t.Errorf("ommersHash = %s, want zero (B3)", head.UncleHash.Hex())
	}
	if head.RequestsHash == nil || *head.RequestsHash != hash.EmptyRootHash {
		t.Errorf("requestsHash = %v, want the empty-trie root (AM3)", head.RequestsHash)
	}
	if head.BlockAccessListHash != nil {
		t.Errorf("element 22 = %s, want the empty gap (B12)", head.BlockAccessListHash.Hex())
	}
	if head.SlotNumber == nil || *head.SlotNumber != 0 {
		t.Errorf("slotNumber = %v, want 0 (B10)", head.SlotNumber)
	}
	if head.MobileRegistryRoot == nil || *head.MobileRegistryRoot != (types.Hash{}) {
		t.Errorf("mobileRegistryRoot = %v, want present and zero (A6, B11)", head.MobileRegistryRoot)
	}
	if head.ParentBeaconRoot == nil || *head.ParentBeaconRoot != (types.Hash{}) || len(head.Extra) != 0 {
		t.Errorf("parentBeaconRoot %v / extra %x, want zero / empty (A6)", head.ParentBeaconRoot, head.Extra)
	}
	if rlpBytes, err := rlp.EncodeToBytes(head); err == nil {
		t.Logf("profile-1 standard genesis header RLP %x", rlpBytes)
	}
	t.Logf("profile-1 standard genesis hash %s (stateRoot %s)", blk.Hash().Hex(), blk.StateRoot().Hex())
	if blk.Hash() != profile1StandardGenesisHash {
		t.Fatalf("standard genesis hash = %s, pinned %s", blk.Hash().Hex(), profile1StandardGenesisHash.Hex())
	}

	// The stored config (what the node reads on every start) still passes.
	enc, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var stored params.ChainConfig
	if err := json.Unmarshal(enc, &stored); err != nil {
		t.Fatal(err)
	}
	if err := params.NormalizeConsensus(&stored).ValidateProfile1(); err != nil {
		t.Fatalf("stored profile-1 config refused: %v", err)
	}
}

// TestProfile1RefusedGenesisVectors loads every refused genesis and checks
// the error names the offending field.
func TestProfile1RefusedGenesisVectors(t *testing.T) {
	dir := filepath.Join(profile1Dir, profile1RefusedDir)
	raw, err := os.ReadFile(filepath.Join(dir, profile1RefusedManifest))
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]string
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		if filepath.Base(f) != profile1RefusedManifest {
			names = append(names, filepath.Base(f))
		}
	}
	sort.Strings(names)
	if len(names) != len(expected) {
		t.Fatalf("%d refused vectors, %d expectations", len(names), len(expected))
	}
	for _, name := range names {
		want, ok := expected[name]
		if !ok {
			t.Errorf("%s: no expected error", name)
			continue
		}
		_, err := loadProfile1Genesis(t, filepath.Join(dir, name))
		if err == nil {
			t.Errorf("%s: accepted, want refusal containing %q", name, want)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %q does not contain %q", name, err, want)
		}
	}
}

// TestProfile1LegacyGenesisUnchanged: a legacy native genesis (number
// devBlockReward, legacy hotstuff fields) still loads and keeps its flags.
func TestProfile1LegacyGenesisUnchanged(t *testing.T) {
	g, err := loadProfile1Genesis(t, filepath.Join("testdata", "glamsterdam_native", "n42_devnet_mobileanchor.json"))
	if err != nil {
		t.Fatalf("legacy genesis refused: %v", err)
	}
	cfg := g.Config
	if cfg.IsProfile1() {
		t.Fatal("legacy genesis read as profile 1")
	}
	if got := cfg.HotStuff.DevBlockReward; got.Quoted() || got.String() != "1000000000000000000" {
		t.Fatalf("legacy devBlockReward = %s (quoted %v)", got, got.Quoted())
	}
	if cfg.IsDeferredExecution(1<<40) || cfg.HotStuffTwoPhaseVoteGate() || !cfg.HotStuffInteropV4() {
		t.Fatal("legacy flags changed meaning")
	}
	if base, max := cfg.HotStuffTimeoutsMs(); base != 6000 || max != 30000 {
		t.Fatalf("legacy timeouts = %d/%d", base, max)
	}
	enc, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(enc, []byte(`"devBlockReward":1000000000000000000`)) {
		t.Fatalf("legacy devBlockReward re-encoded differently: %s", enc)
	}
}

// TestProfile1AllowedKeysFile keeps the published closed key list equal to
// the one the validation applies; N42_UPDATE_PROFILE1_FIXTURE=1 rewrites it.
func TestProfile1AllowedKeysFile(t *testing.T) {
	want, err := json.MarshalIndent(params.Profile1AllowedKeys(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join(profile1Dir, "allowed_keys.json")
	if os.Getenv("N42_UPDATE_PROFILE1_FIXTURE") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from params.Profile1AllowedKeys", path)
	}
}
