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

import (
	"math/big"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// activeRuleNames lists the names of every bool field set in r, sorted, so a
// whole rule set can be pinned as one string.
func activeRuleNames(r *Rules) string {
	v := reflect.ValueOf(*r)
	var names []string
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); f.Kind() == reflect.Bool && f.Bool() {
			names = append(names, v.Type().Field(i).Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// n42ChainNames are the N42-native chain configs. Their behaviour must not
// change when the Ethereum fork lattice is corrected.
var n42ChainNames = []string{
	"mainnet", "mainnet_compat", "mainnet_v2", "mainnet_v2_staggered",
	"mainnet_mpt", "mainnet_qmdb", "mainnet_qmdb_staggered",
	"qs_epoch_test", "h2_interop_test", "testnet",
}

// forkSampleTimes returns 0, every scheduled fork timestamp t of cfg and
// t-1, t+1 around it, plus a far-future timestamp.
func forkSampleTimes(cfg *ChainConfig) []uint64 {
	times := []uint64{0, 4_000_000_000}
	v := reflect.ValueOf(*cfg)
	bigType := reflect.TypeOf((*big.Int)(nil))
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Type() != bigType || f.IsNil() || !strings.HasSuffix(v.Type().Field(i).Name, "Time") {
			continue
		}
		b := f.Interface().(*big.Int)
		if !b.IsUint64() {
			continue
		}
		t := b.Uint64()
		times = append(times, t, t+1)
		if t > 0 {
			times = append(times, t-1)
		}
	}
	return times
}

// TestGlamsterdamDoesNotImplyFusaka pins Ethereum's fork lattice: Amsterdam
// (Glamsterdam) implies Osaka and everything before it, but never the
// N42-private Fusaka gate.
func TestGlamsterdamDoesNotImplyFusaka(t *testing.T) {
	cfg := &ChainConfig{
		ChainID:         big.NewInt(1),
		ShanghaiTime:    big.NewInt(0),
		CancunTime:      big.NewInt(0),
		PragueTime:      big.NewInt(0),
		OsakaTime:       big.NewInt(0),
		GlamsterdamTime: big.NewInt(100),
	}
	r := cfg.RulesWithTimestamp(0, 100)
	if !r.IsGlamsterdam || !r.IsOsaka || !r.IsPrague || !r.IsCancun || !r.IsShanghai {
		t.Fatalf("Glamsterdam must imply Osaka and earlier, got %s", activeRuleNames(r))
	}
	if r.IsFusaka {
		t.Fatalf("Glamsterdam must not imply the N42-private Fusaka gate, got %s", activeRuleNames(r))
	}

	// Glamsterdam alone still pulls in the whole Ethereum lattice below it.
	bare := (&ChainConfig{ChainID: big.NewInt(1), GlamsterdamTime: big.NewInt(0)}).RulesWithTimestamp(0, 0)
	if !bare.IsOsaka || !bare.IsPectra || !bare.IsPrague || !bare.IsCancun || bare.IsFusaka {
		t.Fatalf("bare Glamsterdam rules: %s", activeRuleNames(bare))
	}
}

// TestEthereumConfigsScheduleNoN42PrivateForks guards that the Ethereum
// chain configs never schedule the N42-private Fusaka gate.
func TestEthereumConfigsScheduleNoN42PrivateForks(t *testing.T) {
	for _, cfg := range []*ChainConfig{EthereumMainnetChainConfig, EthereumSepoliaChainConfig, EthereumHoleskyChainConfig} {
		if cfg.FusakaTime != nil {
			t.Errorf("%s schedules fusakaTime=%v", cfg.ChainName, cfg.FusakaTime)
		}
		for _, ts := range forkSampleTimes(cfg) {
			if r := cfg.RulesWithTimestamp(30_000_000, ts); r.IsFusaka {
				t.Errorf("%s: IsFusaka at ts=%d", cfg.ChainName, ts)
			}
		}
	}
}

// TestN42ChainsUnaffectedByGlamsterdamLattice proves the lattice fix is a
// no-op for every N42-native chain. The only rule it changes is that
// IsGlamsterdam no longer forces IsFusaka; everything below Fusaka is
// inherited identically through IsOsaka. So the rule sets are unchanged iff
// IsFusaka already holds wherever IsGlamsterdam does, at every fork boundary.
func TestN42ChainsUnaffectedByGlamsterdamLattice(t *testing.T) {
	for _, name := range n42ChainNames {
		cfg := ChainConfigByChainName(name)
		if cfg == nil {
			t.Fatalf("unknown chain %q", name)
		}
		for _, ts := range forkSampleTimes(cfg) {
			for _, num := range []uint64{0, 4_000_000, 100_000_000} {
				r := cfg.RulesWithTimestamp(num, ts)
				if r.IsGlamsterdam && !r.IsFusaka {
					t.Errorf("%s num=%d ts=%d: Glamsterdam active without Fusaka (%s)", name, num, ts, activeRuleNames(r))
				}
			}
		}
	}
}

// TestMainnetQMDBStaggeredRulesPinned pins the full rule set of
// mainnet_qmdb_staggered around its timestamp forks. osakaTime, eofTime and
// fusakaTime coincide there, so Osaka/EOF/Fusaka switch on together.
func TestMainnetQMDBStaggeredRulesPinned(t *testing.T) {
	cfg := ChainConfigByChainName("mainnet_qmdb_staggered")
	const (
		preShanghai = "IsBeijing,IsBerlin,IsByzantium,IsConstantinople,IsHomestead,IsIstanbul,IsLondon,IsPetersburg,IsSpuriousDragon,IsTangerineWhistle"
		cancun      = "IsBeijing,IsBerlin,IsByzantium,IsCancun,IsConstantinople,IsHomestead,IsIstanbul,IsLondon,IsParis,IsPetersburg,IsShanghai,IsSpuriousDragon,IsTangerineWhistle"
		pectra      = "IsBeijing,IsBerlin,IsByzantium,IsCancun,IsConstantinople,IsHomestead,IsIstanbul,IsLondon,IsParis,IsPectra,IsPetersburg,IsPrague,IsShanghai,IsSpuriousDragon,IsTangerineWhistle"
		fusaka      = "IsBeijing,IsBerlin,IsByzantium,IsCancun,IsConstantinople,IsEOF,IsFusaka,IsHomestead,IsIstanbul,IsLondon,IsOsaka,IsParis,IsPectra,IsPetersburg,IsPrague,IsShanghai,IsSpuriousDragon,IsTangerineWhistle"
		anchor      = "IsBeijing,IsBerlin,IsByzantium,IsCancun,IsConstantinople,IsEOF,IsFusaka,IsHomestead,IsIstanbul,IsLondon,IsMobileAnchor,IsOsaka,IsParis,IsPectra,IsPetersburg,IsPrague,IsShanghai,IsSpuriousDragon,IsTangerineWhistle"
	)
	cases := []struct {
		num, ts uint64
		want    string
	}{
		{304_999, 0, preShanghai},
		{3_935_000, 1746612310, cancun},
		{4_000_000, 1746612311, pectra},
		{4_000_000, 1765238399, pectra},
		{4_000_000, 1765238400, fusaka},
		{4_000_000, 1784371999, fusaka},
		{4_000_000, 1784372000, anchor},
		{9_000_000, 4_000_000_000, anchor},
	}
	for _, c := range cases {
		got := activeRuleNames(cfg.RulesWithTimestamp(c.num, c.ts))
		if got != c.want {
			t.Errorf("num=%d ts=%d:\n got  %s\n want %s", c.num, c.ts, got, c.want)
		}
	}
}
