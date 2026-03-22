package conf

import (
	"math/big"
	"strconv"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

type hiveEnvLookup func(string) (string, bool)

// ApplyHiveGenesisEnv populates or overrides genesis chain config fields from
// the Hive client environment contract. It returns true when any HIVE_* input
// was applied.
func ApplyHiveGenesisEnv(genesis *Genesis, lookup func(string) (string, bool)) bool {
	if genesis == nil || lookup == nil {
		return false
	}
	get := hiveEnvLookup(lookup)
	if !hasHiveGenesisEnv(get) {
		return false
	}

	cfg := cloneChainConfig(genesis.Config)
	if cfg == nil {
		cfg = &params.ChainConfig{}
	}

	if value, ok := envBig(get, "HIVE_CHAIN_ID"); ok {
		cfg.ChainID = value
	}
	if value, ok := envBig(get, "HIVE_FORK_HOMESTEAD"); ok {
		cfg.HomesteadBlock = value
	}
	if value, ok := envBig(get, "HIVE_FORK_DAO_BLOCK"); ok {
		cfg.DAOForkBlock = value
	}
	if value, ok := envBool(get, "HIVE_FORK_DAO_VOTE"); ok {
		cfg.DAOForkSupport = value
	}
	if value, ok := envBig(get, "HIVE_FORK_TANGERINE"); ok {
		cfg.TangerineWhistleBlock = value
	}
	if value, ok := envHash(get, "HIVE_FORK_TANGERINE_HASH"); ok {
		cfg.TangerineWhistleHash = value
	}
	if value, ok := envBig(get, "HIVE_FORK_SPURIOUS"); ok {
		cfg.SpuriousDragonBlock = value
	}
	if value, ok := envBig(get, "HIVE_FORK_BYZANTIUM"); ok {
		cfg.ByzantiumBlock = value
	}
	if value, ok := envBig(get, "HIVE_FORK_CONSTANTINOPLE"); ok {
		cfg.ConstantinopleBlock = value
	}
	if value, ok := envBig(get, "HIVE_FORK_PETERSBURG"); ok {
		cfg.PetersburgBlock = value
	}
	if value, ok := envBig(get, "HIVE_FORK_ISTANBUL"); ok {
		cfg.IstanbulBlock = value
	}
	if value, ok := envBig(get, "HIVE_FORK_MUIR_GLACIER", "HIVE_FORK_MUIRGLACIER"); ok {
		cfg.MuirGlacierBlock = value
	}
	if value, ok := envBig(get, "HIVE_FORK_BERLIN"); ok {
		cfg.BerlinBlock = value
	}
	if value, ok := envBig(get, "HIVE_FORK_LONDON"); ok {
		cfg.LondonBlock = value
	}
	if value, ok := envBig(get, "HIVE_FORK_ARROW_GLACIER"); ok {
		cfg.ArrowGlacierBlock = value
	}
	if value, ok := envBig(get, "HIVE_FORK_GRAY_GLACIER"); ok {
		cfg.GrayGlacierBlock = value
	}
	if value, ok := envBig(get, "HIVE_TERMINAL_TOTAL_DIFFICULTY"); ok {
		cfg.TerminalTotalDifficulty = value
		cfg.TerminalTotalDifficultyPassed = true
	}
	if value, ok := envBig(get, "HIVE_MERGE_BLOCK_ID", "HIVE_FORK_MERGE"); ok {
		cfg.MergeNetsplitBlock = value
	}
	if value, ok := envBig(get, "HIVE_SHANGHAI_TIMESTAMP"); ok {
		cfg.ShanghaiTime = value
	}
	if value, ok := envBig(get, "HIVE_CANCUN_TIMESTAMP"); ok {
		cfg.CancunTime = value
	}
	if value, ok := envBig(get, "HIVE_PRAGUE_TIMESTAMP"); ok {
		cfg.PragueTime = value
	}
	if value, ok := envBig(get, "HIVE_OSAKA_TIMESTAMP"); ok {
		cfg.OsakaTime = value
	}
	if value, ok := envBig(get, "HIVE_BPO1_TIMESTAMP"); ok {
		cfg.BPO1Time = value
	}
	if value, ok := envBig(get, "HIVE_BPO2_TIMESTAMP"); ok {
		cfg.BPO2Time = value
	}
	if value, ok := envBig(get, "HIVE_BPO3_TIMESTAMP"); ok {
		cfg.BPO3Time = value
	}
	if value, ok := envBig(get, "HIVE_BPO4_TIMESTAMP"); ok {
		cfg.BPO4Time = value
	}
	if value, ok := envBig(get, "HIVE_BPO5_TIMESTAMP"); ok {
		cfg.BPO5Time = value
	}
	if value, ok := envUint64(get, "HIVE_CLIQUE_PERIOD"); ok {
		cfg.Clique = &params.CliqueConfig{
			Period: value,
			Epoch:  30000,
		}
	}

	genesis.Config = params.NormalizeConsensus(cfg)
	return true
}

func hasHiveGenesisEnv(lookup hiveEnvLookup) bool {
	keys := []string{
		"HIVE_CHAIN_ID",
		"HIVE_FORK_HOMESTEAD",
		"HIVE_FORK_DAO_BLOCK",
		"HIVE_FORK_TANGERINE",
		"HIVE_FORK_SPURIOUS",
		"HIVE_FORK_BYZANTIUM",
		"HIVE_FORK_CONSTANTINOPLE",
		"HIVE_FORK_PETERSBURG",
		"HIVE_FORK_ISTANBUL",
		"HIVE_FORK_MUIR_GLACIER",
		"HIVE_FORK_MUIRGLACIER",
		"HIVE_FORK_BERLIN",
		"HIVE_FORK_LONDON",
		"HIVE_TERMINAL_TOTAL_DIFFICULTY",
		"HIVE_MERGE_BLOCK_ID",
		"HIVE_FORK_MERGE",
		"HIVE_SHANGHAI_TIMESTAMP",
		"HIVE_CANCUN_TIMESTAMP",
		"HIVE_PRAGUE_TIMESTAMP",
		"HIVE_OSAKA_TIMESTAMP",
		"HIVE_BPO1_TIMESTAMP",
		"HIVE_BPO2_TIMESTAMP",
		"HIVE_BPO3_TIMESTAMP",
		"HIVE_BPO4_TIMESTAMP",
		"HIVE_BPO5_TIMESTAMP",
		"HIVE_CLIQUE_PERIOD",
	}
	for _, key := range keys {
		if value, ok := lookup(key); ok && value != "" {
			return true
		}
	}
	return false
}

func cloneChainConfig(src *params.ChainConfig) *params.ChainConfig {
	if src == nil {
		return nil
	}
	dst := *src
	dst.ChainID = cloneBig(src.ChainID)
	dst.HomesteadBlock = cloneBig(src.HomesteadBlock)
	dst.DAOForkBlock = cloneBig(src.DAOForkBlock)
	dst.TangerineWhistleBlock = cloneBig(src.TangerineWhistleBlock)
	dst.SpuriousDragonBlock = cloneBig(src.SpuriousDragonBlock)
	dst.ByzantiumBlock = cloneBig(src.ByzantiumBlock)
	dst.ConstantinopleBlock = cloneBig(src.ConstantinopleBlock)
	dst.PetersburgBlock = cloneBig(src.PetersburgBlock)
	dst.IstanbulBlock = cloneBig(src.IstanbulBlock)
	dst.MuirGlacierBlock = cloneBig(src.MuirGlacierBlock)
	dst.BerlinBlock = cloneBig(src.BerlinBlock)
	dst.LondonBlock = cloneBig(src.LondonBlock)
	dst.ArrowGlacierBlock = cloneBig(src.ArrowGlacierBlock)
	dst.GrayGlacierBlock = cloneBig(src.GrayGlacierBlock)
	dst.TerminalTotalDifficulty = cloneBig(src.TerminalTotalDifficulty)
	dst.MergeNetsplitBlock = cloneBig(src.MergeNetsplitBlock)
	dst.ShanghaiBlock = cloneBig(src.ShanghaiBlock)
	dst.CancunBlock = cloneBig(src.CancunBlock)
	dst.ShardingForkTime = cloneBig(src.ShardingForkTime)
	dst.ShanghaiTime = cloneBig(src.ShanghaiTime)
	dst.CancunTime = cloneBig(src.CancunTime)
	dst.PragueTime = cloneBig(src.PragueTime)
	dst.PectraTime = cloneBig(src.PectraTime)
	dst.OsakaTime = cloneBig(src.OsakaTime)
	dst.BPO1Time = cloneBig(src.BPO1Time)
	dst.BPO2Time = cloneBig(src.BPO2Time)
	dst.BPO3Time = cloneBig(src.BPO3Time)
	dst.BPO4Time = cloneBig(src.BPO4Time)
	dst.BPO5Time = cloneBig(src.BPO5Time)
	dst.FusakaTime = cloneBig(src.FusakaTime)
	dst.NanoBlock = cloneBig(src.NanoBlock)
	dst.MoranBlock = cloneBig(src.MoranBlock)
	dst.BeijingBlock = cloneBig(src.BeijingBlock)
	dst.Eip1559FeeCollectorTransition = cloneBig(src.Eip1559FeeCollectorTransition)
	dst.BlobSchedule = cloneBlobSchedule(src.BlobSchedule)
	if src.Clique != nil {
		cliqueCopy := *src.Clique
		dst.Clique = &cliqueCopy
	}
	if src.Ethash != nil {
		ethashCopy := *src.Ethash
		dst.Ethash = &ethashCopy
	}
	if src.Aura != nil {
		auraCopy := *src.Aura
		dst.Aura = &auraCopy
	}
	if src.Parlia != nil {
		parliaCopy := *src.Parlia
		dst.Parlia = &parliaCopy
	}
	if src.Bor != nil {
		borCopy := *src.Bor
		dst.Bor = &borCopy
	}
	if src.Apos != nil {
		aposCopy := *src.Apos
		dst.Apos = &aposCopy
	}
	if src.HotStuff != nil {
		hotStuffCopy := *src.HotStuff
		dst.HotStuff = &hotStuffCopy
	}
	return &dst
}

func cloneBig(src *big.Int) *big.Int {
	if src == nil {
		return nil
	}
	return new(big.Int).Set(src)
}

func cloneBlobConfig(src *params.BlobConfig) *params.BlobConfig {
	if src == nil {
		return nil
	}
	return &params.BlobConfig{
		Target:                cloneUint64Ptr(src.Target),
		Max:                   cloneUint64Ptr(src.Max),
		BaseFeeUpdateFraction: cloneUint64Ptr(src.BaseFeeUpdateFraction),
	}
}

func cloneBlobSchedule(src *params.BlobSchedule) *params.BlobSchedule {
	if src == nil {
		return nil
	}
	return &params.BlobSchedule{
		Cancun: cloneBlobConfig(src.Cancun),
		Prague: cloneBlobConfig(src.Prague),
		Osaka:  cloneBlobConfig(src.Osaka),
		BPO1:   cloneBlobConfig(src.BPO1),
		BPO2:   cloneBlobConfig(src.BPO2),
		BPO3:   cloneBlobConfig(src.BPO3),
		BPO4:   cloneBlobConfig(src.BPO4),
		BPO5:   cloneBlobConfig(src.BPO5),
	}
}

func cloneUint64Ptr(src *uint64) *uint64 {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

func envBig(lookup hiveEnvLookup, keys ...string) (*big.Int, bool) {
	for _, key := range keys {
		if raw, ok := lookup(key); ok && raw != "" {
			if value, ok := new(big.Int).SetString(raw, 0); ok {
				return value, true
			}
		}
	}
	return nil, false
}

func envBool(lookup hiveEnvLookup, keys ...string) (bool, bool) {
	for _, key := range keys {
		if raw, ok := lookup(key); ok && raw != "" {
			switch raw {
			case "1", "true", "TRUE", "True":
				return true, true
			case "0", "false", "FALSE", "False":
				return false, true
			}
		}
	}
	return false, false
}

func envHash(lookup hiveEnvLookup, keys ...string) (types.Hash, bool) {
	for _, key := range keys {
		if raw, ok := lookup(key); ok && raw != "" {
			return types.HexToHash(raw), true
		}
	}
	return types.Hash{}, false
}

func envUint64(lookup hiveEnvLookup, keys ...string) (uint64, bool) {
	for _, key := range keys {
		if raw, ok := lookup(key); ok && raw != "" {
			if value, err := strconv.ParseUint(raw, 0, 64); err == nil {
				return value, true
			}
		}
	}
	return 0, false
}
