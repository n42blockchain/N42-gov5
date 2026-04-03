package ethel

import (
	"os"
	"testing"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

const gethAncientPath = `e:\geth\geth\chaindata\ancient\chain`

func skipIfNoGeth(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(gethAncientPath); err != nil {
		t.Skipf("Geth ancient data not available at %s", gethAncientPath)
	}
}

func TestDecodeGethGenesisHeader(t *testing.T) {
	skipIfNoGeth(t)

	tbl, err := freezer.NewFreezerTable(gethAncientPath, "headers", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	data, err := tbl.Retrieve(0)
	if err != nil {
		t.Fatal(err)
	}

	h, err := DecodeGethHeader(data)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("Genesis: number=%s difficulty=%s gasLimit=%d",
		h.Number, h.Difficulty, h.GasLimit)

	if h.Number.Uint64() != 0 {
		t.Fatalf("expected number 0, got %s", h.Number)
	}
	if h.GasLimit != 5000 {
		t.Fatalf("expected gasLimit 5000, got %d", h.GasLimit)
	}

	// Verify hash matches known Ethereum mainnet genesis hash.
	hash := h.Hash()
	expected := "d4e56740f876aef8c010b86a40d5f56745a118d0906a34e69aec8c0db1cb8fa3"
	if hash.Hex()[2:] != expected {
		t.Fatalf("genesis hash mismatch:\n  got:  %s\n  want: %s", hash.Hex(), expected)
	}
	t.Logf("Genesis hash: %s ✓", hash.Hex())
}

func TestDecodeGethHeader1M(t *testing.T) {
	skipIfNoGeth(t)

	tbl, err := freezer.NewFreezerTable(gethAncientPath, "headers", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	data, err := tbl.Retrieve(1_000_000)
	if err != nil {
		t.Fatal(err)
	}

	h, err := DecodeGethHeader(data)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("Block 1M: number=%s gasUsed=%d time=%d",
		h.Number, h.GasUsed, h.Time)

	if h.Number.Uint64() != 1_000_000 {
		t.Fatalf("expected number 1000000, got %s", h.Number)
	}
}

func TestDecodeGethPostLondonHeader(t *testing.T) {
	skipIfNoGeth(t)

	tbl, err := freezer.NewFreezerTable(gethAncientPath, "headers", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	// Block 12965000 = London fork activation.
	data, err := tbl.Retrieve(12_965_000)
	if err != nil {
		t.Fatal(err)
	}

	h, err := DecodeGethHeader(data)
	if err != nil {
		t.Fatal(err)
	}

	if h.BaseFee == nil {
		t.Fatal("expected baseFee to be set post-London")
	}
	t.Logf("London block: number=%s baseFee=%s", h.Number, h.BaseFee)
}

func TestDecodeGethPostMergeHeader(t *testing.T) {
	skipIfNoGeth(t)

	tbl, err := freezer.NewFreezerTable(gethAncientPath, "headers", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	// Block 15537394 = The Merge (first PoS block).
	data, err := tbl.Retrieve(15_537_394)
	if err != nil {
		t.Fatal(err)
	}

	h, err := DecodeGethHeader(data)
	if err != nil {
		t.Fatal(err)
	}

	if h.Difficulty.Uint64() != 0 {
		t.Fatalf("expected difficulty 0 post-merge, got %s", h.Difficulty)
	}
	t.Logf("Merge block: number=%s difficulty=%s nonce=%x",
		h.Number, h.Difficulty, h.Nonce)
}

func TestDecodeGethPostShanghaiHeader(t *testing.T) {
	skipIfNoGeth(t)

	tbl, err := freezer.NewFreezerTable(gethAncientPath, "headers", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	// Block 17034870 = Shanghai activation (first withdrawal block).
	data, err := tbl.Retrieve(17_034_870)
	if err != nil {
		t.Fatal(err)
	}

	h, err := DecodeGethHeader(data)
	if err != nil {
		t.Fatal(err)
	}

	if h.WithdrawalsHash == nil {
		t.Fatal("expected withdrawalsHash to be set post-Shanghai")
	}
	t.Logf("Shanghai block: number=%s withdrawalsHash=%s",
		h.Number, h.WithdrawalsHash.Hex())
}

func TestDecodeGethPostCancunHeader(t *testing.T) {
	skipIfNoGeth(t)

	tbl, err := freezer.NewFreezerTable(gethAncientPath, "headers", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	// Block 19426587 = Cancun activation.
	data, err := tbl.Retrieve(19_426_587)
	if err != nil {
		t.Fatal(err)
	}

	h, err := DecodeGethHeader(data)
	if err != nil {
		t.Fatal(err)
	}

	if h.BlobGasUsed == nil {
		t.Fatal("expected blobGasUsed to be set post-Cancun")
	}
	if h.ParentBeaconRoot == nil {
		t.Fatal("expected parentBeaconRoot to be set post-Cancun")
	}
	t.Logf("Cancun block: number=%s blobGasUsed=%d excessBlobGas=%d",
		h.Number, *h.BlobGasUsed, *h.ExcessBlobGas)
}

func TestDecodeGethGenesisBody(t *testing.T) {
	skipIfNoGeth(t)

	tbl, err := freezer.NewFreezerTable(gethAncientPath, "bodies", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	data, err := tbl.Retrieve(0)
	if err != nil {
		t.Fatal(err)
	}

	body, err := DecodeGethBody(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(body.Transactions) != 0 {
		t.Fatalf("genesis should have 0 txs, got %d", len(body.Transactions))
	}
	t.Log("Genesis body: 0 txs ✓")
}

func TestDecodeGethBody46147(t *testing.T) {
	skipIfNoGeth(t)

	tbl, err := freezer.NewFreezerTable(gethAncientPath, "bodies", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	// Block 46147 = first Ethereum transaction ever.
	data, err := tbl.Retrieve(46147)
	if err != nil {
		t.Fatal(err)
	}

	body, err := DecodeGethBody(data)
	if err != nil {
		t.Fatal(err)
	}

	if len(body.Transactions) != 1 {
		t.Fatalf("expected 1 tx, got %d", len(body.Transactions))
	}
	t.Logf("Block 46147: 1 tx, nonce=%d, gasLimit=%d, uncles=%d",
		body.Transactions[0].Nonce(), body.Transactions[0].Gas(), len(body.Uncles))
}
