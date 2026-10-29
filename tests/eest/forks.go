// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// forks.go — fork name -> ChainConfig mapping for the Ethereum
// execution-spec-tests (EEST) fixtures.
//
// Every network name a fixture can carry (the "post" keys of state tests and
// the "network" field of blockchain tests) maps to an explicit Ethereum
// ruleset here, modelled on go-ethereum's tests/init.go. The configs mirror
// params.EthereumMainnetChainConfig (ethash consensus marker, MPT state
// scheme, mainnet deposit contract) with every fork up to the named one
// active from genesis. Transition networks ("XToYAtTime15k") activate Y at
// timestamp 15000, "XToYAt5" at block 5.

package eest

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/n42blockchain/N42/params"
)

// Env vars that drive the opt-in fixture runners.
const (
	// EnvDir points at an extracted EEST "fixtures" directory (the one that
	// holds state_tests/, blockchain_tests/, ...). Unset = runners skip.
	EnvDir = "N42_EEST_DIR"
	// EnvForks optionally narrows the forks to run (comma-separated network
	// names). Unset = DefaultForks.
	EnvForks = "N42_EEST_FORKS"
	// EnvFilter optionally restricts runs to fixture files whose path
	// (relative to the suite directory, slash-separated) contains it.
	EnvFilter = "N42_EEST_FILTER"
	// EnvWorkers sets how many fixtures the blockchain runners execute in
	// parallel (each on its own chaindata). Default 1.
	EnvWorkers = "N42_EEST_WORKERS"
	// EnvReport optionally names a TSV file that receives one line per
	// executed case (suite, fork, file, test, status, error).
	EnvReport = "N42_EEST_REPORT"
)

// fork is one rung of the Ethereum fork ladder, in activation order.
type fork int

const (
	frontier fork = iota
	homestead
	eip150
	eip158
	byzantium
	constantinople
	petersburg
	istanbul
	muirGlacier
	berlin
	london
	arrowGlacier
	grayGlacier
	paris
	shanghai
	cancun
	prague
	osaka
	bpo1
	bpo2
	bpo3
	bpo4
	bpo5
	// amsterdam follows BPO2 on the fixture schedule; BPO3-5 stay off.
	amsterdam
)

// forkByName lists the single-fork network names fixtures use.
var forkByName = map[string]fork{
	"Frontier":          frontier,
	"Homestead":         homestead,
	"EIP150":            eip150,
	"EIP158":            eip158,
	"Byzantium":         byzantium,
	"Constantinople":    constantinople,
	"ConstantinopleFix": petersburg,
	"Petersburg":        petersburg,
	"Istanbul":          istanbul,
	"MuirGlacier":       muirGlacier,
	"Berlin":            berlin,
	"London":            london,
	"ArrowGlacier":      arrowGlacier,
	"GrayGlacier":       grayGlacier,
	"Paris":             paris,
	"Merge":             paris,
	"Shanghai":          shanghai,
	"Cancun":            cancun,
	"Prague":            prague,
	"Osaka":             osaka,
	"BPO1":              bpo1,
	"BPO2":              bpo2,
	"BPO3":              bpo3,
	"BPO4":              bpo4,
	"BPO5":              bpo5,
	"Amsterdam":         amsterdam,
}

// transition is a network that runs fork `from` at genesis and switches to
// `to` at block `atBlock` (block-based forks) or timestamp `atTime`.
type transition struct {
	from, to        fork
	atBlock, atTime uint64
}

// transitions lists the transition networks, as in geth's tests/init.go.
var transitions = map[string]transition{
	"FrontierToHomesteadAt5":       {from: frontier, to: homestead, atBlock: 5},
	"HomesteadToEIP150At5":         {from: homestead, to: eip150, atBlock: 5},
	"EIP158ToByzantiumAt5":         {from: eip158, to: byzantium, atBlock: 5},
	"ByzantiumToConstantinopleAt5": {from: byzantium, to: constantinople, atBlock: 5},
	"ByzantiumToConstantinopleFixAt5": {
		from: byzantium, to: petersburg, atBlock: 5,
	},
	"ConstantinopleFixToIstanbulAt5": {from: petersburg, to: istanbul, atBlock: 5},
	"BerlinToLondonAt5":              {from: berlin, to: london, atBlock: 5},
	"ParisToShanghaiAtTime15k":       {from: paris, to: shanghai, atTime: 15_000},
	"ShanghaiToCancunAtTime15k":      {from: shanghai, to: cancun, atTime: 15_000},
	"CancunToPragueAtTime15k":        {from: cancun, to: prague, atTime: 15_000},
	"PragueToOsakaAtTime15k":         {from: prague, to: osaka, atTime: 15_000},
	"OsakaToBPO1AtTime15k":           {from: osaka, to: bpo1, atTime: 15_000},
	"BPO1ToBPO2AtTime15k":            {from: bpo1, to: bpo2, atTime: 15_000},
	"BPO2ToBPO3AtTime15k":            {from: bpo2, to: bpo3, atTime: 15_000},
	"BPO3ToBPO4AtTime15k":            {from: bpo3, to: bpo4, atTime: 15_000},
	"BPO4ToBPO5AtTime15k":            {from: bpo4, to: bpo5, atTime: 15_000},
	"BPO2ToAmsterdamAtTime15k":       {from: bpo2, to: amsterdam, atTime: 15_000},
}

