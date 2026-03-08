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
	"errors"
	"time"
)

const (
	DefaultHTTPPort    = "8545"
	DefaultWSPort      = "8546"
	DefaultAuthRPCPort = 8551
	DefaultP2PPort     = 61016

	DefaultDBCacheSize = 512        // MB
	DefaultGasPrice    = 1000000000 // 1 Gwei
	DefaultSyncMode    = "full"

	DefaultBlockCacheLimit    = 512
	DefaultReceiptsCacheLimit = 256
	DefaultHeaderCacheLimit   = 1024
	DefaultTdCacheLimit       = 512
	DefaultNumberCacheLimit   = 2048
)

var (
	ErrMissingChainConfig   = errors.New("chain configuration is required")
	ErrInvalidDataDir       = errors.New("data directory is required for non-ephemeral nodes")
	ErrInvalidHTTPPort      = errors.New("invalid HTTP port")
	ErrInvalidWSPort        = errors.New("invalid WebSocket port")
	ErrInvalidP2PPort       = errors.New("invalid P2P port")
	ErrInvalidEtherbase     = errors.New("etherbase is required for mining")
	ErrInvalidGasPrice      = errors.New("gas price must be positive")
	ErrInvalidTxGenInterval = errors.New("txgen interval must be positive")
)

// ApplyDefaults fills in missing configuration values with sensible defaults.
func ApplyDefaults(cfg *Config) {
	if cfg.NodeCfg.HTTPPort == "" {
		cfg.NodeCfg.HTTPPort = DefaultHTTPPort
	}
	if cfg.NodeCfg.WSPort == "" {
		cfg.NodeCfg.WSPort = DefaultWSPort
	}
	if cfg.NodeCfg.AuthPort == 0 {
		cfg.NodeCfg.AuthPort = DefaultAuthRPCPort
	}

	if cfg.P2PCfg != nil {
		if cfg.P2PCfg.TCPPort == 0 {
			cfg.P2PCfg.TCPPort = DefaultP2PPort
		}
		if cfg.P2PCfg.UDPPort == 0 {
			cfg.P2PCfg.UDPPort = DefaultP2PPort
		}
	}

	if cfg.GPO.Blocks == 0 {
		cfg.GPO.Blocks = 20
	}
	if cfg.GPO.Percentile == 0 {
		cfg.GPO.Percentile = 60
	}

	// Pruning defaults
	if cfg.PruneCfg.Mode == "" {
		cfg.PruneCfg.Mode = PruneModeArchive
	}
	if cfg.PruneCfg.BlockRetention == 0 {
		cfg.PruneCfg.BlockRetention = DefaultBlockRetention
	}
	if cfg.PruneCfg.PruneInterval == 0 {
		cfg.PruneCfg.PruneInterval = DefaultPruneInterval
	}
	if cfg.PruneCfg.PruneBatchLimit == 0 {
		cfg.PruneCfg.PruneBatchLimit = DefaultPruneBatchLimit
	}

	// Snap sync defaults
	if cfg.SnapSyncCfg.PivotDistance == 0 {
		cfg.SnapSyncCfg.PivotDistance = DefaultSnapPivotDistance
	}
	if cfg.SnapSyncCfg.MaxConcurrency == 0 {
		cfg.SnapSyncCfg.MaxConcurrency = DefaultSnapMaxConcurrency
	}
	if cfg.SnapSyncCfg.MaxBytesPerReq == 0 {
		cfg.SnapSyncCfg.MaxBytesPerReq = DefaultSnapMaxBytesPerReq
	}
	if cfg.SnapSyncCfg.MinSnapPeers == 0 {
		cfg.SnapSyncCfg.MinSnapPeers = DefaultSnapMinPeers
	}
	if cfg.SnapSyncCfg.SyncThreshold == 0 {
		cfg.SnapSyncCfg.SyncThreshold = DefaultSnapSyncThreshold
	}

	if cfg.DevCfg.TxGenEnabled {
		if cfg.DevCfg.TxGenInterval == 0 {
			cfg.DevCfg.TxGenInterval = time.Second
		}
		if cfg.DevCfg.TxGenMaxPerBlock == 0 {
			cfg.DevCfg.TxGenMaxPerBlock = 10
		}
		if cfg.DevCfg.TxGenGasPrice == 0 {
			cfg.DevCfg.TxGenGasPrice = DefaultGasPrice
		}
	}
}

// Validate checks the configuration for errors.
func Validate(cfg *Config) error {
	if cfg.ChainCfg == nil && cfg.NodeCfg.Chain != "private" {
		return ErrMissingChainConfig
	}

	if cfg.NodeCfg.Miner {
		if cfg.Miner.Etherbase == "" {
			return ErrInvalidEtherbase
		}
		if cfg.Miner.GasPrice != nil && cfg.Miner.GasPrice.Sign() < 0 {
			return ErrInvalidGasPrice
		}
	}

	if cfg.DevCfg.TxGenEnabled {
		if cfg.DevCfg.TxGenInterval <= 0 {
			return ErrInvalidTxGenInterval
		}
	}

	return nil
}

// ValidateAndApplyDefaults applies defaults and then validates the configuration.
func ValidateAndApplyDefaults(cfg *Config) error {
	ApplyDefaults(cfg)
	return Validate(cfg)
}
