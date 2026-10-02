// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package conf

import "testing"

// TestDefaultConstructors exercises the zero-argument Default*Cfg
// constructors across the subsystem config files. These were previously
// untested (0% coverage); the goal here is to pin their documented
// defaults and catch accidental zero-value regressions.
func TestDefaultConstructors(t *testing.T) {
	if got := DefaultAICfg(); got.Wallet.Enabled {
		t.Errorf("DefaultAICfg: wallet should be disabled by default")
	}
	_ = DefaultBeaconCfg()
	_ = DefaultComputeCfg()
	_ = DefaultCoprocessorCfg()
	_ = DefaultDeferredExecConfig()
	_ = DefaultDevConfig()
	_ = DefaultEthELCfg()
	if got := DefaultGraphQLCfg(); got.Endpoint != "/graphql" || got.Enabled {
		t.Errorf("DefaultGraphQLCfg = %+v, want disabled with /graphql endpoint", got)
	}
	if got := DefaultHistoryExpiryConfig(); got.Enable {
		t.Errorf("DefaultHistoryExpiryConfig should be disabled by default")
	}
	_ = DefaultIngestCfg()
	_ = DefaultMCPCfg()
	if got := DefaultMessagingCfg(); got.Enabled {
		t.Errorf("DefaultMessagingCfg should be disabled by default")
	}
	_ = DefaultMobileVerifyCfg()
	_ = DefaultNotifyCfg()
	_ = DefaultStorageCfg()
	_ = DefaultTileConfig()
	_ = DefaultTorrentSyncCfg()
	_ = DefaultWeb3GatewayCfg()
}

func TestComputeCfgValidate(t *testing.T) {
	c := DefaultComputeCfg()
	if err := c.Validate(); err != nil {
		t.Fatalf("default compute config should validate: %v", err)
	}

	bad := c
	bad.WASMEnabled = true
	bad.WASMMaxMemoryMB = 0
	if err := bad.Validate(); err == nil {
		t.Error("expected error for zero WASMMaxMemoryMB when WASM enabled")
	}
	bad2 := c
	bad2.WASMEnabled = true
	bad2.WASMMaxExecTimeSec = 0
	if err := bad2.Validate(); err == nil {
		t.Error("expected error for zero WASMMaxExecTimeSec when WASM enabled")
	}
	bad3 := c
	bad3.WASMEnabled = true
	bad3.WASMGasMultiplier = 0
	if err := bad3.Validate(); err == nil {
		t.Error("expected error for zero WASMGasMultiplier when WASM enabled")
	}
	bad4 := c
	bad4.BatchEnabled = true
	bad4.MaxJobTasks = 0
	if err := bad4.Validate(); err == nil {
		t.Error("expected error for zero MaxJobTasks when batch enabled")
	}
	bad5 := c
	bad5.BatchEnabled = true
	bad5.MaxParallelTasks = 0
	if err := bad5.Validate(); err == nil {
		t.Error("expected error for zero MaxParallelTasks when batch enabled")
	}
}

func TestCoprocessorCfgValidate(t *testing.T) {
	c := DefaultCoprocessorCfg()
	if err := c.Validate(); err != nil {
		t.Fatalf("default coprocessor config should validate: %v", err)
	}

	disabled := c
	disabled.Enabled = false
	disabled.MaxConcurrentTasks = -1
	if err := disabled.Validate(); err != nil {
		t.Errorf("disabled config should always validate: %v", err)
	}

	enabled := c
	enabled.Enabled = true
	enabled.MaxConcurrentTasks = 0
	if err := enabled.Validate(); err == nil {
		t.Error("expected error for zero MaxConcurrentTasks")
	}

	enabled2 := c
	enabled2.Enabled = true
	enabled2.TaskTimeoutSec = 0
	if err := enabled2.Validate(); err == nil {
		t.Error("expected error for zero TaskTimeoutSec")
	}

	enabled3 := c
	enabled3.Enabled = true
	enabled3.ProviderEnabled = true
	enabled3.ProviderAddress = ""
	if err := enabled3.Validate(); err == nil {
		t.Error("expected error for missing ProviderAddress")
	}

	enabled4 := c
	enabled4.Enabled = true
	enabled4.ProviderEnabled = true
	enabled4.ProviderAddress = "0xabc"
	enabled4.ProviderStakeWei = 0
	if err := enabled4.Validate(); err == nil {
		t.Error("expected error for zero ProviderStakeWei")
	}

	enabled5 := c
	enabled5.Enabled = true
	enabled5.ProviderEnabled = true
	enabled5.ProviderAddress = "0xabc"
	enabled5.ProviderEntryPoint = ""
	if err := enabled5.Validate(); err == nil {
		t.Error("expected error for missing ProviderEntryPoint")
	}
}

