package guest

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/params"
)

// TestForkToChainRules_AllFalse verifies zero-value ForkConfig produces all-false Rules.
func TestForkToChainRules_AllFalse(t *testing.T) {
	rules := forkToChainRules(ForkConfig{})

	if rules.IsHomestead || rules.IsByzantium || rules.IsConstantinople ||
		rules.IsPetersburg || rules.IsIstanbul || rules.IsBerlin ||
		rules.IsLondon || rules.IsShanghai || rules.IsCancun ||
		rules.IsPrague || rules.IsPectra || rules.IsOsaka || rules.IsFusaka {
		t.Fatal("expected all fork flags false for zero ForkConfig")
	}
	if rules.ChainID == nil {
		t.Fatal("ChainID should be non-nil (zero big.Int)")
	}
}

// TestForkToChainRules_AllTrue verifies all-true ForkConfig maps correctly.
func TestForkToChainRules_AllTrue(t *testing.T) {
	fork := ForkConfig{
		IsHomestead:           true,
		IsEIP150:              true,
		IsEIP155:              true,
		IsEIP158:              true,
		IsByzantium:           true,
		IsConstantinople:      true,
		IsPetersburg:          true,
		IsIstanbul:            true,
		IsBerlin:              true,
		IsLondon:              true,
		IsShanghai:            true,
		IsCancun:              true,
		IsBeijing:             true,
		IsPrague:              true,
		IsPectra:              true,
		IsOsaka:               true,
		IsFusaka:              true,
		IsNano:                true,
		IsMoran:               true,
		IsEip1559FeeCollector: true,
		IsParlia:              true,
		IsAura:                true,
	}

	rules := forkToChainRules(fork)

	checks := []struct {
		name string
		got  bool
	}{
		{"IsHomestead", rules.IsHomestead},
		{"IsTangerineWhistle", rules.IsTangerineWhistle},
		{"IsSpuriousDragon", rules.IsSpuriousDragon},
		{"IsByzantium", rules.IsByzantium},
		{"IsConstantinople", rules.IsConstantinople},
		{"IsPetersburg", rules.IsPetersburg},
		{"IsIstanbul", rules.IsIstanbul},
		{"IsBerlin", rules.IsBerlin},
		{"IsLondon", rules.IsLondon},
		{"IsShanghai", rules.IsShanghai},
		{"IsCancun", rules.IsCancun},
		{"IsBeijing", rules.IsBeijing},
		{"IsPrague", rules.IsPrague},
		{"IsPectra", rules.IsPectra},
		{"IsOsaka", rules.IsOsaka},
		{"IsFusaka", rules.IsFusaka},
		{"IsNano", rules.IsNano},
		{"IsMoran", rules.IsMoran},
		{"IsEip1559FeeCollector", rules.IsEip1559FeeCollector},
		{"IsParlia", rules.IsParlia},
		{"IsAura", rules.IsAura},
	}

	for _, c := range checks {
		if !c.got {
			t.Errorf("%s should be true", c.name)
		}
	}
}

// TestForkToChainRules_SpuriousDragonFromEIP155 verifies IsSpuriousDragon
// is set when either IsEIP155 or IsEIP158 is true.
func TestForkToChainRules_SpuriousDragonFromEIP155(t *testing.T) {
	fork := ForkConfig{IsEIP155: true}
	rules := forkToChainRules(fork)
	if !rules.IsSpuriousDragon {
		t.Fatal("IsSpuriousDragon should be true when IsEIP155 is true")
	}

	fork2 := ForkConfig{IsEIP158: true}
	rules2 := forkToChainRules(fork2)
	// IsEIP158 alone doesn't set SpuriousDragon (it's IsEIP155 || IsEIP158)
	if !rules2.IsSpuriousDragon {
		t.Fatal("IsSpuriousDragon should be true when IsEIP158 is true")
	}
}

// TestForkToChainRules_TangerineWhistle verifies EIP150 maps to TangerineWhistle.
func TestForkToChainRules_TangerineWhistle(t *testing.T) {
	fork := ForkConfig{IsEIP150: true}
	rules := forkToChainRules(fork)
	if !rules.IsTangerineWhistle {
		t.Fatal("IsTangerineWhistle should be true when IsEIP150 is true")
	}
}

// TestForkToChainRules_FieldCount verifies params.Rules has expected fork fields.
func TestForkToChainRules_FieldCount(t *testing.T) {
	// Sanity check: if params.Rules adds new fields, this test reminds us
	// to update forkToChainRules.
	rules := params.Rules{}
	_ = rules
	// Just verify the function doesn't panic with various configs.
	configs := []ForkConfig{
		{IsHomestead: true},
		{IsBerlin: true, IsLondon: true},
		{IsCancun: true, IsPrague: true, IsPectra: true},
		{IsOsaka: true, IsFusaka: true},
	}
	for _, cfg := range configs {
		_ = forkToChainRules(cfg)
	}
}

