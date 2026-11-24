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
// Fail-closed start-up checks of a Profile-1 chain config (standard P4): an
// unknown field, a legacy or activation field, a missing mandatory field or
// a value outside what this client implements refuses the genesis.

package params

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"sort"
	"strings"

	"github.com/n42blockchain/N42/common/types"
)

// Profile1MaxBlockBytes is the only maxBlockBytes this client implements: the
// sync standard's 64 MiB decoded-RLP block cap (p2p encoder
// StandardBlockBytes).
const Profile1MaxBlockBytes = uint64(64 << 20)

// Profile1DeferredExecutionDepth is the only deferredExecutionDepth this
// client implements (A10, decision O4).
const Profile1DeferredExecutionDepth = 1

// profile1MinValidators is the smallest validator set (A3: n >= 4).
const profile1MinValidators = 4

// The field set of a Profile-1 config (standard §5). Every key is mandatory
// except the optional ones listed separately; nothing else is accepted.
var (
	profile1ZeroForkKeys = []string{
		"homesteadBlock", "eip150Block", "eip155Block", "eip158Block",
		"byzantiumBlock", "constantinopleBlock", "petersburgBlock", "istanbulBlock",
		"berlinBlock", "londonBlock",
		"mergeNetsplitBlock", "terminalTotalDifficulty",
		"shanghaiTime", "cancunTime", "pragueTime", "osakaTime",
		"amsterdamTime",
	}
	profile1ConfigKeys = append([]string{
		"chainId", "n42Profile", "terminalTotalDifficultyPassed",
		"consensus", "stateScheme", "hotstuff",
	}, profile1ZeroForkKeys...)
	profile1ConfigOptionalKeys = []string{"blobSchedule"}
	profile1HotStuffKeys       = []string{
		"validators", "leaderTenure", "baseTimeoutMs", "maxTimeoutMs", "blockIntervalMs",
		"epochLength", "deferredExecutionDepth", "gasLimit", "maxBlockBytes",
		"committeePool", "devBlockReward", "devFaucetAddress",
	}
	profile1CommitteePoolKeys = []string{"seedHex", "poolSize", "committeeSize", "rampBlocks"}
	profile1ValidatorKeys     = []string{"address", "blsKey"}
)

// ConfigJSONDeclaresProfile reports whether a raw genesis "config" object
// carries the n42Profile key at all (whatever its value): such a config is
// held to the profile rules and refused if the value is not implemented.
func ConfigJSONDeclaresProfile(raw []byte) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m["n42Profile"]
	return ok
}

