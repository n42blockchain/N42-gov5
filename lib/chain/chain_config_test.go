/*
   Copyright 2023 The Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package chain

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/n42blockchain/N42/lib/common"
)

func TestBorKeyValueConfigHelper(t *testing.T) {
	backupMultiplier := map[string]uint64{
		"0":        2,
		"25275000": 5,
		"29638656": 2,
	}
	assert.Equal(t, borKeyValueConfigHelper(backupMultiplier, 0), uint64(2))
	assert.Equal(t, borKeyValueConfigHelper(backupMultiplier, 1), uint64(2))
	assert.Equal(t, borKeyValueConfigHelper(backupMultiplier, 25275000-1), uint64(2))
	assert.Equal(t, borKeyValueConfigHelper(backupMultiplier, 25275000), uint64(5))
	assert.Equal(t, borKeyValueConfigHelper(backupMultiplier, 25275000+1), uint64(5))
	assert.Equal(t, borKeyValueConfigHelper(backupMultiplier, 29638656-1), uint64(5))
	assert.Equal(t, borKeyValueConfigHelper(backupMultiplier, 29638656), uint64(2))
	assert.Equal(t, borKeyValueConfigHelper(backupMultiplier, 29638656+1), uint64(2))

	config := map[string]uint64{
		"0":         1,
		"90000000":  2,
		"100000000": 3,
	}
	assert.Equal(t, borKeyValueConfigHelper(config, 0), uint64(1))
	assert.Equal(t, borKeyValueConfigHelper(config, 1), uint64(1))
	assert.Equal(t, borKeyValueConfigHelper(config, 90000000-1), uint64(1))
	assert.Equal(t, borKeyValueConfigHelper(config, 90000000), uint64(2))
	assert.Equal(t, borKeyValueConfigHelper(config, 90000000+1), uint64(2))
	assert.Equal(t, borKeyValueConfigHelper(config, 100000000-1), uint64(2))
	assert.Equal(t, borKeyValueConfigHelper(config, 100000000), uint64(3))
	assert.Equal(t, borKeyValueConfigHelper(config, 100000000+1), uint64(3))

	address1 := common.HexToAddress("0x70bcA57F4579f58670aB2d18Ef16e02C17553C38")
	address2 := common.HexToAddress("0x617b94CCCC2511808A3C9478ebb96f455CF167aA")

	burntContract := map[string]common.Address{
		"22640000": address1,
		"41874000": address2,
	}
	assert.Equal(t, borKeyValueConfigHelper(burntContract, 22640000), address1)
	assert.Equal(t, borKeyValueConfigHelper(burntContract, 22640000+1), address1)
	assert.Equal(t, borKeyValueConfigHelper(burntContract, 41874000-1), address1)
	assert.Equal(t, borKeyValueConfigHelper(burntContract, 41874000), address2)
	assert.Equal(t, borKeyValueConfigHelper(burntContract, 41874000+1), address2)
}

func TestNilBlobSchedule(t *testing.T) {
	var b *BlobSchedule

	// Original EIP-4844 values
	isPrague := false
	isOsaka := false
	assert.Equal(t, uint64(3), b.TargetBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(6), b.MaxBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(3338477), b.BaseFeeUpdateFraction(isPrague, isOsaka))

	// EIP-7691: Blob throughput increase
	isPrague = true
	assert.Equal(t, uint64(6), b.TargetBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(9), b.MaxBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(5007716), b.BaseFeeUpdateFraction(isPrague, isOsaka))

	// Fusaka/Osaka: PeerDAS blob throughput increase (EIP-7691)
	isOsaka = true
	assert.Equal(t, uint64(10), b.TargetBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(15), b.MaxBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(8346618), b.BaseFeeUpdateFraction(isPrague, isOsaka))
}

// EIP-7840: Add blob schedule to EL config files
// Tests for BlobSchedule structure and methods

func TestEIP7840BlobScheduleStructure(t *testing.T) {
	// Test BlobConfig structure
	target := uint64(3)
	max := uint64(6)
	fraction := uint64(3338477)

	config := &BlobConfig{
		Target:                &target,
		Max:                   &max,
		BaseFeeUpdateFraction: &fraction,
	}

	assert.Equal(t, uint64(3), *config.Target)
	assert.Equal(t, uint64(6), *config.Max)
	assert.Equal(t, uint64(3338477), *config.BaseFeeUpdateFraction)
}

func TestEIP7840BlobScheduleWithCancunConfig(t *testing.T) {
	target := uint64(3)
	max := uint64(6)
	fraction := uint64(3338477)

	schedule := &BlobSchedule{
		Cancun: &BlobConfig{
			Target:                &target,
			Max:                   &max,
			BaseFeeUpdateFraction: &fraction,
		},
	}

	// Pre-Prague should use Cancun values
	isPrague := false
	isOsaka := false
	assert.Equal(t, uint64(3), schedule.TargetBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(6), schedule.MaxBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(3338477), schedule.BaseFeeUpdateFraction(isPrague, isOsaka))
}

func TestEIP7840BlobScheduleWithPragueConfig(t *testing.T) {
	cancunTarget := uint64(3)
	cancunMax := uint64(6)
	cancunFraction := uint64(3338477)

	pragueTarget := uint64(6)
	pragueMax := uint64(9)
	pragueFraction := uint64(5007716)

	schedule := &BlobSchedule{
		Cancun: &BlobConfig{
			Target:                &cancunTarget,
			Max:                   &cancunMax,
			BaseFeeUpdateFraction: &cancunFraction,
		},
		Prague: &BlobConfig{
			Target:                &pragueTarget,
			Max:                   &pragueMax,
			BaseFeeUpdateFraction: &pragueFraction,
		},
	}

	// Pre-Prague should use Cancun values
	isPrague := false
	isOsaka := false
	assert.Equal(t, uint64(3), schedule.TargetBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(6), schedule.MaxBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(3338477), schedule.BaseFeeUpdateFraction(isPrague, isOsaka))

	// Prague should use Prague values
	isPrague = true
	assert.Equal(t, uint64(6), schedule.TargetBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(9), schedule.MaxBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(5007716), schedule.BaseFeeUpdateFraction(isPrague, isOsaka))
}

func TestEIP7840BlobScheduleWithOsakaConfig(t *testing.T) {
	osakaTarget := uint64(10)
	osakaMax := uint64(15)
	osakaFraction := uint64(8346618)

	schedule := &BlobSchedule{
		Osaka: &BlobConfig{
			Target:                &osakaTarget,
			Max:                   &osakaMax,
			BaseFeeUpdateFraction: &osakaFraction,
		},
	}

	// Osaka should use Osaka values
	isPrague := true
	isOsaka := true
	assert.Equal(t, uint64(10), schedule.TargetBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(15), schedule.MaxBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(8346618), schedule.BaseFeeUpdateFraction(isPrague, isOsaka))
}

func TestEIP7840BlobSchedulePartialConfig(t *testing.T) {
	// Test with only Target specified
	target := uint64(5)
	schedule := &BlobSchedule{
		Prague: &BlobConfig{
			Target: &target,
		},
	}

	isPrague := true
	isOsaka := false

	// Target should use specified value
	assert.Equal(t, uint64(5), schedule.TargetBlobsPerBlock(isPrague, isOsaka))

	// Max and BaseFeeUpdateFraction should use defaults (EIP-7691)
	assert.Equal(t, uint64(9), schedule.MaxBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(5007716), schedule.BaseFeeUpdateFraction(isPrague, isOsaka))
}

func TestEIP7840BlobScheduleEmptyConfigs(t *testing.T) {
	// Test with empty BlobConfig (all nil fields)
	schedule := &BlobSchedule{
		Cancun: &BlobConfig{},
		Prague: &BlobConfig{},
		Osaka:  &BlobConfig{},
	}

	// Should fall back to defaults for each fork
	isPrague := false
	isOsaka := false
	assert.Equal(t, uint64(3), schedule.TargetBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(6), schedule.MaxBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(3338477), schedule.BaseFeeUpdateFraction(isPrague, isOsaka))

	isPrague = true
	assert.Equal(t, uint64(6), schedule.TargetBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(9), schedule.MaxBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(5007716), schedule.BaseFeeUpdateFraction(isPrague, isOsaka))

	isOsaka = true
	assert.Equal(t, uint64(10), schedule.TargetBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(15), schedule.MaxBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(8346618), schedule.BaseFeeUpdateFraction(isPrague, isOsaka))
}

func TestEIP7840BlobScheduleGnosisStyle(t *testing.T) {
	// Test Gnosis-style config (smaller blob limits)
	target := uint64(1)
	max := uint64(2)
	fraction := uint64(1112826)

	schedule := &BlobSchedule{
		Cancun: &BlobConfig{
			Target:                &target,
			Max:                   &max,
			BaseFeeUpdateFraction: &fraction,
		},
		Prague: &BlobConfig{
			Target:                &target,
			Max:                   &max,
			BaseFeeUpdateFraction: &fraction,
		},
	}

	// Both Cancun and Prague should use the same values
	isPrague := false
	isOsaka := false
	assert.Equal(t, uint64(1), schedule.TargetBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(2), schedule.MaxBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(1112826), schedule.BaseFeeUpdateFraction(isPrague, isOsaka))

	isPrague = true
	assert.Equal(t, uint64(1), schedule.TargetBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(2), schedule.MaxBlobsPerBlock(isPrague, isOsaka))
	assert.Equal(t, uint64(1112826), schedule.BaseFeeUpdateFraction(isPrague, isOsaka))
}

func TestEIP7840BlobSchedulePrecedence(t *testing.T) {
	// Test that Osaka takes precedence over Prague, which takes precedence over Cancun
	cancunTarget := uint64(3)
	pragueTarget := uint64(6)
	osakaTarget := uint64(10)

	schedule := &BlobSchedule{
		Cancun: &BlobConfig{Target: &cancunTarget},
		Prague: &BlobConfig{Target: &pragueTarget},
		Osaka:  &BlobConfig{Target: &osakaTarget},
	}

	// Test precedence order
	assert.Equal(t, uint64(3), schedule.TargetBlobsPerBlock(false, false))  // Cancun
	assert.Equal(t, uint64(6), schedule.TargetBlobsPerBlock(true, false))   // Prague
	assert.Equal(t, uint64(10), schedule.TargetBlobsPerBlock(true, true))   // Osaka
	assert.Equal(t, uint64(10), schedule.TargetBlobsPerBlock(false, true))  // Osaka (even without Prague)
}