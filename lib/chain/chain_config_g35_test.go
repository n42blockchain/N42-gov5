/*
   Tests for the fork-activation predicates, compatibility checking, fork
   ordering validation, blob schedule and Rules synthesis in chain_config.go.
*/

package chain

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/lib/common"
)

func bi(n int64) *big.Int { return big.NewInt(n) }

func TestG35IsForkedPredicates(t *testing.T) {
	cfg := &Config{
		HomesteadBlock:        bi(1),
		DAOForkBlock:          bi(2),
		TangerineWhistleBlock: bi(3),
		SpuriousDragonBlock:   bi(4),
		ByzantiumBlock:        bi(5),
		ConstantinopleBlock:   bi(6),
		IstanbulBlock:         bi(8),
		MuirGlacierBlock:      bi(9),
		BerlinBlock:           bi(10),
		LondonBlock:           bi(11),
		ArrowGlacierBlock:     bi(12),
		GrayGlacierBlock:      bi(13),
		ShanghaiTime:          bi(100),
		CancunTime:            bi(200),
		PragueTime:            bi(300),
		OsakaTime:             bi(400),
	}

	type predicate struct {
		name string
		fn   func(uint64) bool
		at   uint64
	}
	preds := []predicate{
		{"Homestead", cfg.IsHomestead, 1},
		{"DAOFork", cfg.IsDAOFork, 2},
		{"TangerineWhistle", cfg.IsTangerineWhistle, 3},
		{"SpuriousDragon", cfg.IsSpuriousDragon, 4},
		{"Byzantium", cfg.IsByzantium, 5},
		{"Constantinople", cfg.IsConstantinople, 6},
		{"Istanbul", cfg.IsIstanbul, 8},
		{"MuirGlacier", cfg.IsMuirGlacier, 9},
		{"Berlin", cfg.IsBerlin, 10},
		{"London", cfg.IsLondon, 11},
		{"ArrowGlacier", cfg.IsArrowGlacier, 12},
		{"GrayGlacier", cfg.IsGrayGlacier, 13},
		{"Shanghai", cfg.IsShanghai, 100},
		{"Cancun", cfg.IsCancun, 200},
		{"Prague", cfg.IsPrague, 300},
		{"Osaka", cfg.IsOsaka, 400},
	}
	for _, p := range preds {
		if !p.fn(p.at) {
			t.Fatalf("%s: expected activation exactly at fork block/time %d", p.name, p.at)
		}
		if p.fn(p.at - 1) {
			t.Fatalf("%s: expected inactive one before fork block/time %d", p.name, p.at)
		}
		if !p.fn(p.at + 1) {
			t.Fatalf("%s: expected active one after fork block/time %d", p.name, p.at)
		}
	}
}

func TestG35IsPetersburgImpliedByConstantinople(t *testing.T) {
	cfg := &Config{ConstantinopleBlock: bi(5)}
	if !cfg.IsPetersburg(5) {
		t.Fatalf("expected Petersburg to follow Constantinople when PetersburgBlock is nil")
	}
	cfg2 := &Config{ConstantinopleBlock: bi(5), PetersburgBlock: bi(10)}
	if cfg2.IsPetersburg(5) {
		t.Fatalf("expected Petersburg not yet active when explicitly scheduled later")
	}
	if !cfg2.IsPetersburg(10) {
		t.Fatalf("expected Petersburg active at its explicit block")
	}
}

func TestG35IsAgraIsNapoliNilSafe(t *testing.T) {
	var cfg *Config
	if cfg.IsAgra(10) {
		t.Fatalf("expected false for nil config")
	}
	if cfg.IsNapoli(10) {
		t.Fatalf("expected false for nil config")
	}

	cfg2 := &Config{}
	if cfg2.IsAgra(10) || cfg2.IsNapoli(10) {
		t.Fatalf("expected false when Bor is nil")
	}
}

type fakeBorConfig struct {
	agraBlock, napoliBlock uint64
}

func (f *fakeBorConfig) String() string                 { return "bor" }
func (f *fakeBorConfig) IsAgra(num uint64) bool         { return num >= f.agraBlock }
func (f *fakeBorConfig) GetAgraBlock() *big.Int         { return bi(int64(f.agraBlock)) }
func (f *fakeBorConfig) IsNapoli(num uint64) bool       { return num >= f.napoliBlock }
func (f *fakeBorConfig) GetNapoliBlock() *big.Int       { return bi(int64(f.napoliBlock)) }
func (f *fakeBorConfig) IsAhmedabad(number uint64) bool { return false }

