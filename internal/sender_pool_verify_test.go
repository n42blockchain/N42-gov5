package internal

import (
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func poolVerifyFixture(t testing.TB, n int) ([]*transaction.Transaction, mapHintSource, *countingSigner) {
	t.Helper()
	txs, addresses := buildSignedTxs(t, n)
	signer := &countingSigner{Signer: transaction.NewLondonSigner(senderRecoveryChainID)}
	pool := mapHintSource{}
	for i, tx := range txs {
		raw, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			t.Fatal(err)
		}
		p, err := transaction.DecodeEthereumTransaction(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = transaction.Sender(signer, p); err != nil {
			t.Fatal(err)
		}
		pool[tx.Hash()] = p
		tx.SetFrom(addresses[i])
	}
	signer.calls.Store(0)
	return txs, pool, signer
}

func TestVerifyBlockSendersPoolMemo(t *testing.T) {
	txs, pool, signer := poolVerifyFixture(t, 64)
	// Replace the global entries with another signer identity. Only the pool
	// object's independently recovered memo still matches the required signer.
	other := &countingSigner{Signer: signer.Signer}
	for _, tx := range txs {
		if _, err := transaction.Sender(other, tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyBlockSendersWithHints(signer, txs, pool); err != nil {
		t.Fatal(err)
	}
	if got := signer.calls.Load(); got != 0 {
		t.Fatalf("warm pool re-derived %d signatures", got)
	}
	if err := verifyBlockSenders(signer, txs); err != nil {
		t.Fatal(err)
	}
	if signer.calls.Load() == 0 {
		t.Fatal("fixture did not evict the global cache")
	}
}

func TestVerifyBlockSendersPoolSafety(t *testing.T) {
	for _, n := range []int{3, 64} {
		for _, mode := range []string{"forged-wire", "forged-pool-field", "pool-miss", "wrong-pool-hash", "wrong-chain"} {
			t.Run(fmt.Sprintf("%d/%s", n, mode), func(t *testing.T) {
				txs, pool, signer := poolVerifyFixture(t, n)
				original := *txs[0].From()
				switch mode {
				case "forged-wire":
					txs[0].SetFrom(types.Address{0xff})
					txs[n-1].SetFrom(types.Address{0xee})
				case "forged-pool-field":
					pool[txs[0].Hash()].SetFrom(types.Address{0xff})
				case "pool-miss":
					delete(pool, txs[0].Hash())
				case "wrong-pool-hash":
					pool[txs[0].Hash()] = pool[txs[1].Hash()]
				case "wrong-chain":
					signer = &countingSigner{Signer: transaction.NewLondonSigner(big.NewInt(987654321))}
				}
				err := verifyBlockSendersWithHints(signer, txs, pool)
				reject := mode == "forged-wire" || mode == "wrong-chain"
				if (err != nil) != reject {
					t.Fatalf("error=%v want rejection=%v", err, reject)
				}
				if reject && !strings.Contains(err.Error(), "tx 0 ") {
					t.Fatalf("wrong offender: %v", err)
				}
				if mode != "forged-wire" && *txs[0].From() != original {
					t.Fatal("verification mutated wire sender")
				}
			})
		}
	}
}

func BenchmarkVerifyBlockSendersPoolMemo(b *testing.B) {
	for _, hint := range []bool{false, true} {
		b.Run(fmt.Sprintf("pool=%v", hint), func(b *testing.B) {
			txs, pool, signer := poolVerifyFixture(b, 163000)
			if !hint {
				pool = nil
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := verifyBlockSendersWithHints(signer, txs, pool); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
