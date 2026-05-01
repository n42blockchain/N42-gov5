package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/params"
)

type engineBlockhashFixtureJSON struct {
	Pre                map[string]stablePragueModexpAccountJSON `json:"pre"`
	GenesisBlockHeader engineBlockhashGenesisHeaderJSON         `json:"genesisBlockHeader"`
	EngineNewPayloads  []stablePragueModexpEngineCallJSON       `json:"engineNewPayloads"`
}

type engineBlockhashGenesisHeaderJSON struct {
	Hash          string `json:"hash"`
	ExtraData     string `json:"extraData"`
	GasLimit      string `json:"gasLimit"`
	Timestamp     string `json:"timestamp"`
	BaseFeePerGas string `json:"baseFeePerGas"`
}

type engineBlockhashPayloadCall struct {
	Payload                     *ExecutionPayloadV4
	ExpectedBlobVersionedHashes []types.Hash
	ParentBeaconBlockRoot       types.Hash
	ExecutionRequests           []hexutil.Bytes
}

const engineBlockhashFixtureFile = "test_genesis_hash_available.json"
const engineBlockhashOsakaFixtureKey = "tests/frontier/opcodes/test_blockhash.py::test_genesis_hash_available[fork_Osaka-blockchain_test_engine-256_empty_blocks]"
const engineBlockhashPragueFixtureKey = "tests/frontier/opcodes/test_blockhash.py::test_genesis_hash_available[fork_Prague-blockchain_test_engine-256_empty_blocks]"

