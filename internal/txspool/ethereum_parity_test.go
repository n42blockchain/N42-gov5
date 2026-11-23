package txspool

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/params"
)

// A chain that reached Prague through PectraTime alone keeps set-code
// transactions out of the pool until the parity time, and admits them from it.
func TestValidateTxAdmitsSetCodeUnderEthereumParity(t *testing.T) {
	for _, parity := range []*big.Int{nil, big.NewInt(0)} {
		cfg := *params.TestChainConfig
		cfg.PragueTime = nil
		cfg.PectraTime = big.NewInt(0)
		cfg.EthereumParityTime = parity

		pool := g49NewValidationPool()
		pool.chainconfig = &cfg
		pool.all = newTxLookup()
		pool.pending = make(map[types.Address]*txsList)
		pool.queue = make(map[types.Address]*txsList)
		pool.pendingNonces = newTxNoncer(pool.currentState.(*mockReadState))
		senderKey, _ := crypto.GenerateKey()
		authKey, _ := crypto.GenerateKey()
		from := crypto.PubkeyToAddress(senderKey.PublicKey)
		pool.currentState.(*mockReadState).balances[from] = uint256.NewInt(1_000_000_000_000_000)

		// signedSetCodeTx with a gas limit that covers the authorization.
		inner := signedSetCodeTx(t, senderKey, authKey, 0)
		to := *inner.To()
		tx, err := transaction.SignNewTx(senderKey, transaction.LatestSignerForChainID(big.NewInt(1)), &transaction.SetCodeTx{
			ChainID:   uint256.NewInt(1),
			GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(1),
			Gas:       100_000,
			To:        &to,
			Value:     uint256.NewInt(0),
			AuthList:  inner.AuthList(),
		})
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		tx.SetFrom(from)
		err = pool.validateTx(tx, true, nil)
		if parity == nil && err != internal.ErrTxTypeNotSupported {
			t.Errorf("without a parity time: %v, want ErrTxTypeNotSupported", err)
		}
		if parity != nil && err != nil {
			t.Errorf("under the parity time: %v, want the transaction admitted", err)
		}
	}
}