func TestG35IsAgraIsNapoliWithBor(t *testing.T) {
	cfg := &Config{Bor: &fakeBorConfig{agraBlock: 5, napoliBlock: 10}}
	if cfg.IsAgra(4) || !cfg.IsAgra(5) {
		t.Fatalf("expected Agra activation exactly at block 5")
	}
	if cfg.IsNapoli(9) || !cfg.IsNapoli(10) {
		t.Fatalf("expected Napoli activation exactly at block 10")
	}
}

func TestG35GetBurntContract(t *testing.T) {
	cfg := &Config{}
	if cfg.GetBurntContract(1) != nil {
		t.Fatalf("expected nil for empty BurntContract map")
	}

	var addr common.Address
	addr[0] = 0xAB
	cfg2 := &Config{BurntContract: map[string]common.Address{"0": addr}}
	got := cfg2.GetBurntContract(5)
	if got == nil || *got != addr {
		t.Fatalf("expected burnt contract address %x, got %v", addr, got)
	}
}

func TestG35GetMinBlobGasPrice(t *testing.T) {
	var nilCfg *Config
	if nilCfg.GetMinBlobGasPrice() != 1 {
		t.Fatalf("expected default 1 for nil config")
	}
	cfg := &Config{}
	if cfg.GetMinBlobGasPrice() != 1 {
		t.Fatalf("expected default 1 when unset")
	}
	v := uint64(5)
	cfg.MinBlobGasPrice = &v
	if cfg.GetMinBlobGasPrice() != 5 {
		t.Fatalf("expected configured value 5")
	}
}

func TestG35BlobScheduleDefaults(t *testing.T) {
	cfg := &Config{CancunTime: bi(0), PragueTime: bi(100), OsakaTime: bi(200)}

	if got := cfg.GetTargetBlobsPerBlock(0); got != 3 {
		t.Fatalf("expected default Cancun target 3, got %d", got)
	}
	if got := cfg.GetMaxBlobsPerBlock(100); got != 9 {
		t.Fatalf("expected default Prague max 9, got %d", got)
	}
	if got := cfg.GetTargetBlobsPerBlock(200); got != 10 {
		t.Fatalf("expected default Osaka target 10, got %d", got)
	}
	if got := cfg.GetBlobGasPriceUpdateFraction(0); got != 3338477 {
		t.Fatalf("expected default Cancun update fraction, got %d", got)
	}
	if got := cfg.GetMaxBlobGasPerBlock(0); got == 0 {
		t.Fatalf("expected non-zero max blob gas per block")
	}
}

func TestG35BlobScheduleOverride(t *testing.T) {
	target := uint64(99)
	cfg := &Config{
		CancunTime: bi(0),
		BlobSchedule: &BlobSchedule{
			Cancun: &BlobConfig{Target: &target},
		},
	}
	if got := cfg.GetTargetBlobsPerBlock(0); got != 99 {
		t.Fatalf("expected overridden target 99, got %d", got)
	}
}

func TestG35GetMaxRlpBlockSize(t *testing.T) {
	cfg := &Config{OsakaTime: bi(100)}
	if got := cfg.GetMaxRlpBlockSize(99); got <= 10_485_760 {
		t.Fatalf("expected pre-Osaka block size to be effectively unbounded, got %d", got)
	}
	if got := cfg.GetMaxRlpBlockSize(100); got != 10_485_760-2_097_152 {
		t.Fatalf("expected Osaka-capped RLP block size, got %d", got)
	}
}

func TestG35SecondsPerSlot(t *testing.T) {
	if (&Config{Bor: &fakeBorConfig{}}).SecondsPerSlot() != 2 {
		t.Fatalf("expected 2s slots for Bor chains")
	}
	if (&Config{Aura: &AuRaConfig{}}).SecondsPerSlot() != 5 {
		t.Fatalf("expected 5s slots for Aura chains")
	}
	if (&Config{}).SecondsPerSlot() != 12 {
		t.Fatalf("expected 12s slots by default")
	}
}

func TestG35StringAndGetEngine(t *testing.T) {
	cfg := &Config{ChainID: bi(1), Ethash: &EthashConfig{}}
	if s := cfg.String(); s == "" {
		t.Fatalf("expected non-empty String()")
	}
	if cfg.getEngine() != "ethash" {
		t.Fatalf("expected ethash engine")
	}
	cfg2 := &Config{Clique: &CliqueConfig{}}
	if cfg2.getEngine() != "clique" {
		t.Fatalf("expected clique engine")
	}
	cfg3 := &Config{Aura: &AuRaConfig{}}
	if cfg3.getEngine() != "aura" {
		t.Fatalf("expected aura engine")
	}
	cfg4 := &Config{}
	if cfg4.getEngine() != "unknown" {
		t.Fatalf("expected unknown engine for empty config")
	}
}

