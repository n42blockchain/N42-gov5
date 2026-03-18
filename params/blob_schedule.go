package params

import (
	"math/big"

	"github.com/holiman/uint256"
)

type BlobConfig struct {
	Target                *uint64 `json:"target,omitempty"`
	Max                   *uint64 `json:"max,omitempty"`
	BaseFeeUpdateFraction *uint64 `json:"baseFeeUpdateFraction,omitempty"`
}

type BlobSchedule struct {
	Cancun *BlobConfig `json:"Cancun,omitempty"`
	Prague *BlobConfig `json:"Prague,omitempty"`
	Osaka  *BlobConfig `json:"Osaka,omitempty"`
	BPO1   *BlobConfig `json:"BPO1,omitempty"`
	BPO2   *BlobConfig `json:"BPO2,omitempty"`
	BPO3   *BlobConfig `json:"BPO3,omitempty"`
	BPO4   *BlobConfig `json:"BPO4,omitempty"`
	BPO5   *BlobConfig `json:"BPO5,omitempty"`
}

var defaultBlobSchedule = map[string]BlobConfig{
	"cancun": {Target: uint64Ptr(3), Max: uint64Ptr(6), BaseFeeUpdateFraction: uint64Ptr(BlobTxBlobGaspriceUpdateFraction)},
	"prague": {Target: uint64Ptr(6), Max: uint64Ptr(9), BaseFeeUpdateFraction: uint64Ptr(5007716)},
	"osaka":  {Target: uint64Ptr(6), Max: uint64Ptr(9), BaseFeeUpdateFraction: uint64Ptr(5007716)},
	"bpo1":   {Target: uint64Ptr(10), Max: uint64Ptr(15), BaseFeeUpdateFraction: uint64Ptr(8346193)},
	"bpo2":   {Target: uint64Ptr(14), Max: uint64Ptr(21), BaseFeeUpdateFraction: uint64Ptr(11684671)},
	"bpo3":   {Target: uint64Ptr(21), Max: uint64Ptr(32), BaseFeeUpdateFraction: uint64Ptr(20609697)},
	"bpo4":   {Target: uint64Ptr(14), Max: uint64Ptr(21), BaseFeeUpdateFraction: uint64Ptr(13739630)},
	"bpo5":   {Target: uint64Ptr(14), Max: uint64Ptr(21), BaseFeeUpdateFraction: uint64Ptr(13739630)},
}

func uint64Ptr(v uint64) *uint64 {
	return &v
}

func isForkedTime(s *big.Int, t uint64) bool {
	if s == nil {
		return false
	}
	return s.Uint64() <= t
}

func (c *ChainConfig) IsBPO1(time uint64) bool {
	return isForkedTime(c.BPO1Time, time)
}

func (c *ChainConfig) IsBPO2(time uint64) bool {
	return isForkedTime(c.BPO2Time, time)
}

func (c *ChainConfig) IsBPO3(time uint64) bool {
	return isForkedTime(c.BPO3Time, time)
}

func (c *ChainConfig) IsBPO4(time uint64) bool {
	return isForkedTime(c.BPO4Time, time)
}

func (c *ChainConfig) IsBPO5(time uint64) bool {
	return isForkedTime(c.BPO5Time, time)
}

func (c *ChainConfig) blobConfigForTimestamp(time uint64) *BlobConfig {
	if c == nil || c.BlobSchedule == nil {
		return nil
	}
	switch {
	case c.IsBPO5(time) && c.BlobSchedule.BPO5 != nil:
		return c.BlobSchedule.BPO5
	case c.IsBPO4(time) && c.BlobSchedule.BPO4 != nil:
		return c.BlobSchedule.BPO4
	case c.IsBPO3(time) && c.BlobSchedule.BPO3 != nil:
		return c.BlobSchedule.BPO3
	case c.IsBPO2(time) && c.BlobSchedule.BPO2 != nil:
		return c.BlobSchedule.BPO2
	case c.IsBPO1(time) && c.BlobSchedule.BPO1 != nil:
		return c.BlobSchedule.BPO1
	case c.IsOsaka(time) && c.BlobSchedule.Osaka != nil:
		return c.BlobSchedule.Osaka
	case c.IsPrague(time) && c.BlobSchedule.Prague != nil:
		return c.BlobSchedule.Prague
	case c.BlobSchedule.Cancun != nil:
		return c.BlobSchedule.Cancun
	default:
		return nil
	}
}