func TestEngineAPIv4AcceptsGenesisBlockhashFixtures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fixtureKey string
		hiveEnv    map[string]string
	}{
		{
			name:       "prague",
			fixtureKey: engineBlockhashPragueFixtureKey,
			hiveEnv: map[string]string{
				"HIVE_CHAIN_ID":                  "1",
				"HIVE_NETWORK_ID":                "1",
				"HIVE_FORK_HOMESTEAD":            "0",
				"HIVE_FORK_DAO_VOTE":             "1",
				"HIVE_FORK_TANGERINE":            "0",
				"HIVE_FORK_SPURIOUS":             "0",
				"HIVE_FORK_BYZANTIUM":            "0",
				"HIVE_FORK_CONSTANTINOPLE":       "0",
				"HIVE_FORK_PETERSBURG":           "0",
				"HIVE_FORK_ISTANBUL":             "0",
				"HIVE_FORK_BERLIN":               "0",
				"HIVE_FORK_LONDON":               "0",
				"HIVE_FORK_MERGE":                "0",
				"HIVE_TERMINAL_TOTAL_DIFFICULTY": "0",
				"HIVE_SHANGHAI_TIMESTAMP":        "0",
				"HIVE_CANCUN_TIMESTAMP":          "0",
				"HIVE_PRAGUE_TIMESTAMP":          "0",
			},
		},
		{
			name:       "osaka",
			fixtureKey: engineBlockhashOsakaFixtureKey,
			hiveEnv: map[string]string{
				"HIVE_CHAIN_ID":                  "1",
				"HIVE_NETWORK_ID":                "1",
				"HIVE_FORK_HOMESTEAD":            "0",
				"HIVE_FORK_DAO_VOTE":             "1",
				"HIVE_FORK_TANGERINE":            "0",
				"HIVE_FORK_SPURIOUS":             "0",
				"HIVE_FORK_BYZANTIUM":            "0",
				"HIVE_FORK_CONSTANTINOPLE":       "0",
				"HIVE_FORK_PETERSBURG":           "0",
				"HIVE_FORK_ISTANBUL":             "0",
				"HIVE_FORK_BERLIN":               "0",
				"HIVE_FORK_LONDON":               "0",
				"HIVE_FORK_MERGE":                "0",
				"HIVE_TERMINAL_TOTAL_DIFFICULTY": "0",
				"HIVE_SHANGHAI_TIMESTAMP":        "0",
				"HIVE_CANCUN_TIMESTAMP":          "0",
				"HIVE_OSAKA_TIMESTAMP":           "0",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			modules.N42Init()
			prevTables := kv.ChaindataTablesCfg
			kv.ChaindataTablesCfg = modules.N42TableCfg
			t.Cleanup(func() {
				kv.ChaindataTablesCfg = prevTables
			})

			fixture := loadEngineBlockhashFixtureJSON(t, tc.fixtureKey)
			payloads := loadEngineBlockhashPayloadCalls(t, fixture)

			gasLimit, err := parseFixtureUint64(fixture.GenesisBlockHeader.GasLimit)
			require.NoError(t, err)
			timestamp, err := parseFixtureUint64(fixture.GenesisBlockHeader.Timestamp)
			require.NoError(t, err)
			baseFee, err := parseFixtureUint64(fixture.GenesisBlockHeader.BaseFeePerGas)
			require.NoError(t, err)
			extraData, err := hexutil.Decode(fixture.GenesisBlockHeader.ExtraData)
			require.NoError(t, err)

			genesis := &conf.Genesis{
				Config: &params.ChainConfig{
					Consensus: params.Faker,
				},
				Alloc:      decodeStablePragueModexpAlloc(t, fixture.Pre),
				Number:     0,
				GasLimit:   gasLimit,
				Difficulty: uint256.NewInt(0),
				Timestamp:  timestamp,
				BaseFee:    uint256.NewInt(baseFee),
				Coinbase:   types.Address{},
				ExtraData:  extraData,
			}
			require.True(t, conf.ApplyHiveGenesisEnv(genesis, func(key string) (string, bool) {
				value, ok := tc.hiveEnv[key]
				return value, ok
			}))

			db := memdb.NewTestDB(t)
			genesisBlock := writeBlockhashFixtureGenesis(t, db, genesis)
			require.Equal(t, types.HexToHash(fixture.GenesisBlockHeader.Hash), ethCompatibleBlockHash(genesisBlock, genesis.Config))

			chain := &canonicalCheckChainStub{
				header: genesisBlock.Header().(*block.Header),
				blk:    genesisBlock,
				config: genesis.Config,
				db:     db,
			}
			api := &API{
				bc:            chain,
				db:            db,
				engine:        &apiTestEngine{},
				chainConfig:   genesis.Config,
				engineOverlay: newEngineOverlay(),
			}
			engine := NewEngineAPIv4(NewBlockChainAPI(api))
			engine.SetStateAdapter(NewEngineStateAdapter(db, nil, genesis.Config, &apiTestEngine{}))

			genesisHash := ethCompatibleBlockHash(genesisBlock, genesis.Config)
			fcuResp, err := engine.ForkchoiceUpdatedV4(context.Background(), &ForkchoiceStateV1{
				HeadBlockHash:      genesisHash,
				SafeBlockHash:      genesisHash,
				FinalizedBlockHash: genesisHash,
			}, nil)
			require.NoError(t, err)
			require.Equal(t, PayloadStatusValid, fcuResp.PayloadStatus.Status)

			for i, call := range payloads {
				resp, err := engine.NewPayloadV4(
					context.Background(),
					call.Payload,
					call.ExpectedBlobVersionedHashes,
					&call.ParentBeaconBlockRoot,
					call.ExecutionRequests,
				)
				require.NoErrorf(t, err, "payload %d/%d", i+1, len(payloads))
				require.NotNilf(t, resp, "payload %d/%d", i+1, len(payloads))
				if resp.Status != PayloadStatusValid {
					validationError := "<nil>"
					if resp.ValidationError != nil {
						validationError = *resp.ValidationError
					}
					t.Fatalf(
						"payload %d/%d invalid: status=%s latestValid=%v validationError=%s block=%d hash=%s",
						i+1,
						len(payloads),
						resp.Status,
						resp.LatestValidHash,
						validationError,
						uint64(call.Payload.BlockNumber),
						call.Payload.BlockHash,
					)
				}

				fcuResp, err = engine.ForkchoiceUpdatedV4(context.Background(), &ForkchoiceStateV1{
					HeadBlockHash:      call.Payload.BlockHash,
					SafeBlockHash:      call.Payload.BlockHash,
					FinalizedBlockHash: call.Payload.BlockHash,
				}, nil)
				require.NoErrorf(t, err, "forkchoice %d/%d", i+1, len(payloads))
				require.Equalf(t, PayloadStatusValid, fcuResp.PayloadStatus.Status, "forkchoice %d/%d", i+1, len(payloads))
			}
		})
	}
}

