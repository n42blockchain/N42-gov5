package tracers

import (
	"math/big"
	"reflect"
	"testing"
	"unsafe"

	"github.com/holiman/uint256"

	types "github.com/n42blockchain/N42/common/block"
)

func TestRequireBlockNumberRejectsNilBlockNumber(t *testing.T) {
	block := testBlock(&types.Header{
		Difficulty: uint256.NewInt(1),
	}, &types.Body{})

	_, err := requireBlockNumber(block, "block number unavailable")
	if err == nil || err.Error() != "block number unavailable" {
		t.Fatalf("requireBlockNumber() error = %v", err)
	}
}

func TestRequireBlockNumberAcceptsNumber(t *testing.T) {
	block := types.NewBlock(&types.Header{
		Number:     uint256.NewInt(9),
		Difficulty: uint256.NewInt(1),
	}, nil).(*types.Block)

	number, err := requireBlockNumber(block, "block number unavailable")
	if err != nil {
		t.Fatalf("requireBlockNumber() error = %v", err)
	}
	if number.Uint64() != 9 {
		t.Fatalf("requireBlockNumber() = %d, want 9", number.Uint64())
	}
}

func TestBlockBaseFeeBigAllowsNil(t *testing.T) {
	block := types.NewBlock(&types.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
	}, nil).(*types.Block)

	if got := blockBaseFeeBig(block); got != nil {
		t.Fatalf("blockBaseFeeBig() = %v, want nil", got)
	}
}

func TestBlockBaseFeeBigReturnsValue(t *testing.T) {
	block := types.NewBlock(&types.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(7),
	}, nil).(*types.Block)

	if got := blockBaseFeeBig(block); got == nil || got.Cmp(big.NewInt(7)) != 0 {
		t.Fatalf("blockBaseFeeBig() = %v, want 7", got)
	}
}

func TestTraceBlockRejectsNilBlockNumber(t *testing.T) {
	block := testBlock(&types.Header{
		Difficulty: uint256.NewInt(1),
	}, &types.Body{})

	api := &API{}
	_, err := api.traceBlock(t.Context(), block, nil)
	if err == nil || err.Error() != "block number unavailable" {
		t.Fatalf("traceBlock() error = %v", err)
	}
}

func testBlock(header *types.Header, body *types.Body) *types.Block {
	blk := &types.Block{}
	setUnexportedField(blk, "header", header)
	setUnexportedField(blk, "body", body)
	return blk
}

func setUnexportedField(target interface{}, name string, value interface{}) {
	field := reflect.ValueOf(target).Elem().FieldByName(name)
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}
