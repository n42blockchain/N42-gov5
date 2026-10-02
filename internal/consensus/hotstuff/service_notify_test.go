package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestService_NotifyBlockRejected covers the nil-engine guard and the
// engine-present delegation path.
func TestService_NotifyBlockRejected(t *testing.T) {
	s := &Service{}
	s.NotifyBlockRejected(types.Hash{0x01}) // nil engine: must not panic

	setup := newTestSetup(t, 4)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	s.engine = h
	s.NotifyBlockRejected(types.Hash{0x02})
}

// TestService_NotifyBlockCheckedAndHeaderKnown covers the engine-present
// delegation path for both notifiers.
func TestService_NotifyBlockCheckedAndHeaderKnown(t *testing.T) {
	setup := newTestSetup(t, 4)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	s := &Service{engine: h}

	hash := types.Hash{0x03}
	parent := types.Hash{0x04}
	s.NotifyBlockChecked(hash, parent)
	s.NotifyBlockHeaderKnown(hash, parent, 7)
}
