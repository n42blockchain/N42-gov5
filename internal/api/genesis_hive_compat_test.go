package api

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	n42crypto "github.com/n42blockchain/N42/crypto"

	"github.com/n42blockchain/N42/conf"
	internalcore "github.com/n42blockchain/N42/internal"

	"github.com/n42blockchain/N42/common/types"
)

func TestEthCompatibleBlockHashMatchesHiveForkIDGenesisFixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		timestamp uint64
		wantHash  types.Hash
	}{
		{
			name:      "genesis_0_paris",
			timestamp: 0,
			wantHash:  types.HexToHash("0x34cb47b1a70a73ad1e455e97f33827d94284f5e7b819f4132e466cf3cd0a0d56"),
		},
		{
			name:      "genesis_1_paris",
			timestamp: 1,
			wantHash:  types.HexToHash("0x76541b14776a04c88dff1d5a8e46e14373cf1fffb618fc4bf22f2ce1abcbfb37"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			genesis := loadHiveEngineGenesisFixture(t)
			genesis.Timestamp = tc.timestamp
			genesis.Config.TerminalTotalDifficulty = genesis.Difficulty.ToBig()
			genesis.Config.MergeNetsplitBlock = big.NewInt(0)
			addHiveEngineTestAccounts(genesis)

			blk, _, err := (&internalcore.GenesisBlock{GenesisConfig: genesis}).ToBlock()
			if err != nil {
				t.Fatalf("ToBlock() error = %v", err)
			}
			if got := blk.Hash(); got != tc.wantHash {
				t.Fatalf("ToBlock().Hash() = %s, want %s", got, tc.wantHash)
			}

			got, err := internalcore.EthereumCompatibleGenesisHash(genesis)
			if err != nil {
				t.Fatalf("EthereumCompatibleGenesisHash() error = %v", err)
			}

			if got != tc.wantHash {
				t.Fatalf("EthereumCompatibleGenesisHash() = %s, want %s", got, tc.wantHash)
			}
		})
	}
}

func loadHiveEngineGenesisFixture(t *testing.T) *conf.Genesis {
	t.Helper()

	path := filepath.Join("..", "..", "tests", "eth-hive", "simulators", "ethereum", "engine", "init", "genesis.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(data, &genesis); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return &genesis
}

func addHiveEngineTestAccounts(genesis *conf.Genesis) {
	if genesis == nil {
		return
	}
	if genesis.Alloc == nil {
		genesis.Alloc = make(conf.GenesisAlloc)
	}
	for i := uint64(0); i < 1000; i++ {
		var raw [8]byte
		binary.BigEndian.PutUint64(raw[:], i)
		seed := sha256.Sum256(raw[:])
		key, err := n42crypto.ToECDSA(seed[:])
		if err != nil {
			panic(err)
		}
		addr := n42crypto.PubkeyToAddress(key.PublicKey)
		genesis.Alloc[addr] = conf.GenesisAccount{Balance: "0x123450000000000000000"}
	}
}
