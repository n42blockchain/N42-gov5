package rawdb

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/ancientera"
)

func executionReceiptFixture(t *testing.T, number uint64, receipts block.Receipts, compact bool) (kv.RwDB, *block.Block) {
	t.Helper()
	withChaindataTables(t)

	to := types.Address{19: 0x42}
	txs := []*transaction.Transaction{
		transaction.NewTx(&transaction.LegacyTx{
			Nonce: 1, GasPrice: uint256.NewInt(3), Gas: 30_000, To: &to, Value: uint256.NewInt(7),
		}),
		transaction.NewTx(&transaction.DynamicFeeTx{
			ChainID: uint256.NewInt(4205001), Nonce: 2, Gas: 40_000,
			GasTipCap: uint256.NewInt(2), GasFeeCap: uint256.NewInt(9), To: &to, Value: uint256.NewInt(11),
		}),
	}
	blk := block.NewBlock(testHeader(number), txs).(*block.Block)
	db := memdb.NewTestDB(t)
	var raw []byte
	var err error
	if compact {
		raw = receipts.MarshalCompact()
	} else {
		raw, err = receipts.Marshal()
		if err != nil {
			t.Fatalf("marshal protobuf receipts: %v", err)
		}
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return tx.Put(modules.Receipts, modules.EncodeBlockNumber(number), raw)
	}); err != nil {
		t.Fatalf("store receipts: %v", err)
	}
	return db, blk
}

func executionReceiptPair() (stored, want block.Receipts) {
	wrongHash := types.Hash{0xee}
	blockNumber := uint256.NewInt(999)
	addr := types.Address{19: 0x99}
	topic := types.Hash{31: 0x7a}
	logs := []*block.Log{{
		Address: addr, Topics: []types.Hash{topic}, Data: []byte("receipt-execution"),
	}}
	bloom := block.CreateBloom(block.Receipts{{Logs: logs[0:1]}})
	want = block.Receipts{
		{Type: transaction.LegacyTxType, Status: block.ReceiptStatusSuccessful, CumulativeGasUsed: 21_000, GasUsed: 21_000, Bloom: bloom, Logs: logs},
		{Type: transaction.DynamicFeeTxType, Status: block.ReceiptStatusFailed, CumulativeGasUsed: 50_000, GasUsed: 29_000, Bloom: block.CreateBloom(block.Receipts{{}})},
	}
	stored = block.Receipts{
		{Type: transaction.DynamicFeeTxType, Status: want[0].Status, CumulativeGasUsed: want[0].CumulativeGasUsed, GasUsed: want[0].GasUsed,
			Bloom: block.Bloom{0xff}, TxHash: wrongHash, BlockHash: wrongHash, BlockNumber: blockNumber, TransactionIndex: 77,
			Logs: []*block.Log{{Address: addr, Topics: []types.Hash{topic}, Data: []byte("receipt-execution"), BlockNumber: blockNumber, TxHash: wrongHash, BlockHash: wrongHash, TxIndex: 77, Index: 88, Removed: true}}},
		{Type: transaction.LegacyTxType, Status: want[1].Status, CumulativeGasUsed: want[1].CumulativeGasUsed, GasUsed: want[1].GasUsed,
			Bloom: want[1].Bloom, TxHash: wrongHash, BlockHash: wrongHash, BlockNumber: blockNumber, TransactionIndex: 78},
	}
	return stored, want
}

