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
//
// Per-block fork-rules resolution for the EVM and N42 extensions.
// Defines the Rules struct (IsHomestead..IsGlamsterdam, IsPQPrecompiles,
// IsContentStore, IsAIInference, IsRandomness) and ChainConfig.Rules /
// RulesWithTimestamp which select the active fork set from block number
// and header timestamp. Used by the interpreter and gas tables.

package params

import (
	"math/big"
	"sync"
)

// rulesCacheKey memoises *Rules per (config, num, timestamp). Rules is
// documented as read-only ("one time interface, shouldn't be used
// between transition phases" — see Rules type doc), so sharing a
// single instance across callers is safe.
type rulesCacheKey struct {
	cfg *ChainConfig
	num uint64
	ts  uint64
}

// rulesCacheCap bounds the cache. Replay is monotone — old (num, ts)
// keys are never queried again — so the simplest correct eviction is a
// full clear when the map crosses the cap. Sized to comfortably hold
// every active worker's current block plus margin: 32 workers × ~30
// blocks of look-ahead × 4 keys per block << 4096.
const rulesCacheCap = 4096

var (
	rulesCacheMu sync.RWMutex
	rulesCache   = make(map[rulesCacheKey]*Rules, rulesCacheCap)
)

// Rules wraps ChainConfig and is merely syntactic sugar or can be used for functions
// that do not have or require information about the block.
//
// Rules is a one time interface meaning that it shouldn't be used in between transition
// phases.
type Rules struct {
	ChainID                                                     *big.Int
	IsHomestead, IsTangerineWhistle, IsSpuriousDragon           bool
	IsByzantium, IsConstantinople, IsPetersburg, IsIstanbul     bool
	IsBerlin, IsLondon, IsParis, IsShanghai, IsCancun, IsPrague bool
	IsPectra, IsOsaka, IsFusaka, IsGlamsterdam                  bool // Pectra: EIP-7702, Osaka: EOF, Fusaka: Native AA, Glamsterdam: EIP-7904
	IsNano, IsMoran                                             bool
	IsEip1559FeeCollector                                       bool
	IsParlia, IsStarknet, IsAura, IsBeijing                     bool
	IsPQPrecompiles                                             bool // N42 extension: post-quantum precompiles enabled
	IsContentStore                                              bool // N42 extension: content-addressed storage precompile
	IsAIInference                                               bool // N42 extension: AI inference precompile
	IsRandomness                                                bool // N42 extension: on-chain randomness beacon precompile
	IsBAL                                                       bool // N42 extension: EIP-7928 block-level access list
	IsMobileAnchor                                              bool // N42 native chain: mobile-attestation accumulator root anchor (NOT eth-el)
}

// Rules ensures c's ChainID is not nil.
// DEPRECATED: Use RulesWithTimestamp instead for timestamp-based forks (Prague+)
func (c *ChainConfig) Rules(num uint64) *Rules {
	return c.RulesWithTimestamp(num, num)
}