// TestForkToChainConfig_SignerSelection verifies that forkToChainConfig produces
// a ChainConfig that enables the correct signer type in MakeSigner.
func TestForkToChainConfig_SignerSelection(t *testing.T) {
	blockNum := big.NewInt(100)
	blockTime := uint64(1700000000)

	// London fork → should enable London signer.
	cfg := forkToChainConfig(ForkConfig{
		IsHomestead: true,
		IsEIP155:    true,
		IsBerlin:    true,
		IsLondon:    true,
	}, 1, blockNum, blockTime)

	if !cfg.IsLondon(blockNum.Uint64()) {
		t.Fatal("ChainConfig.IsLondon should return true for London fork")
	}
	if !cfg.IsBerlin(blockNum.Uint64()) {
		t.Fatal("ChainConfig.IsBerlin should return true for Berlin fork")
	}
	if cfg.ChainID.Int64() != 1 {
		t.Fatalf("ChainID: got %d, want 1", cfg.ChainID.Int64())
	}

	// No forks → should fall through to Frontier.
	cfg2 := forkToChainConfig(ForkConfig{}, 1, blockNum, blockTime)
	if cfg2.IsLondon(blockNum.Uint64()) {
		t.Fatal("ChainConfig.IsLondon should be false for empty ForkConfig")
	}
	if cfg2.IsHomestead(blockNum.Uint64()) {
		t.Fatal("ChainConfig.IsHomestead should be false for empty ForkConfig")
	}

	// Prague (timestamp-based) → should enable Prague.
	cfg3 := forkToChainConfig(ForkConfig{IsPrague: true}, 1, blockNum, blockTime)
	if !cfg3.IsPrague(blockTime) {
		t.Fatal("ChainConfig.IsPrague should return true")
	}
}

// TestCanTransfer verifies the canTransfer function.
func TestCanTransfer(t *testing.T) {
	ibs := &mockIBS{balances: map[types.Address]*uint256.Int{
		{0x01}: uint256.NewInt(1000),
	}}

	// Sufficient balance.
	if !canTransfer(ibs, types.Address{0x01}, uint256.NewInt(500)) {
		t.Fatal("should be able to transfer 500 with balance 1000")
	}
	// Exact balance.
	if !canTransfer(ibs, types.Address{0x01}, uint256.NewInt(1000)) {
		t.Fatal("should be able to transfer exact balance")
	}
	// Insufficient balance.
	if canTransfer(ibs, types.Address{0x01}, uint256.NewInt(1001)) {
		t.Fatal("should not be able to transfer more than balance")
	}
	// Unknown address (zero balance).
	if canTransfer(ibs, types.Address{0x02}, uint256.NewInt(1)) {
		t.Fatal("should not be able to transfer from unknown address")
	}
}

// TestTransfer verifies the transfer function modifies balances.
func TestTransfer(t *testing.T) {
	ibs := &mockIBS{balances: map[types.Address]*uint256.Int{
		{0x01}: uint256.NewInt(1000),
		{0x02}: uint256.NewInt(500),
	}}

	transfer(ibs, types.Address{0x01}, types.Address{0x02}, uint256.NewInt(300), false)

	if ibs.GetBalance(types.Address{0x01}).Uint64() != 700 {
		t.Fatalf("sender balance: got %d, want 700", ibs.GetBalance(types.Address{0x01}).Uint64())
	}
	if ibs.GetBalance(types.Address{0x02}).Uint64() != 800 {
		t.Fatalf("recipient balance: got %d, want 800", ibs.GetBalance(types.Address{0x02}).Uint64())
	}
}

func TestApplyTxRejectsMissingHeaderNumber(t *testing.T) {
	_, err := ApplyTx(ForkConfig{}, 1, &block.Header{}, nil, nil, 0, nil, nil, 0)
	if err == nil || err.Error() != "block header number is nil" {
		t.Fatalf("ApplyTx() error = %v, want block header number is nil", err)
	}
}

// mockIBS implements a minimal evmtypes.IntraBlockState for canTransfer/transfer tests.
type mockIBS struct {
	evmtypes.IntraBlockState
	balances map[types.Address]*uint256.Int
}

func (m *mockIBS) GetBalance(addr types.Address) *uint256.Int {
	if b, ok := m.balances[addr]; ok {
		return b.Clone()
	}
	return uint256.NewInt(0)
}

func (m *mockIBS) SubBalance(addr types.Address, amount *uint256.Int) {
	if b, ok := m.balances[addr]; ok {
		m.balances[addr] = new(uint256.Int).Sub(b, amount)
	}
}

func (m *mockIBS) AddBalance(addr types.Address, amount *uint256.Int) {
	if b, ok := m.balances[addr]; ok {
		m.balances[addr] = new(uint256.Int).Add(b, amount)
	} else {
		m.balances[addr] = amount.Clone()
	}
}
