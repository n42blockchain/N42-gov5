package api

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func TestAggSignCheckMismatchedRoot(t *testing.T) {
	s := &AggSign{StateRoot: types.Hash{1}}
	if s.Check(types.Hash{2}) {
		t.Error("expected Check() to fail when state roots differ")
	}
}

func TestAggSignCheckInvalidSignatureBytes(t *testing.T) {
	s := &AggSign{StateRoot: types.Hash{1}}
	// Sign/PublicKey are zero-valued; bls.SignatureFromBytes should reject them.
	if s.Check(types.Hash{1}) {
		t.Error("expected Check() to fail for invalid signature bytes")
	}
}

func TestDepositInfoNoRecord(t *testing.T) {
	db := memdb.NewTestDB(t)
	addr := types.HexToAddress("0x1000000000000000000000000000000000000001")
	info := DepositInfo(db, addr)
	if info != nil {
		t.Errorf("expected nil deposit info for unknown address, got %+v", info)
	}
}

func TestIsDepositNoRecord(t *testing.T) {
	db := memdb.NewTestDB(t)
	addr := types.HexToAddress("0x1000000000000000000000000000000000000002")
	ok, err := IsDeposit(db, addr)
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if ok {
		t.Error("expected IsDeposit to be false for unknown address")
	}
}