// ValidateProfile1ConfigJSON checks the raw genesis "config" object of a
// genesis that declares n42Profile: exactly the §5 field set (exact key
// spelling, no duplicates), every mandatory field present, the Ethereum fork
// fields all 0 and devBlockReward a decimal string. Values with a struct
// representation are range-checked by ChainConfig.ValidateProfile1.
func ValidateProfile1ConfigJSON(raw []byte) error {
	cfg, err := decodeStrictObject(raw, "config")
	if err != nil {
		return err
	}
	var profile uint64
	if v, ok := cfg["n42Profile"]; ok {
		if err := json.Unmarshal(v, &profile); err != nil || profile != N42Profile1 {
			return fmt.Errorf("config.n42Profile %s is not implemented: this client implements profile %d", bytes.TrimSpace(v), N42Profile1)
		}
	}
	if err := checkObjectKeys(cfg, "config", profile1ConfigKeys, profile1ConfigOptionalKeys, reflect.TypeOf(ChainConfig{})); err != nil {
		return err
	}
	for _, k := range profile1ZeroForkKeys {
		var n json.Number
		dec := json.NewDecoder(bytes.NewReader(cfg[k]))
		dec.UseNumber()
		if err := dec.Decode(&n); err != nil || n.String() != "0" {
			return fmt.Errorf("config.%s must be the JSON number 0 on profile 1 (every fork from genesis), got %s", k, bytes.TrimSpace(cfg[k]))
		}
	}
	if s := string(bytes.TrimSpace(cfg["terminalTotalDifficultyPassed"])); s != "true" {
		return fmt.Errorf("config.terminalTotalDifficultyPassed must be true on profile 1, got %s", s)
	}
	if v, ok := cfg["blobSchedule"]; ok {
		dec := json.NewDecoder(bytes.NewReader(v))
		dec.DisallowUnknownFields()
		var bs BlobSchedule
		if err := dec.Decode(&bs); err != nil {
			return fmt.Errorf("config.blobSchedule: %w", err)
		}
	}

	hs, err := decodeStrictObject(cfg["hotstuff"], "config.hotstuff")
	if err != nil {
		return err
	}
	if err := checkObjectKeys(hs, "config.hotstuff", profile1HotStuffKeys, nil, reflect.TypeOf(HotStuffConfig{})); err != nil {
		return err
	}
	for _, k := range []string{"leaderTenure", "baseTimeoutMs", "maxTimeoutMs", "blockIntervalMs", "epochLength", "deferredExecutionDepth", "gasLimit", "maxBlockBytes"} {
		if err := requireJSONUint64(hs[k], "config.hotstuff."+k); err != nil {
			return err
		}
	}
	if v := bytes.TrimSpace(hs["devBlockReward"]); len(v) == 0 || v[0] != '"' {
		return fmt.Errorf("config.hotstuff.devBlockReward must be a decimal string (wei) on profile 1, got %s", v)
	}
	pool, err := decodeStrictObject(hs["committeePool"], "config.hotstuff.committeePool")
	if err != nil {
		return err
	}
	if err := checkObjectKeys(pool, "config.hotstuff.committeePool", profile1CommitteePoolKeys, nil, reflect.TypeOf(HotStuffCommitteePoolConfig{})); err != nil {
		return err
	}
	for _, k := range []string{"poolSize", "committeeSize", "rampBlocks"} {
		if err := requireJSONUint64(pool[k], "config.hotstuff.committeePool."+k); err != nil {
			return err
		}
	}
	var validators []json.RawMessage
	if err := json.Unmarshal(hs["validators"], &validators); err != nil {
		return fmt.Errorf("config.hotstuff.validators must be an array: %w", err)
	}
	for i, v := range validators {
		where := fmt.Sprintf("config.hotstuff.validators[%d]", i)
		obj, err := decodeStrictObject(v, where)
		if err != nil {
			return err
		}
		if err := checkObjectKeys(obj, where, profile1ValidatorKeys, nil, reflect.TypeOf(HotStuffValidatorConfig{})); err != nil {
			return err
		}
	}
	return nil
}

// ValidateProfile1 checks the values of a Profile-1 config. It runs on the
// genesis at load and again on the stored config at every start, so it only
// relies on what survives a JSON round trip of the struct.
func (c *ChainConfig) ValidateProfile1() error {
	if c == nil {
		return errors.New("profile 1: missing chain config")
	}
	if c.N42Profile != N42Profile1 {
		return fmt.Errorf("config.n42Profile %d is not implemented: this client implements profile %d", c.N42Profile, N42Profile1)
	}
	if c.ChainID == nil || c.ChainID.Sign() <= 0 || !c.ChainID.IsUint64() {
		return fmt.Errorf("profile 1: config.chainId must be a positive uint64, got %v", c.ChainID)
	}
	if c.Consensus != HotStuffConsensus {
		return fmt.Errorf("profile 1: config.consensus must be %q, got %q", HotStuffConsensus, c.Consensus)
	}
	if c.StateScheme != string(StateCommitmentPresetQMDB) {
		return fmt.Errorf("profile 1: config.stateScheme must be %q, got %q", StateCommitmentPresetQMDB, c.StateScheme)
	}
	for _, f := range []struct {
		name string
		v    *big.Int
	}{
		{"homesteadBlock", c.HomesteadBlock}, {"eip150Block", c.TangerineWhistleBlock},
		{"eip155Block", c.SpuriousDragonBlock}, {"byzantiumBlock", c.ByzantiumBlock},
		{"constantinopleBlock", c.ConstantinopleBlock}, {"petersburgBlock", c.PetersburgBlock},
		{"istanbulBlock", c.IstanbulBlock}, {"berlinBlock", c.BerlinBlock},
		{"londonBlock", c.LondonBlock}, {"mergeNetsplitBlock", c.MergeNetsplitBlock},
		{"terminalTotalDifficulty", c.TerminalTotalDifficulty},
		{"shanghaiTime", c.ShanghaiTime}, {"cancunTime", c.CancunTime},
		{"pragueTime", c.PragueTime}, {"osakaTime", c.OsakaTime},
		{"amsterdamTime", c.AmsterdamTime},
	} {
		if f.v == nil || f.v.Sign() != 0 {
			return fmt.Errorf("profile 1: config.%s must be 0 (every fork from genesis), got %v", f.name, f.v)
		}
	}
	if !c.TerminalTotalDifficultyPassed {
		return errors.New("profile 1: config.terminalTotalDifficultyPassed must be true")
	}
	if c.GlamsterdamTime != nil && c.GlamsterdamTime.Cmp(c.AmsterdamTime) != 0 {
		return fmt.Errorf("profile 1: legacy field config.glamsterdamTime is refused (use amsterdamTime), got %v", c.GlamsterdamTime)
	}
	if name := c.profile1RefusedFieldSet(); name != "" {
		return fmt.Errorf("profile 1: legacy/activation field config.%s is refused: every rule holds from genesis (P2, P4)", name)
	}
	return c.HotStuff.validateProfile1()
}