func TestCheckpointConfig(t *testing.T) {
	c := &CheckpointConfig{}
	if c.IsEnabled() {
		t.Error("disabled checkpoint config should not be enabled")
	}
	if err := c.Validate(); err != nil {
		t.Errorf("disabled checkpoint config should validate: %v", err)
	}

	c.Enable = true
	if err := c.Validate(); err == nil {
		t.Error("expected error for zero block number")
	}

	c.BlockNumber = 100
	if err := c.Validate(); err == nil {
		t.Error("expected error for empty block hash")
	}

	c.BlockHash = "deadbeef"
	if err := c.Validate(); err == nil {
		t.Error("expected error for missing 0x prefix")
	}

	c.BlockHash = "0xdead"
	if err := c.Validate(); err == nil {
		t.Error("expected error for wrong-length hash")
	}

	validHash := "0x" + "11" + "aa"
	// Build an exact 64-hex-char hash.
	hex64 := ""
	for i := 0; i < 64; i++ {
		hex64 += "a"
	}
	c.BlockHash = "0x" + hex64
	if err := c.Validate(); err != nil {
		t.Errorf("valid checkpoint should validate: %v", err)
	}
	if !c.IsEnabled() {
		t.Error("fully configured + enabled checkpoint should report enabled")
	}
	_ = validHash
}

func TestLatestCheckpoint(t *testing.T) {
	if got := LatestCheckpoint("unknown-chain"); got != nil {
		t.Errorf("LatestCheckpoint(unknown) = %v, want nil", got)
	}
	if got := LatestCheckpoint("mainnet"); got != nil {
		t.Errorf("LatestCheckpoint(mainnet) with empty list = %v, want nil", got)
	}
	if got := LatestCheckpoint("testnet"); got != nil {
		t.Errorf("LatestCheckpoint(testnet) with empty list = %v, want nil", got)
	}
}

func TestEncryptedPoolCfg(t *testing.T) {
	c := &EncryptedPoolCfg{}
	if c.IsEnabled() {
		t.Error("default encrypted pool cfg should be disabled")
	}
	if err := c.Validate(); err != nil {
		t.Errorf("disabled config should validate: %v", err)
	}

	c.Enabled = true
	if err := c.Validate(); err != nil {
		t.Errorf("enabled config with zero fields should apply defaults and validate: %v", err)
	}
	if c.MaxSize == 0 {
		t.Error("Validate should have filled MaxSize default")
	}
	if c.BlockWindow == 0 {
		t.Error("Validate should have filled BlockWindow default")
	}

	tooBig := &EncryptedPoolCfg{Enabled: true, MaxSize: MaxEncryptedPoolMaxSize + 1}
	if err := tooBig.Validate(); err == nil {
		t.Error("expected error for MaxSize exceeding maximum")
	}

	tooSmall := &EncryptedPoolCfg{Enabled: true, MaxSize: 1}
	if err := tooSmall.Validate(); err == nil {
		t.Error("expected error for MaxSize below minimum")
	}

	bigWindow := &EncryptedPoolCfg{Enabled: true, BlockWindow: MaxEncryptedPoolBlockWindow + 1}
	if err := bigWindow.Validate(); err == nil {
		t.Error("expected error for BlockWindow exceeding maximum")
	}
}

func TestGraphQLCfgEffectiveEndpoint(t *testing.T) {
	c := &GraphQLCfg{}
	if got := c.EffectiveEndpoint(); got != "/graphql" {
		t.Errorf("EffectiveEndpoint() = %q, want /graphql", got)
	}
	c.Endpoint = "/custom"
	if got := c.EffectiveEndpoint(); got != "/custom" {
		t.Errorf("EffectiveEndpoint() = %q, want /custom", got)
	}
}

func TestHistoryExpiryConfig(t *testing.T) {
	c := &HistoryExpiryConfig{}
	if c.IsEnabled() {
		t.Error("default config should not be enabled")
	}
	c.Enable = true
	c.Retention = 10
	if !c.IsEnabled() {
		t.Error("enabled config with positive retention should be enabled")
	}

	zero := &HistoryExpiryConfig{}
	zero.Validate()
	if zero.Retention == 0 || zero.BatchLimit == 0 || zero.Interval == 0 {
		t.Errorf("Validate should fill defaults: %+v", zero)
	}
}