func TestG35CheckConfigForkOrderValid(t *testing.T) {
	cfg := &Config{
		ChainID:               bi(1),
		HomesteadBlock:        bi(1),
		TangerineWhistleBlock: bi(2),
		SpuriousDragonBlock:   bi(3),
		ByzantiumBlock:        bi(4),
		ConstantinopleBlock:   bi(5),
		PetersburgBlock:       bi(5),
		IstanbulBlock:         bi(6),
		BerlinBlock:           bi(7),
		LondonBlock:           bi(8),
	}
	if err := cfg.CheckConfigForkOrder(); err != nil {
		t.Fatalf("expected valid fork order, got %v", err)
	}
}

func TestG35CheckConfigForkOrderOutOfOrder(t *testing.T) {
	cfg := &Config{
		ChainID:        bi(1),
		HomesteadBlock: bi(5),
		ByzantiumBlock: bi(1), // earlier than homestead -> invalid given intervening required forks nil
	}
	if err := cfg.CheckConfigForkOrder(); err == nil {
		t.Fatalf("expected fork-ordering error")
	}
}

func TestG35CheckConfigForkOrderSkipsChain77(t *testing.T) {
	cfg := &Config{ChainID: bi(77), ByzantiumBlock: bi(1)}
	if err := cfg.CheckConfigForkOrder(); err != nil {
		t.Fatalf("expected chain 77 to bypass fork-order checks, got %v", err)
	}
}

func TestG35CheckCompatible(t *testing.T) {
	old := &Config{HomesteadBlock: bi(10)}
	newer := &Config{HomesteadBlock: bi(20)}

	// head before the fork: rescheduling is fine.
	if err := old.CheckCompatible(newer, 5); err != nil {
		t.Fatalf("expected compatible before fork activation, got %v", err)
	}

	// head after old fork activation but new config reschedules it later -> incompatible.
	if err := old.CheckCompatible(newer, 15); err == nil {
		t.Fatalf("expected incompatible config change past activation")
	} else if err.RewindTo != 9 {
		t.Fatalf("expected RewindTo 9, got %d", err.RewindTo)
	}
}

func TestG35CheckCompatibleEIP155ChainID(t *testing.T) {
	old := &Config{SpuriousDragonBlock: bi(1), ChainID: bi(1)}
	newer := &Config{SpuriousDragonBlock: bi(1), ChainID: bi(2)}
	if err := old.CheckCompatible(newer, 5); err == nil {
		t.Fatalf("expected EIP155 chain ID mismatch to be incompatible")
	}
}

func TestG35ConfigCompatErrorString(t *testing.T) {
	err := &ConfigCompatError{What: "test fork", StoredConfig: bi(1), NewConfig: bi(2), RewindTo: 0}
	if err.Error() == "" {
		t.Fatalf("expected non-empty error string")
	}
}

func TestG35EthashCliqueAuraString(t *testing.T) {
	if (&EthashConfig{}).String() != "ethash" {
		t.Fatalf("expected ethash string")
	}
	if (&CliqueConfig{}).String() != "clique" {
		t.Fatalf("expected clique string")
	}
	if (&AuRaConfig{}).String() != "aura" {
		t.Fatalf("expected aura string")
	}
}

func TestG35Rules(t *testing.T) {
	cfg := &Config{
		ChainID:        bi(5),
		HomesteadBlock: bi(1),
		LondonBlock:    bi(2),
		ShanghaiTime:   bi(10),
		CancunTime:     bi(20),
		Aura:           &AuRaConfig{},
	}
	r := cfg.Rules(2, 20)
	if r.ChainID.Int64() != 5 {
		t.Fatalf("expected ChainID 5, got %v", r.ChainID)
	}
	if !r.IsHomestead || !r.IsLondon || !r.IsShanghai || !r.IsCancun || !r.IsAura {
		t.Fatalf("expected all active forks/engine flags to be set: %+v", r)
	}

	// Nil ChainID must not panic and defaults to zero.
	cfgNilID := &Config{}
	r2 := cfgNilID.Rules(0, 0)
	if r2.ChainID.Sign() != 0 {
		t.Fatalf("expected zero ChainID when unset")
	}
}

func TestG35ConsensusNames(t *testing.T) {
	names := []ConsensusName{AuRaConsensus, EtHashConsensus, CliqueConsensus, BorConsensus}
	want := []string{"aura", "ethash", "clique", "bor"}
	for i, n := range names {
		if string(n) != want[i] {
			t.Fatalf("expected %q, got %q", want[i], n)
		}
	}
}