func loadEngineBlockhashFixtureJSON(t *testing.T, fixtureKey string) engineBlockhashFixtureJSON {
	t.Helper()

	raw, err := os.ReadFile(engineBlockhashFixturePath(t))
	require.NoError(t, err)

	var fixtures map[string]engineBlockhashFixtureJSON
	require.NoError(t, json.Unmarshal(raw, &fixtures))

	fixture, ok := fixtures[fixtureKey]
	require.True(t, ok)
	return fixture
}

func loadEngineBlockhashPayloadCalls(t *testing.T, fixture engineBlockhashFixtureJSON) []engineBlockhashPayloadCall {
	t.Helper()

	calls := make([]engineBlockhashPayloadCall, 0, len(fixture.EngineNewPayloads))
	for _, call := range fixture.EngineNewPayloads {
		require.Equal(t, "4", call.NewPayloadVersion)
		require.Len(t, call.Params, 4)

		var payload ExecutionPayloadV4
		require.NoError(t, json.Unmarshal(call.Params[0], &payload))

		var blobVersionedHashes []types.Hash
		require.NoError(t, json.Unmarshal(call.Params[1], &blobVersionedHashes))

		var parentBeaconBlockRoot types.Hash
		require.NoError(t, json.Unmarshal(call.Params[2], &parentBeaconBlockRoot))

		var executionRequests []hexutil.Bytes
		require.NoError(t, json.Unmarshal(call.Params[3], &executionRequests))

		calls = append(calls, engineBlockhashPayloadCall{
			Payload:                     &payload,
			ExpectedBlobVersionedHashes: blobVersionedHashes,
			ParentBeaconBlockRoot:       parentBeaconBlockRoot,
			ExecutionRequests:           executionRequests,
		})
	}
	return calls
}

func engineBlockhashFixturePath(t *testing.T) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)

	var candidates []string
	if cacheDir, err := os.UserCacheDir(); err == nil {
		candidates = append(candidates, filepath.Join(
			cacheDir,
			"ethereum-execution-spec-tests",
			"cached_downloads",
			"ethereum",
			"execution-spec-tests",
			"v5.4.0",
			"fixtures_develop",
			"fixtures",
			"blockchain_tests_engine",
			"frontier",
			"opcodes",
			engineBlockhashFixtureFile,
		))
	}
	candidates = append(candidates, filepath.Join(
		filepath.Dir(currentFile),
		"..", "..", "..",
		"erigon2.7",
		"tests",
		"execution-spec-tests",
		"blockchain_tests_engine",
		"frontier",
		"opcodes",
		engineBlockhashFixtureFile,
	))

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	t.Skipf("develop EEST blockhash fixture missing; checked: %s", strings.Join(candidates, ", "))
	return ""
}

func writeBlockhashFixtureGenesis(t *testing.T, db kv.RwDB, genesis *conf.Genesis) *block.Block {
	t.Helper()

	genesisBlock := (*block.Block)(nil)
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := (&internalcore.GenesisBlock{GenesisConfig: genesis}).Write(tx)
		if err != nil {
			return err
		}
		genesisBlock = blk
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, genesisBlock)
	return genesisBlock
}