func TestReadReceiptsForExecutionWithErrorPreservesExecutionFieldsAcrossEncodings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		compact bool
	}{
		{name: "compact", compact: true},
		{name: "legacy-protobuf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored, want := executionReceiptPair()
			db, blk := executionReceiptFixture(t, 501, stored, tc.compact)
			var got block.Receipts
			if err := db.View(context.Background(), func(tx kv.Tx) error {
				var err error
				got, err = ReadReceiptsForExecutionWithError(tx, blk, nil)
				return err
			}); err != nil {
				t.Fatalf("read execution receipts: %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("receipt count = %d, want %d", len(got), len(want))
			}
			for i := range got {
				if got[i].Type != want[i].Type {
					t.Fatalf("receipt %d type = %d, want transaction type %d", i, got[i].Type, want[i].Type)
				}
				if got[i].Status != want[i].Status || got[i].CumulativeGasUsed != want[i].CumulativeGasUsed {
					t.Fatalf("receipt %d execution scalars changed: status=%d cumulative=%d", i, got[i].Status, got[i].CumulativeGasUsed)
				}
			}

			// The native receipt root omits Type; the Ethereum root includes the
			// typed-transaction prefix. Both must match the execution receipts.
			if gotRoot, wantRoot := hash.DeriveSha(got), hash.DeriveSha(want); gotRoot != wantRoot {
				t.Fatalf("native receipt root = %s, want %s", gotRoot, wantRoot)
			}
			if gotRoot, wantRoot := block.EthereumReceiptRoot(got, true), block.EthereumReceiptRoot(want, true); gotRoot != wantRoot {
				t.Fatalf("Ethereum receipt root = %s, want %s", gotRoot, wantRoot)
			}
			if gotBloom, wantBloom := block.CreateBloom(got), block.CreateBloom(want); gotBloom != wantBloom {
				t.Fatalf("receipt bloom = %x, want %x", gotBloom, wantBloom)
			}
			if gotGas, wantGas := got[len(got)-1].CumulativeGasUsed, want[len(want)-1].CumulativeGasUsed; gotGas != wantGas || gotGas != 50_000 {
				t.Fatalf("execution cumulative gas = %d, want %d", gotGas, wantGas)
			}
			if got[0].GasUsed != 21_000 || got[1].GasUsed != 29_000 {
				t.Fatalf("per-receipt gas = [%d %d], want [21000 29000]", got[0].GasUsed, got[1].GasUsed)
			}

			// The execution reader may set the actual type, but must not fabricate
			// RPC location metadata. The protobuf form retains its old stored values;
			// the compact form omits them by design.
			if tc.compact {
				if got[0].TxHash != (types.Hash{}) || got[0].BlockHash != (types.Hash{}) || got[0].BlockNumber != nil {
					t.Fatal("compact execution read fabricated block/transaction location metadata")
				}
			} else if got[0].TxHash != (types.Hash{0xee}) || got[0].BlockHash != (types.Hash{0xee}) || got[0].BlockNumber == nil || got[0].BlockNumber.Uint64() != 999 || got[0].TransactionIndex != 77 {
				t.Fatal("execution read rewrote legacy protobuf RPC metadata")
			} else if got[0].Bloom == want[0].Bloom {
				t.Fatal("execution read unexpectedly trusted/replaced the legacy stored bloom")
			}

			var rpc block.Receipts
			if err := db.View(context.Background(), func(tx kv.Tx) error {
				var err error
				rpc, err = ReadReceiptsWithError(tx, blk, nil)
				return err
			}); err != nil {
				t.Fatalf("read RPC receipts: %v", err)
			}
			blockHash := blk.Hash()
			for i, receipt := range rpc {
				if receipt.TxHash != blk.Transactions()[i].Hash() || receipt.BlockHash != blockHash || receipt.BlockNumber == nil || receipt.BlockNumber.Uint64() != 501 || receipt.TransactionIndex != uint(i) {
					t.Fatalf("RPC receipt %d missing derived location metadata", i)
				}
			}
			if rpc[0].Logs[0].TxHash != blk.Transactions()[0].Hash() || rpc[0].Logs[0].BlockHash != blockHash || rpc[0].Logs[0].BlockNumber == nil || rpc[0].Logs[0].BlockNumber.Uint64() != 501 || rpc[0].Logs[0].TxIndex != 0 || rpc[0].Logs[0].Index != 0 || rpc[0].Logs[0].Removed {
				t.Fatal("RPC log metadata was not derived")
			}
		})
	}
}

