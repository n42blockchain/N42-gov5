package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/forkid"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/params"
)

// ethConfigBlobSchedule is the EIP-7910 blobSchedule object.
type ethConfigBlobSchedule struct {
	Target                uint64 `json:"target"`
	Max                   uint64 `json:"max"`
	BaseFeeUpdateFraction uint64 `json:"baseFeeUpdateFraction"`
}

// ethForkConfig is the EIP-7910 configuration of one fork.
type ethForkConfig struct {
	ActivationTime  uint64                 `json:"activationTime"`
	BlobSchedule    *ethConfigBlobSchedule `json:"blobSchedule"`
	ChainID         *hexutil.Big           `json:"chainId"`
	ForkID          hexutil.Bytes          `json:"forkId"`
	Precompiles     map[string]string      `json:"precompiles"`
	SystemContracts map[string]string      `json:"systemContracts"`
}

// ethConfigResponse is the eth_config result.
type ethConfigResponse struct {
	Current *ethForkConfig `json:"current"`
	Next    *ethForkConfig `json:"next"`
	Last    *ethForkConfig `json:"last"`
}

// ethConfigForks are the timestamp-scheduled Ethereum forks EIP-7910
// describes, in activation order. Glamsterdam is left out until its N42
// definition matches Amsterdam (it is unscheduled on every Ethereum chain).
var ethConfigForks = []struct {
	name string
	time func(*params.ChainConfig) *big.Int
}{
	{"shanghai", func(c *params.ChainConfig) *big.Int { return c.ShanghaiTime }},
	{"cancun", func(c *params.ChainConfig) *big.Int { return c.CancunTime }},
	{"prague", func(c *params.ChainConfig) *big.Int { return c.PragueTime }},
	{"osaka", func(c *params.ChainConfig) *big.Int { return c.OsakaTime }},
	{"bpo1", func(c *params.ChainConfig) *big.Int { return c.BPO1Time }},
	{"bpo2", func(c *params.ChainConfig) *big.Int { return c.BPO2Time }},
	{"bpo3", func(c *params.ChainConfig) *big.Int { return c.BPO3Time }},
	{"bpo4", func(c *params.ChainConfig) *big.Int { return c.BPO4Time }},
	{"bpo5", func(c *params.ChainConfig) *big.Int { return c.BPO5Time }},
}

// ethConfigPrecompileNames are the EIP-7910 precompile names (the names geth
// reports) by address.
var ethConfigPrecompileNames = map[types.Address]string{
	types.BytesToAddress([]byte{0x01}):       "ECREC",
	types.BytesToAddress([]byte{0x02}):       "SHA256",
	types.BytesToAddress([]byte{0x03}):       "RIPEMD160",
	types.BytesToAddress([]byte{0x04}):       "ID",
	types.BytesToAddress([]byte{0x05}):       "MODEXP",
	types.BytesToAddress([]byte{0x06}):       "BN254_ADD",
	types.BytesToAddress([]byte{0x07}):       "BN254_MUL",
	types.BytesToAddress([]byte{0x08}):       "BN254_PAIRING",
	types.BytesToAddress([]byte{0x09}):       "BLAKE2F",
	types.BytesToAddress([]byte{0x0a}):       "KZG_POINT_EVALUATION",
	types.BytesToAddress([]byte{0x0b}):       "BLS12_G1ADD",
	types.BytesToAddress([]byte{0x0c}):       "BLS12_G1MSM",
	types.BytesToAddress([]byte{0x0d}):       "BLS12_G2ADD",
	types.BytesToAddress([]byte{0x0e}):       "BLS12_G2MSM",
	types.BytesToAddress([]byte{0x0f}):       "BLS12_PAIRING_CHECK",
	types.BytesToAddress([]byte{0x10}):       "BLS12_MAP_FP_TO_G1",
	types.BytesToAddress([]byte{0x11}):       "BLS12_MAP_FP2_TO_G2",
	types.BytesToAddress([]byte{0x01, 0x00}): "P256VERIFY",
}

var errEthConfigNativeChain = errors.New("eth_config is only defined for Ethereum fork schedules")

// Config implements the EIP-7910 eth_config method: the current, next and
// last scheduled fork configurations of the chain.
//
// It is answered for Ethereum chain configs only. N42 native chains
// (HotStuff/APoS) schedule their own forks (Pectra, Fusaka and the N42
// precompiles) that the EIP-7910 fork model cannot express, so they get an
// error rather than a misleading answer.
func (api *BlockChainAPI) Config(ctx context.Context) (*ethConfigResponse, error) {
	if api == nil || api.api == nil {
		return nil, errors.New("eth_config: no backend")
	}
	cfg := api.api.GetChainConfig()
	if cfg == nil {
		return nil, errors.New("eth_config: no chain config")
	}
	bc := api.api.BlockChain()
	if bc == nil {
		return nil, errors.New("eth_config: no chain")
	}
	genesis, ok := bc.GetHeaderByNumber(uint256.NewInt(0)).(*block.Header)
	if !ok || genesis == nil {
		return nil, errors.New("eth_config: unable to load genesis")
	}
	// The head time selects the current fork. A datadir seeded from a
	// snapshot may not hold its head block yet; fall back to the clock.
	now := uint64(time.Now().Unix())
	if head := blockHeader(bc.CurrentBlock()); head != nil {
		now = head.Time
	}
	return ethConfigAt(cfg, rpcHeaderHash(genesis, cfg), genesis.Time, now)
}

