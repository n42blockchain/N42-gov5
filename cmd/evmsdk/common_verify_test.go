package evmsdk

import (
	"encoding/json"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/modules/state"
)

func TestVerifyRejectsMissingHeaderNumber(t *testing.T) {
	payload, err := json.Marshal(state.EntireCode{
		Entire: state.Entire{
			Header: &block.Header{},
			Snap:   state.NewReadableSnapshot(),
		},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	_, err = (&EvmEngine{}).vertify(payload)
	if err == nil || err.Error() != "block number unavailable" {
		t.Fatalf("vertify() error = %v", err)
	}
}

func TestVerifyRejectsMissingSnapshot(t *testing.T) {
	payload, err := json.Marshal(state.EntireCode{
		Entire: state.Entire{
			Header: &block.Header{Number: uint256.NewInt(1)},
		},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	_, err = (&EvmEngine{}).vertify(payload)
	if err == nil || err.Error() != "snapshot unavailable" {
		t.Fatalf("vertify() error = %v", err)
	}
}
