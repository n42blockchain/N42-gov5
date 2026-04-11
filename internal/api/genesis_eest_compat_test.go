package api

import (
	"encoding/json"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	internalcore "github.com/n42blockchain/N42/internal"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
)

func TestEthCompatibleBlockHashMatchesEESTGenesisFixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		genesisJSON string
		wantHash    types.Hash
	}{
		{
			name: "paris",
			genesisJSON: `{
				"config": {
					"chainId": 1,
					"homesteadBlock": 0,
					"eip150Block": 0,
					"eip155Block": 0,
					"eip158Block": 0,
					"byzantiumBlock": 0,
					"constantinopleBlock": 0,
					"petersburgBlock": 0,
					"istanbulBlock": 0,
					"muirGlacierBlock": 0,
					"berlinBlock": 0,
					"londonBlock": 0,
					"mergeNetsplitBlock": 0,
					"terminalTotalDifficulty": 0,
					"terminalTotalDifficultyPassed": true,
					"blobSchedule": {}
				},
				"nonce": "0x0000000000000000",
				"timestamp": "0x00",
				"extraData": "0x00",
				"gasLimit": "0x01c9c380",
				"difficulty": "0x00",
				"mixHash": "0x0000000000000000000000000000000000000000000000000000000000000000",
				"coinbase": "0x0000000000000000000000000000000000000000",
				"stateRoot": "0x95a6b74fbcb35dd5bd4dc03e03236164da625fc661cadfe58674b7cd27e664e1",
				"transactionsTrie": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
				"receiptTrie": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
				"alloc": {},
				"number": "0x00",
				"gasUsed": "0x00",
				"parentHash": "0x0000000000000000000000000000000000000000000000000000000000000000",
				"baseFeePerGas": "0x07"
			}`,
			wantHash: types.HexToHash("0x94d2ccdb925e2d2541118a8e6a7fde7b0baeb9fabb060a97580df63edc359128"),
		},
		{
			name: "shanghai",
			genesisJSON: `{
				"config": {
					"chainId": 1,
					"homesteadBlock": 0,
					"eip150Block": 0,
					"eip155Block": 0,
					"eip158Block": 0,
					"byzantiumBlock": 0,
					"constantinopleBlock": 0,
					"petersburgBlock": 0,
					"istanbulBlock": 0,
					"muirGlacierBlock": 0,
					"berlinBlock": 0,
					"londonBlock": 0,
					"mergeNetsplitBlock": 0,
					"terminalTotalDifficulty": 0,
					"terminalTotalDifficultyPassed": true,
					"shanghaiTime": 0,
					"blobSchedule": {}
				},
				"nonce": "0x0000000000000000",
				"timestamp": "0x00",
				"extraData": "0x00",
				"gasLimit": "0x01c9c380",
				"difficulty": "0x00",
				"mixHash": "0x0000000000000000000000000000000000000000000000000000000000000000",
				"coinbase": "0x0000000000000000000000000000000000000000",
				"stateRoot": "0x95a6b74fbcb35dd5bd4dc03e03236164da625fc661cadfe58674b7cd27e664e1",
				"transactionsTrie": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
				"receiptTrie": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
				"alloc": {},
				"number": "0x00",
				"gasUsed": "0x00",
				"parentHash": "0x0000000000000000000000000000000000000000000000000000000000000000",
				"baseFeePerGas": "0x07",
				"withdrawalsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"
			}`,
			wantHash: types.HexToHash("0xc70fcf42824013847ebfb61d7a4b5079884bde36feb9e9393da82b17655d49c3"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var genesis conf.Genesis
			if err := json.Unmarshal([]byte(tc.genesisJSON), &genesis); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}

			blk, _, err := (&internalcore.GenesisBlock{GenesisConfig: &genesis}).ToBlock()
			if err != nil {
				t.Fatalf("ToBlock() error = %v", err)
			}
			if got := blk.Hash(); got != tc.wantHash {
				t.Fatalf("ToBlock().Hash() = %s, want %s", got, tc.wantHash)
			}

			if got := ethCompatibleBlockHash(blk, genesis.Config); got != tc.wantHash {
				t.Fatalf("ethCompatibleBlockHash() = %s, want %s", got, tc.wantHash)
			}

			header := blk.Header().(*block.Header)
			if header.TxHash != hash.EmptyRootHash {
				t.Fatalf("TxHash = %s, want %s", header.TxHash, hash.EmptyRootHash)
			}
			if header.ReceiptHash != hash.EmptyRootHash {
				t.Fatalf("ReceiptHash = %s, want %s", header.ReceiptHash, hash.EmptyRootHash)
			}

			headerFields := RPCMarshalHeader(blk.Header(), genesis.Config)
			if got := mustJSONString(t, headerFields["hash"]); got != `"`+tc.wantHash.Hex()+`"` {
				t.Fatalf("RPC hash = %s, want %q", got, tc.wantHash.Hex())
			}
			if got := mustJSONString(t, headerFields["transactionsRoot"]); got != `"`+hash.EmptyRootHash.Hex()+`"` {
				t.Fatalf("RPC transactionsRoot = %s, want %q", got, hash.EmptyRootHash.Hex())
			}
			if got := mustJSONString(t, headerFields["receiptsRoot"]); got != `"`+hash.EmptyRootHash.Hex()+`"` {
				t.Fatalf("RPC receiptsRoot = %s, want %q", got, hash.EmptyRootHash.Hex())
			}
		})
	}
}

func mustJSONString(t *testing.T, value interface{}) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(encoded)
}