func TestMEVBoostCfgValidateAndParse(t *testing.T) {
	c := &MEVBoostCfg{}
	if c.IsEnabled() {
		t.Error("disabled config should not be enabled")
	}
	if err := c.Validate(); err != nil {
		t.Errorf("disabled config should validate: %v", err)
	}

	noRelays := &MEVBoostCfg{Enabled: true}
	if err := noRelays.Validate(); err == nil {
		t.Error("expected error for no relay URLs")
	}

	tooMany := &MEVBoostCfg{Enabled: true}
	for i := 0; i < MaxRelayURLs+1; i++ {
		tooMany.RelayURLs = append(tooMany.RelayURLs, "https://relay.example/ "+string(rune('a'+i%26)))
	}
	if err := tooMany.Validate(); err == nil {
		t.Error("expected error for too many relay URLs")
	}

	emptyURL := &MEVBoostCfg{Enabled: true, RelayURLs: []string{"   "}}
	if err := emptyURL.Validate(); err == nil {
		t.Error("expected error for empty relay URL")
	}

	badURL := &MEVBoostCfg{Enabled: true, RelayURLs: []string{"://bad-url"}}
	if err := badURL.Validate(); err == nil {
		t.Error("expected error for malformed URL")
	}

	badScheme := &MEVBoostCfg{Enabled: true, RelayURLs: []string{"ftp://relay.example"}}
	if err := badScheme.Validate(); err == nil {
		t.Error("expected error for non-http(s) scheme")
	}

	noHost := &MEVBoostCfg{Enabled: true, RelayURLs: []string{"https://"}}
	if err := noHost.Validate(); err == nil {
		t.Error("expected error for missing host")
	}

	dup := &MEVBoostCfg{Enabled: true, RelayURLs: []string{"https://relay.example", "https://relay.example"}}
	if err := dup.Validate(); err == nil {
		t.Error("expected error for duplicate relay URL")
	}

	good := &MEVBoostCfg{Enabled: true, RelayURLs: []string{"https://relay.example/"}}
	if err := good.Validate(); err != nil {
		t.Errorf("valid config should pass: %v", err)
	}
	if good.RelayURLs[0] != "https://relay.example" {
		t.Errorf("Validate should trim trailing slash: %q", good.RelayURLs[0])
	}
	if good.MinBidWei != DefaultMinBidWei {
		t.Errorf("Validate should default MinBidWei: %q", good.MinBidWei)
	}
	if !good.IsEnabled() {
		t.Error("valid enabled config with relays should report enabled")
	}

	badBid := &MEVBoostCfg{Enabled: true, RelayURLs: []string{"https://relay.example"}, MinBidWei: "not-a-number"}
	if err := badBid.Validate(); err == nil {
		t.Error("expected error for unparseable MinBidWei")
	}

	zeroBid := &MEVBoostCfg{MinBidWei: "0"}
	v, err := zeroBid.ParseMinBidWei()
	if err != nil || v.Sign() != 0 {
		t.Errorf("ParseMinBidWei(\"0\") = %v, %v", v, err)
	}

	nonZero := &MEVBoostCfg{MinBidWei: "123456"}
	v2, err := nonZero.ParseMinBidWei()
	if err != nil || v2.Uint64() != 123456 {
		t.Errorf("ParseMinBidWei(123456) = %v, %v", v2, err)
	}
}

func TestPeerDASConfig(t *testing.T) {
	c := &PeerDASConfig{}
	if c.IsEnabled() {
		t.Error("default config should not be enabled")
	}
	if err := c.Validate(); err != nil {
		t.Errorf("disabled config should validate: %v", err)
	}

	c.Enable = true
	if err := c.Validate(); err != nil {
		t.Errorf("enabled config should fill defaults and validate: %v", err)
	}
	if c.CustodyCount == 0 || c.SampleCount == 0 {
		t.Errorf("Validate should fill defaults: %+v", c)
	}

	tooMuchCustody := &PeerDASConfig{Enable: true, CustodyCount: MaxPeerDASCustodyCount + 1}
	if err := tooMuchCustody.Validate(); err == nil {
		t.Error("expected error for CustodyCount exceeding max")
	}

	tooMuchSample := &PeerDASConfig{Enable: true, SampleCount: MaxPeerDASSampleCount + 1}
	if err := tooMuchSample.Validate(); err == nil {
		t.Error("expected error for SampleCount exceeding max")
	}
}