// profile1RefusedFieldSet names the first config field outside the profile
// that holds a value, or "".
func (c *ChainConfig) profile1RefusedFieldSet() string {
	for _, f := range []struct {
		name string
		v    *big.Int
	}{
		{"daoForkBlock", c.DAOForkBlock}, {"muirGlacierBlock", c.MuirGlacierBlock},
		{"arrowGlacierBlock", c.ArrowGlacierBlock}, {"grayGlacierBlock", c.GrayGlacierBlock},
		{"shanghaiBlock", c.ShanghaiBlock}, {"cancunBlock", c.CancunBlock},
		{"shardingForkTime", c.ShardingForkTime}, {"pectraTime", c.PectraTime},
		{"bpo1Time", c.BPO1Time}, {"bpo2Time", c.BPO2Time}, {"bpo3Time", c.BPO3Time},
		{"bpo4Time", c.BPO4Time}, {"bpo5Time", c.BPO5Time}, {"fusakaTime", c.FusakaTime},
		{"pqPrecompilesTime", c.PQPrecompilesTime}, {"eofTime", c.EOFTime},
		{"contentStoreTime", c.ContentStoreTime}, {"aiInferenceTime", c.AIInferenceTime},
		{"randomnessTime", c.RandomnessTime}, {"ltHashTime", c.LtHashTime},
		{"balTime", c.BALTime}, {"mobileAnchorTime", c.MobileAnchorTime},
		{"nativeAssetTime", c.NativeAssetTime}, {"txRootBlake3Time", c.TxRootBlake3Time},
		{"deferredExecutionTime", c.DeferredExecutionTime},
		{"unchangedAccountNoOpTime", c.UnchangedAccountNoOpTime},
		{"storageNetChangeTime", c.StorageNetChangeTime},
		{"nanoBlock", c.NanoBlock}, {"moranBlock", c.MoranBlock}, {"beijingBlock", c.BeijingBlock},
		{"eip1559FeeCollectorTransition", c.Eip1559FeeCollectorTransition},
	} {
		if f.v != nil {
			return f.name
		}
	}
	switch {
	case c.DAOForkSupport:
		return "daoForkSupport"
	case c.TangerineWhistleHash != (types.Hash{}):
		return "eip150Hash"
	case c.DepositContractAddress != nil:
		return "depositContractAddress"
	case c.Eip1559FeeCollector != nil:
		return "eip1559FeeCollector"
	case c.Ethash != nil:
		return "ethash"
	case c.Clique != nil:
		return "clique"
	case c.Aura != nil:
		return "aura"
	case c.Parlia != nil:
		return "parlia"
	case c.Bor != nil:
		return "bor"
	case c.Apos != nil:
		return "apos"
	}
	return ""
}

