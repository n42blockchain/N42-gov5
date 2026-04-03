package params

import (
	"math/big"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params/networkname"
)

func ethBigInt(value string) *big.Int {
	n, ok := new(big.Int).SetString(value, 10)
	if !ok {
		panic("invalid big integer literal: " + value)
	}
	return n
}

var (
	EthereumMainnetGenesisHash = types.HexToHash("0xd4e56740f876aef8c010b86a40d5f56745a118d0906a34e69aec8c0db1cb8fa3")
	EthereumSepoliaGenesisHash = types.HexToHash("0x25a5cc106eea7138acab33231d7160d69cb777ee0c2c553fcddf5138993e6dd9")
	EthereumHoleskyGenesisHash = types.HexToHash("0xb5f7f912443c940f21fd611f12828d75b534364ed9e95ca4e307572b4e500310")

	EthereumMainnetChainConfig = &ChainConfig{
		ChainName:               networkname.EthereumMainnetChainName,
		ChainID:                 big.NewInt(1),
		Consensus:               EtHashConsensus,
		HomesteadBlock:          big.NewInt(1150000),
		DAOForkBlock:            big.NewInt(1920000),
		DAOForkSupport:          true,
		TangerineWhistleBlock:   big.NewInt(2463000),
		SpuriousDragonBlock:     big.NewInt(2675000),
		ByzantiumBlock:          big.NewInt(4370000),
		ConstantinopleBlock:     big.NewInt(7280000),
		PetersburgBlock:         big.NewInt(7280000),
		IstanbulBlock:           big.NewInt(9069000),
		MuirGlacierBlock:        big.NewInt(9200000),
		BerlinBlock:             big.NewInt(12244000),
		LondonBlock:             big.NewInt(12965000),
		ArrowGlacierBlock:       big.NewInt(13773000),
		GrayGlacierBlock:        big.NewInt(15050000),
		TerminalTotalDifficulty: ethBigInt("58750000000000000000000"),
		ShanghaiTime:            big.NewInt(1681338455),
		CancunTime:              big.NewInt(1710338135),
		PragueTime:              big.NewInt(1746612311),
		OsakaTime:               big.NewInt(1764798551),
		BPO1Time:                big.NewInt(1765290071),
		BPO2Time:                big.NewInt(1767747671),
		Ethash:                  new(EthashConfig),
	}

	EthereumSepoliaChainConfig = &ChainConfig{
		ChainName:               networkname.EthereumSepoliaChainName,
		ChainID:                 big.NewInt(11155111),
		Consensus:               EtHashConsensus,
		HomesteadBlock:          big.NewInt(0),
		DAOForkSupport:          true,
		TangerineWhistleBlock:   big.NewInt(0),
		SpuriousDragonBlock:     big.NewInt(0),
		ByzantiumBlock:          big.NewInt(0),
		ConstantinopleBlock:     big.NewInt(0),
		PetersburgBlock:         big.NewInt(0),
		IstanbulBlock:           big.NewInt(0),
		MuirGlacierBlock:        big.NewInt(0),
		BerlinBlock:             big.NewInt(0),
		LondonBlock:             big.NewInt(0),
		TerminalTotalDifficulty: ethBigInt("17000000000000000"),
		MergeNetsplitBlock:      big.NewInt(1735371),
		ShanghaiTime:            big.NewInt(1677557088),
		CancunTime:              big.NewInt(1706655072),
		PragueTime:              big.NewInt(1741159776),
		OsakaTime:               big.NewInt(1760427360),
		BPO1Time:                big.NewInt(1761017184),
		BPO2Time:                big.NewInt(1761607008),
		Ethash:                  new(EthashConfig),
	}

	// Holesky is the primary Ethereum PoS testnet (launched Sep 2023).
	// All forks from genesis, PoS from block 0 (TTD=0).
	EthereumHoleskyChainConfig = &ChainConfig{
		ChainName:               networkname.EthereumHoleskyChainName,
		ChainID:                 big.NewInt(17000),
		Consensus:               EtHashConsensus,
		HomesteadBlock:          big.NewInt(0),
		DAOForkSupport:          false,
		TangerineWhistleBlock:   big.NewInt(0),
		SpuriousDragonBlock:     big.NewInt(0),
		ByzantiumBlock:          big.NewInt(0),
		ConstantinopleBlock:     big.NewInt(0),
		PetersburgBlock:         big.NewInt(0),
		IstanbulBlock:           big.NewInt(0),
		MuirGlacierBlock:        big.NewInt(0),
		BerlinBlock:             big.NewInt(0),
		LondonBlock:             big.NewInt(0),
		TerminalTotalDifficulty: big.NewInt(0),
		TerminalTotalDifficultyPassed: true,
		ShanghaiTime:            big.NewInt(1696000704),
		CancunTime:              big.NewInt(1707305664),
		PragueTime:              big.NewInt(1742224320),
		Ethash:                  new(EthashConfig),
	}
)
