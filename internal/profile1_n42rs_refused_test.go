// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/conf"
)

// profile1N42rsRefusedDir holds n42-rs's refused-genesis cases, copied
// byte for byte from n42-rs crates/n42/qmdb-reth/testdata/profile1. Each
// changes exactly one thing in n42-rs's standard genesis.
const profile1N42rsRefusedDir = "testdata/profile1/vectors/V8/refused-n42rs"

// profile1N42rsRefusals is the gov5 error substring each n42-rs case must
// produce: the refusal names the case's own change.
var profile1N42rsRefusals = map[string]string{
	"refused_depth_not_implemented.json":          "deferredExecutionDepth 3 is not implemented",
	"refused_extra_data.json":                     "extraData must be empty",
	"refused_gas_limit_mismatch.json":             "differs from config.hotstuff.gasLimit",
	"refused_legacy_alt_sig_tx.json":              "unknown field(s) altSigTx",
	"refused_legacy_deferred_execution_time.json": "legacy/activation field(s) deferredExecutionTime",
	"refused_legacy_eof_time.json":                "legacy/activation field(s) eofTime",
	"refused_legacy_frame_blocks.json":            "unknown field(s) frameBlocks",
	"refused_legacy_mobile_anchor_time.json":      "legacy/activation field(s) mobileAnchorTime",
	"refused_legacy_period.json":                  "legacy/activation field(s) period",
	"refused_legacy_pool_enabled.json":            "committeePool: legacy/activation field(s) enabled",
	"refused_legacy_rotate_on_timeout_view.json":  "legacy/activation field(s) rotateOnTimeoutView",
	"refused_max_timeout_below_base.json":         "maxTimeoutMs 1000 is below baseTimeoutMs",
	"refused_missing_amsterdam_time.json":         "mandatory field \"amsterdamTime\" missing",
	"refused_missing_deferred_depth.json":         "mandatory field \"deferredExecutionDepth\" missing",
	"refused_missing_epoch_length.json":           "mandatory field \"epochLength\" missing",
	"refused_missing_leader_tenure.json":          "mandatory field \"leaderTenure\" missing",
	"refused_missing_max_block_bytes.json":        "mandatory field \"maxBlockBytes\" missing",
	"refused_missing_state_scheme.json":           "mandatory field \"stateScheme\" missing",
	"refused_nonzero_fork.json":                   "config.cancunTime must be the JSON number 0",
	"refused_profile_2.json":                      "config.n42Profile 2 is not implemented",
	"refused_profile_string.json":                 "config.n42Profile \"1\" is not implemented",
	"refused_reward_above_u64_gwei.json":          "exceeds 2^64-1 gwei",
	"refused_reward_json_number.json":             "devBlockReward must be a decimal string",
	"refused_reward_not_whole_gwei.json":          "not a whole number of gwei",
	"refused_storage_only_alloc.json":             "storage-only entry",
	"refused_three_validators.json":               "validators needs at least 4 entries, got 3",
	"refused_unknown_config_key.json":             "unknown field(s) someNewRuleTime",
	"refused_unknown_hotstuff_key.json":           "unknown field(s) slotMs",
	"refused_unknown_validator_key.json":          "validators[2]: unknown field(s) peerId",
	"refused_zero_epoch_length.json":              "epochLength must be >= 1",
	"refused_zero_faucet.json":                    "devFaucetAddress is mandatory and must not be the zero address",
	"refused_zero_leader_tenure.json":             "leaderTenure must be >= 1",
	"refused_zero_value_storage.json":             "has value 0 (A7)",
}

// TestProfile1RefusedGenesisN42rs: gov5 refuses every n42-rs refused
// genesis on load, and accepts their common base (the extra_data case with
// its one change undone), so each refusal is for the case's own change.
func TestProfile1RefusedGenesisN42rs(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(profile1N42rsRefusedDir, "refused_*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	if len(files) != len(profile1N42rsRefusals) {
		t.Fatalf("%d n42-rs refused cases, %d expectations", len(files), len(profile1N42rsRefusals))
	}

	raw, err := os.ReadFile(filepath.Join(profile1N42rsRefusedDir, "refused_extra_data.json"))
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	base["extraData"] = "0x"
	baseRaw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(baseRaw, new(conf.Genesis)); err != nil {
		t.Fatalf("n42-rs base genesis refused: %v", err)
	}

	for _, f := range files {
		name := filepath.Base(f)
		want, ok := profile1N42rsRefusals[name]
		if !ok {
			t.Errorf("%s: no expected error", name)
			continue
		}
		_, err := loadProfile1Genesis(t, f)
		if err == nil {
			t.Errorf("%s: accepted, want refusal containing %q", name, want)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %q does not contain %q", name, err, want)
		}
	}
}