func (c *ChainConfig) defaultBlobConfigName(time uint64) string {
	switch {
	case c != nil && c.IsBPO5(time):
		return "bpo5"
	case c != nil && c.IsBPO4(time):
		return "bpo4"
	case c != nil && c.IsBPO3(time):
		return "bpo3"
	case c != nil && c.IsBPO2(time):
		return "bpo2"
	case c != nil && c.IsBPO1(time):
		return "bpo1"
	case c != nil && c.IsOsaka(time):
		return "osaka"
	case c != nil && c.IsPrague(time):
		return "prague"
	default:
		return "cancun"
	}
}

func (c *ChainConfig) BlobTargetBlobsPerBlock(time uint64) uint64 {
	if cfg := c.blobConfigForTimestamp(time); cfg != nil && cfg.Target != nil {
		return *cfg.Target
	}
	return *defaultBlobSchedule[c.defaultBlobConfigName(time)].Target
}

func (c *ChainConfig) BlobMaxBlobsPerBlock(time uint64) uint64 {
	if cfg := c.blobConfigForTimestamp(time); cfg != nil && cfg.Max != nil {
		return *cfg.Max
	}
	return *defaultBlobSchedule[c.defaultBlobConfigName(time)].Max
}

func (c *ChainConfig) BlobBaseFeeUpdateFraction(time uint64) uint64 {
	if cfg := c.blobConfigForTimestamp(time); cfg != nil && cfg.BaseFeeUpdateFraction != nil {
		return *cfg.BaseFeeUpdateFraction
	}
	return *defaultBlobSchedule[c.defaultBlobConfigName(time)].BaseFeeUpdateFraction
}

func (c *ChainConfig) BlobGasPerBlob(time uint64) uint64 {
	return BlobTxBlobGasPerBlob
}

func (c *ChainConfig) BlobTargetGasPerBlock(time uint64) uint64 {
	return c.BlobTargetBlobsPerBlock(time) * c.BlobGasPerBlob(time)
}

func (c *ChainConfig) BlobMaxGasPerBlock(time uint64) uint64 {
	return c.BlobMaxBlobsPerBlock(time) * c.BlobGasPerBlob(time)
}

func (c *ChainConfig) CalcExcessBlobGas(parentExcessBlobGas, parentBlobGasUsed, time uint64) uint64 {
	excessBlobGas := parentExcessBlobGas + parentBlobGasUsed
	targetBlobGas := c.BlobTargetGasPerBlock(time)
	if excessBlobGas < targetBlobGas {
		return 0
	}
	return excessBlobGas - targetBlobGas
}

func (c *ChainConfig) CalcBlobFee(excessBlobGas, time uint64) *uint256.Int {
	return fakeExponentialBlobFee(
		uint256.NewInt(BlobTxMinBlobGasprice),
		uint256.NewInt(excessBlobGas),
		uint256.NewInt(c.BlobBaseFeeUpdateFraction(time)),
	)
}

func fakeExponentialBlobFee(factor, numerator, denominator *uint256.Int) *uint256.Int {
	i := uint256.NewInt(1)
	output := uint256.NewInt(0)
	numeratorAccum := new(uint256.Int).Mul(factor, denominator)

	for {
		output.Add(output, numeratorAccum)
		numeratorAccum.Mul(numeratorAccum, numerator)
		numeratorAccum.Div(numeratorAccum, denominator)
		numeratorAccum.Div(numeratorAccum, i)
		i.AddUint64(i, 1)
		if numeratorAccum.IsZero() {
			break
		}
	}

	return output.Div(output, denominator)
}
