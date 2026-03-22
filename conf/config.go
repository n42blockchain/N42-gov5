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

package conf

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v2"

	"github.com/n42blockchain/N42/params"
)

type Config struct {
	NodeCfg     NodeConfig          `json:"node" yaml:"node"`
	NetworkCfg  NetWorkConfig       `json:"network" yaml:"network"`
	LoggerCfg   LoggerConfig        `json:"logger" yaml:"logger"`
	DatabaseCfg DatabaseConfig      `json:"database" yaml:"database"`
	PprofCfg    PprofConfig         `json:"pprof" yaml:"pprof"`
	ChainCfg    *params.ChainConfig `json:"chain" yaml:"chain"`
	AccountCfg  AccountConfig       `json:"account" yaml:"account"`
	MetricsCfg  MetricsConfig       `json:"metrics" yaml:"metrics"`
	P2PCfg      *P2PConfig          `json:"p2p" yaml:"p2p"`
	GPO      GpoConfig   `json:"gpo" yaml:"gpo"`
	Miner    MinerConfig `json:"miner" yaml:"miner"`
	DevCfg   DevConfig   `json:"dev" yaml:"dev"`
	PruneCfg         PruneConfig         `json:"prune" yaml:"prune"`
	HistoryExpiryCfg HistoryExpiryConfig `json:"history_expiry" yaml:"history_expiry"`
	SnapSyncCfg    SnapSyncConfig   `json:"snap_sync" yaml:"snap_sync"`
	CheckpointCfg  CheckpointConfig `json:"checkpoint" yaml:"checkpoint"`
	SnapshotCfg    SnapshotConfig   `json:"snapshot" yaml:"snapshot"`
	LayeredDBCfg   LayeredDBConfig  `json:"layered_db" yaml:"layered_db"`
	BundlerCfg     BundlerConfig    `json:"bundler" yaml:"bundler"`
	TracingCfg     TracingConfig    `json:"tracing" yaml:"tracing"`
	PeerDASCfg       PeerDASConfig       `json:"peerdas" yaml:"peerdas"`
	SnapshotAccelCfg SnapshotAccelConfig `json:"snapshot_accel" yaml:"snapshot_accel"`
	MCPCfg           MCPCfg              `json:"mcp" yaml:"mcp"`
	GraphQL          GraphQLCfg          `json:"graphql" yaml:"graphql"`
	MEVBoost         MEVBoostCfg         `json:"mev_boost" yaml:"mev_boost"`
	EncryptedPool    EncryptedPoolCfg    `json:"encrypted_pool" yaml:"encrypted_pool"`
	ZKProverCfg      ZKProverCfg         `json:"zkprover" yaml:"zkprover"`
	DeferredExec     DeferredExecConfig  `json:"deferred_exec" yaml:"deferred_exec"`

	// Web3 gateway
	Web3GatewayCfg   Web3GatewayCfg   `json:"web3_gateway" yaml:"web3_gateway"`

	// Distributed infrastructure
	ComputeCfg       ComputeCfg       `json:"compute" yaml:"compute"`
	CoprocessorCfg   CoprocessorCfg   `json:"coprocessor" yaml:"coprocessor"`
	MessagingCfg     MessagingCfg     `json:"messaging" yaml:"messaging"`
	StorageCfg       StorageCfg       `json:"storage" yaml:"storage"`
	NotifyCfg        NotifyCfg        `json:"notify" yaml:"notify"`
	TorrentDistCfg   TorrentDistCfg   `json:"torrent_dist" yaml:"torrent_dist"`
	Ed2kCfg          Ed2kCfg          `json:"ed2k" yaml:"ed2k"`

	// AI infrastructure (unified)
	AICfg            AICfg            `json:"ai" yaml:"ai"`

	// Ingest server (stress testing)
	IngestCfg        IngestCfg        `json:"ingest" yaml:"ingest"`
}

func SaveConfigToFile(file string, config Config) error {
	if file == "" {
		file = "./config2.yaml"
	}

	fd, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer fd.Close()

	enc := yaml.NewEncoder(fd)
	defer enc.Close()
	return enc.Encode(config)
}

func LoadConfigFromFile(file string, config *Config) error {
	if file == "" {
		return fmt.Errorf("failed to load config from file: file path is empty")
	}

	fd, err := os.Open(file)
	if err != nil {
		return err
	}
	defer fd.Close()

	return yaml.NewDecoder(fd).Decode(config)
}
