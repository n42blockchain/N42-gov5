package misc

import (
	"testing"

	lru "github.com/hashicorp/golang-lru"
	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

type sealHeaderStub struct{}

func (h *sealHeaderStub) Number64() *uint256.Int               { return uint256.NewInt(1) }
func (h *sealHeaderStub) BaseFee64() *uint256.Int              { return nil }
func (h *sealHeaderStub) Hash() types.Hash                     { return types.Hash{} }
func (h *sealHeaderStub) ToProtoMessage() proto.Message        { return nil }
func (h *sealHeaderStub) FromProtoMessage(proto.Message) error { return nil }
func (h *sealHeaderStub) Marshal() ([]byte, error)             { return nil, nil }
func (h *sealHeaderStub) Unmarshal([]byte) error               { return nil }
func (h *sealHeaderStub) StateRoot() types.Hash                { return types.Hash{} }

var _ block.IHeader = (*sealHeaderStub)(nil)

func TestEcrecoverRejectsUnexpectedHeaderType(t *testing.T) {
	cache, err := lru.NewARC(1)
	if err != nil {
		t.Fatalf("NewARC() error = %v", err)
	}

	_, err = Ecrecover(&sealHeaderStub{}, cache)
	if err != ErrInvalidHeaderType {
		t.Fatalf("Ecrecover() error = %v, want %v", err, ErrInvalidHeaderType)
	}
}