// RulesWithTimestamp returns the chain rules for the given block number and timestamp.
// This correctly handles both block-based forks (pre-Prague) and timestamp-based forks (Prague+).
//
// The result is memoised per (config pointer, num, timestamp). Rules is
// read-only by contract; callers MUST NOT mutate fields on the
// returned struct.
func (c *ChainConfig) RulesWithTimestamp(num uint64, timestamp uint64) *Rules {
	key := rulesCacheKey{cfg: c, num: num, ts: timestamp}
	rulesCacheMu.RLock()
	r, ok := rulesCache[key]
	rulesCacheMu.RUnlock()
	if ok {
		return r
	}
	chainID := c.ChainID
	if chainID == nil {
		chainID = new(big.Int)
	}
	rules := &Rules{
		// Share the chain config's ChainID pointer instead of copying.
		// ChainConfig is treated as immutable across the codebase
		// (only pointer receivers, no mutator methods); the previous
		// new(big.Int).Set was a defensive copy that allocated 619 MB
		// per 30 s of replay for no real benefit.
		ChainID:               chainID,
		IsHomestead:           c.IsHomestead(num),
		IsTangerineWhistle:    c.IsTangerineWhistle(num),
		IsSpuriousDragon:      c.IsSpuriousDragon(num),
		IsByzantium:           c.IsByzantium(num),
		IsConstantinople:      c.IsConstantinople(num),
		IsPetersburg:          c.IsPetersburg(num),
		IsIstanbul:            c.IsIstanbul(num),
		IsBerlin:              c.IsBerlin(num),
		IsLondon:              c.IsLondon(num),
		IsParis:               c.IsParisAt(num, timestamp),
		IsShanghai:            c.IsShanghaiAt(num, timestamp),
		IsCancun:              c.IsCancunAt(num, timestamp),
		IsPrague:              c.IsPrague(timestamp),
		IsPectra:              c.IsPectra(timestamp),
		IsOsaka:               c.IsOsaka(timestamp),
		IsFusaka:              c.IsFusaka(timestamp),
		IsGlamsterdam:         c.IsGlamsterdam(timestamp),
		IsNano:                c.IsNano(num),
		IsMoran:               c.IsMoran(num),
		IsEip1559FeeCollector: c.IsEip1559FeeCollector(num),
		IsParlia:              c.UsesParliaRules(),
		IsAura:                c.UsesAuraRules(),
		IsBeijing:             c.IsBeijing(num),
		IsPQPrecompiles:       c.IsPQPrecompiles(timestamp),
		IsContentStore:        c.IsContentStore(timestamp),
		IsAIInference:         c.IsAIInference(timestamp),
		IsRandomness:          c.IsRandomness(timestamp),
		IsBAL:                 c.IsBAL(timestamp),
		IsMobileAnchor:        c.IsMobileAnchor(timestamp),
	}
	rules.applyForkInheritance()
	rulesCacheMu.Lock()
	if len(rulesCache) >= rulesCacheCap {
		// Replay is monotone; old keys won't be queried again.
		rulesCache = make(map[rulesCacheKey]*Rules, rulesCacheCap)
	}
	rulesCache[key] = rules
	rulesCacheMu.Unlock()
	return rules
}

func (r *Rules) applyForkInheritance() {
	if r == nil {
		return
	}
	// Walk the fork chain top-down: each fork implies all earlier forks.
	if r.IsGlamsterdam {
		r.IsFusaka = true
	}
	if r.IsFusaka {
		r.IsOsaka = true
	}
	if r.IsOsaka {
		r.IsPectra = true
	}
	if r.IsPectra {
		r.IsPrague = true
	}
	if r.IsPrague {
		r.IsCancun = true
	}
	if r.IsCancun {
		r.IsShanghai = true
	}
	if r.IsShanghai {
		r.IsParis = true
		r.IsLondon = true
		r.IsBerlin = true
		r.IsIstanbul = true
		r.IsPetersburg = true
		r.IsConstantinople = true
		r.IsByzantium = true
		r.IsSpuriousDragon = true
		r.IsTangerineWhistle = true
		r.IsHomestead = true
	}
}

// ---------------------------------------------------------------------------
// Fork activation predicates (block-based)
// ---------------------------------------------------------------------------

// IsHomestead returns whether num is either equal to the homestead block or greater.
func (c *ChainConfig) IsHomestead(num uint64) bool {
	return isForked(c.HomesteadBlock, num)
}

// IsDAOFork returns whether num is either equal to the DAO fork block or greater.
func (c *ChainConfig) IsDAOFork(num uint64) bool {
	return isForked(c.DAOForkBlock, num)
}

// IsTangerineWhistle returns whether num is either equal to the Tangerine Whistle (EIP150) fork block or greater.
func (c *ChainConfig) IsTangerineWhistle(num uint64) bool {
	return isForked(c.TangerineWhistleBlock, num)
}

// IsSpuriousDragon returns whether num is either equal to the Spurious Dragon fork block or greater.
func (c *ChainConfig) IsSpuriousDragon(num uint64) bool {
	return isForked(c.SpuriousDragonBlock, num)
}

// IsByzantium returns whether num is either equal to the Byzantium fork block or greater.
func (c *ChainConfig) IsByzantium(num uint64) bool {
	return isForked(c.ByzantiumBlock, num)
}

// IsConstantinople returns whether num is either equal to the Constantinople fork block or greater.
func (c *ChainConfig) IsConstantinople(num uint64) bool {
	return isForked(c.ConstantinopleBlock, num)
}

// IsPetersburg returns whether num is either
// - equal to or greater than the PetersburgBlock fork block,
// - OR is nil, and Constantinople is active
func (c *ChainConfig) IsPetersburg(num uint64) bool {
	return isForked(c.PetersburgBlock, num) || c.PetersburgBlock == nil && isForked(c.ConstantinopleBlock, num)
}

