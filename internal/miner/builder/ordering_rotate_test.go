package builder

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// rotTx builds a tx whose Gas field encodes the sender id.
func rotTx(sender, nonce, tipGwei uint64) *transaction.Transaction {
	tip := new(uint256.Int).Mul(uint256.NewInt(tipGwei), uint256.NewInt(1e9))
	feeCap := new(uint256.Int).Add(tip, uint256.NewInt(1e9))
	return transaction.NewTx(&transaction.DynamicFeeTx{
		Nonce: nonce, GasTipCap: tip, GasFeeCap: feeCap, Gas: 21000 + sender,
	})
}

type rotOut struct{ sender, nonce uint64 }

func drainRot(rotate bool, chains map[uint64]int, tips map[uint64]uint64) []rotOut {
	old := senderRotate
	senderRotate = rotate
	defer func() { senderRotate = old }()
	pending := map[types.Address][]*transaction.Transaction{}
	for s, n := range chains {
		var txs []*transaction.Transaction
		for i := 0; i < n; i++ {
			txs = append(txs, rotTx(s, uint64(i), tips[s]))
		}
		pending[types.BytesToAddress([]byte{byte(s)})] = txs
	}
	set := NewTxByPriceAndNonce(pending, uint256.NewInt(1e9))
	var out []rotOut
	for tx := set.Peek(); tx != nil; tx = set.Peek() {
		out = append(out, rotOut{tx.Gas() - 21000, tx.Nonce()})
		set.Shift()
	}
	return out
}

func checkChains(t *testing.T, out []rotOut, chains map[uint64]int) {
	t.Helper()
	next := map[uint64]uint64{}
	for _, o := range out {
		if o.nonce != next[o.sender] {
			t.Fatalf("sender %d nonce order broken: got %d want %d", o.sender, o.nonce, next[o.sender])
		}
		next[o.sender]++
	}
	for s, n := range chains {
		if int(next[s]) != n {
			t.Fatalf("sender %d: %d txs, want %d", s, next[s], n)
		}
	}
}

func TestSenderRotateRoundRobin(t *testing.T) {
	chains := map[uint64]int{1: 5, 2: 3, 3: 1, 4: 7}
	tips := map[uint64]uint64{1: 7, 2: 7, 3: 7, 4: 7}
	out := drainRot(true, chains, tips)
	if len(out) != 16 {
		t.Fatalf("got %d txs", len(out))
	}
	checkChains(t, out, chains)
	seen := map[uint64]bool{}
	for _, o := range out[:4] {
		seen[o.sender] = true
	}
	if len(seen) != 4 {
		t.Fatalf("first 4 txs not distinct senders: %v", out[:4])
	}
	// Second round: senders 1,2,4 again (3 is exhausted), in the same order.
	for i := 0; i < 3; i++ {
		if out[4+i].sender == 3 {
			t.Fatalf("exhausted sender reappeared: %v", out)
		}
	}
}

func TestSenderRotateHigherTipFirst(t *testing.T) {
	chains := map[uint64]int{1: 5, 2: 3, 3: 1, 4: 7}
	tips := map[uint64]uint64{1: 7, 2: 7, 3: 7, 4: 20}
	out := drainRot(true, chains, tips)
	checkChains(t, out, chains)
	for i := 0; i < 7; i++ {
		if out[i].sender != 4 {
			t.Fatalf("high-tip sender not first at %d: %v", i, out)
		}
	}
}

func TestSenderRotateOffDrainsFirst(t *testing.T) {
	chains := map[uint64]int{1: 5, 2: 3, 3: 1, 4: 7}
	tips := map[uint64]uint64{1: 7, 2: 7, 3: 7, 4: 7}
	out := drainRot(false, chains, tips)
	checkChains(t, out, chains)
	first := out[0].sender
	for i := 0; i < chains[first]; i++ {
		if out[i].sender != first {
			t.Fatalf("rotation off should drain first sender fully: %v", out)
		}
	}
}