func (h *HotStuffConfig) validateProfile1() error {
	if h == nil {
		return errors.New("profile 1: config.hotstuff is mandatory")
	}
	legacy := ""
	switch {
	case h.Period != 0:
		legacy = "period"
	case h.BaseTimeout != 0:
		legacy = "baseTimeout"
	case h.MaxTimeout != 0:
		legacy = "maxTimeout"
	case h.RotateOnTimeoutView != nil:
		legacy = "rotateOnTimeoutView"
	case h.FastPropose:
		legacy = "fastPropose"
	case h.MinProposeDelayMs != 0:
		legacy = "minProposeDelayMs"
	case h.EthELCompat:
		legacy = "ethELCompat"
	case h.TwoPhaseVoteGate:
		legacy = "twoPhaseVoteGate"
	case h.InteropV4:
		legacy = "interopV4"
	case h.CommitteePool != nil && h.CommitteePool.Enabled:
		legacy = "committeePool.enabled"
	case h.CommitteePool != nil && h.CommitteePool.AllowHandover:
		legacy = "committeePool.allowHandover"
	}
	if legacy != "" {
		return fmt.Errorf("profile 1: legacy/activation field config.hotstuff.%s is refused (P2, P4)", legacy)
	}

	if n := len(h.Validators); n < profile1MinValidators {
		return fmt.Errorf("profile 1: config.hotstuff.validators needs at least %d entries, got %d", profile1MinValidators, n)
	}
	seenAddr := make(map[types.Address]int, len(h.Validators))
	seenKey := make(map[string]int, len(h.Validators))
	for i, v := range h.Validators {
		addr, err := parseProfile1Address(v.Address)
		if err != nil {
			return fmt.Errorf("profile 1: config.hotstuff.validators[%d].address: %w", i, err)
		}
		if j, dup := seenAddr[addr]; dup {
			return fmt.Errorf("profile 1: config.hotstuff.validators[%d].address repeats validators[%d]", i, j)
		}
		seenAddr[addr] = i
		key, err := decodeFixedHex(v.BLSKey, 48)
		if err != nil {
			return fmt.Errorf("profile 1: config.hotstuff.validators[%d].blsKey: %w", i, err)
		}
		if j, dup := seenKey[string(key)]; dup {
			return fmt.Errorf("profile 1: config.hotstuff.validators[%d].blsKey repeats validators[%d]", i, j)
		}
		seenKey[string(key)] = i
	}

	switch {
	case h.LeaderTenure < 1:
		return errors.New("profile 1: config.hotstuff.leaderTenure must be >= 1")
	case h.BaseTimeoutMs < 1:
		return errors.New("profile 1: config.hotstuff.baseTimeoutMs must be >= 1")
	case h.MaxTimeoutMs < h.BaseTimeoutMs:
		return fmt.Errorf("profile 1: config.hotstuff.maxTimeoutMs %d is below baseTimeoutMs %d", h.MaxTimeoutMs, h.BaseTimeoutMs)
	case h.EpochLength < 1:
		return errors.New("profile 1: config.hotstuff.epochLength must be >= 1")
	case h.DeferredExecutionDepth != Profile1DeferredExecutionDepth:
		return fmt.Errorf("profile 1: config.hotstuff.deferredExecutionDepth %d is not implemented (this client implements %d)", h.DeferredExecutionDepth, Profile1DeferredExecutionDepth)
	case h.GasLimit < MinGasLimit:
		return fmt.Errorf("profile 1: config.hotstuff.gasLimit %d is below the minimum %d", h.GasLimit, MinGasLimit)
	case h.MaxBlockBytes != Profile1MaxBlockBytes:
		return fmt.Errorf("profile 1: config.hotstuff.maxBlockBytes %d is not implemented (this client implements %d)", h.MaxBlockBytes, Profile1MaxBlockBytes)
	}

	cp := h.CommitteePool
	if cp == nil {
		return errors.New("profile 1: config.hotstuff.committeePool is mandatory")
	}
	if _, err := decodeFixedHex(cp.SeedHex, 32); err != nil {
		return fmt.Errorf("profile 1: config.hotstuff.committeePool.seedHex: %w", err)
	}
	if cp.CommitteeSize < 1 || cp.PoolSize < cp.CommitteeSize {
		return fmt.Errorf("profile 1: config.hotstuff.committeePool needs 1 <= committeeSize <= poolSize, got committeeSize %d poolSize %d", cp.CommitteeSize, cp.PoolSize)
	}

	if !h.DevBlockReward.Quoted() {
		return errors.New("profile 1: config.hotstuff.devBlockReward must be a decimal string (wei)")
	}
	if h.DevFaucetAddress == nil || *h.DevFaucetAddress == (types.Address{}) {
		return errors.New("profile 1: config.hotstuff.devFaucetAddress is mandatory and must not be the zero address")
	}
	return nil
}