// IsIstanbul returns whether num is either equal to the Istanbul fork block or greater.
func (c *ChainConfig) IsIstanbul(num uint64) bool {
	return isForked(c.IstanbulBlock, num)
}

// IsMuirGlacier returns whether num is either equal to the Muir Glacier (EIP-2384) fork block or greater.
func (c *ChainConfig) IsMuirGlacier(num uint64) bool {
	return isForked(c.MuirGlacierBlock, num)
}

// IsBerlin returns whether num is either equal to the Berlin fork block or greater.
func (c *ChainConfig) IsBerlin(num uint64) bool {
	return isForked(c.BerlinBlock, num)
}

// IsLondon returns whether num is either equal to the London fork block or greater.
func (c *ChainConfig) IsLondon(num uint64) bool {
	return isForked(c.LondonBlock, num)
}

// IsParis returns whether num is either equal to the Paris/Merge netsplit block or greater.
func (c *ChainConfig) IsParis(num uint64) bool {
	return isForked(c.MergeNetsplitBlock, num)
}

// IsParisAt returns whether Paris-or-later semantics are active at the given block number or timestamp.
func (c *ChainConfig) IsParisAt(num uint64, time uint64) bool {
	return c.IsParis(num) || c.IsShanghaiAt(num, time)
}

// IsArrowGlacier returns whether num is either equal to the Arrow Glacier (EIP-4345) fork block or greater.
func (c *ChainConfig) IsArrowGlacier(num uint64) bool {
	return isForked(c.ArrowGlacierBlock, num)
}

// IsGrayGlacier returns whether num is either equal to the Gray Glacier (EIP-5133) fork block or greater.
func (c *ChainConfig) IsGrayGlacier(num uint64) bool {
	return isForked(c.GrayGlacierBlock, num)
}

// IsShanghai returns whether num is either equal to the Shanghai fork block or greater.
func (c *ChainConfig) IsShanghai(num uint64) bool {
	return isForked(c.ShanghaiBlock, num)
}

// IsShanghaiTime returns whether time is either equal to the Shanghai fork time or greater.
func (c *ChainConfig) IsShanghaiTime(time uint64) bool {
	return isForked(c.ShanghaiTime, time)
}

// IsShanghaiAt returns whether Shanghai is active at the given block number or timestamp.
func (c *ChainConfig) IsShanghaiAt(num uint64, time uint64) bool {
	return c.IsShanghai(num) || c.IsShanghaiTime(time) || c.IsCancunAt(num, time)
}

// IsCancun returns whether num is either equal to the Cancun fork block or greater.
func (c *ChainConfig) IsCancun(num uint64) bool {
	return isForked(c.CancunBlock, num)
}

// IsCancunTime returns whether time is either equal to the Cancun fork time or greater.
func (c *ChainConfig) IsCancunTime(time uint64) bool {
	return isForked(c.CancunTime, time)
}

// IsCancunAt returns whether Cancun-or-later semantics are active at the given block number or timestamp.
func (c *ChainConfig) IsCancunAt(num uint64, time uint64) bool {
	return c.IsCancun(num) ||
		c.IsCancunTime(time) ||
		c.IsPrague(time) ||
		c.IsPectra(time) ||
		c.IsOsaka(time) ||
		c.IsFusaka(time) ||
		c.IsGlamsterdam(time)
}

// IsNano returns whether num is either equal to the Nano fork block or greater.
func (c *ChainConfig) IsNano(num uint64) bool {
	return isForked(c.NanoBlock, num)
}

// IsMoran returns whether num is either equal to the Moran fork block or greater.
func (c *ChainConfig) IsMoran(num uint64) bool {
	return isForked(c.MoranBlock, num)
}

// IsBeijing returns whether num is either equal to the Beijing fork block or greater.
func (c *ChainConfig) IsBeijing(num uint64) bool {
	return isForked(c.BeijingBlock, num)
}

// ---------------------------------------------------------------------------
// Fork activation predicates (timestamp-based)
// ---------------------------------------------------------------------------

// IsPrague returns whether time is either equal to the Prague fork time or greater.
func (c *ChainConfig) IsPrague(time uint64) bool {
	return isForked(c.PragueTime, time)
}

