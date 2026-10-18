package rawdb

import (
	"bytes"
	"context"
	"encoding/binary"
	"math/big"
	"reflect"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// No Parallel: the storage switch and recovery counter are process globals.
func TestWriteBodyWireTransactionsPreserveStorageWithoutSenderRecovery(t *testing.T) {
	withChaindataTables(t)
	previous := CompactTxWrites
	t.Cleanup(func() { CompactTxWrites = previous })
	key, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000019")
	if err != nil {
		t.Fatal(err)
	}
	chainID := big.NewInt(4205001)
	signer := transaction.NewLondonSigner(chainID)
	to := types.HexToAddress("0x2000000000000000000000000000000000000049")
	for _, compact := range []bool{false, true} {
		name := "ethereum-rlp"
		if compact {
			name = "compact"
		}
		t.Run(name, func(t *testing.T) {
			CompactTxWrites = compact
			var txs []*transaction.Transaction
			var wire, encoded [][]byte
			for _, data := range []transaction.TxData{
				&transaction.LegacyTx{Nonce: 173, Gas: 25000, GasPrice: uint256.NewInt(11), Value: uint256.NewInt(7), To: &to, Data: []byte("rawdb-writebody-no-sender-warmup")},
				&transaction.DynamicFeeTx{ChainID: uint256.NewInt(4205001), Nonce: 174, Gas: 27000, GasTipCap: uint256.NewInt(2), GasFeeCap: uint256.NewInt(13), Value: uint256.NewInt(9), To: &to, Data: []byte{1, 3, 5}},
			} {
				signed, err := transaction.SignTx(transaction.NewTx(data), signer, key)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := transaction.EncodeEthereumTransaction(signed)
				if err != nil {
					t.Fatal(err)
				}
				txn, err := transaction.DecodeEthereumTransaction(raw)
				if err != nil || txn.From() != nil || txn.HasSenderMemo(signer) {
					t.Fatal("fixture is not a wire-decoded uncached transaction", err)
				}
				storage, err := encodeTxForStorage(txn)
				if err != nil {
					t.Fatal(err)
				}
				txs = append(txs, txn)
				wire = append(wire, raw)
				encoded = append(encoded, storage)
			}
			body := &block.Body{Txs: txs, Verifiers: []*block.Verify{{Address: to, PublicKey: types.PublicKey{3}}}, Rewards: []*block.Reward{{Address: to, Amount: uint256.NewInt(17)}}}
			db := memdb.NewTestDB(t)
			hash, number := types.Hash{91}, uint64(27)
			stopCounting := transaction.CountSenderRecoveriesForTest()
			err := db.Update(context.Background(), func(tx kv.RwTx) error { return WriteBody(tx, hash, number, body) })
			count := stopCounting()
			if err != nil || count != 0 {
				t.Fatalf("storage performed %d sender recoveries: %v", count, err)
			}
			for _, txn := range txs {
				if txn.From() != nil || txn.HasSenderMemo(signer) {
					t.Fatal("storage changed sender ownership/cache")
				}
			}
			if err := db.View(context.Background(), func(tx kv.Tx) error {
				stored, err := ReadBodyForStorageByKey(tx, modules.BlockBodyKey(number, hash))
				if err != nil || stored == nil || stored.TxAmount != uint32(len(txs))+2 {
					t.Fatal("body layout changed", err)
				}
				for i := range txs {
					var k [8]byte
					binary.BigEndian.PutUint64(k[:], stored.BaseTxId+1+uint64(i))
					raw, err := tx.GetOne(modules.BlockTx, k[:])
					if err != nil || !bytes.Equal(raw, encoded[i]) {
						t.Fatal("stored transaction encoding changed", err)
					}
				}
				got := ReadCanonicalBodyWithTransactions(tx, hash, number)
				if got == nil || len(got.Txs) != len(txs) || !reflect.DeepEqual(got.Verifiers, body.Verifiers) || !reflect.DeepEqual(got.Rewards, body.Rewards) {
					t.Fatal("stored body fields changed")
				}
				for i, txn := range got.Txs {
					raw, err := transaction.EncodeEthereumTransaction(txn)
					if err != nil || !bytes.Equal(raw, wire[i]) || txn.Hash() != txs[i].Hash() || txn.From() != nil {
						t.Fatal("transaction wire/hash/derived sender changed", err)
					}
				}
				senders, err := ReadSenders(tx, hash, number)
				if err != nil || len(senders) != 0 {
					t.Fatal("storage invented an authenticated sender row", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