// ethConfigAt assembles the eth_config answer for a chain whose genesis has
// the given hash and time, as seen at time now. It mirrors geth's
// BlockChainAPI.Config.
func ethConfigAt(cfg *params.ChainConfig, genesisHash types.Hash, genesisTime, now uint64) (*ethConfigResponse, error) {
	if cfg.Consensus == params.HotStuffConsensus || cfg.Consensus == params.AposConsensu {
		return nil, errEthConfigNativeChain
	}
	// Index of the latest fork active at t; -1 before Shanghai (Paris).
	latest := func(t uint64) int {
		idx := -1
		for i, fork := range ethConfigForks {
			if ts := fork.time(cfg); ts != nil && ts.Uint64() <= t {
				idx = i
			}
		}
		return idx
	}
	activation := func(idx int) *uint64 {
		if idx < 0 {
			return nil
		}
		ts := ethConfigForks[idx].time(cfg)
		if ts == nil {
			return nil
		}
		t := ts.Uint64()
		return &t
	}
	assemble := func(idx int) (*ethForkConfig, error) {
		ts := activation(idx)
		if ts == nil {
			return nil, nil
		}
		return ethForkConfigAt(cfg, genesisHash, genesisTime, *ts)
	}

	current, last := latest(now), latest(math.MaxUint64)
	// Optional forks (the BPOs) may be unscheduled: the next fork is the
	// first one after current with an activation time.
	next := -1
	for i := current + 1; i <= last; i++ {
		if activation(i) != nil {
			next = i
			break
		}
	}
	var (
		resp ethConfigResponse
		err  error
	)
	if resp.Current, err = assemble(current); err != nil {
		return nil, err
	}
	if resp.Next, err = assemble(next); err != nil {
		return nil, err
	}
	// Last is only reported when a future fork is scheduled.
	if resp.Next != nil {
		if resp.Last, err = assemble(last); err != nil {
			return nil, err
		}
	}
	return &resp, nil
}

// ethForkConfigAt describes the fork active at activation time t.
func ethForkConfigAt(cfg *params.ChainConfig, genesisHash types.Hash, genesisTime, t uint64) (*ethForkConfig, error) {
	// Every block fork has passed at any timestamp-scheduled fork; geth
	// evaluates the rules at the London block.
	var london uint64
	if cfg.LondonBlock != nil {
		london = cfg.LondonBlock.Uint64()
	}
	rules := cfg.RulesWithTimestamp(london, t)

	precompiles := make(map[string]string)
	for _, addr := range vm.ActivePrecompiles(rules) {
		name, ok := ethConfigPrecompileNames[addr]
		if !ok {
			return nil, fmt.Errorf("eth_config: precompile %s has no EIP-7910 name", addr.Hex())
		}
		precompiles[name] = hexutil.Encode(addr[:])
	}

	system := make(map[string]string)
	if rules.IsCancun {
		system["BEACON_ROOTS_ADDRESS"] = hexutil.Encode(params.BeaconRootsAddress[:])
	}
	if rules.IsPrague {
		var deposit types.Address
		if cfg.DepositContractAddress != nil {
			deposit = *cfg.DepositContractAddress
		}
		system["CONSOLIDATION_REQUEST_PREDEPLOY_ADDRESS"] = hexutil.Encode(vm.ConsolidationRequestsAddress[:])
		system["DEPOSIT_CONTRACT_ADDRESS"] = hexutil.Encode(deposit[:])
		system["HISTORY_STORAGE_ADDRESS"] = hexutil.Encode(vm.HistoryStorageAddress[:])
		system["WITHDRAWAL_REQUEST_PREDEPLOY_ADDRESS"] = hexutil.Encode(vm.WithdrawalRequestsAddress[:])
	}

	var blobs *ethConfigBlobSchedule
	if rules.IsCancun {
		blobs = &ethConfigBlobSchedule{
			Target:                cfg.BlobTargetBlobsPerBlock(t),
			Max:                   cfg.BlobMaxBlobsPerBlock(t),
			BaseFeeUpdateFraction: cfg.BlobBaseFeeUpdateFraction(t),
		}
	}

	// A fork active at genesis reports activation time 0.
	activation := t
	if genesisTime >= t {
		activation = 0
	}
	id := forkid.NewID(cfg, genesisHash, genesisTime, math.MaxUint64, t)
	return &ethForkConfig{
		ActivationTime:  activation,
		BlobSchedule:    blobs,
		ChainID:         (*hexutil.Big)(cfg.ChainID),
		ForkID:          id.Hash[:],
		Precompiles:     precompiles,
		SystemContracts: system,
	}, nil
}
