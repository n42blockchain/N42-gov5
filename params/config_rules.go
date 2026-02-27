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

package params

import "math/big"

// Rules wraps ChainConfig and is merely syntactic sugar or can be used for functions
// that do not have or require information about the block.
//
// Rules is a one time interface meaning that it shouldn't be used in between transition
// phases.
type Rules struct {
	ChainID                                                 *big.Int
	IsHomestead, IsTangerineWhistle, IsSpuriousDragon       bool
	IsByzantium, IsConstantinople, IsPetersburg, IsIstanbul bool
	IsBerlin, IsLondon, IsShanghai, IsCancun, IsPrague      bool
	IsPectra, IsOsaka, IsFusaka                             bool // Pectra: EIP-7702, Osaka: EOF, Fusaka: Native AA
	IsNano, IsMoran                                         bool
	IsEip1559FeeCollector                                   bool
	IsParlia, IsStarknet, IsAura, IsBeijing                 bool
}

// Rules ensures c's ChainID is not nil.
// DEPRECATED: Use RulesWithTimestamp instead for timestamp-based forks (Prague+)
func (c *ChainConfig) Rules(num uint64) *Rules {
	return c.RulesWithTimestamp(num, num)
}

// RulesWithTimestamp returns the chain rules for the given block number and timestamp.
// This correctly handles both block-based forks (pre-Prague) and timestamp-based forks (Prague+).
func (c *ChainConfig) RulesWithTimestamp(num uint64, timestamp uint64) *Rules {
	chainID := c.ChainID
	if chainID == nil {
		chainID = new(big.Int)
	}
	return &Rules{
		ChainID:               new(big.Int).Set(chainID),
		IsHomestead:           c.IsHomestead(num),
		IsTangerineWhistle:    c.IsTangerineWhistle(num),
		IsSpuriousDragon:      c.IsSpuriousDragon(num),
		IsByzantium:           c.IsByzantium(num),
		IsConstantinople:      c.IsConstantinople(num),
		IsPetersburg:          c.IsPetersburg(num),
		IsIstanbul:            c.IsIstanbul(num),
		IsBerlin:              c.IsBerlin(num),
		IsLondon:              c.IsLondon(num),
		IsShanghai:            c.IsShanghai(num),
		IsCancun:              c.IsCancun(num),
		IsPrague:              c.IsPrague(timestamp),
		IsPectra:              c.IsPectra(timestamp),
		IsOsaka:               c.IsOsaka(timestamp),
		IsFusaka:              c.IsFusaka(timestamp),
		IsNano:                c.IsNano(num),
		IsMoran:               c.IsMoran(num),
		IsEip1559FeeCollector: c.IsEip1559FeeCollector(num),
		IsParlia:              c.Parlia != nil,
		IsAura:                c.Aura != nil,
		IsBeijing:             c.IsBeijing(num),
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

// IsCancun returns whether num is either equal to the Cancun fork block or greater.
func (c *ChainConfig) IsCancun(num uint64) bool {
	return isForked(c.CancunBlock, num)
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
	return s.Uint64() <= head
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
