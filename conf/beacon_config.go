// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package conf

// BeaconCfg holds Caplin (consensus layer) options for n42-el mode.
type BeaconCfg struct {
	// Enabled is the master switch. When false (default), no Caplin code path
	// is executed and no extra goroutines, sockets, or DBs are created.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Network selects the Ethereum beacon-chain network preset.
	// Valid values: "mainnet", "sepolia", "holesky", "hoodi".
	Network string `json:"network" yaml:"network"`

	// DataDir is the directory where Caplin keeps its independent MDBX
	// instance and any blob/snapshot data. Defaults to <ethexec-datadir>/caplin
	// when empty.
	DataDir string `json:"datadir" yaml:"datadir"`

	// SentinelPort is the libp2p TCP port for Caplin's standalone P2P host.
	// It is independent of the EL P2P port to avoid topic / scoring collisions.
	SentinelPort int `json:"sentinel_port" yaml:"sentinel_port"`

	// SentinelDiscoveryPort is the discv5 UDP port for Caplin sentinel.
	SentinelDiscoveryPort int `json:"sentinel_discovery_port" yaml:"sentinel_discovery_port"`

	// BootnodesENR are extra ENR bootnodes appended to the network preset.
	BootnodesENR []string `json:"bootnodes_enr" yaml:"bootnodes_enr"`

	// CheckpointSyncURL is an optional beacon checkpoint-sync endpoint used
	// to start from a recent finalized state instead of genesis.
	CheckpointSyncURL string `json:"checkpoint_sync_url" yaml:"checkpoint_sync_url"`

	// BeaconAPIEnabled exposes the standard /eth/v1/beacon/* REST API.
	// Disabled by default; planned for a later phase.
	BeaconAPIEnabled bool `json:"beacon_api_enabled" yaml:"beacon_api_enabled"`

	// BeaconAPIPort is the port for the optional Beacon REST API.
	BeaconAPIPort int `json:"beacon_api_port" yaml:"beacon_api_port"`
}

// DefaultBeaconCfg returns Caplin defaults: completely disabled.
func DefaultBeaconCfg() BeaconCfg {
	return BeaconCfg{
		Enabled:               false,
		Network:               "mainnet",
		DataDir:               "",
		SentinelPort:          9000,
		SentinelDiscoveryPort: 9000,
		BootnodesENR:          nil,
		CheckpointSyncURL:     "",
		BeaconAPIEnabled:      false,
		BeaconAPIPort:         5052,
	}
}
