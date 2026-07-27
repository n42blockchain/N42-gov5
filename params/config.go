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
// Chain configuration registry and embedded chainspec loader.
// Declares ConsensusType constants, ChainConfig fork schedule, genesis
// wiring and helpers such as readChainSpec and EthMainnetGenesisJSON
// that resolve JSON specs from the embedded chainspecs FS. Drives fork
// activation for Homestead through Glamsterdam plus N42 extensions
// (PQPrecompilesTime, AIInferenceTime, ContentStoreTime, Randomness).

package params

import (
	"embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"path"
	"sort"
	"strconv"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/paths"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params/networkname"
)

//go:embed chainspecs
var chainspecs embed.FS

func readChainSpec(filename string) *ChainConfig {
	f, err := chainspecs.Open(filename)
	if err != nil {
		panic(fmt.Sprintf("Could not open chainspec for %s: %v", filename, err))
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	spec := &ChainConfig{}
	err = decoder.Decode(&spec)
	if err != nil {
		panic(fmt.Sprintf("Could not parse chainspec for %s: %v", filename, err))
	}
	return spec
}

// EthMainnetGenesisJSON returns the embedded Ethereum mainnet genesis JSON.
func EthMainnetGenesisJSON() []byte {
	data, err := chainspecs.ReadFile("chainspecs/eth_mainnet_genesis.json")
	if err != nil {
		panic(fmt.Sprintf("read embedded eth mainnet genesis: %v", err))
	}
	return data
}

// ---------------------------------------------------------------------------
// Consensus type constants
// ---------------------------------------------------------------------------

type ConsensusType string

const (
	AuRaConsensus     ConsensusType = "aura"
	EtHashConsensus   ConsensusType = "ethash"
	CliqueConsensus   ConsensusType = "clique"
	ParliaConsensus   ConsensusType = "parlia"
	BorConsensus      ConsensusType = "bor"
	AposConsensu      ConsensusType = "apos"
	HotStuffConsensus ConsensusType = "hotstuff"
	Faker             ConsensusType = "faker"
)

func (c ConsensusType) DisplayName() string {
	if c == AposConsensu {
		return "Mobile Consensus"
	}
	return string(c)
}

func (c ConsensusType) UsesSignerListGenesisExtraData() bool {
	return c == CliqueConsensus || c == AposConsensu
}

func (c ConsensusType) UsesLegacyGenesisTrieRoots() bool {
	return c == AposConsensu
}

func (c ConsensusType) UsesTimerDrivenSealing() bool {
	return c != HotStuffConsensus
}

func (c ConsensusType) UsesBeijingAggregateBodySignature() bool {
	return c != HotStuffConsensus
}

func (c ConsensusType) UsesBorTransferMode() bool {
	return c == BorConsensus
}

// ---------------------------------------------------------------------------
// Genesis hashes
// ---------------------------------------------------------------------------

var (
	// MainnetGenesisHash and TestnetGenesisHash are the DEPLOYED identities of
	// the two real networks. They no longer equal what WriteGenesisBlock
	// computes from the chainspec, because the header struct evolved after
	// those chains launched; the constant is what peers expect on the wire, so
	// it stays. See GenesisHashByChainName.
	MainnetGenesisHash = types.HexToHash("0x594aad383881f3af7a4e7ecfa0f07589f0211a9794bb4ff105ae13d1360e497f")
	TestnetGenesisHash = types.HexToHash("0x5c0555d9ec963f58c63112862294e7e4836b12802304c23f2ec480a8f55cc5bb")

	// The replay-built chains each carry their OWN genesis, computed from their
	// own chainspec and alloc. They used to share MainnetGenesisHash, which
	// collapsed their identities: the p2p fork digest is the first four bytes
	// of this value, so six distinct networks — including chainId 95 — all
	// advertised /n42/594aad38/ and their Status handshakes accepted each
	// other, because the handshake compares only the fork digest and carries no
	// chain id. TestGenesisHashesMatchChainspecs pins each of these against a
	// freshly computed genesis, so they cannot drift again.
	MainnetV2GenesisHash            = types.HexToHash("0x005d94ae40f0ec0b1cac522420a687d30f078e8d1717841e6a5e9f7257f873ea")
	MainnetMPTGenesisHash           = types.HexToHash("0x005d94ae40f0ec0b1cac522420a687d30f078e8d1717841e6a5e9f7257f873ea")
	MainnetV2StaggeredGenesisHash   = types.HexToHash("0x3f833433dcdd45938946ab7953aa35a926898de3ccee8b4fbe6c541d51e77f80")
	MainnetQMDBGenesisHash          = types.HexToHash("0x5fcf94b7a5e7e337005c4b6333904983d9e5aa97e950bf1b63d42fb0be81ee69")
	MainnetQMDBStaggeredGenesisHash = types.HexToHash("0xa2d2ff5d814552bb9a113b68ad7ed2b824fbb52caed42dbe573068845b57be99")
	QSEpochTestGenesisHash          = types.HexToHash("0xb0ff0c044f867741ab595d2756cc5cfb873cff42c6f2ebb913ac4a73a6ad5271")
)

// ---------------------------------------------------------------------------
// Pre-configured chain configs
// ---------------------------------------------------------------------------

var (
	MainnetChainConfig              = readChainSpec("chainspecs/mainnet.json")
	MainnetCompatChainConfig        = readChainSpec("chainspecs/mainnet_compat.json")         // backward-compatible (no Shanghai+), matches legacy genesis hash
	MainnetV2ChainConfig            = readChainSpec("chainspecs/mainnet_v2.json")             // all forks from genesis (replay-v2)
	MainnetV2StaggeredChainConfig   = readChainSpec("chainspecs/mainnet_v2_staggered.json")   // forks staggered at N42-block heights/times calendar-matched to real Ethereum mainnet activation dates (Shanghai/Cancun by block; Pectra/Osaka/Fusaka/Glamsterdam by real-world Unix time, since N42 block timestamps are real wall-clock)
	MainnetMPTChainConfig           = readChainSpec("chainspecs/mainnet_mpt.json")            // replay-v2 with ethereum-mpt state roots
	MainnetQMDBChainConfig          = readChainSpec("chainspecs/mainnet_qmdb.json")           // replay-v2 qmdb state roots + hotstuff live production
	MainnetQMDBStaggeredChainConfig = readChainSpec("chainspecs/mainnet_qmdb_staggered.json") // qmdb + hotstuff live production with the staggered (calendar-parity) fork schedule — 7-node live network base
	QSEpochTestChainConfig          = readChainSpec("chainspecs/qs_epoch_test.json")          // isolated reconfig test chain (chainId 95, epochLength 20)
	TestnetChainConfig              = readChainSpec("chainspecs/testnet.json")

	TestChainConfig = &ChainConfig{
		ChainID:               big.NewInt(1),
		Consensus:             EtHashConsensus,
		HomesteadBlock:        big.NewInt(0),
		DAOForkBlock:          nil,
		DAOForkSupport:        false,
		TangerineWhistleBlock: big.NewInt(0),
		TangerineWhistleHash:  types.Hash{},
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(0),
		MuirGlacierBlock:      big.NewInt(0),
		BerlinBlock:           big.NewInt(0),
		LondonBlock:           nil,
		ArrowGlacierBlock:     nil,
		Ethash:                new(EthashConfig),
		Clique:                nil,
	}
)

// ---------------------------------------------------------------------------
// ChainConfig
// ---------------------------------------------------------------------------

// ChainConfig is the core config which determines the blockchain settings.
//
// ChainConfig is stored in the database on a per block basis. This means
// that any network, identified by its genesis block, can have its own
// set of configuration options.
type ChainConfig struct {
	ChainName string
	ChainID   *big.Int `json:"chainId"` // chainId identifies the current chain and is used for replay protection

	Consensus ConsensusType `json:"consensus,omitempty"` // aura, ethash or clique

	// Block-based fork fields
	HomesteadBlock *big.Int `json:"homesteadBlock,omitempty"`

	DAOForkBlock   *big.Int `json:"daoForkBlock,omitempty"`
	DAOForkSupport bool     `json:"daoForkSupport,omitempty"`

	TangerineWhistleBlock *big.Int   `json:"eip150Block,omitempty"`
	TangerineWhistleHash  types.Hash `json:"eip150Hash,omitempty"`

	SpuriousDragonBlock *big.Int `json:"eip155Block,omitempty"`

	ByzantiumBlock      *big.Int `json:"byzantiumBlock,omitempty"`
	ConstantinopleBlock *big.Int `json:"constantinopleBlock,omitempty"`
	PetersburgBlock     *big.Int `json:"petersburgBlock,omitempty"`
	IstanbulBlock       *big.Int `json:"istanbulBlock,omitempty"`
	MuirGlacierBlock    *big.Int `json:"muirGlacierBlock,omitempty"`
	BerlinBlock         *big.Int `json:"berlinBlock,omitempty"`
	LondonBlock         *big.Int `json:"londonBlock,omitempty"`
	ArrowGlacierBlock   *big.Int `json:"arrowGlacierBlock,omitempty"`
	GrayGlacierBlock    *big.Int `json:"grayGlacierBlock,omitempty"`

	// EIP-3675: Upgrade consensus to Proof-of-Stake
	TerminalTotalDifficulty       *big.Int `json:"terminalTotalDifficulty,omitempty"`
	TerminalTotalDifficultyPassed bool     `json:"terminalTotalDifficultyPassed,omitempty"`
	MergeNetsplitBlock            *big.Int `json:"mergeNetsplitBlock,omitempty"`

	ShanghaiBlock    *big.Int `json:"shanghaiBlock,omitempty"`
	CancunBlock      *big.Int `json:"cancunBlock,omitempty"`
	ShardingForkTime *big.Int `json:"shardingForkTime,omitempty"`

	// Optional timestamp-based activation for Shanghai/Cancun.
	// Hive transition fixtures use timestamp schedules for these forks.
	ShanghaiTime *big.Int `json:"shanghaiTime,omitempty"`
	CancunTime   *big.Int `json:"cancunTime,omitempty"`

	// Timestamp-based fork fields
	PragueTime      *big.Int `json:"pragueTime,omitempty"`
	PectraTime      *big.Int `json:"pectraTime,omitempty"`
	OsakaTime       *big.Int `json:"osakaTime,omitempty"`
	BPO1Time        *big.Int `json:"bpo1Time,omitempty"`
	BPO2Time        *big.Int `json:"bpo2Time,omitempty"`
	BPO3Time        *big.Int `json:"bpo3Time,omitempty"`
	BPO4Time        *big.Int `json:"bpo4Time,omitempty"`
	BPO5Time        *big.Int `json:"bpo5Time,omitempty"`
	FusakaTime      *big.Int `json:"fusakaTime,omitempty"`
	GlamsterdamTime *big.Int `json:"glamsterdamTime,omitempty"` // EIP-7904 gas repricing

	// N42 extension: post-quantum precompile activation (independent of standard forks).
	// When set, PQ precompiles (Falcon 0x14, Dilithium2 0x15, Dilithium3 0x16, SQIsign 0x17)
	// become available at the specified timestamp. These are N42-specific extensions and
	// are NOT part of any standard Ethereum fork surface.
	PQPrecompilesTime *big.Int `json:"pqPrecompilesTime,omitempty"`

	// N42 extension: content-addressed storage precompile (0x0300).
	// When set, the CAS precompile becomes available for storing/loading
	// arbitrary data by content hash at the specified timestamp.
	ContentStoreTime *big.Int `json:"contentStoreTime,omitempty"`

	// N42 extension: AI inference precompile (0x0301).
	// When set, smart contracts can submit inference requests and read verified results.
	AIInferenceTime *big.Int `json:"aiInferenceTime,omitempty"`

	// N42 extension: on-chain randomness beacon precompile (0x0302).
	// When set, smart contracts can access deterministic per-block randomness.
	// Inspired by Aptos AIP-41.
	RandomnessTime *big.Int `json:"randomnessTime,omitempty"`

	// N42 extension: LtHash lattice state digest activation.
	// When set, each block computes a 2048-byte homomorphic hash of the state
	// and stores BLAKE3(digest) in Header.LtHashRoot.
	LtHashTime *big.Int `json:"ltHashTime,omitempty"`

	// N42 extension: EIP-7928 Block-Level Access List activation.
	// When set, blocks carry the keccak256 of the canonical BAL RLP in
	// Header.BlockAccessListHash (produced from the block-STM execution views),
	// enabling parallel state prefetch and validation. Nil = disabled (the
	// header field stays nil and is omitted from the RLP, so pre-fork block
	// hashes are byte-identical).
	//
	// Every consensus-critical block path now carries BlockAccessListHash via RLP
	// (protoc was never required): the consensus hash, gossip send+receive, direct
	// push, sync chunked responses, block download, rawdb header trailer and torrent
	// export. The former proto reconstruction paths (legacy direct-push and the
	// sync_proto download package) have been removed. Miner generation (BALCapture
	// -> header.BlockAccessListHash) and import verification (recompute + compare)
	// are wired behind Rules.IsBAL and dormant while this is nil. Keep nil until
	// cross-node determinism is validated on a test chain (every node must compute
	// an identical BAL hash for each block); only then set an activation time.
	// Note: system-call writes are not yet harvested and the full BAL is not
	// transmitted (only the header hash), so BAL-driven prefetch is not yet active.
	BALTime *big.Int `json:"balTime,omitempty"`

	// N42 extension: mobile-attestation accumulator anchor activation
	// (docs/mobile-attestation-design.md §3.5, phase 6c). When set, the leader
	// stamps the committed mobile-registry accumulator root into
	// Header.MobileRegistryRoot. This is a HEADER commitment only — exactly the
	// mechanism the 200K/512 committee uses (header hash-link + rawdb side
	// table): the root is bound into the block hash via RLP, with NO state-trie
	// write, so it needs no system contract, no genesis alloc and no replay to
	// activate. The root is a HEADER-PROVIDED value stamped by the leader;
	// followers store it verbatim (no recomputation), so it can never fork
	// consensus. The full history lives in the rawdb anchor log (phase 6b).
	// Nil = disabled (header field omitted from RLP, pre-fork block hashes
	// byte-identical). Keep nil until validated on a test chain.
	MobileAnchorTime *big.Int `json:"mobileAnchorTime,omitempty"`

	// StateScheme determines the state commitment algorithm for Header.Root.
	// Set at genesis, immutable thereafter. Nodes MUST refuse to start if the
	// configured scheme does not match the database.
	// Values: "legacy-keccak" (default), "jmt-blake3", "bmt-blake3"
	StateScheme string `json:"stateScheme,omitempty"`

	BlobSchedule *BlobSchedule `json:"blobSchedule,omitempty"`

	// BSC / custom fork fields
	NanoBlock    *big.Int `json:"nanoBlock,omitempty" toml:",omitempty"`
	MoranBlock   *big.Int `json:"moranBlock,omitempty" toml:",omitempty"`
	BeijingBlock *big.Int `json:"beijingBlock,omitempty" toml:",omitempty"`

	Eip1559FeeCollector           *types.Address `json:"eip1559FeeCollector,omitempty"`
	Eip1559FeeCollectorTransition *big.Int       `json:"eip1559FeeCollectorTransition,omitempty"`

	// Consensus engine configs
	Ethash   *EthashConfig   `json:"ethash,omitempty"`
	Clique   *CliqueConfig   `json:"clique,omitempty"`
	Aura     *AuRaConfig     `json:"aura,omitempty"`
	Parlia   *ParliaConfig   `json:"parlia,omitempty" toml:",omitempty"`
	Bor      *BorConfig      `json:"bor,omitempty"`
	Apos     *APosConfig     `json:"apos,omitempty"`
	HotStuff *HotStuffConfig `json:"hotstuff,omitempty"`
}

// NormalizeConsensus infers a missing consensus value from explicit engine
// sub-configs. When a custom/private genesis omits engine settings entirely,
// default to Faker so the execution layer remains bootable for external-driver
// scenarios such as Hive Engine API tests.
func NormalizeConsensus(cfg *ChainConfig) *ChainConfig {
	if cfg == nil || cfg.Consensus != "" {
		return cfg
	}
	switch {
	case cfg.Clique != nil:
		cfg.Consensus = CliqueConsensus
	case cfg.Apos != nil:
		cfg.Consensus = AposConsensu
	case cfg.HotStuff != nil:
		cfg.Consensus = HotStuffConsensus
	case cfg.Aura != nil:
		cfg.Consensus = AuRaConsensus
	case cfg.Parlia != nil:
		cfg.Consensus = ParliaConsensus
	case cfg.Bor != nil:
		cfg.Consensus = BorConsensus
	case cfg.Ethash != nil:
		cfg.Consensus = EtHashConsensus
	default:
		cfg.Consensus = Faker
	}
	return cfg
}

// String implements the fmt.Stringer interface.
func (c *ChainConfig) String() string {
	return fmt.Sprintf("{ChainID: %v, Homestead: %v, DAO: %v, DAO Support: %v, Tangerine Whistle: %v, Spurious Dragon: %v, Byzantium: %v, Constantinople: %v, Petersburg: %v, Istanbul: %v, Muir Glacier: %v, Berlin: %v, London: %v, Arrow Glacier: %v, Gray Glacier: %v, Terminal Total Difficulty: %v, Merge Netsplit: %v, Shanghai: %v, Cancun: %v}",
		c.ChainID,
		c.HomesteadBlock,
		c.DAOForkBlock,
		c.DAOForkSupport,
		c.TangerineWhistleBlock,
		c.SpuriousDragonBlock,
		c.ByzantiumBlock,
		c.ConstantinopleBlock,
		c.PetersburgBlock,
		c.IstanbulBlock,
		c.MuirGlacierBlock,
		c.BerlinBlock,
		c.LondonBlock,
		c.ArrowGlacierBlock,
		c.GrayGlacierBlock,
		c.TerminalTotalDifficulty,
		c.MergeNetsplitBlock,
		c.ShanghaiBlock,
		c.CancunBlock,
	)
}

// IsHeaderWithSeal returns true for consensus engines that include a seal in the header.
func (c *ChainConfig) IsHeaderWithSeal() bool {
	return c.Consensus == AuRaConsensus
}

func (c *ChainConfig) UsesBorSystemCallContext() bool {
	return c != nil && c.Bor != nil
}

func (c *ChainConfig) UsesParliaRules() bool {
	return c != nil && c.Parlia != nil
}

func (c *ChainConfig) UsesAuraRules() bool {
	return c != nil && c.Aura != nil
}

// Description returns a human-readable description of ChainConfig.
func (c *ChainConfig) Description() string {
	network := NetworkNames[c.ChainID.String()]
	if network == "" {
		network = "unknown"
	}

	return fmt.Sprintf("Version    %s\nChain      %v (%s)\nConsensus  %s\n",
		VersionWithMeta, c.ChainID, network, c.Consensus.DisplayName())
}

// ---------------------------------------------------------------------------
// Consensus engine config structs
// ---------------------------------------------------------------------------

// EthashConfig is the consensus engine configs for proof-of-work based sealing.
type EthashConfig struct{}

func (c *EthashConfig) String() string { return "ethash" }

// CliqueConfig is the consensus engine configs for proof-of-authority based sealing.
type CliqueConfig struct {
	Period uint64 `json:"period"` // Number of seconds between blocks to enforce
	Epoch  uint64 `json:"epoch"`  // Epoch length to reset votes and checkpoint
}

func (c *CliqueConfig) String() string { return "clique" }

// APosConfig is the consensus engine configs for Authority Proof-of-Stake.
type APosConfig struct {
	Period uint64 `json:"period"`
	Epoch  uint64 `json:"epoch"`

	RewardEpoch uint64   `json:"rewardEpoch"`
	RewardLimit *big.Int `json:"rewardLimit"`

	DepositContract     string `json:"depositContract"`
	DepositNFTContract  string `json:"depositNFTContract"`
	DepositFUJIContract string `json:"depositFUJIContract"`
}

func (b *APosConfig) String() string {
	return fmt.Sprintf("{DepositContract: %v, NFTDepositContract:%v, Period: %v, Epoch: %v, RewardEpoch: %v, RewardLimit: %v}",
		b.DepositContract,
		b.DepositNFTContract,
		b.Period,
		b.Epoch,
		b.RewardEpoch,
		b.RewardLimit,
	)
}

// AuRaConfig is the consensus engine configs for proof-of-authority based sealing.
type AuRaConfig struct {
	DBPath    string
	InMemory  bool
	Etherbase avmutil.Address // same as miner etherbase
}

func (c *AuRaConfig) String() string { return "aura" }

// ParliaConfig is the consensus engine configs for Parlia based sealing.
type ParliaConfig struct {
	DBPath   string
	InMemory bool
	Period   uint64 `json:"period"`
	Epoch    uint64 `json:"epoch"`
}

func (b *ParliaConfig) String() string { return "parlia" }

// BorConfig is the consensus engine configs for Matic bor based sealing.
type BorConfig struct {
	Period                map[string]uint64 `json:"period"`
	ProducerDelay         uint64            `json:"producerDelay"`
	Sprint                uint64            `json:"sprint"`
	BackupMultiplier      map[string]uint64 `json:"backupMultiplier"`
	ValidatorContract     string            `json:"validatorContract"`
	StateReceiverContract string            `json:"stateReceiverContract"`

	OverrideStateSyncRecords map[string]int         `json:"overrideStateSyncRecords"`
	BlockAlloc               map[string]interface{} `json:"blockAlloc"`
	JaipurBlock              uint64                 `json:"jaipurBlock"`
}

func (b *BorConfig) String() string { return "bor" }

// HotStuffValidatorConfig defines a genesis validator for HotStuff consensus.
type HotStuffValidatorConfig struct {
	Address string `json:"address"` // Hex-encoded Ethereum address
	BLSKey  string `json:"blsKey"`  // Hex-encoded BLS12-381 public key (48 bytes)
}

// HotStuffConfig is the consensus engine config for HotStuff-2 BFT consensus.
type HotStuffConfig struct {
	Period      uint64 `json:"period"`      // Block period in seconds (default 3)
	BaseTimeout uint64 `json:"baseTimeout"` // Base timeout in milliseconds (default 60000)
	MaxTimeout  uint64 `json:"maxTimeout"`  // Max timeout in milliseconds (default 120000)
	EpochLength uint64 `json:"epochLength"` // Epoch length in blocks for validator set rotation

	// FastPropose skips slot boundary wait, reducing consensus latency by ~72%.
	// When enabled, the leader proposes immediately after receiving ViewChanged,
	// waiting only MinProposeDelayMs before building the block.
	FastPropose       bool   `json:"fastPropose,omitempty"`
	MinProposeDelayMs uint64 `json:"minProposeDelayMs,omitempty"` // minimum delay before proposing (default 200ms)

	// Validators is the genesis validator set for HotStuff consensus.
	Validators []HotStuffValidatorConfig `json:"validators,omitempty"`

	// CommitteePool, when enabled, runs the simulated BLS committee multi-sig
	// (the 200K-voter / 512-committee pool carried over from the replay reseal)
	// for live block production: each new block is stamped with consensus
	// evidence and a ParentBeaconRoot link, continuing the resealed chain.
	CommitteePool *HotStuffCommitteePoolConfig `json:"committeePool,omitempty"`

	// DevBlockReward, when positive, credits the block's coinbase with a fixed
	// per-block reward (wei) in Finalize. Dev/test chains only: replay-seeded
	// meshes have no staking deposits, so the APoS reward module pays nothing
	// and validator accounts stay at zero balance — which starves the txgen
	// faucet. Consensus-relevant (part of state-root validation); every node
	// carries it via the compiled-in chainspec. Zero/absent = no behavior
	// change.
	DevBlockReward uint64 `json:"devBlockReward,omitempty"`

	// DevFaucetAddress, when set together with DevBlockReward, receives the
	// same per-block credit as the coinbase. Needed on chains whose validator
	// addresses are BLS-derived (no secp key exists), so no validator account
	// can ever sign an EVM transaction — the faucet must be a separate,
	// well-known dev account.
	DevFaucetAddress *types.Address `json:"devFaucetAddress,omitempty"`

	// EthELCompat disables N42-native reward application so blocks are
	// representable by the standard Ethereum Engine API execution model.
	// Consensus-critical: all validators must use the same chainspec value.
	EthELCompat bool `json:"ethELCompat,omitempty"`

	// TwoPhaseVoteGate moves the execution guarantee from Round 1 to Round 2
	// (order-then-execute): Round-1 prepare votes fire on static validation,
	// decoupling view progress from execution latency; the Round-2 CommitVote
	// waits for the local import, so a CommitQC still proves 2f+1 validators
	// executed the block before it commits. Consensus-behavior switch — all
	// validators must agree. Off (default) = classic import-gated Round 1.
	TwoPhaseVoteGate bool `json:"twoPhaseVoteGate,omitempty"`
}

// HotStuffCommitteePoolConfig configures the live BLS committee-evidence pool.
// The seed and sizes MUST match the replay resealer's (--bls-seed / pool sizes)
// for the live node to continue a resealed chain byte-identically.
type HotStuffCommitteePoolConfig struct {
	Enabled       bool   `json:"enabled"`
	SeedHex       string `json:"seedHex"`       // 32-byte hex master seed (= replay --bls-seed)
	PoolSize      int    `json:"poolSize"`      // total mobile-voter pool (e.g. 200000)
	CommitteeSize int    `json:"committeeSize"` // per-block committee (e.g. 512)
	RampBlocks    uint64 `json:"rampBlocks"`    // blocks over which the active pool ramps

	// AllowHandover enables the validator hand-over RPC surface: real validators
	// register their BLS key for a pool slot and submit per-block partial
	// signatures. Off by default — leaving the committee fully simulated and the
	// per-block evidence deterministic across nodes. Enable only once the
	// operator is ready to propagate per-block evidence to followers.
	AllowHandover bool `json:"allowHandover,omitempty"`
}

func (c *HotStuffConfig) String() string {
	return fmt.Sprintf("{Period: %v, BaseTimeout: %v, MaxTimeout: %v, EpochLength: %v, Validators: %d}",
		c.Period, c.BaseTimeout, c.MaxTimeout, c.EpochLength, len(c.Validators))
}

func (c *BorConfig) CalculateBackupMultiplier(number uint64) uint64 {
	return c.calculateBorConfigHelper(c.BackupMultiplier, number)
}

func (c *BorConfig) CalculatePeriod(number uint64) uint64 {
	return c.calculateBorConfigHelper(c.Period, number)
}

func (c *BorConfig) IsJaipur(number uint64) bool {
	return number >= c.JaipurBlock
}

func (c *BorConfig) calculateBorConfigHelper(field map[string]uint64, number uint64) uint64 {
	if len(field) == 0 {
		return 0
	}
	keys := make([]string, 0, len(field))
	for k := range field {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i := 0; i < len(keys)-1; i++ {
		valUint, _ := strconv.ParseUint(keys[i], 10, 64)
		valUintNext, _ := strconv.ParseUint(keys[i+1], 10, 64)
		if number >= valUint && number < valUintNext {
			return field[keys[i]]
		}
	}
	return field[keys[len(keys)-1]]
}

// ---------------------------------------------------------------------------
// Snapshot config
// ---------------------------------------------------------------------------

type ConsensusSnapshotConfig struct {
	CheckpointInterval uint64 // Number of blocks after which to save the vote snapshot to the database
	InmemorySnapshots  int    // Number of recent vote snapshots to keep in memory
	InmemorySignatures int    // Number of recent block signatures to keep in memory
	DBPath             string
	InMemory           bool
}

const cliquePath = "clique"

func NewSnapshotConfig(checkpointInterval uint64, inmemorySnapshots int, inmemorySignatures int, inmemory bool, dbPath string) *ConsensusSnapshotConfig {
	if len(dbPath) == 0 {
		dbPath = paths.DefaultDataDir()
	}

	return &ConsensusSnapshotConfig{
		CheckpointInterval: checkpointInterval,
		InmemorySnapshots:  inmemorySnapshots,
		InmemorySignatures: inmemorySignatures,
		DBPath:             path.Join(dbPath, cliquePath),
		InMemory:           inmemory,
	}
}

// ---------------------------------------------------------------------------
// Network lookups
// ---------------------------------------------------------------------------

// NetworkNames are user friendly names to use in the chain spec banner.
var NetworkNames = map[string]string{
	"100100100": "testnet",
	"94":        "mainnet",
	"1":         networkname.EthereumMainnetChainName,
	"11155111":  networkname.EthereumSepoliaChainName,
	"17000":     networkname.EthereumHoleskyChainName,
}

func ChainConfigByChainName(chain string) *ChainConfig {
	switch chain {
	case networkname.MainnetChainName, networkname.N42MainnetAlias:
		return MainnetChainConfig
	case "mainnet_compat":
		return MainnetCompatChainConfig
	case "mainnet_v2":
		return MainnetV2ChainConfig
	case "mainnet_v2_staggered":
		return MainnetV2StaggeredChainConfig
	case "mainnet_mpt":
		return MainnetMPTChainConfig
	case "mainnet_qmdb":
		return MainnetQMDBChainConfig
	case "mainnet_qmdb_staggered":
		return MainnetQMDBStaggeredChainConfig
	case "qs_epoch_test":
		return QSEpochTestChainConfig
	case networkname.TestnetChainName:
		return TestnetChainConfig
	case networkname.EthereumMainnetChainName:
		return EthereumMainnetChainConfig
	case networkname.EthereumSepoliaChainName, networkname.EthereumTestnetAlias:
		return EthereumSepoliaChainConfig
	case networkname.EthereumHoleskyChainName:
		return EthereumHoleskyChainConfig
	default:
		return nil
	}
}

func GenesisHashByChainName(chain string) *types.Hash {
	switch chain {
	case networkname.MainnetChainName, networkname.N42MainnetAlias, "mainnet_compat":
		return &MainnetGenesisHash
	case networkname.TestnetChainName, networkname.N42TestnetAlias:
		return &TestnetGenesisHash
	case "mainnet_v2":
		return &MainnetV2GenesisHash
	case "mainnet_mpt":
		return &MainnetMPTGenesisHash
	case "mainnet_v2_staggered":
		return &MainnetV2StaggeredGenesisHash
	case "mainnet_qmdb":
		return &MainnetQMDBGenesisHash
	case "mainnet_qmdb_staggered":
		return &MainnetQMDBStaggeredGenesisHash
	case "qs_epoch_test":
		return &QSEpochTestGenesisHash
	case networkname.EthereumMainnetChainName:
		return &EthereumMainnetGenesisHash
	case networkname.EthereumSepoliaChainName, networkname.EthereumTestnetAlias:
		return &EthereumSepoliaGenesisHash
	case networkname.EthereumHoleskyChainName:
		return &EthereumHoleskyGenesisHash
	default:
		return nil
	}
}

func ChainConfigByGenesisHash(genesisHash types.Hash) *ChainConfig {
	switch {
	case genesisHash == MainnetGenesisHash:
		return MainnetChainConfig
	case genesisHash == TestnetGenesisHash:
		return TestnetChainConfig
	case genesisHash == EthereumMainnetGenesisHash:
		return EthereumMainnetChainConfig
	case genesisHash == EthereumSepoliaGenesisHash:
		return EthereumSepoliaChainConfig
	case genesisHash == EthereumHoleskyGenesisHash:
		return EthereumHoleskyChainConfig
	default:
		return nil
	}
}

func NetworkIDByChainName(chain string) uint64 {
	switch chain {
	case networkname.MainnetChainName, networkname.N42MainnetAlias:
		return 97
	case networkname.TestnetChainName, networkname.N42TestnetAlias:
		return 10042
	case networkname.EthereumMainnetChainName:
		return 1
	case networkname.EthereumSepoliaChainName, networkname.EthereumTestnetAlias:
		return 11155111
	case networkname.EthereumHoleskyChainName:
		return 17000
	default:
		config := ChainConfigByChainName(chain)
		if config == nil {
			return 0
		}
		return config.ChainID.Uint64()
	}
}

// ---------------------------------------------------------------------------
// Compatibility checking
// ---------------------------------------------------------------------------

// CheckCompatible checks whether scheduled fork transitions have been imported
// with a mismatching chain configuration.
func (c *ChainConfig) CheckCompatible(newcfg *ChainConfig, height uint64) *ConfigCompatError {
	bhead := height

	var lasterr *ConfigCompatError
	for {
		err := c.checkCompatible(newcfg, bhead)
		if err == nil || (lasterr != nil && err.RewindTo == lasterr.RewindTo) {
			break
		}
		lasterr = err
		bhead = err.RewindTo
	}
	return lasterr
}

// CheckConfigForkOrder checks that we don't "skip" any forks, geth isn't pluggable enough
// to guarantee that forks can be implemented in a different order than on official networks
func (c *ChainConfig) CheckConfigForkOrder() error {
	if c != nil && c.ChainID != nil && c.ChainID.Uint64() == 77 {
		return nil
	}
	type fork struct {
		name     string
		block    *big.Int
		optional bool
	}
	var lastFork fork
	for _, cur := range []fork{
		{name: "homesteadBlock", block: c.HomesteadBlock},
		{name: "daoForkBlock", block: c.DAOForkBlock, optional: true},
		{name: "eip150Block", block: c.TangerineWhistleBlock},
		{name: "eip155Block", block: c.SpuriousDragonBlock},
		{name: "byzantiumBlock", block: c.ByzantiumBlock},
		{name: "constantinopleBlock", block: c.ConstantinopleBlock},
		{name: "petersburgBlock", block: c.PetersburgBlock},
		{name: "istanbulBlock", block: c.IstanbulBlock},
		{name: "muirGlacierBlock", block: c.MuirGlacierBlock, optional: true},
		{name: "berlinBlock", block: c.BerlinBlock},
		{name: "londonBlock", block: c.LondonBlock},
		{name: "arrowGlacierBlock", block: c.ArrowGlacierBlock, optional: true},
		{name: "grayGlacierBlock", block: c.GrayGlacierBlock, optional: true},
		{name: "mergeNetsplitBlock", block: c.MergeNetsplitBlock, optional: true},
		{name: "shanghaiBlock", block: c.ShanghaiBlock},
		{name: "cancunBlock", block: c.CancunBlock},
		{name: "pectraTime", block: c.PectraTime, optional: true},
		{name: "osakaTime", block: c.OsakaTime, optional: true},
		{name: "fusakaTime", block: c.FusakaTime, optional: true},
		{name: "glamsterdamTime", block: c.GlamsterdamTime, optional: true},
	} {
		if lastFork.name != "" {
			if lastFork.block == nil && cur.block != nil {
				return fmt.Errorf("unsupported fork ordering: %v not enabled, but %v enabled at %v",
					lastFork.name, cur.name, cur.block)
			}
			if lastFork.block != nil && cur.block != nil {
				if lastFork.block.Cmp(cur.block) > 0 {
					return fmt.Errorf("unsupported fork ordering: %v enabled at %v, but %v enabled at %v",
						lastFork.name, lastFork.block, cur.name, cur.block)
				}
			}
		}
		if !cur.optional || cur.block != nil {
			lastFork = cur
		}
	}
	return nil
}

func (c *ChainConfig) checkCompatible(newcfg *ChainConfig, head uint64) *ConfigCompatError {
	if isForkIncompatible(c.HomesteadBlock, newcfg.HomesteadBlock, head) {
		return newCompatError("Homestead fork block", c.HomesteadBlock, newcfg.HomesteadBlock)
	}
	if isForkIncompatible(c.DAOForkBlock, newcfg.DAOForkBlock, head) {
		return newCompatError("DAO fork block", c.DAOForkBlock, newcfg.DAOForkBlock)
	}
	if c.IsDAOFork(head) && c.DAOForkSupport != newcfg.DAOForkSupport {
		return newCompatError("DAO fork support flag", c.DAOForkBlock, newcfg.DAOForkBlock)
	}
	if isForkIncompatible(c.TangerineWhistleBlock, newcfg.TangerineWhistleBlock, head) {
		return newCompatError("Tangerine Whistle fork block", c.TangerineWhistleBlock, newcfg.TangerineWhistleBlock)
	}
	if isForkIncompatible(c.SpuriousDragonBlock, newcfg.SpuriousDragonBlock, head) {
		return newCompatError("Spurious Dragon fork block", c.SpuriousDragonBlock, newcfg.SpuriousDragonBlock)
	}
	if c.IsSpuriousDragon(head) && !configNumEqual(c.ChainID, newcfg.ChainID) {
		return newCompatError("EIP155 chain ID", c.SpuriousDragonBlock, newcfg.SpuriousDragonBlock)
	}
	if isForkIncompatible(c.ByzantiumBlock, newcfg.ByzantiumBlock, head) {
		return newCompatError("Byzantium fork block", c.ByzantiumBlock, newcfg.ByzantiumBlock)
	}
	if isForkIncompatible(c.ConstantinopleBlock, newcfg.ConstantinopleBlock, head) {
		return newCompatError("Constantinople fork block", c.ConstantinopleBlock, newcfg.ConstantinopleBlock)
	}
	if isForkIncompatible(c.PetersburgBlock, newcfg.PetersburgBlock, head) {
		if isForkIncompatible(c.ConstantinopleBlock, newcfg.PetersburgBlock, head) {
			return newCompatError("Petersburg fork block", c.PetersburgBlock, newcfg.PetersburgBlock)
		}
	}
	if isForkIncompatible(c.IstanbulBlock, newcfg.IstanbulBlock, head) {
		return newCompatError("Istanbul fork block", c.IstanbulBlock, newcfg.IstanbulBlock)
	}
	if isForkIncompatible(c.MuirGlacierBlock, newcfg.MuirGlacierBlock, head) {
		return newCompatError("Muir Glacier fork block", c.MuirGlacierBlock, newcfg.MuirGlacierBlock)
	}
	if isForkIncompatible(c.BerlinBlock, newcfg.BerlinBlock, head) {
		return newCompatError("Berlin fork block", c.BerlinBlock, newcfg.BerlinBlock)
	}
	if isForkIncompatible(c.LondonBlock, newcfg.LondonBlock, head) {
		return newCompatError("London fork block", c.LondonBlock, newcfg.LondonBlock)
	}
	if isForkIncompatible(c.ArrowGlacierBlock, newcfg.ArrowGlacierBlock, head) {
		return newCompatError("Arrow Glacier fork block", c.ArrowGlacierBlock, newcfg.ArrowGlacierBlock)
	}
	if isForkIncompatible(c.GrayGlacierBlock, newcfg.GrayGlacierBlock, head) {
		return newCompatError("Gray Glacier fork block", c.GrayGlacierBlock, newcfg.GrayGlacierBlock)
	}
	if isForkIncompatible(c.MergeNetsplitBlock, newcfg.MergeNetsplitBlock, head) {
		return newCompatError("Merge netsplit block", c.MergeNetsplitBlock, newcfg.MergeNetsplitBlock)
	}
	if isForkIncompatible(c.ShanghaiBlock, newcfg.ShanghaiBlock, head) {
		return newCompatError("Shanghai fork block", c.ShanghaiBlock, newcfg.ShanghaiBlock)
	}
	if isForkIncompatible(c.CancunBlock, newcfg.CancunBlock, head) {
		return newCompatError("Cancun fork block", c.CancunBlock, newcfg.CancunBlock)
	}
	if isForkIncompatible(c.PectraTime, newcfg.PectraTime, head) {
		return newCompatError("Pectra fork time", c.PectraTime, newcfg.PectraTime)
	}
	if isForkIncompatible(c.OsakaTime, newcfg.OsakaTime, head) {
		return newCompatError("Osaka fork time", c.OsakaTime, newcfg.OsakaTime)
	}
	if isForkIncompatible(c.FusakaTime, newcfg.FusakaTime, head) {
		return newCompatError("Fusaka fork time", c.FusakaTime, newcfg.FusakaTime)
	}
	if isForkIncompatible(c.GlamsterdamTime, newcfg.GlamsterdamTime, head) {
		return newCompatError("Glamsterdam fork time", c.GlamsterdamTime, newcfg.GlamsterdamTime)
	}
	return nil
}

// ---------------------------------------------------------------------------
// ConfigCompatError
// ---------------------------------------------------------------------------

// ConfigCompatError is raised if the locally-stored blockchain is initialised with a
// ChainConfig that would alter the past.
type ConfigCompatError struct {
	What string
	// block numbers of the stored and new configurations
	StoredConfig, NewConfig *big.Int
	// the block number to which the local chain must be rewound to correct the error
	RewindTo uint64
}

func newCompatError(what string, storedblock, newblock *big.Int) *ConfigCompatError {
	var rew *big.Int
	switch {
	case storedblock == nil:
		rew = newblock
	case newblock == nil || storedblock.Cmp(newblock) < 0:
		rew = storedblock
	default:
		rew = newblock
	}
	err := &ConfigCompatError{what, storedblock, newblock, 0}
	if rew != nil && rew.Sign() > 0 {
		err.RewindTo = rew.Uint64() - 1
	}
	return err
}

func (err *ConfigCompatError) Error() string {
	return fmt.Sprintf("mismatching %s in database (have %d, want %d, rewindto %d)", err.What, err.StoredConfig, err.NewConfig, err.RewindTo)
}

// ---------------------------------------------------------------------------
// TrustedCheckpoint / CheckpointOracleConfig
// ---------------------------------------------------------------------------

// TrustedCheckpoint represents a set of post-processed trie roots (CHT and
// BloomTrie) associated with the appropriate section index and head hash. It is
// used to start light syncing from this checkpoint and avoid downloading the
// entire header chain while still being able to securely access old headers/logs.
type TrustedCheckpoint struct {
	SectionIndex uint64     `json:"sectionIndex"`
	SectionHead  types.Hash `json:"sectionHead"`
	CHTRoot      types.Hash `json:"chtRoot"`
	BloomRoot    types.Hash `json:"bloomRoot"`
}

// HashEqual returns an indicator comparing the itself hash with given one.
func (c *TrustedCheckpoint) HashEqual(hash types.Hash) bool {
	if c.Empty() {
		return hash == types.Hash{}
	}
	return c.Hash() == hash
}

// Hash returns the hash of checkpoint's four key fields(index, sectionHead, chtRoot and bloomTrieRoot).
func (c *TrustedCheckpoint) Hash() types.Hash {
	var sectionIndex [8]byte
	binary.BigEndian.PutUint64(sectionIndex[:], c.SectionIndex)

	w := sha3.NewLegacyKeccak256()
	w.Write(sectionIndex[:])
	w.Write(c.SectionHead[:])
	w.Write(c.CHTRoot[:])
	w.Write(c.BloomRoot[:])

	var h types.Hash
	w.Sum(h[:0])
	return h
}

// Empty returns an indicator whether the checkpoint is regarded as empty.
func (c *TrustedCheckpoint) Empty() bool {
	return c.SectionHead == (types.Hash{}) || c.CHTRoot == (types.Hash{}) || c.BloomRoot == (types.Hash{})
}

// CheckpointOracleConfig represents a set of checkpoint contract(which acts as an oracle)
// blockchain which used for light client checkpoint syncing.
type CheckpointOracleConfig struct {
	Address   types.Address   `json:"address"`
	Signers   []types.Address `json:"signers"`
	Threshold uint64          `json:"threshold"`
}
