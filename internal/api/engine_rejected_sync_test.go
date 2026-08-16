package api

import (
	"context"
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

func TestSyncedForkBlockNotifiesMissingAncestorObserver(t *testing.T) {
	core := NewAPI(nil, nil, nil, nil, nil, nil)
	engine := NewEngineAPIV1(NewBlockChainAPI(core))
	parent := types.HexToHash("0x44")
	var notified types.Hash
	engine.SetMissingAncestorObserver(func(hash types.Hash) { notified = hash })
	header := &block.Header{Number: uint256.NewInt(2), ParentHash: parent}
	blk := block.NewBlock(header, nil).(*block.Block)
	status, err := engine.ImportSyncedBlock(context.Background(), blk, nil)
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

func TestForkEngineAPIsForwardMissingAncestorObserver(t *testing.T) {
	core := NewAPI(nil, nil, nil, nil, nil, nil)
	parent := types.HexToHash("0x55")

	t.Run("cancun", func(t *testing.T) {
		blob := NewEngineAPIBlob(NewBlockChainAPI(core))
		var notified types.Hash
		blob.SetMissingAncestorObserver(func(hash types.Hash) { notified = hash })
		if blob.v1().missingAncestorObserver == nil {
			t.Fatal("missing ancestor observer was not forwarded")
		}
		blob.v1().missingAncestorObserver(parent)
		if notified != parent {
			t.Fatalf("notified hash = %s, want %s", notified, parent)
		}
	})
	t.Run("prague", func(t *testing.T) {
		v4 := NewEngineAPIv4(NewBlockChainAPI(core))
		var notified types.Hash
		v4.SetMissingAncestorObserver(func(hash types.Hash) { notified = hash })
		if v4.v1.missingAncestorObserver == nil || v4.blob.v1().missingAncestorObserver == nil {
			t.Fatal("missing ancestor observer was not forwarded")
		}
		v4.v1.missingAncestorObserver(parent)
		if notified != parent {
			t.Fatalf("notified hash = %s, want %s", notified, parent)
		}
	})
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
