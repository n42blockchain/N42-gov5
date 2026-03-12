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
	"os"
	"path/filepath"
)

const datadirDefaultKeyStore = "keystore"

type NodeConfig struct {
	NodePrivate string `json:"private" yaml:"private"`
	HTTP        bool   `json:"http" yaml:"http" `
	HTTPHost    string `json:"http_host" yaml:"http_host" `
	HTTPPort    string `json:"http_port" yaml:"http_port"`
	HTTPApi     string `json:"http_api" yaml:"http_api"`
	HTTPCors string `json:"http_cors" yaml:"http_cors"`

	WS     bool   `json:"ws" yaml:"ws" `
	WSHost string `json:"ws_host" yaml:"ws_host" `
	WSPort string `json:"ws_port" yaml:"ws_port"`
	WSApi  string `json:"ws_api" yaml:"ws_api"`
	WSOrigins        string `json:"ws_origins,omitempty" yaml:"ws_origins,omitempty"`
	IPCPath          string `json:"ipc_path" yaml:"ipc_path"`
	DataDir          string `json:"data_dir" yaml:"data_dir"`
	MinFreeDiskSpace int    `json:"min_free_disk_space" yaml:"min_free_disk_space"`
	Chain            string `json:"chain" yaml:"chain"`
	Miner            bool   `json:"miner" yaml:"miner"`

	AuthRPC          bool     `json:"auth_rpc" yaml:"auth_rpc"`
	AuthAddr         string   `json:"auth_addr" yaml:"auth_addr"`
	AuthPort         int      `json:"auth_port" yaml:"auth_port"`
	AuthVirtualHosts []string `json:"auth_virtual_hosts" yaml:"auth_virtual_hosts"`
	JWTSecret        string   `json:"jwt_secret" yaml:"jwt_secret"`

	// KeyStoreDir is the folder that contains private keys.
	// If empty, defaults to DataDir/keystore; if DataDir is also empty,
	// an ephemeral directory is created and destroyed when the node stops.
	KeyStoreDir string `json:"key_store_dir" yaml:"key_store_dir"`

	// Rate limiting configuration
	HTTPRateLimit      int `json:"http_rate_limit" yaml:"http_rate_limit"`           // Max requests per second per IP (0 = disabled)
	HTTPRateLimitBurst int `json:"http_rate_limit_burst" yaml:"http_rate_limit_burst"` // Max burst size for rate limiting

	// ParallelEVM enables Block-STM parallel transaction execution.
	// When enabled, blocks with >4 transactions are executed in parallel
	// using optimistic concurrency with automatic conflict detection.
	ParallelEVM bool `json:"parallel_evm" yaml:"parallel_evm"`

	// Prefetch enables state prefetching before block execution.
	// Pre-loads sender, recipient, and access list state into cache.
	Prefetch bool `json:"prefetch" yaml:"prefetch"`

	// JMTCommitment enables the Jellyfish Merkle Tree state commitment.
	// When enabled, Header.Root is computed from a Blake3-based JMT
	// instead of the legacy incremental Keccak hash.
	// Requires offline migration for existing databases (cmd/n42 migrate-jmt).
	JMTCommitment bool `json:"jmt_commitment" yaml:"jmt_commitment"`

	// AncientDB enables the freezer/ancient DB for moving old block data
	// to append-only flat files, reducing hot MDBX database size.
	AncientDB bool `json:"ancient_db" yaml:"ancient_db"`

	// AncientFreezeThreshold is the number of blocks behind chain head
	// before blocks are eligible for freezing. Default: 90000.
	AncientFreezeThreshold uint64 `json:"ancient_freeze_threshold,omitempty" yaml:"ancient_freeze_threshold,omitempty"`

	ExternalSigner        string `json:"external_signer" yaml:"external_signer"`
	UseLightweightKDF     bool   `json:"use_lightweight_kdf" yaml:"use_lightweight_kdf"`
	InsecureUnlockAllowed bool   `json:"insecure_unlock_allowed" yaml:"insecure_unlock_allowed"`
	PasswordFile          string `json:"password_file" yaml:"password_file"`
}

// KeyDirConfig determines the settings for the key directory.
func (c *NodeConfig) KeyDirConfig() (string, error) {
	switch {
	case filepath.IsAbs(c.KeyStoreDir):
		return c.KeyStoreDir, nil
	case c.DataDir != "" && c.KeyStoreDir == "":
		return filepath.Join(c.DataDir, datadirDefaultKeyStore), nil
	case c.KeyStoreDir != "":
		return filepath.Abs(c.KeyStoreDir)
	default:
		return "", nil
	}
}

// getKeyStoreDir retrieves the key directory and will create
// an ephemeral one if necessary.
func getKeyStoreDir(conf *NodeConfig) (keydir string, isEphemeral bool, err error) {
	keydir, err = conf.KeyDirConfig()
	if err != nil {
		return "", false, err
	}
	if keydir == "" {
		keydir, err = os.MkdirTemp("", "N42-keystore")
		if err != nil {
			return "", false, err
		}
		isEphemeral = true
	}
	if err = os.MkdirAll(keydir, 0700); err != nil {
		return "", false, err
	}
	return keydir, isEphemeral, nil
}

// ExtRPCEnabled returns the indicator whether node enables the external
// RPC(http, ws or graphql).
func (c *NodeConfig) ExtRPCEnabled() bool {
	return c.HTTPHost != "" || c.WSHost != ""
}
