package eth69

import (
	"context"
	"reflect"
	"testing"
	"unsafe"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/block"
)

type chainStub struct {
	current *block.Block
	genesis *block.Block
}

func (s *chainStub) CurrentBlock() *block.Block           { return s.current }
func (s *chainStub) GetBlockByNumber(uint64) *block.Block { return nil }
func (s *chainStub) GenesisBlock() *block.Block           { return s.genesis }

type peerSenderStub struct {
	broadcasts int
}

func (s *peerSenderStub) SendBlockRangeUpdate(context.Context, peer.ID, *BlockRangeUpdatePacket) error {
	return nil
}

func (s *peerSenderStub) BroadcastBlockRangeUpdate(context.Context, *BlockRangeUpdatePacket) error {
	s.broadcasts++
	return nil
}

func TestMakeStatusPacketRejectsNilCurrentBlockNumber(t *testing.T) {
	handler := NewHandler(&chainStub{
		current: testEth69Block(nil),
		genesis: testEth69Block(uint256.NewInt(0)),
	}, 1, 0, &peerSenderStub{})

	if packet := handler.MakeStatusPacket(); packet != nil {
		t.Fatalf("MakeStatusPacket() = %#v, want nil", packet)
	}
}

func TestMakeBlockRangeUpdatePacketRejectsNilCurrentBlockNumber(t *testing.T) {
	handler := NewHandler(&chainStub{
		current: testEth69Block(nil),
		genesis: testEth69Block(uint256.NewInt(0)),
	}, 1, 0, &peerSenderStub{})

	if packet := handler.MakeBlockRangeUpdatePacket(); packet != nil {
		t.Fatalf("MakeBlockRangeUpdatePacket() = %#v, want nil", packet)
	}
}

func TestOnNewBlockSkipsNilBlockNumber(t *testing.T) {
	sender := &peerSenderStub{}
	handler := NewHandler(&chainStub{
		current: testEth69Block(uint256.NewInt(8)),
		genesis: testEth69Block(uint256.NewInt(0)),
	}, 1, 0, sender)

	handler.OnNewBlock(testEth69Block(nil))

	if sender.broadcasts != 0 {
		t.Fatalf("BroadcastBlockRangeUpdate calls = %d, want 0", sender.broadcasts)
	}
}

func TestSetEarliestBlockSkipsNilCurrentBlockNumber(t *testing.T) {
	handler := NewHandler(&chainStub{
		current: testEth69Block(nil),
		genesis: testEth69Block(uint256.NewInt(0)),
	}, 1, 0, &peerSenderStub{})

	handler.SetEarliestBlock(5)

	local := handler.GetLocalRange()
	if local.EarliestBlock != 0 || local.LatestBlock != 0 {
		t.Fatalf("GetLocalRange() = %#v, want unchanged zero range", local)
	}
}

func testEth69Block(number *uint256.Int) *block.Block {
	blk := &block.Block{}
	setEth69BlockField(blk, "header", &block.Header{
		Number:     number,
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	})
	setEth69BlockField(blk, "body", &block.Body{})
	return blk
}

func setEth69BlockField(target interface{}, name string, value interface{}) {
	field := reflect.ValueOf(target).Elem().FieldByName(name)
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}
