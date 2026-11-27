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
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"
)

func TestDevRewardForms(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		quoted  bool
		wantErr bool
	}{
		{in: `1000000000000000000`, want: "1000000000000000000"},
		{in: `"1000000000000000000"`, want: "1000000000000000000", quoted: true},
		{in: `"0"`, want: "0", quoted: true},
		{in: `"115792089237316195423570985008687907853269984665640564039457584007913129639935"`, want: "115792089237316195423570985008687907853269984665640564039457584007913129639935", quoted: true},
		{in: `"115792089237316195423570985008687907853269984665640564039457584007913129639936"`, wantErr: true},
		{in: `"01"`, wantErr: true},
		{in: `"-1"`, wantErr: true},
		{in: `"0x10"`, wantErr: true},
		{in: `18446744073709551616`, wantErr: true}, // legacy form stays uint64
		{in: `-1`, wantErr: true},
	} {
		var r DevReward
		err := json.Unmarshal([]byte(tc.in), &r)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: accepted as %s", tc.in, r)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.in, err)
			continue
		}
		if r.String() != tc.want || r.Quoted() != tc.quoted {
			t.Errorf("%s: got %s quoted=%v", tc.in, r, r.Quoted())
		}
		out, err := json.Marshal(r)
		if err != nil || string(out) != tc.in {
			t.Errorf("%s: re-encoded as %s (%v)", tc.in, out, err)
		}
	}
	// Absent stays absent in a legacy config.
	out, err := json.Marshal(&HotStuffConfig{})
	if err != nil || strings.Contains(string(out), "devBlockReward") {
		t.Fatalf("zero reward encoded: %s (%v)", out, err)
	}
}

// profile1TestConfig is a valid Profile-1 config object (JSON); the BLS keys
// are only syntactically checked here.
func profile1TestConfig() map[string]any {
	validators := make([]any, 4)
	for i := range validators {
		validators[i] = map[string]any{
			"address": fmt.Sprintf("0x%040x", i+1),
			"blsKey":  "0x" + strings.Repeat(fmt.Sprintf("%02x", i+1), 48),
		}
	}
	cfg := map[string]any{
		"chainId": 1144, "n42Profile": 1, "terminalTotalDifficultyPassed": true,
		"consensus": "hotstuff", "stateScheme": "qmdb",
		"hotstuff": map[string]any{
			"validators": validators, "leaderTenure": 8, "baseTimeoutMs": 6000, "maxTimeoutMs": 30000,
			"blockIntervalMs": 500, "epochLength": 200, "deferredExecutionDepth": 1,
			"gasLimit": 480000000, "maxBlockBytes": 67108864,
			"committeePool": map[string]any{
				"seedHex": "0x" + strings.Repeat("01", 32), "poolSize": 200000, "committeeSize": 512, "rampBlocks": 1000000,
			},
			"devBlockReward":   "1000000000000000000",
			"devFaucetAddress": "0x42e9819036f61bF665D5f727E8C03121f12f586e",
		},
	}
	for _, k := range profile1ZeroForkKeys {
		cfg[k] = 0
	}
	return cfg
}

func decodeProfile1(t *testing.T, cfg map[string]any) error {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !ConfigJSONDeclaresProfile(raw) {
		t.Fatal("config does not declare a profile")
	}
	if err := ValidateProfile1ConfigJSON(raw); err != nil {
		return err
	}
	var c ChainConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	return NormalizeConsensus(&c).ValidateProfile1()
}