// decodeStrictObject decodes a JSON object into its members, refusing
// anything that is not an object and any repeated key.
func decodeStrictObject(raw []byte, where string) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("%s must be a JSON object", where)
	}
	out := make(map[string]json.RawMessage)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
		key, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("%s.%s: %w", where, key, err)
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("%s: field %q appears twice", where, key)
		}
		out[key] = v
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%s: trailing data after the object", where)
	}
	return out, nil
}

// checkObjectKeys refuses a key outside mandatory+optional and a missing
// mandatory key. A refused key that the legacy struct knows is reported as a
// legacy field, any other as unknown.
func checkObjectKeys(obj map[string]json.RawMessage, where string, mandatory, optional []string, legacy reflect.Type) error {
	allowed := make(map[string]bool, len(mandatory)+len(optional))
	for _, k := range mandatory {
		allowed[k] = true
	}
	for _, k := range optional {
		allowed[k] = true
	}
	known := jsonFieldNames(legacy)
	var unknown, refused []string
	for k := range obj {
		if allowed[k] {
			continue
		}
		if known[k] {
			refused = append(refused, k)
		} else {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("%s: unknown field(s) %s refused on profile 1 (P4)", where, strings.Join(unknown, ", "))
	}
	if len(refused) > 0 {
		sort.Strings(refused)
		return fmt.Errorf("%s: legacy/activation field(s) %s refused on profile 1 (P2, P4)", where, strings.Join(refused, ", "))
	}
	for _, k := range mandatory {
		if _, ok := obj[k]; !ok {
			return fmt.Errorf("%s: mandatory field %q missing on profile 1 (P4)", where, k)
		}
		if bytes.Equal(bytes.TrimSpace(obj[k]), []byte("null")) {
			return fmt.Errorf("%s: mandatory field %q is null on profile 1 (P4)", where, k)
		}
	}
	return nil
}

// requireJSONUint64 refuses anything but a JSON number that fits a uint64.
func requireJSONUint64(raw json.RawMessage, where string) error {
	var n uint64
	v := bytes.TrimSpace(raw)
	if len(v) == 0 || v[0] == '"' {
		return fmt.Errorf("%s must be a JSON number, got %s", where, v)
	}
	if err := json.Unmarshal(v, &n); err != nil {
		return fmt.Errorf("%s must be a uint64 JSON number: %w", where, err)
	}
	return nil
}

// jsonFieldNames lists the JSON keys of a struct type (its legacy field set).
func jsonFieldNames(t reflect.Type) map[string]bool {
	out := make(map[string]bool)
	if t == nil || t.Kind() != reflect.Struct {
		return out
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Name
		if tag := f.Tag.Get("json"); tag != "" {
			if n := strings.Split(tag, ",")[0]; n == "-" {
				continue
			} else if n != "" {
				name = n
			}
		}
		out[name] = true
	}
	return out
}

func parseProfile1Address(s string) (types.Address, error) {
	b, err := decodeFixedHex(s, types.AddressLength)
	if err != nil {
		return types.Address{}, err
	}
	addr := types.BytesToAddress(b)
	if addr == (types.Address{}) {
		return types.Address{}, errors.New("zero address")
	}
	return addr, nil
}

// decodeFixedHex decodes a 0x-prefixed hex string of exactly n bytes.
func decodeFixedHex(s string, n int) ([]byte, error) {
	if !strings.HasPrefix(s, "0x") {
		return nil, fmt.Errorf("%q must be 0x-prefixed hex", s)
	}
	b, err := hex.DecodeString(s[2:])
	if err != nil {
		return nil, fmt.Errorf("%q is not hex: %w", s, err)
	}
	if len(b) != n {
		return nil, fmt.Errorf("%q is %d bytes, want %d", s, len(b), n)
	}
	return b, nil
}