func TestPruneConfigIsEnabled(t *testing.T) {
	c := &PruneConfig{Mode: PruneModeFull}
	if !c.IsEnabled() {
		t.Error("PruneModeFull should report enabled")
	}
	c.Mode = "archive"
	if c.IsEnabled() {
		t.Error("non-full mode should report disabled")
	}
}

func TestSnapSyncConfig(t *testing.T) {
	c := &SnapSyncConfig{}
	if c.IsEnabled() {
		t.Error("default config should not be enabled")
	}
	if err := c.Validate(); err != nil {
		t.Errorf("disabled config should validate: %v", err)
	}

	c.Enable = true
	if err := c.Validate(); err != nil {
		t.Errorf("enabled config should validate with defaults filled: %v", err)
	}
	if !c.IsEnabled() {
		t.Error("enabled config should report enabled")
	}

	tooFar := &SnapSyncConfig{Enable: true, PivotDistance: 257}
	if err := tooFar.Validate(); err == nil {
		t.Error("expected error for PivotDistance exceeding max")
	}

	tooConcurrent := &SnapSyncConfig{Enable: true, MaxConcurrency: 65}
	if err := tooConcurrent.Validate(); err == nil {
		t.Error("expected error for MaxConcurrency exceeding max")
	}
}

func TestSnapshotAccelConfigValidate(t *testing.T) {
	c := &SnapshotAccelConfig{}
	if err := c.Validate(); err != nil {
		t.Errorf("disabled config should validate: %v", err)
	}

	c.Enable = true
	if err := c.Validate(); err != nil {
		t.Errorf("enabled config should fill defaults and validate: %v", err)
	}
	if c.MaxDiffLayers == 0 || c.WarmupAccounts == 0 {
		t.Errorf("Validate should fill defaults: %+v", c)
	}

	badLayers := &SnapshotAccelConfig{Enable: true, MaxDiffLayers: 5000}
	if err := badLayers.Validate(); err == nil {
		t.Error("expected error for MaxDiffLayers out of range")
	}

	negLayers := &SnapshotAccelConfig{Enable: true, MaxDiffLayers: -1}
	if err := negLayers.Validate(); err == nil {
		t.Error("expected error for negative MaxDiffLayers")
	}

	negAccounts := &SnapshotAccelConfig{Enable: true, WarmupAccounts: -1}
	if err := negAccounts.Validate(); err == nil {
		t.Error("expected error for negative WarmupAccounts")
	}
}

func TestNodeConfigKeyDir(t *testing.T) {
	abs := &NodeConfig{KeyStoreDir: "/abs/path/keystore"}
	got, err := abs.KeyDirConfig()
	if err != nil || got != "/abs/path/keystore" {
		t.Errorf("KeyDirConfig(abs) = %q, %v", got, err)
	}

	fromDataDir := &NodeConfig{DataDir: "/data/blockchain"}
	got, err = fromDataDir.KeyDirConfig()
	if err != nil || got != "/data/blockchain/keystore" {
		t.Errorf("KeyDirConfig(datadir) = %q, %v", got, err)
	}

	relative := &NodeConfig{KeyStoreDir: "relative-keystore"}
	got, err = relative.KeyDirConfig()
	if err != nil || got == "" {
		t.Errorf("KeyDirConfig(relative) = %q, %v", got, err)
	}

	empty := &NodeConfig{}
	got, err = empty.KeyDirConfig()
	if err != nil || got != "" {
		t.Errorf("KeyDirConfig(empty) = %q, %v, want empty string", got, err)
	}
}

func TestNodeConfigGetKeyStoreDirEphemeral(t *testing.T) {
	cfg := &NodeConfig{}
	dir, ephemeral, err := getKeyStoreDir(cfg)
	if err != nil {
		t.Fatalf("getKeyStoreDir failed: %v", err)
	}
	if !ephemeral {
		t.Error("expected ephemeral keystore dir for empty config")
	}
	if dir == "" {
		t.Error("expected non-empty ephemeral dir")
	}
}

func TestNodeConfigExtRPCEnabled(t *testing.T) {
	c := &NodeConfig{}
	if c.ExtRPCEnabled() {
		t.Error("no hosts configured should report disabled")
	}
	c.HTTPHost = "127.0.0.1"
	if !c.ExtRPCEnabled() {
		t.Error("HTTPHost set should report enabled")
	}
	c2 := &NodeConfig{WSHost: "127.0.0.1"}
	if !c2.ExtRPCEnabled() {
		t.Error("WSHost set should report enabled")
	}
}