func TestProfile1ConfigRefusals(t *testing.T) {
	if err := decodeProfile1(t, profile1TestConfig()); err != nil {
		t.Fatalf("valid profile-1 config refused: %v", err)
	}
	hs := func(c map[string]any) map[string]any { return c["hotstuff"].(map[string]any) }
	for name, tc := range map[string]struct {
		edit func(c map[string]any)
		want string
	}{
		"unknown config field":    {func(c map[string]any) { c["fooTime"] = 0 }, `unknown field(s) fooTime`},
		"legacy eofTime":          {func(c map[string]any) { c["eofTime"] = 0 }, `legacy/activation field(s) eofTime`},
		"legacy glamsterdamTime":  {func(c map[string]any) { c["glamsterdamTime"] = 0 }, `glamsterdamTime`},
		"legacy period":           {func(c map[string]any) { hs(c)["period"] = 3 }, `config.hotstuff: legacy/activation field(s) period`},
		"legacy interopV4":        {func(c map[string]any) { hs(c)["interopV4"] = true }, `interopV4`},
		"legacy twoPhaseVoteGate": {func(c map[string]any) { hs(c)["twoPhaseVoteGate"] = false }, `twoPhaseVoteGate`},
		"legacy rotateOnTimeout":  {func(c map[string]any) { hs(c)["rotateOnTimeoutView"] = 0 }, `rotateOnTimeoutView`},
		"pool enabled":            {func(c map[string]any) { hs(c)["committeePool"].(map[string]any)["enabled"] = true }, `committeePool: legacy/activation field(s) enabled`},
		"pool allowHandover":      {func(c map[string]any) { hs(c)["committeePool"].(map[string]any)["allowHandover"] = false }, `allowHandover`},
		"validator unknown":       {func(c map[string]any) { hs(c)["validators"].([]any)[0].(map[string]any)["weight"] = 1 }, `validators[0]: unknown field(s) weight`},
		"missing tenure":          {func(c map[string]any) { delete(hs(c), "leaderTenure") }, `mandatory field "leaderTenure" missing`},
		"missing amsterdam":       {func(c map[string]any) { delete(c, "amsterdamTime") }, `mandatory field "amsterdamTime" missing`},
		"null pool":               {func(c map[string]any) { hs(c)["committeePool"] = nil }, `mandatory field "committeePool" is null`},
		"profile 2":               {func(c map[string]any) { c["n42Profile"] = 2 }, `n42Profile 2 is not implemented`},
		"fork not zero":           {func(c map[string]any) { c["eip158Block"] = 1 }, `eip158Block must be the JSON number 0`},
		"ttd not passed":          {func(c map[string]any) { c["terminalTotalDifficultyPassed"] = false }, `terminalTotalDifficultyPassed must be true`},
		"number reward":           {func(c map[string]any) { hs(c)["devBlockReward"] = 1 }, `devBlockReward must be a decimal string`},
		"string tenure":           {func(c map[string]any) { hs(c)["leaderTenure"] = "8" }, `leaderTenure must be a JSON number`},
		"depth 2":                 {func(c map[string]any) { hs(c)["deferredExecutionDepth"] = 2 }, `deferredExecutionDepth 2 is not implemented`},
		"tenure 0":                {func(c map[string]any) { hs(c)["leaderTenure"] = 0 }, `leaderTenure must be >= 1`},
		"max below base":          {func(c map[string]any) { hs(c)["maxTimeoutMs"] = 5000 }, `maxTimeoutMs 5000 is below baseTimeoutMs 6000`},
		"epoch 0":                 {func(c map[string]any) { hs(c)["epochLength"] = 0 }, `epochLength must be >= 1`},
		"block bytes":             {func(c map[string]any) { hs(c)["maxBlockBytes"] = 1 << 20 }, `maxBlockBytes 1048576 is not implemented`},
		"three validators":        {func(c map[string]any) { hs(c)["validators"] = hs(c)["validators"].([]any)[:3] }, `at least 4 entries, got 3`},
		"short bls key":           {func(c map[string]any) { hs(c)["validators"].([]any)[1].(map[string]any)["blsKey"] = "0x01" }, `validators[1].blsKey`},
		"duplicate validator": {func(c map[string]any) {
			v := hs(c)["validators"].([]any)
			v[3].(map[string]any)["address"] = v[0].(map[string]any)["address"]
		}, `validators[3].address repeats validators[0]`},
		"short seed":           {func(c map[string]any) { hs(c)["committeePool"].(map[string]any)["seedHex"] = "0x01" }, `seedHex`},
		"zero faucet":          {func(c map[string]any) { hs(c)["devFaucetAddress"] = "0x0000000000000000000000000000000000000000" }, `devFaucetAddress`},
		"wrong scheme":         {func(c map[string]any) { c["stateScheme"] = "jmt-blake3" }, `stateScheme must be "qmdb"`},
		"wrong engine":         {func(c map[string]any) { c["consensus"] = "apos" }, `consensus must be "hotstuff"`},
		"blob unknown":         {func(c map[string]any) { c["blobSchedule"] = map[string]any{"cancun": map[string]any{"foo": 1}} }, `config.blobSchedule`},
		"reward not gwei":      {func(c map[string]any) { hs(c)["devBlockReward"] = "1000000000000000001" }, `not a whole number of gwei`},
		"reward over u64 gwei": {func(c map[string]any) { hs(c)["devBlockReward"] = "18446744073709551616000000000" }, `exceeds 2^64-1 gwei`},
		"case mismatch":        {func(c map[string]any) { c["ChainId"] = c["chainId"]; delete(c, "chainId") }, `unknown field(s) ChainId`},
	} {
		cfg := profile1TestConfig()
		tc.edit(cfg)
		err := decodeProfile1(t, cfg)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
		}
	}
}

