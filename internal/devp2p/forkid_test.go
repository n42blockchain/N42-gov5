package devp2p

import (
	"math/big"
	"testing"

	gethforkid "github.com/ethereum/go-ethereum/core/forkid"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	gethparams "github.com/ethereum/go-ethereum/params"

	n42types "github.com/n42blockchain/N42/common/types"
	n42params "github.com/n42blockchain/N42/params"
)

func uint64Ptr(v uint64) *uint64 {
	return &v
}

func TestNewForkIDMatchesGeth(t *testing.T) {
	tests := []struct {
		name        string
		genesisTime uint64
		head        uint64
		time        uint64
		n42Cfg      *n42params.ChainConfig
		gethCfg     *gethparams.ChainConfig
	}{
		{
			name:        "genesis only",
			genesisTime: 0,
			head:        0,
			time:        0,
			n42Cfg: &n42params.ChainConfig{
				ChainID:     big.NewInt(7),
				LondonBlock: big.NewInt(0),
			},
			gethCfg: &gethparams.ChainConfig{
				ChainID:     big.NewInt(7),
				LondonBlock: big.NewInt(0),
			},
		},
		{
			name:        "future block fork",
			genesisTime: 0,
			head:        4,
			time:        4,
			n42Cfg: &n42params.ChainConfig{
				ChainID:           big.NewInt(7),
				LondonBlock:       big.NewInt(0),
				ArrowGlacierBlock: big.NewInt(5),
			},
			gethCfg: &gethparams.ChainConfig{
				ChainID:           big.NewInt(7),
				LondonBlock:       big.NewInt(0),
				ArrowGlacierBlock: big.NewInt(5),
			},
		},
		{
			name:        "time forks",
			genesisTime: 1,
			head:        0,
			time:        15,
			n42Cfg: &n42params.ChainConfig{
				ChainID:      big.NewInt(7),
				LondonBlock:  big.NewInt(0),
				ShanghaiTime: big.NewInt(10),
				CancunTime:   big.NewInt(20),
			},
			gethCfg: &gethparams.ChainConfig{
				ChainID:      big.NewInt(7),
				LondonBlock:  big.NewInt(0),
				ShanghaiTime: uint64Ptr(10),
				CancunTime:   uint64Ptr(20),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			genesis := gethtypes.NewBlockWithHeader(&gethtypes.Header{
				Number: big.NewInt(0),
				Time:   tt.genesisTime,
			})
			want := gethforkid.NewID(tt.gethCfg, genesis, tt.head, tt.time)
			got := newForkID(tt.n42Cfg, n42types.Hash(genesis.Hash()), tt.genesisTime, tt.head, tt.time)
			if got.Hash != want.Hash || got.Next != want.Next {
				t.Fatalf("newForkID() = (hash=%#x next=%d), want (hash=%#x next=%d)", got.Hash, got.Next, want.Hash, want.Next)
			}
		})
	}
}
