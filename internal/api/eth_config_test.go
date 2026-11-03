package api

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

// ethConfigChainStub serves a separate genesis header for block 0.
type ethConfigChainStub struct {
	*canonicalCheckChainStub
	genesis *block.Header
}

func (m *ethConfigChainStub) GetHeaderByNumber(number *uint256.Int) block.IHeader {
	if number != nil && number.IsZero() {
		return m.genesis
	}
	return m.header
}

func TestEthConfigServedOverRPC(t *testing.T) {
	t.Parallel()

	cfg := *params.EthereumMainnetChainConfig
	bpo1 := cfg.BPO1Time.Uint64()
	genesis := &block.Header{Number: uint256.NewInt(0), Difficulty: uint256.NewInt(0), Time: 0}
	head := &block.Header{Number: uint256.NewInt(23_000_000), Difficulty: uint256.NewInt(0), Time: bpo1 + 12, BaseFee: uint256.NewInt(1)}
	api := &API{
		bc: &ethConfigChainStub{
			canonicalCheckChainStub: &canonicalCheckChainStub{header: head, blk: block.NewBlock(head, nil), config: &cfg},
			genesis:                 genesis,
		},
		chainConfig: &cfg,
	}
	server := jsonrpc.NewServer()
	require.NoError(t, server.RegisterName("eth", NewBlockChainAPI(api)))
	client := jsonrpc.DialInProc(server)
	defer client.Close()

	var raw json.RawMessage
	require.NoError(t, client.Call(&raw, "eth_config"))
	var got struct {
		Current *struct {
			ActivationTime uint64 `json:"activationTime"`
			ChainID        string `json:"chainId"`
		} `json:"current"`
		Next *struct {
			ActivationTime uint64 `json:"activationTime"`
		} `json:"next"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.NotNil(t, got.Current, "%s", raw)
	require.Equal(t, bpo1, got.Current.ActivationTime)
	require.Equal(t, "0x1", got.Current.ChainID)
	require.NotNil(t, got.Next)
	require.Equal(t, cfg.BPO2Time.Uint64(), got.Next.ActivationTime)
}

// hoodiPragueSnapshot is geth's TestEIP7910Config config: Hoodi with only
// Prague scheduled after genesis.
func hoodiPragueSnapshot() *params.ChainConfig {
	deposit := types.HexToAddress("0x00000000219ab540356cBB839Cbe05303d7705Fa")
	return &params.ChainConfig{
		ChainID:                 big.NewInt(560048),
		Consensus:               params.EtHashConsensus,
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
		TerminalTotalDifficulty: big.NewInt(0),
		MergeNetsplitBlock:      big.NewInt(0),
		ShanghaiTime:            big.NewInt(0),
		CancunTime:              big.NewInt(0),
		PragueTime:              big.NewInt(1742999832),
		DepositContractAddress:  &deposit,
	}
}

var (
	hoodiGenesisHash = types.HexToHash("0xbbe312868b376a3001692a646dd2d7d1e4406380dfd86b98aa8a34d1557c971b")
	hoodiGenesisTime = uint64(1742212800)
)

func requireEthConfigJSON(t *testing.T, want string, got *ethConfigResponse) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, want, string(gotJSON))
}

// geth's own eth_config fixtures (testdata/README.md), reproduced exactly.
func TestEthConfigMatchesGethHoodiVectors(t *testing.T) {
	t.Parallel()

	cfg := hoodiPragueSnapshot()
	for _, tc := range []struct {
		now  uint64
		file string
	}{
		{0, "testdata/eth_config-hoodi-next-and-last.json"},
		{1742999832, "testdata/eth_config-hoodi-current.json"},
	} {
		want, err := os.ReadFile(tc.file)
		require.NoError(t, err)
		got, err := ethConfigAt(cfg, hoodiGenesisHash, hoodiGenesisTime, tc.now)
		require.NoError(t, err)
		requireEthConfigJSON(t, string(want), got)
	}
}

// Ethereum mainnet at the BPO1 activation. The fork ids are geth's
// core/forkid mainnet table (BPO1 0xcba2a1c0, BPO2 0x07c9462e) and the blob
// parameters geth's DefaultBPO1/BPO2BlobConfig.
func TestEthConfigEthereumMainnetAtBPO1(t *testing.T) {
	t.Parallel()

	const fork = `{
		"activationTime": %d,
		"blobSchedule": {"target": %d, "max": %d, "baseFeeUpdateFraction": %d},
		"chainId": "0x1",
		"forkId": "%s",
		"precompiles": {
			"BLAKE2F": "0x0000000000000000000000000000000000000009",
			"BLS12_G1ADD": "0x000000000000000000000000000000000000000b",
			"BLS12_G1MSM": "0x000000000000000000000000000000000000000c",
			"BLS12_G2ADD": "0x000000000000000000000000000000000000000d",
			"BLS12_G2MSM": "0x000000000000000000000000000000000000000e",
			"BLS12_MAP_FP2_TO_G2": "0x0000000000000000000000000000000000000011",
			"BLS12_MAP_FP_TO_G1": "0x0000000000000000000000000000000000000010",
			"BLS12_PAIRING_CHECK": "0x000000000000000000000000000000000000000f",
			"BN254_ADD": "0x0000000000000000000000000000000000000006",
			"BN254_MUL": "0x0000000000000000000000000000000000000007",
			"BN254_PAIRING": "0x0000000000000000000000000000000000000008",
			"ECREC": "0x0000000000000000000000000000000000000001",
			"ID": "0x0000000000000000000000000000000000000004",
			"KZG_POINT_EVALUATION": "0x000000000000000000000000000000000000000a",
			"MODEXP": "0x0000000000000000000000000000000000000005",
			"P256VERIFY": "0x0000000000000000000000000000000000000100",
			"RIPEMD160": "0x0000000000000000000000000000000000000003",
			"SHA256": "0x0000000000000000000000000000000000000002"
		},
		"systemContracts": {
			"BEACON_ROOTS_ADDRESS": "0x000f3df6d732807ef1319fb7b8bb8522d0beac02",
			"CONSOLIDATION_REQUEST_PREDEPLOY_ADDRESS": "0x0000bbddc7ce488642fb579f8b00f3a590007251",
			"DEPOSIT_CONTRACT_ADDRESS": "0x00000000219ab540356cbb839cbe05303d7705fa",
			"HISTORY_STORAGE_ADDRESS": "0x0000f90827f1c53a10cb7a02335b175320002935",
			"WITHDRAWAL_REQUEST_PREDEPLOY_ADDRESS": "0x00000961ef480eb55e80d19ad83579a64c007002"
		}
	}`
	bpo1 := fmt.Sprintf(fork, 1765290071, 10, 15, 8346193, "0xcba2a1c0")
	bpo2 := fmt.Sprintf(fork, 1767747671, 14, 21, 11684671, "0x07c9462e")

	got, err := ethConfigAt(params.EthereumMainnetChainConfig, params.EthereumMainnetGenesisHash, 0, 1765290071)
	require.NoError(t, err)
	requireEthConfigJSON(t, fmt.Sprintf(`{"current": %s, "next": %s, "last": %s}`, bpo1, bpo2, bpo2), got)

	// From BPO2 on nothing further is scheduled: next and last are null.
	got, err = ethConfigAt(params.EthereumMainnetChainConfig, params.EthereumMainnetGenesisHash, 0, 1767747671)
	require.NoError(t, err)
	requireEthConfigJSON(t, fmt.Sprintf(`{"current": %s, "next": null, "last": null}`, bpo2), got)
}

func TestEthConfigRejectsN42NativeChains(t *testing.T) {
	t.Parallel()

	for _, cfg := range []*params.ChainConfig{params.MainnetQMDBStaggeredChainConfig, params.MainnetQMDBChainConfig} {
		require.NotNil(t, cfg)
		_, err := ethConfigAt(cfg, types.Hash{0x01}, 0, 2_000_000_000)
		require.ErrorIs(t, err, errEthConfigNativeChain)
	}
}