func TestProfile1RepeatedKeyRefused(t *testing.T) {
	raw, _ := json.Marshal(profile1TestConfig())
	dup := strings.Replace(string(raw), `"chainId":1144`, `"chainId":1144,"chainId":1145`, 1)
	if err := ValidateProfile1ConfigJSON([]byte(dup)); err == nil || !strings.Contains(err.Error(), `"chainId" appears twice`) {
		t.Fatalf("repeated key: %v", err)
	}
}

// TestProfile1StructRefusesLegacyFields covers the start-up re-check of a
// stored config: every legacy field the struct can carry is refused.
func TestProfile1StructRefusesLegacyFields(t *testing.T) {
	base := func(t *testing.T) *ChainConfig {
		raw, _ := json.Marshal(profile1TestConfig())
		var c ChainConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		return NormalizeConsensus(&c)
	}
	one := big.NewInt(1)
	for name, edit := range map[string]func(c *ChainConfig){
		"deferredExecutionTime":    func(c *ChainConfig) { c.DeferredExecutionTime = one },
		"mobileAnchorTime":         func(c *ChainConfig) { c.MobileAnchorTime = one },
		"unchangedAccountNoOpTime": func(c *ChainConfig) { c.UnchangedAccountNoOpTime = one },
		"storageNetChangeTime":     func(c *ChainConfig) { c.StorageNetChangeTime = one },
		"balTime":                  func(c *ChainConfig) { c.BALTime = one },
		"eofTime":                  func(c *ChainConfig) { c.EOFTime = one },
		"ltHashTime":               func(c *ChainConfig) { c.LtHashTime = one },
		"pqPrecompilesTime":        func(c *ChainConfig) { c.PQPrecompilesTime = one },
		"txRootBlake3Time":         func(c *ChainConfig) { c.TxRootBlake3Time = one },
		"fusakaTime":               func(c *ChainConfig) { c.FusakaTime = one },
		"apos":                     func(c *ChainConfig) { c.Apos = &APosConfig{} },
		"hotstuff.period":          func(c *ChainConfig) { c.HotStuff.Period = 3 },
		"hotstuff.baseTimeout":     func(c *ChainConfig) { c.HotStuff.BaseTimeout = 1 },
		"hotstuff.fastPropose":     func(c *ChainConfig) { c.HotStuff.FastPropose = true },
		"hotstuff.ethELCompat":     func(c *ChainConfig) { c.HotStuff.EthELCompat = true },
		"hotstuff.devBlockReward":  func(c *ChainConfig) { c.HotStuff.DevBlockReward = NewDevReward(1) },
	} {
		c := base(t)
		edit(c)
		if err := c.ValidateProfile1(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := base(t).ValidateProfile1(); err != nil {
		t.Fatalf("valid config refused: %v", err)
	}
	// The largest reward a withdrawal can carry: 2^64-1 gwei.
	c := base(t)
	max, err := NewDevRewardDecimal("18446744073709551615000000000")
	if err != nil {
		t.Fatal(err)
	}
	c.HotStuff.DevBlockReward = max
	if err := c.ValidateProfile1(); err != nil {
		t.Fatalf("2^64-1 gwei refused: %v", err)
	}
}

func TestProfile1Predicates(t *testing.T) {
	legacy := &ChainConfig{HotStuff: &HotStuffConfig{BaseTimeout: 60000, MaxTimeout: 120000, CommitteePool: &HotStuffCommitteePoolConfig{}}}
	if legacy.IsProfile1() || legacy.IsDeferredExecution(1<<40) || legacy.IsMobileAnchor(1<<40) ||
		legacy.IsUnchangedAccountNoOp(1<<40) || legacy.IsStorageNetChange(1<<40) || legacy.IsGlamsterdam(1<<40) ||
		legacy.HotStuffTwoPhaseVoteGate() || legacy.HotStuffInteropV4() || legacy.HotStuffCommitteePoolEnabled() ||
		legacy.HasMobileAnchor() {
		t.Fatal("a legacy config without fields turned a rule on")
	}
	if on, _ := legacy.HotStuffRotateOnTimeout(); on {
		t.Fatal("legacy rotate-on-timeout on")
	}
	if b, m := legacy.HotStuffTimeoutsMs(); b != 60000 || m != 120000 {
		t.Fatalf("legacy timeouts %d/%d", b, m)
	}
	// A legacy genesis that happens to carry amsterdamTime keeps ignoring it.
	legacy.AmsterdamTime = big.NewInt(0)
	if NormalizeConsensus(legacy).IsGlamsterdam(1) || legacy.GlamsterdamTime != nil {
		t.Fatal("legacy amsterdamTime honoured")
	}

	p1 := &ChainConfig{N42Profile: N42Profile1, AmsterdamTime: big.NewInt(0), HotStuff: &HotStuffConfig{
		BaseTimeoutMs: 6000, MaxTimeoutMs: 30000, CommitteePool: &HotStuffCommitteePoolConfig{},
	}}
	NormalizeConsensus(p1)
	if !p1.IsDeferredExecution(0) || !p1.IsMobileAnchor(0) || !p1.IsUnchangedAccountNoOp(0) ||
		!p1.IsStorageNetChange(0) || !p1.IsGlamsterdam(0) || !p1.IsMobileRegistryRootInHeader(0) ||
		!p1.HotStuffTwoPhaseVoteGate() || !p1.HotStuffInteropV4() || !p1.HotStuffCommitteePoolEnabled() ||
		!p1.HasMobileAnchor() {
		t.Fatal("a profile-1 rule is off from genesis")
	}
	if on, from := p1.HotStuffRotateOnTimeout(); !on || from != 0 {
		t.Fatalf("profile-1 rotate-on-timeout %v from %d", on, from)
	}
	if b, m := p1.HotStuffTimeoutsMs(); b != 6000 || m != 30000 {
		t.Fatalf("profile-1 timeouts %d/%d", b, m)
	}
	if p1.GlamsterdamTime == nil || p1.GlamsterdamTime.Sign() != 0 {
		t.Fatal("amsterdamTime not mapped to GlamsterdamTime")
	}
}

func TestProfile1ForbiddenEnv(t *testing.T) {
	env := []string{
		"PATH=/bin",
		"N42_HOTSTUFF_LEADER_TENURE=1",
		"N42_DEFERRED_DEPTH_EXTRA=2",
		"N42_SYNC_LEGACY_HEIGHT_GATE=1",
		"N42_SYNC_ANCHOR=observe",
		"N42_PARALLEL_WORKERS=8", // permitted bench knob
		"N42_STRESS_GASLIMIT=",   // set, even if empty
	}
	got := Profile1ForbiddenEnv(env)
	want := []string{"N42_DEFERRED_DEPTH_EXTRA", "N42_HOTSTUFF_LEADER_TENURE", "N42_STRESS_GASLIMIT", "N42_SYNC_ANCHOR", "N42_SYNC_LEGACY_HEIGHT_GATE"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forbidden = %v, want %v", got, want)
	}
	if got := Profile1ForbiddenEnv([]string{"N42_SYNC_ANCHOR=enforce", "N42_MINER_FILL_GAS=1", "N42_MDBX_SYNC=durable"}); len(got) != 0 {
		t.Fatalf("permitted settings refused: %v", got)
	}
}

// AM35: a commit vote certifies a durable block, so profile 1 refuses the
// chaindata fsync opt-out (and any spelling other than the default).
func TestProfile1RefusesMDBXSafeNoSync(t *testing.T) {
	for _, v := range []string{"safe-nosync", "SAFE-NOSYNC", "", "nosync"} {
		got := Profile1ForbiddenEnv([]string{"N42_MDBX_SYNC=" + v})
		if !reflect.DeepEqual(got, []string{"N42_MDBX_SYNC"}) {
			t.Fatalf("N42_MDBX_SYNC=%q: forbidden = %v, want refused", v, got)
		}
	}
}