// DefaultForks is the set the runners execute when EnvForks is unset:
// Cancun and every later fork, plus the transitions into them.
var DefaultForks = []string{
	"ShanghaiToCancunAtTime15k",
	"Cancun",
	"CancunToPragueAtTime15k",
	"Prague",
	"PragueToOsakaAtTime15k",
	"Osaka",
	"OsakaToBPO1AtTime15k",
	"BPO1",
	"BPO1ToBPO2AtTime15k",
	"BPO2",
	"BPO2ToBPO3AtTime15k",
	"BPO3",
	"BPO3ToBPO4AtTime15k",
	"BPO4",
	"BPO5",
}

// Networks returns every network name ChainConfig accepts, sorted.
func Networks() []string {
	out := make([]string, 0, len(forkByName)+len(transitions))
	for name := range forkByName {
		out = append(out, name)
	}
	for name := range transitions {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// UnsupportedForkError reports a fixture network with no mapping.
type UnsupportedForkError struct{ Name string }

func (e UnsupportedForkError) Error() string { return "unsupported fork " + e.Name }

// FixtureConfig is the optional "config" object EEST fixtures carry.
type FixtureConfig struct {
	Network      string                       `json:"network"`
	ChainID      string                       `json:"chainid"`
	BlobSchedule map[string]FixtureBlobConfig `json:"blobSchedule"`
}

// FixtureBlobConfig is one blobSchedule entry; values are hex quantities.
type FixtureBlobConfig struct {
	Target                string `json:"target"`
	Max                   string `json:"max"`
	BaseFeeUpdateFraction string `json:"baseFeeUpdateFraction"`
}

// ChainConfig returns the chain config for a fixture network. fixture may be
// nil; when present its chain id and blob schedule override the defaults.
func ChainConfig(network string, fixture *FixtureConfig) (*params.ChainConfig, error) {
	cfg := &params.ChainConfig{
		ChainID:                big.NewInt(1),
		Consensus:              params.EtHashConsensus,
		StateScheme:            params.StateCommitmentPresetEthereumMPT.StateScheme(),
		DepositContractAddress: params.EthereumMainnetChainConfig.DepositContractAddress,
		Ethash:                 new(params.EthashConfig),
	}
	if f, ok := forkByName[network]; ok {
		activate(cfg, f, 0, 0)
	} else if tr, ok := transitions[network]; ok {
		activate(cfg, tr.from, 0, 0)
		activate(cfg, tr.to, tr.atBlock, tr.atTime)
	} else {
		return nil, UnsupportedForkError{network}
	}
	if fixture != nil {
		if fixture.ChainID != "" {
			id, ok := new(big.Int).SetString(strings.TrimPrefix(fixture.ChainID, "0x"), 16)
			if !ok {
				return nil, fmt.Errorf("invalid fixture chainid %q", fixture.ChainID)
			}
			cfg.ChainID = id
		}
		if len(fixture.BlobSchedule) > 0 {
			schedule, err := blobSchedule(fixture.BlobSchedule)
			if err != nil {
				return nil, err
			}
			cfg.BlobSchedule = schedule
		}
	}
	return cfg, nil
}

// activate switches on every fork up to and including f. Forks already
// active keep their activation point, so a transition only moves the forks
// past its `from` fork.
func activate(cfg *params.ChainConfig, f fork, atBlock, atTime uint64) {
	blk := func(dst **big.Int) {
		if *dst == nil {
			*dst = new(big.Int).SetUint64(atBlock)
		}
	}
	tm := func(dst **big.Int) {
		if *dst == nil {
			*dst = new(big.Int).SetUint64(atTime)
		}
	}
	if f >= homestead {
		blk(&cfg.HomesteadBlock)
	}
	if f >= eip150 {
		blk(&cfg.TangerineWhistleBlock)
	}
	if f >= eip158 {
		blk(&cfg.SpuriousDragonBlock)
	}
	if f >= byzantium {
		blk(&cfg.ByzantiumBlock)
	}
	if f >= constantinople {
		blk(&cfg.ConstantinopleBlock)
	}
	if f >= petersburg {
		blk(&cfg.PetersburgBlock)
	}
	if f >= istanbul {
		blk(&cfg.IstanbulBlock)
	}
	if f >= muirGlacier {
		blk(&cfg.MuirGlacierBlock)
	}
	if f >= berlin {
		blk(&cfg.BerlinBlock)
	}
	if f >= london {
		blk(&cfg.LondonBlock)
	}
	if f >= arrowGlacier {
		blk(&cfg.ArrowGlacierBlock)
	}
	if f >= grayGlacier {
		blk(&cfg.GrayGlacierBlock)
	}
	if f >= paris {
		if cfg.TerminalTotalDifficulty == nil {
			cfg.TerminalTotalDifficulty = big.NewInt(0)
			cfg.TerminalTotalDifficultyPassed = true
		}
		blk(&cfg.MergeNetsplitBlock)
	}
	if f >= shanghai {
		tm(&cfg.ShanghaiTime)
	}
	if f >= cancun {
		tm(&cfg.CancunTime)
	}
	if f >= prague {
		tm(&cfg.PragueTime)
	}
	if f >= osaka {
		tm(&cfg.OsakaTime)
	}
	if f >= bpo1 {
		tm(&cfg.BPO1Time)
	}
	if f >= bpo2 {
		tm(&cfg.BPO2Time)
	}
	if f >= bpo3 && f != amsterdam {
		tm(&cfg.BPO3Time)
	}
	if f >= bpo4 && f != amsterdam {
		tm(&cfg.BPO4Time)
	}
	if f >= bpo5 && f != amsterdam {
		tm(&cfg.BPO5Time)
	}
	if f >= amsterdam {
		tm(&cfg.GlamsterdamTime)
	}
}

func blobSchedule(raw map[string]FixtureBlobConfig) (*params.BlobSchedule, error) {
	out := new(params.BlobSchedule)
	for name, entry := range raw {
		bc, err := entry.toParams()
		if err != nil {
			return nil, fmt.Errorf("blobSchedule %s: %w", name, err)
		}
		switch name {
		case "Cancun":
			out.Cancun = bc
		case "Prague":
			out.Prague = bc
		case "Osaka":
			out.Osaka = bc
		case "BPO1":
			out.BPO1 = bc
		case "BPO2":
			out.BPO2 = bc
		case "BPO3":
			out.BPO3 = bc
		case "BPO4":
			out.BPO4 = bc
		case "BPO5":
			out.BPO5 = bc
		case "Amsterdam":
			out.Amsterdam = bc
		default:
			return nil, fmt.Errorf("unknown blobSchedule fork %q", name)
		}
	}
	return out, nil
}

func (b FixtureBlobConfig) toParams() (*params.BlobConfig, error) {
	parse := func(s string) (*uint64, error) {
		if s == "" {
			return nil, nil
		}
		v, err := strconv.ParseUint(s, 0, 64)
		if err != nil {
			return nil, err
		}
		return &v, nil
	}
	target, err := parse(b.Target)
	if err != nil {
		return nil, err
	}
	maxBlobs, err := parse(b.Max)
	if err != nil {
		return nil, err
	}
	fraction, err := parse(b.BaseFeeUpdateFraction)
	if err != nil {
		return nil, err
	}
	return &params.BlobConfig{Target: target, Max: maxBlobs, BaseFeeUpdateFraction: fraction}, nil
}

// Dir returns the fixture root from EnvDir, or "" when the runners are off.
func Dir() string { return os.Getenv(EnvDir) }

// Workers returns the EnvWorkers parallelism (at least 1).
func Workers() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvWorkers)))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// SelectedForks returns the networks to run.
func SelectedForks() map[string]bool {
	names := DefaultForks
	if v := strings.TrimSpace(os.Getenv(EnvForks)); v != "" {
		names = strings.Split(v, ",")
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out[n] = true
		}
	}
	return out
}

// FixtureFiles lists the JSON files under dir/suite, sorted, after applying
// EnvFilter. Paths are returned with their suite-relative slash form.
func FixtureFiles(dir, suite string) (root string, rel []string, err error) {
	root = filepath.Join(dir, suite)
	filter := filepath.ToSlash(os.Getenv(EnvFilter))
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		r, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		r = filepath.ToSlash(r)
		if filter != "" && !strings.Contains(r, filter) {
			return nil
		}
		rel = append(rel, r)
		return nil
	})
	sort.Strings(rel)
	return root, rel, err
}
