package graphql

import (
	"testing"

	"github.com/holiman/uint256"

	blockpkg "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
)

func TestUint256ToUint64OrZero(t *testing.T) {
	if got := uint256ToUint64OrZero(nil); got != 0 {
		t.Fatalf("nil -> %d, want 0", got)
	}
	if got := uint256ToUint64OrZero(uint256.NewInt(42)); got != 42 {
		t.Fatalf("42 -> %d, want 42", got)
	}
}

func TestNewResolverWrapsAPIBackend(t *testing.T) {
	backend := api.NewAPI(nil, nil, nil, nil, nil, nil)
	r := NewResolver(backend)
	if r == nil || r.api != backend {
		t.Fatalf("NewResolver() = %+v, want wrapping the given backend", r)
	}
}

func TestNewBuildsHandlerFromAPIBackend(t *testing.T) {
	backend := api.NewAPI(nil, nil, nil, nil, nil, nil)
	h := New(backend)
	if h == nil {
		t.Fatal("New() returned nil handler")
	}
}

func resolverTestLog(addr types.Address, topics ...types.Hash) *blockpkg.Log {
	return &blockpkg.Log{Address: addr, Topics: topics}
}

func TestMatchLogNoFilterMatchesEverything(t *testing.T) {
	lg := resolverTestLog(types.HexToAddress("0x01"))
	if !matchLog(lg, LogFilter{}) {
		t.Fatal("empty filter should match any log")
	}
}

func TestMatchLogFiltersByAddress(t *testing.T) {
	a1 := types.HexToAddress("0x01")
	a2 := types.HexToAddress("0x02")
	lg := resolverTestLog(a1)
	if !matchLog(lg, LogFilter{Addresses: []types.Address{a1, a2}}) {
		t.Fatal("log address in filter list should match")
	}
	if matchLog(lg, LogFilter{Addresses: []types.Address{a2}}) {
		t.Fatal("log address not in filter list should not match")
	}
}

func TestMatchLogFiltersByTopics(t *testing.T) {
	t0 := types.HexToHash("0x10")
	t1 := types.HexToHash("0x11")
	lg := resolverTestLog(types.HexToAddress("0x01"), t0, t1)

	// Empty topic group at a position means "don't care" for that slot.
	if !matchLog(lg, LogFilter{Topics: [][]types.Hash{{}, {t1}}}) {
		t.Fatal("expected match: empty group wildcards position 0, t1 matches position 1")
	}
	// Position beyond the log's topic count can never match.
	if matchLog(lg, LogFilter{Topics: [][]types.Hash{{}, {}, {types.HexToHash("0x99")}}}) {
		t.Fatal("filter topic group beyond log's topic count should not match")
	}
	// None of the candidates in the group match.
	if matchLog(lg, LogFilter{Topics: [][]types.Hash{{types.HexToHash("0x99")}}}) {
		t.Fatal("expected no match when no candidate in the topic group matches")
	}
}

func TestMarshalTransactionLegacyFields(t *testing.T) {
	to := types.HexToAddress("0x02")
	from := types.HexToAddress("0x03")
	tx := transaction.NewTx(&transaction.LegacyTx{
		Nonce:    7,
		GasPrice: uint256.NewInt(100),
		Gas:      21000,
		To:       &to,
		From:     &from,
		Value:    uint256.NewInt(1000),
		Data:     []byte{0xde, 0xad},
	})

	blockHash := types.HexToHash("0xabc")
	got := marshalTransaction(tx, blockHash, 10, 2)

	if got.Hash != tx.Hash() {
		t.Fatalf("Hash = %s, want %s", got.Hash, tx.Hash())
	}
	if got.Nonce != 7 {
		t.Fatalf("Nonce = %d, want 7", got.Nonce)
	}
	if got.Gas != 21000 {
		t.Fatalf("Gas = %d, want 21000", got.Gas)
	}
	if got.BlockNumber == nil || *got.BlockNumber != 10 {
		t.Fatalf("BlockNumber = %v, want 10", got.BlockNumber)
	}
	if got.BlockHash == nil || *got.BlockHash != blockHash {
		t.Fatalf("BlockHash = %v, want %s", got.BlockHash, blockHash)
	}
	if got.TransactionIndex == nil || *got.TransactionIndex != 2 {
		t.Fatalf("TransactionIndex = %v, want 2", got.TransactionIndex)
	}
	if got.To == nil || *got.To != to {
		t.Fatalf("To = %v, want %s", got.To, to)
	}
	if got.From != from {
		t.Fatalf("From = %s, want %s", got.From, from)
	}
	if got.Value == nil || got.Value.ToInt().Int64() != 1000 {
		t.Fatalf("Value = %v, want 1000", got.Value)
	}
	if got.GasPrice == nil || got.GasPrice.ToInt().Int64() != 100 {
		t.Fatalf("GasPrice = %v, want 100", got.GasPrice)
	}
	// LegacyTx reports GasPrice for both GasFeeCap and GasTipCap (see
	// transaction.LegacyTx accessors), so marshalTransaction's EIP-1559
	// fields come back populated with that same value rather than nil.
	if got.MaxFeePerGas == nil || got.MaxFeePerGas.ToInt().Int64() != 100 {
		t.Fatalf("MaxFeePerGas = %v, want 100 (mirrors GasPrice for legacy)", got.MaxFeePerGas)
	}
	if got.MaxPriorityFeePerGas == nil || got.MaxPriorityFeePerGas.ToInt().Int64() != 100 {
		t.Fatalf("MaxPriorityFeePerGas = %v, want 100 (mirrors GasPrice for legacy)", got.MaxPriorityFeePerGas)
	}
}

func TestMarshalTransactionDynamicFeeFields(t *testing.T) {
	to := types.HexToAddress("0x02")
	tx := transaction.NewTx(&transaction.DynamicFeeTx{
		Nonce:      1,
		Gas:        21000,
		To:         &to,
		Value:      uint256.NewInt(0),
		GasTipCap:  uint256.NewInt(2),
		GasFeeCap:  uint256.NewInt(5),
		AccessList: transaction.AccessList{},
	})

	got := marshalTransaction(tx, types.Hash{}, 1, 0)
	if got.MaxFeePerGas == nil || got.MaxFeePerGas.ToInt().Int64() != 5 {
		t.Fatalf("MaxFeePerGas = %v, want 5", got.MaxFeePerGas)
	}
	if got.MaxPriorityFeePerGas == nil || got.MaxPriorityFeePerGas.ToInt().Int64() != 2 {
		t.Fatalf("MaxPriorityFeePerGas = %v, want 2", got.MaxPriorityFeePerGas)
	}
}

func TestMarshalTransactionNoToNoFrom(t *testing.T) {
	tx := transaction.NewTx(&transaction.LegacyTx{
		Nonce:    0,
		GasPrice: uint256.NewInt(1),
		Gas:      21000,
		Value:    uint256.NewInt(0),
	})
	got := marshalTransaction(tx, types.Hash{}, 0, 0)
	if got.To != nil {
		t.Fatalf("To = %v, want nil for a contract-creation tx", got.To)
	}
	if got.From != (types.Address{}) {
		t.Fatalf("From = %s, want zero value when unset", got.From)
	}
}
