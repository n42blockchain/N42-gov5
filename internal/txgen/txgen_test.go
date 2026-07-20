package txgen

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

type fundingTestPool struct {
	faucet       types.Address
	fundingCount int
}

func (p *fundingTestPool) Stop() error         { return nil }
func (p *fundingTestPool) Has(types.Hash) bool { return false }
func (p *fundingTestPool) Pending(bool) map[types.Address][]*transaction.Transaction {
	return nil
}
func (p *fundingTestPool) GetTransaction() ([]*transaction.Transaction, error) { return nil, nil }
func (p *fundingTestPool) GetTx(types.Hash) *transaction.Transaction           { return nil }
func (p *fundingTestPool) AddRemotes(txs []*transaction.Transaction) []error {
	return make([]error, len(txs))
}
func (p *fundingTestPool) AddLocal(tx *transaction.Transaction) error {
	if from := tx.From(); from != nil && *from == p.faucet {
		p.fundingCount++
		return nil
	}
	return errors.New("insufficient funds for gas * price + value")
}
func (p *fundingTestPool) Stats() (int, int, int, int) { return 0, 0, 0, 0 }
func (p *fundingTestPool) Nonce(types.Address) uint64  { return 0 }
func (p *fundingTestPool) Content() (map[types.Address][]*transaction.Transaction, map[types.Address][]*transaction.Transaction) {
	return nil, nil
}

func TestPendingFundingDoesNotTriggerDuplicateFaucetBatch(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	faucet := crypto.PubkeyToAddress(key.PublicKey)
	pool := &fundingTestPool{faucet: faucet}
	g := New(context.Background(), &Config{
		Enabled:        true,
		MaxTxsPerBlock: 1,
		Interval:       time.Second,
		GasPrice:       1,
		GasLimit:       21_000,
		FaucetAmount:   1_000_000,
		CoinbaseKey:    hex.EncodeToString(crypto.FromECDSA(key)),
	}, pool, uint256.NewInt(94), faucet, nil)

	g.fundTestAccounts()
	if pool.fundingCount != len(g.accounts) {
		t.Fatalf("funding submissions = %d, want %d", pool.fundingCount, len(g.accounts))
	}

	// The faucet transactions are accepted but deliberately not reflected in
	// account balances. This is the startup window that used to clear funded
	// and submit another ten faucet transactions on every tick.
	g.generateAndSubmitTxs()
	if !g.funded.Load() {
		t.Fatal("pending faucet batch was mistaken for depleted accounts")
	}
	g.fundTestAccounts()
	if pool.fundingCount != len(g.accounts) {
		t.Fatalf("duplicate funding batch submitted: got %d funding txs", pool.fundingCount)
	}
}