func TestReadReceiptsForExecutionWithErrorRejectsInvalidRows(t *testing.T) {
	valid, _ := executionReceiptPair()
	txs := block.NewBlock(testHeader(601), []*transaction.Transaction{
		transaction.NewTx(&transaction.LegacyTx{Nonce: 1, GasPrice: uint256.NewInt(1), Gas: 30_000}),
		transaction.NewTx(&transaction.DynamicFeeTx{ChainID: uint256.NewInt(4205001), Nonce: 2, Gas: 40_000, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(2)}),
	}).(*block.Block)

	for _, tc := range []struct {
		name    string
		body    []*transaction.Transaction
		receipts block.Receipts
		raw     []byte
		write   bool
	}{
		{name: "nil-receipt", body: txs.Transactions(), receipts: block.Receipts{{CumulativeGasUsed: 1}, nil}},
		{name: "nil-transaction", body: []*transaction.Transaction{nil}, receipts: block.Receipts{{CumulativeGasUsed: 1}}},
		{name: "nil-log", body: txs.Transactions(), receipts: block.Receipts{{Logs: []*block.Log{nil}}, {CumulativeGasUsed: 1}}},
		{name: "decreasing-cumulative-gas", body: txs.Transactions(), receipts: block.Receipts{{CumulativeGasUsed: 2}, {CumulativeGasUsed: 1}}, write: true},
		{name: "receipt-count", body: txs.Transactions(), receipts: valid[:1], write: true},
		{name: "missing-nonempty-body", body: txs.Transactions()},
		{name: "malformed-hot-encoding", body: txs.Transactions(), raw: []byte{0xff}, write: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "nil-receipt" || tc.name == "nil-transaction" || tc.name == "nil-log" {
				b := block.NewBlock(testHeader(601), tc.body).(*block.Block)
				if deriveReceiptExecutionFields(tc.receipts, b) {
					t.Fatal("accepted invalid execution receipt fields")
				}
				return
			}
			var db kv.RwDB
			var b *block.Block
			if tc.write && tc.raw == nil {
				db, b = executionReceiptFixture(t, 601, tc.receipts, true)
			} else {
				withChaindataTables(t)
				db = memdb.NewTestDB(t)
				b = block.NewBlock(testHeader(601), tc.body).(*block.Block)
				if tc.write {
					if err := db.Update(context.Background(), func(tx kv.RwTx) error {
						return tx.Put(modules.Receipts, modules.EncodeBlockNumber(601), tc.raw)
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := db.View(context.Background(), func(tx kv.Tx) error {
				_, err := ReadReceiptsForExecutionWithError(tx, b, nil)
				return err
			})
			if err == nil {
				t.Fatal("invalid receipt row accepted")
			}
		})
	}
}

func TestReadReceiptsForExecutionWithErrorAcceptsEmptyAndPropagatesReadErrors(t *testing.T) {
	withChaindataTables(t)
	db := memdb.NewTestDB(t)
	empty := block.NewBlock(testHeader(900_001), nil).(*block.Block)
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		got, err := ReadReceiptsForExecutionWithError(tx, empty, nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("valid empty block receipts = %v, %v", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Reuse the real sealed-era fixture: the execution reader must preserve the
	// valid empty archived row and propagate an archive failure instead of
	// silently converting it into empty receipts.
	hashes, _ := buildFallbackFixture(t)
	archiveDB := memdb.NewTestDB(t)
	tx, err := archiveDB.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := tx.Put(modules.HeaderCanonical, modules.EncodeBlockNumber(123), hashes[123][:]); err != nil {
		t.Fatal(err)
	}
	archived := ReadBlock(tx, hashes[123], 123)
	if archived == nil {
		t.Fatal("missing archived empty block")
	}
	if got, err := ReadReceiptsForExecutionWithError(tx, archived, nil); err != nil || len(got) != 0 {
		t.Fatalf("valid empty archived receipts = %v, %v", got, err)
	}
	AncientStore().Close()
	if _, err := ReadReceiptsForExecutionWithError(tx, archived, nil); !errors.Is(err, ancientera.ErrPruned) {
		t.Fatalf("archive execution error hidden: %v", err)
	}
	sentinel := errors.New("hot receipt store read failed")
	if _, err := ReadReceiptsForExecutionWithError(checkedReceiptReadErrorTx{Tx: tx, err: sentinel}, archived, nil); !errors.Is(err, sentinel) {
		t.Fatalf("hot IO error replaced by archive fallback: %v", err)
	}

	badHashes, _ := buildFallbackFixture(t, []byte{0xff})
	badDB := memdb.NewTestDB(t)
	badTx, err := badDB.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer badTx.Rollback()
	if err := badTx.Put(modules.HeaderCanonical, modules.EncodeBlockNumber(123), badHashes[123][:]); err != nil {
		t.Fatal(err)
	}
	badBlock := ReadBlock(badTx, badHashes[123], 123)
	if badBlock == nil {
		t.Fatal("missing malformed archived fixture block")
	}
	if _, err := ReadReceiptsForExecutionWithError(badTx, badBlock, nil); err == nil {
		t.Fatal("malformed archived receipt encoding accepted")
	}
}

