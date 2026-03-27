package zkprover

import (
	"math/big"
	"reflect"
	"testing"
	"unsafe"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestBuildGuestInputRejectsNilBlockNumber(t *testing.T) {
	blk := testZKProverBlock(nil)
	parent := &block.Header{
		Number:     uint256.NewInt(0),
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}

	_, err := BuildGuestInput(&params.ChainConfig{}, blk, parent, nil)
	if err == nil || err.Error() != "block number unavailable" {
		t.Fatalf("BuildGuestInput() error = %v", err)
	}
}

func TestBuildGuestInputRejectsMissingChainID(t *testing.T) {
	blk := testZKProverBlock(uint256.NewInt(1))
	parent := &block.Header{
		Number:     uint256.NewInt(0),
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}

	_, err := BuildGuestInput(&params.ChainConfig{}, blk, parent, nil)
	if err == nil || err.Error() != "chain ID unavailable" {
		t.Fatalf("BuildGuestInput() error = %v", err)
	}
}

func TestBuildGuestForkConfigCapturesBlockTimeAndConsensusFlags(t *testing.T) {
	feeCollector := types.HexToAddress("0x00000000000000000000000000000000000000f1")
	cfg := &params.ChainConfig{
		HomesteadBlock:                big.NewInt(0),
		TangerineWhistleBlock:         big.NewInt(0),
		SpuriousDragonBlock:           big.NewInt(0),
		ByzantiumBlock:                big.NewInt(0),
		ConstantinopleBlock:           big.NewInt(0),
		PetersburgBlock:               big.NewInt(0),
		IstanbulBlock:                 big.NewInt(0),
		BerlinBlock:                   big.NewInt(0),
		LondonBlock:                   big.NewInt(0),
		ShanghaiBlock:                 big.NewInt(0),
		CancunBlock:                   big.NewInt(0),
		BeijingBlock:                  big.NewInt(0),
		PragueTime:                    big.NewInt(20),
		PectraTime:                    big.NewInt(20),
		OsakaTime:                     big.NewInt(20),
		FusakaTime:                    big.NewInt(20),
		NanoBlock:                     big.NewInt(0),
		MoranBlock:                    big.NewInt(0),
		Eip1559FeeCollector:           &feeCollector,
		Eip1559FeeCollectorTransition: big.NewInt(0),
		Parlia:                        &params.ParliaConfig{},
		Aura:                          &params.AuRaConfig{},
		PQPrecompilesTime:             big.NewInt(20),
	}

	forkConfig := buildGuestForkConfig(cfg, 10, 20)
	if !forkConfig.IsHomestead || !forkConfig.IsEIP150 || !forkConfig.IsEIP155 || !forkConfig.IsEIP158 {
		t.Fatalf("expected early block forks to be enabled, got %+v", forkConfig)
	}
	if !forkConfig.IsByzantium || !forkConfig.IsConstantinople || !forkConfig.IsPetersburg || !forkConfig.IsIstanbul {
		t.Fatalf("expected mid block forks to be enabled, got %+v", forkConfig)
	}
	if !forkConfig.IsBerlin || !forkConfig.IsLondon || !forkConfig.IsShanghai || !forkConfig.IsCancun {
		t.Fatalf("expected late execution forks to be enabled, got %+v", forkConfig)
	}
	if !forkConfig.IsBeijing || !forkConfig.IsPrague || !forkConfig.IsPectra || !forkConfig.IsOsaka || !forkConfig.IsFusaka {
		t.Fatalf("expected timestamp/block feature forks to be enabled, got %+v", forkConfig)
	}
	if !forkConfig.IsNano || !forkConfig.IsMoran || !forkConfig.IsEip1559FeeCollector {
		t.Fatalf("expected n42 block features to be enabled, got %+v", forkConfig)
	}
	if !forkConfig.IsParlia || !forkConfig.IsAura || !forkConfig.IsPQPrecompiles {
		t.Fatalf("expected consensus/precompile flags to be enabled, got %+v", forkConfig)
	}
}

func testZKProverBlock(number *uint256.Int) *block.Block {
	blk := &block.Block{}
	setZKProverBlockField(blk, "header", &block.Header{
		Number:     number,
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	})
	setZKProverBlockField(blk, "body", &block.Body{})
	return blk
}

func setZKProverBlockField(target interface{}, name string, value interface{}) {
	field := reflect.ValueOf(target).Elem().FieldByName(name)
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}
