package api

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

func TestMarkRejectedPayloadHashSharesEngineOverlay(t *testing.T) {
	core := NewAPI(nil, nil, nil, nil, nil, nil)
	rejected := types.HexToHash("0x11")
	latest := types.HexToHash("0x22")

	core.MarkRejectedPayloadHash(rejected, &latest)
	got, ok := core.engineOverlay.rejectedLatestValidHash(rejected)
	if !ok || got == nil || *got != latest {
		t.Fatalf("rejected payload lookup = (%v, %v), want (%s, true)", got, ok, latest)
	}
}

func TestUnknownPayloadNotifiesMissingAncestorObserver(t *testing.T) {
	core := NewAPI(nil, nil, nil, nil, nil, nil)
	engine := NewEngineAPIV1(NewBlockChainAPI(core))
	parent := types.HexToHash("0x33")
	var notified types.Hash
	engine.SetMissingAncestorObserver(func(hash types.Hash) { notified = hash })
	header := &block.Header{Number: uint256.NewInt(1), ParentHash: parent}
	blk := block.NewBlock(header, nil)
	status, err := engine.executeOrValidate(blk, header.Hash(), parent, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != PayloadStatusAccepted {
		t.Fatalf("status = %s, want %s", status.Status, PayloadStatusAccepted)
	}
	if notified != parent {
		t.Fatalf("notified hash = %s, want %s", notified, parent)
	}
}
