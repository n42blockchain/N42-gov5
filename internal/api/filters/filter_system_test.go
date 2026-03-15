package filters

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"google.golang.org/protobuf/proto"
)

type headerStub struct {
	hash   types.Hash
	number *uint256.Int
}

func (h *headerStub) Number64() *uint256.Int                       { return h.number }
func (h *headerStub) BaseFee64() *uint256.Int                      { return nil }
func (h *headerStub) Hash() types.Hash                             { return h.hash }
func (h *headerStub) ToProtoMessage() proto.Message                { return nil }
func (h *headerStub) FromProtoMessage(message proto.Message) error { return nil }
func (h *headerStub) Marshal() ([]byte, error)                     { return nil, nil }
func (h *headerStub) Unmarshal(data []byte) error                  { return nil }
func (h *headerStub) StateRoot() types.Hash                        { return types.Hash{} }

var _ block.IHeader = (*headerStub)(nil)

func TestLightFilterNewHeadRejectsUnexpectedOldHeaderType(t *testing.T) {
	es := &EventSystem{
		lastHead: &headerStub{
			hash:   types.HexToHash("0x01"),
			number: uint256.NewInt(2),
		},
	}
	newHeader := &headerStub{
		hash:   types.HexToHash("0x02"),
		number: uint256.NewInt(1),
	}

	called := false
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("lightFilterNewHead panicked: %v", r)
		}
	}()
	es.lightFilterNewHead(newHeader, func(block.IHeader, bool) {
		called = true
	})

	if called {
		t.Fatal("callback should not be called for unexpected old header type")
	}
}

func TestLightFilterNewHeadRejectsUnexpectedNewHeaderType(t *testing.T) {
	es := &EventSystem{
		lastHead: &headerStub{
			hash:   types.HexToHash("0x01"),
			number: uint256.NewInt(1),
		},
	}
	newHeader := &headerStub{
		hash:   types.HexToHash("0x02"),
		number: uint256.NewInt(2),
	}

	called := false
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("lightFilterNewHead panicked: %v", r)
		}
	}()
	es.lightFilterNewHead(newHeader, func(block.IHeader, bool) {
		called = true
	})

	if called {
		t.Fatal("callback should not be called for unexpected new header type")
	}
}
