package engineapi

import (
	"context"
	"testing"

	"math/big"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

func TestEthAPIMinimal_Syncing(t *testing.T) {
	api := NewEthAPIMinimal(nil, nil)
	got, err := api.Syncing(context.Background())
	if err != nil {
		t.Fatalf("Syncing: %v", err)
	}
	if got != false {
		t.Errorf("Syncing() = %v, want false", got)
	}
}

func TestEthAPIMinimal_ChainId(t *testing.T) {
	api := NewEthAPIMinimal(nil, nil)
	if id := api.ChainId(); id.ToInt().Sign() != 0 {
		t.Errorf("ChainId() with nil cfg = %v, want 0", id)
	}

	cfg := &params.ChainConfig{ChainID: big.NewInt(7)}
	api2 := NewEthAPIMinimal(nil, cfg)
	if id := api2.ChainId(); id.ToInt().Uint64() != 7 {
		t.Errorf("ChainId() = %v, want 7", id)
	}
}

func TestEthAPIMinimal_BlockNumber_NilDB(t *testing.T) {
	api := NewEthAPIMinimal(nil, nil)
	n, err := api.BlockNumber(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("BlockNumber() = %d, %v; want 0, nil", n, err)
	}
}

func TestEthAPIMinimal_BlockNumber_HeadMarkerFallback(t *testing.T) {
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := ethel.WriteHeadMarker(tx, 42); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	api := NewEthAPIMinimal(db, nil)
	n, err := api.BlockNumber(context.Background())
	if err != nil {
		t.Fatalf("BlockNumber: %v", err)
	}
	if n != 42 {
		t.Errorf("BlockNumber() = %d, want 42 (head marker fallback)", n)
	}
}

func TestEthAPIMinimal_BlockNumber_HeadBlockHash(t *testing.T) {
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h := types.Hash{0x01, 0x02}
	rawdb.WriteHeadBlockHash(tx, h)
	if err := rawdb.WriteHeaderNumber(tx, h, 99); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	api := NewEthAPIMinimal(db, nil)
	n, err := api.BlockNumber(context.Background())
	if err != nil {
		t.Fatalf("BlockNumber: %v", err)
	}
	if n != 99 {
		t.Errorf("BlockNumber() = %d, want 99", n)
	}
}

func TestEthAPIMinimal_BlockNumber_HashWithNoNumber(t *testing.T) {
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h := types.Hash{0x03}
	rawdb.WriteHeadBlockHash(tx, h)
	// No header number written for h, and no head marker -> falls through to 0.
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	api := NewEthAPIMinimal(db, nil)
	n, err := api.BlockNumber(context.Background())
	if err != nil {
		t.Fatalf("BlockNumber: %v", err)
	}
	if n != 0 {
		t.Errorf("BlockNumber() = %d, want 0", n)
	}
}

func TestNetAPIMinimal(t *testing.T) {
	api := NewNetAPIMinimal(nil)
	if v := api.Version(); v != "0" {
		t.Errorf("Version() with nil cfg = %q, want %q", v, "0")
	}
	api2 := NewNetAPIMinimal(&params.ChainConfig{ChainID: big.NewInt(5)})
	if v := api2.Version(); v != "5" {
		t.Errorf("Version() = %q, want %q", v, "5")
	}
	if !api.Listening() {
		t.Error("Listening() = false, want true")
	}
	if api.PeerCount() != 0 {
		t.Errorf("PeerCount() = %d, want 0", api.PeerCount())
	}
}

func TestWeb3APIMinimal(t *testing.T) {
	api := NewWeb3APIMinimal()
	if v := api.ClientVersion(); v != "n42-eth-el/v0" {
		t.Errorf("ClientVersion() = %q", v)
	}
}

func TestUint64FromBytes(t *testing.T) {
	if got := uint64FromBytes([]byte{0, 0, 0, 0, 0, 0, 0, 1}); got != 1 {
		t.Errorf("uint64FromBytes = %d, want 1", got)
	}
	if got := uint64FromBytes([]byte{1, 2, 3}); got != 0 {
		t.Errorf("uint64FromBytes(short) = %d, want 0", got)
	}
}
