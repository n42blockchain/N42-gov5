package zkprover

import (
	"reflect"
	"testing"
	"unsafe"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
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
