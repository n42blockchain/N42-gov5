package conf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestGenesisUnmarshalHiveStyle(t *testing.T) {
	t.Parallel()

	const input = `{
		"config": {"chainId": 7},
		"nonce": "0x0000000000000001",
		"timestamp": "0x1234",
		"extraData": "0x0102",
		"gasLimit": "0x2fefd8",
		"difficulty": "0x30000",
		"coinbase": "0x0000000000000000000000000000000000000000",
		"alloc": {
			"cf49fda3be353c69b41ed96333cd24302da4556f": {
				"balance": "0x1",
				"nonce": "0x2",
				"code": "0x6000",
				"storage": {
					"0x0000000000000000000000000000000000000000000000000000000000000000":
						"0x0000000000000000000000000000000000000000000000000000000000001234"
				}
			}
		},
		"baseFeePerGas": "0x7"
	}`

	var genesis Genesis
	if err := json.Unmarshal([]byte(input), &genesis); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if genesis.Nonce != 1 {
		t.Fatalf("unexpected nonce: got %d want 1", genesis.Nonce)
	}
	if genesis.Timestamp != 0x1234 {
		t.Fatalf("unexpected timestamp: got %d want %d", genesis.Timestamp, 0x1234)
	}
	if genesis.GasLimit != 0x2fefd8 {
		t.Fatalf("unexpected gasLimit: got %d want %d", genesis.GasLimit, 0x2fefd8)
	}
	if genesis.Difficulty == nil || genesis.Difficulty.Uint64() != 0x30000 {
		t.Fatalf("unexpected difficulty: got %v", genesis.Difficulty)
	}
	if got := types.BytesToAddress([]byte{
		0xcf, 0x49, 0xfd, 0xa3, 0xbe, 0x35, 0x3c, 0x69, 0xb4, 0x1e,
		0xd9, 0x63, 0x33, 0xcd, 0x24, 0x30, 0x2d, 0xa4, 0x55, 0x6f,
	}); genesis.Alloc[got].Nonce != 2 {
		t.Fatalf("unexpected alloc nonce: got %d want 2", genesis.Alloc[got].Nonce)
	}
	if len(genesis.ExtraData) != 2 || genesis.ExtraData[0] != 0x01 || genesis.ExtraData[1] != 0x02 {
		t.Fatalf("unexpected extraData: %#v", genesis.ExtraData)
	}
	if len(genesis.Alloc) != 1 {
		t.Fatalf("unexpected alloc size: got %d want 1", len(genesis.Alloc))
	}
	if genesis.BaseFee == nil || genesis.BaseFee.Uint64() != 7 {
		t.Fatalf("unexpected base fee: got %v", genesis.BaseFee)
	}
}

func TestGenesisUnmarshalAllowsLeadingZeroHexQuantities(t *testing.T) {
	t.Parallel()

	const input = `{
		"config": {"chainId": 7},
		"gasLimit": "0x07270e00",
		"difficulty": "0x00",
		"baseFeePerGas": "0x07",
		"alloc": {
			"0000000000000000000000000000000000000001": {
				"balance": "0x00",
				"nonce": "0x00"
			}
		}
	}`

	var genesis Genesis
	if err := json.Unmarshal([]byte(input), &genesis); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if genesis.GasLimit != 0x07270e00 {
		t.Fatalf("unexpected gasLimit: got %d want %d", genesis.GasLimit, 0x07270e00)
	}
	if genesis.Difficulty == nil || genesis.Difficulty.Sign() != 0 {
		t.Fatalf("unexpected difficulty: got %v want 0", genesis.Difficulty)
	}
	if genesis.BaseFee == nil || genesis.BaseFee.Uint64() != 7 {
		t.Fatalf("unexpected base fee: got %v want 7", genesis.BaseFee)
	}
}

func TestGenesisUnmarshalHiveEngineFixture(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "testdata", "hive-engine-genesis.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile failed: %v", err)
	}

	var genesis Genesis
	if err := json.Unmarshal(data, &genesis); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if genesis.Config == nil || genesis.Config.ChainID == nil || genesis.Config.ChainID.Uint64() != 7 {
		t.Fatalf("unexpected chain config: %#v", genesis.Config)
	}
	if genesis.Config.Consensus != params.Faker {
		t.Fatalf("unexpected consensus: got %q want %q", genesis.Config.Consensus, params.Faker)
	}
	if genesis.Timestamp != 0x1234 {
		t.Fatalf("unexpected timestamp: got %d want %d", genesis.Timestamp, 0x1234)
	}
	if genesis.GasLimit == 0 {
		t.Fatal("gasLimit should not be zero")
	}
	if len(genesis.Alloc) < 4 {
		t.Fatalf("alloc should contain fixture accounts, got %d", len(genesis.Alloc))
	}
}

func TestGenesisUnmarshalPreservesExplicitHeaderFields(t *testing.T) {
	t.Parallel()

	const input = `{
		"config": {"chainId": 1},
		"gasLimit": "0x07270e00",
		"difficulty": "0x0",
		"stateRoot": "0x95a6b74fbcb35dd5bd4dc03e03236164da625fc661cadfe58674b7cd27e664e1",
		"transactionsTrie": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		"receiptTrie": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
		"blobGasUsed": "0x0",
		"excessBlobGas": "0x60000",
		"hash": "0xcb4c993e716e052eac1d566becae628b24d0f490c4efaff9f4153b8cf092a5f0",
		"alloc": {}
	}`

	var genesis Genesis
	if err := json.Unmarshal([]byte(input), &genesis); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if genesis.StateRoot != types.HexToHash("0x95a6b74fbcb35dd5bd4dc03e03236164da625fc661cadfe58674b7cd27e664e1") {
		t.Fatalf("unexpected stateRoot: %s", genesis.StateRoot)
	}
	if genesis.TxHash != types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421") {
		t.Fatalf("unexpected transactionsTrie: %s", genesis.TxHash)
	}
	if genesis.ReceiptHash != types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421") {
		t.Fatalf("unexpected receiptTrie: %s", genesis.ReceiptHash)
	}
	if genesis.Hash != types.HexToHash("0xcb4c993e716e052eac1d566becae628b24d0f490c4efaff9f4153b8cf092a5f0") {
		t.Fatalf("unexpected hash: %s", genesis.Hash)
	}
	if genesis.BlobGasUsed != 0 {
		t.Fatalf("unexpected blobGasUsed: got %d want 0", genesis.BlobGasUsed)
	}
	if genesis.ExcessBlobGas != 0x60000 {
		t.Fatalf("unexpected excessBlobGas: got %d want %d", genesis.ExcessBlobGas, 0x60000)
	}
}

func TestGenesisUnmarshalInfersConsensusFromExplicitEngine(t *testing.T) {
	t.Parallel()

	const input = `{
		"config": {
			"chainId": 7,
			"clique": {"period": 1, "epoch": 30000}
		},
		"gasLimit": "0x1",
		"difficulty": "0x1"
	}`

	var genesis Genesis
	if err := json.Unmarshal([]byte(input), &genesis); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if genesis.Config == nil {
		t.Fatal("config should not be nil")
	}
	if genesis.Config.Consensus != params.CliqueConsensus {
		t.Fatalf("unexpected consensus: got %q want %q", genesis.Config.Consensus, params.CliqueConsensus)
	}
}
