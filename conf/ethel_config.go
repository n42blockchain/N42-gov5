// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Root configuration for the cmd/eth-el execution-layer binary.
// EthELCfg bundles DataDir, Network preset plus storage, bootstrap,
// catch-up, Engine API, torrent, OtterSync and optional Caplin beacon
// sub-configs so the CLI flag loader wires one struct into the node.
// EthELStorageCfg tunes the chaindata MDBX environment (MapSize,
// PageSize) with Windows-aware defaults.

package conf

import "github.com/c2h5oh/datasize"

// EthELCfg is the root configuration for the eth-el node binary
// (cmd/eth-el). It bundles every sub-config the node needs so the cli flag
// loader has one place to assemble.
type EthELCfg struct {
	// DataDir is the working directory. Subdirectories: chaindata/, freezer/,
	// torrent/, caplin/.
	DataDir string `json:"datadir" yaml:"datadir"`

	// Network selects the chain spec. "mainnet", "sepolia", "holesky", "hoodi".
	Network string `json:"network" yaml:"network"`

	// Storage tuning for the chaindata MDBX environment.
	Storage EthELStorageCfg `json:"storage" yaml:"storage"`

	// Bootstrap controls the leaves-journal-driven bootstrap.
	Bootstrap BootstrapCfg `json:"bootstrap" yaml:"bootstrap"`

	// CatchUp controls block replay between bootstrap height and chain tip.
	CatchUp CatchUpCfg `json:"catch_up" yaml:"catch_up"`

	// EngineAPI controls the auth-RPC server that the CL talks to.
	EngineAPI EngineAPICfg `json:"engine_api" yaml:"engine_api"`

	// Torrent controls the BT downloader/seeder used to fetch leaves and
	// historical segments.
	Torrent TorrentDistCfg `json:"torrent" yaml:"torrent"`

	// TorrentSync controls OtterSync (BT-distributed chain segment import).
	TorrentSync TorrentSyncCfg `json:"torrent_sync" yaml:"torrent_sync"`

	// Beacon embeds the optional Caplin (consensus layer) configuration.
	// Honored only when the binary is built with -tags n42el.
	Beacon BeaconCfg `json:"beacon" yaml:"beacon"`
}

// EthELStorageCfg controls the on-disk MDBX environment.
type EthELStorageCfg struct {
	// MapSize is the maximum mmap reservation. Default 256 GiB on 64-bit
	// platforms; smaller on Windows where mmap reservation counts as commit.
	MapSize datasize.ByteSize `json:"map_size" yaml:"map_size"`

	// PageSize must be a power of two between 256 B and 64 KiB. 4 KiB matches
	// the OS page size on every supported platform.
	PageSize uint64 `json:"page_size" yaml:"page_size"`
}

// BootstrapCfg controls the one-shot leaves-based bootstrap.
type BootstrapCfg struct {
	// Enabled gates the bootstrap step. When false the node assumes the
	// chaindata MDBX already holds a populated PlainState.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Manifest is the URL or magnet link of the leaves-journal segment set
	// to download. Empty disables the BT fetch (caller must place files
	// under DataDir/leaves manually).
	Manifest string `json:"manifest" yaml:"manifest"`

	// EndBlock caps the rebuild range. 0 means rebuild every block in the
	// downloaded leaves journal.
	EndBlock uint64 `json:"end_block" yaml:"end_block"`
}

// CatchUpCfg controls the executor that runs after bootstrap.
type CatchUpCfg struct {
	// Manifest is the URL or file path of a segments manifest. Empty
	// means "no segment fetch, just run the executor on whatever is
	// already in the freezer".
	Manifest string `json:"manifest" yaml:"manifest"`

	// CommitInterval is the MDBX commit interval in blocks.
	CommitInterval uint64 `json:"commit_interval" yaml:"commit_interval"`

	// VerifyInterval is the state-root verification interval (0 disables).
	VerifyInterval uint64 `json:"verify_interval" yaml:"verify_interval"`
}

// EngineAPICfg controls the auth-RPC server (the CL talks to it).
type EngineAPICfg struct {
	// Enabled gates the entire Engine API listener.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Host:Port for the auth-RPC HTTP listener.
	Host string `json:"host" yaml:"host"`
	Port int    `json:"port" yaml:"port"`

	// JWTSecretPath is the file containing the 32-byte JWT shared secret.
	// Mandatory whenever Enabled is true (Engine API spec section 3.1).
	JWTSecretPath string `json:"jwt_secret_path" yaml:"jwt_secret_path"`

	// IPCEnabled exposes the same Engine API surface over a Unix socket /
	// Windows named pipe at IPCPath. Useful for in-machine CL setups.
	IPCEnabled bool   `json:"ipc_enabled" yaml:"ipc_enabled"`
	IPCPath    string `json:"ipc_path" yaml:"ipc_path"`
}

// DefaultEthELCfg returns sane defaults. Call this from cli flag setup, then
// overwrite individual fields from flags.
func DefaultEthELCfg() EthELCfg {
	return EthELCfg{
		Network: "mainnet",
		Storage: EthELStorageCfg{
			MapSize:  64 * datasize.GB,
			PageSize: 4096,
		},
		Bootstrap: BootstrapCfg{
			Enabled: true,
		},
		CatchUp: CatchUpCfg{
			CommitInterval: 10000,
			VerifyInterval: 0,
		},
		EngineAPI: EngineAPICfg{
			Enabled: true,
			Host:    "127.0.0.1",
			Port:    20014,
		},
		Torrent:     DefaultTorrentDistCfg(),
		TorrentSync: DefaultTorrentSyncCfg(),
		Beacon:      DefaultBeaconCfg(),
	}
}