// IsPectra returns whether time is either equal to the Pectra fork time or greater.
// Pectra includes EIP-7702 (Account Abstraction), EIP-2537 (BLS), EIP-2935 (Historical hashes)
func (c *ChainConfig) IsPectra(time uint64) bool {
	return isForked(c.PectraTime, time)
}

// IsOsaka returns whether time is either equal to the Osaka fork time or greater.
func (c *ChainConfig) IsOsaka(time uint64) bool {
	return isForked(c.OsakaTime, time)
}

// IsFusaka returns whether time is either equal to the Fusaka fork time or greater.
// Fusaka enables native account abstraction with protocol-level transaction validation.
func (c *ChainConfig) IsFusaka(time uint64) bool {
	return isForked(c.FusakaTime, time)
}

// IsGlamsterdam returns whether time is either equal to the Glamsterdam fork time or greater.
// Glamsterdam implements EIP-7904 gas repricing, reducing simple transfer gas from 21000 to 4500.
func (c *ChainConfig) IsGlamsterdam(time uint64) bool {
	return isForked(c.GlamsterdamTime, time)
}

// IsPQPrecompiles returns whether time is at or past the PQ precompiles activation timestamp.
// This is an N42-specific extension independent of standard Ethereum forks.
func (c *ChainConfig) IsPQPrecompiles(time uint64) bool {
	return isForked(c.PQPrecompilesTime, time)
}

// IsContentStore returns whether time is at or past the content-addressed storage precompile activation.
func (c *ChainConfig) IsContentStore(time uint64) bool {
	return isForked(c.ContentStoreTime, time)
}

// IsAIInference returns whether time is at or past the AI inference precompile activation.
func (c *ChainConfig) IsAIInference(time uint64) bool {
	return isForked(c.AIInferenceTime, time)
}

// IsRandomness returns whether time is at or past the on-chain randomness beacon precompile activation.
func (c *ChainConfig) IsRandomness(time uint64) bool {
	return isForked(c.RandomnessTime, time)
}

// IsLtHash returns whether time is at or past the LtHash lattice state digest activation.
func (c *ChainConfig) IsLtHash(time uint64) bool {
	return isForked(c.LtHashTime, time)
}

// IsBAL returns whether time is at or past the EIP-7928 block-level access list
// activation. When enabled, blocks bind the canonical BAL hash into the header.
func (c *ChainConfig) IsBAL(time uint64) bool {
	return isForked(c.BALTime, time)
}

// IsMobileAnchor returns whether time is at or past the mobile-attestation
// accumulator anchor activation (n42 native chain only; eth-el chainspecs
// leave MobileAnchorTime nil, so this is always false there). When enabled,
// the n42 build/import paths bind the mobile-registry root into the header and
// write it to a ring-buffer system contract.
func (c *ChainConfig) IsMobileAnchor(time uint64) bool {
	return isForked(c.MobileAnchorTime, time)
}

// IsEip1559FeeCollector returns whether num has reached the EIP-1559 fee collector transition.
func (c *ChainConfig) IsEip1559FeeCollector(num uint64) bool {
	return c.Eip1559FeeCollector != nil && isForked(c.Eip1559FeeCollectorTransition, num)
}

// ---------------------------------------------------------------------------
// Fork helpers
// ---------------------------------------------------------------------------

// isForked returns whether a fork scheduled at block s is active at the given head block.
func isForked(s *big.Int, head uint64) bool {
	if s == nil {
		return false
	}
	// Fast path: every real chain config's fork values fit in uint64.
	// Avoids the per-call new(big.Int).SetUint64 alloc that previously
	// dominated pprof under parallel witness replay.
	if s.IsUint64() {
		return s.Uint64() <= head
	}
	// Slow path: s is either negative (fork retroactively active) or
	// > 2^64 (effectively never relative to a uint64 head).
	return s.Sign() <= 0
}

// isForkIncompatible returns true if a fork scheduled at s1 cannot be rescheduled to
// block s2 because head is already past the fork.
func isForkIncompatible(s1, s2 *big.Int, head uint64) bool {
	return (isForked(s1, head) || isForked(s2, head)) && !configNumEqual(s1, s2)
}

// configNumEqual returns whether two big.Int pointers represent the same value.
func configNumEqual(x, y *big.Int) bool {
	if x == nil {
		return y == nil
	}
	if y == nil {
		return false
	}
	return x.Cmp(y) == 0
}
