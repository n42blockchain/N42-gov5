package main

// Measures what a native stablecoin transfer would save over the EVM.
//
// Both paths start from the same committed state (real mainnet bytecode for
// USDT, and for the USDC proxy plus its FiatTokenV2_2 implementation) and read
// it through a fresh IntraBlockState each iteration, so storage reads, keccak
// slot derivation, journaling and the log are paid on both sides. The native
// path performs exactly the storage accesses the contract performs on its
// happy path; the difference between the two is interpreter work.
//
// Bytecode is read from an N42 datadir's Code table (N42_CODE_DB, default
// D:/N42-eth1177). The balance mapping slot is discovered by trial rather
// than assumed.

import (
	"context"
	"math/big"
	"os"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/runtime"
	"github.com/n42blockchain/N42/lib/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

var (
	benchUSDT     = types.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7")
	benchUSDC     = types.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	benchUSDCImpl = types.HexToAddress("0x43506849d7c04f9138d1a2050bbf3a0c054402dd")
	benchSender   = types.HexToAddress("0x1111111111111111111111111111111111111111")
	benchRecip    = types.HexToAddress("0x2222222222222222222222222222222222222222")
	benchOwner    = types.HexToAddress("0x3333333333333333333333333333333333333333")

	codeHashUSDT     = types.HexToHash("0xb44fb4e949d0f78f87f79ee46428f23a2a5713ce6fc6e0beb3dda78c2ac1ea55")
	codeHashUSDCProx = types.HexToHash("0xd80d4b7c890cb9d6a4893e6b52bc34b56b25335cb13716e0d1d31383e6b41505")
	codeHashUSDCImpl = types.HexToHash("0xcdfb7d322961af3acae7a8f7ee8b69c205b36f576cc5b077f170c7eb8ecbe3ea")

	usdcImplSlot  = types.HexToHash("0x7050c9e0f4ca769c69bd3a8ef740bc37934f8e2c036e5a723fd8ee048ed3f8c3")
	usdcAdminSlot = types.HexToHash("0x10d6a54a4754c8869d6886b5f5d7fbfa5b4522237ea5c60d11bc4e7a1ff9390b")

	benchAmount = uint256.NewInt(1_000_000) // 1 token at 6 decimals
)

const benchGas = 1_000_000

func benchChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        new(big.Int),
		TangerineWhistleBlock: new(big.Int),
		SpuriousDragonBlock:   new(big.Int),
		ByzantiumBlock:        new(big.Int),
		ConstantinopleBlock:   new(big.Int),
		PetersburgBlock:       new(big.Int),
		IstanbulBlock:         new(big.Int),
		MuirGlacierBlock:      new(big.Int),
		BerlinBlock:           new(big.Int),
		LondonBlock:           new(big.Int),
		ArrowGlacierBlock:     new(big.Int),
		GrayGlacierBlock:      new(big.Int),
		ShanghaiBlock:         new(big.Int),
		CancunBlock:           new(big.Int),
		PragueTime:            new(big.Int),
	}
}

func loadCodes(tb testing.TB) map[types.Hash][]byte {
	dir := os.Getenv("N42_CODE_DB")
	if dir == "" {
		dir = "D:/N42-eth1177"
	}
	db, err := mdbx.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).Accede().Readonly().Open(context.Background())
	if err != nil {
		tb.Skipf("code db %s unavailable: %v", dir, err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		tb.Fatal(err)
	}
	defer tx.Rollback()
	out := map[types.Hash][]byte{}
	for _, h := range []types.Hash{codeHashUSDT, codeHashUSDCProx, codeHashUSDCImpl} {
		c, err := tx.GetOne(modules.Code, h[:])
		if err != nil || len(c) == 0 {
			tb.Fatalf("code %x missing: %v", h, err)
		}
		out[h] = append([]byte(nil), c...)
	}
	return out
}

func mapKey(addr types.Address, slot uint64) types.Hash {
	var buf [64]byte
	copy(buf[12:32], addr[:])
	new(uint256.Int).SetUint64(slot).WriteToSlice(buf[32:])
	return types.BytesToHash(crypto.Keccak256(buf[:]))
}

func slotHash(n uint64) types.Hash {
	var h types.Hash
	new(uint256.Int).SetUint64(n).WriteToSlice(h[:])
	return h
}

func addrWord(a types.Address) uint256.Int {
	var v uint256.Int
	v.SetBytes(a[:])
	return v
}

func transferInput(to types.Address, amount *uint256.Int) []byte {
	in := make([]byte, 68)
	copy(in[:4], selTransfer[:])
	copy(in[16:36], to[:])
	amount.WriteToSlice(in[36:68])
	return in
}

type benchToken struct {
	name    string
	addr    types.Address
	balSlot uint64
}

// buildState commits contract code, the proxy wiring, owner/pauser words and,
// when balSlot >= 0, the sender and recipient balances. The recipient already
// holds the token, which is the common case (a nonzero -> nonzero SSTORE).
func buildState(tb testing.TB, codes map[types.Hash][]byte, tok types.Address, balSlot int) kv.RwTx {
	db := memdb.NewTestDB(tb)
	tx := memdb.BeginRw(tb, db)
	ibs := state.New(state.NewPlainStateReader(tx))

	ibs.CreateAccount(benchSender, false)
	ibs.SetBalance(benchSender, uint256.NewInt(1e18))
	owner := addrWord(benchOwner)
	if tok == benchUSDT {
		ibs.CreateAccount(benchUSDT, true)
		ibs.SetCode(benchUSDT, codes[codeHashUSDT])
		s0 := slotHash(0)
		ibs.SetState(benchUSDT, &s0, owner)
	} else {
		ibs.CreateAccount(benchUSDC, true)
		ibs.SetCode(benchUSDC, codes[codeHashUSDCProx])
		ibs.CreateAccount(benchUSDCImpl, true)
		ibs.SetCode(benchUSDCImpl, codes[codeHashUSDCImpl])
		impl := addrWord(benchUSDCImpl)
		ibs.SetState(benchUSDC, &usdcImplSlot, impl)
		ibs.SetState(benchUSDC, &usdcAdminSlot, owner)
		s0, s1 := slotHash(0), slotHash(1)
		ibs.SetState(benchUSDC, &s0, owner)
		ibs.SetState(benchUSDC, &s1, owner)
	}
	if balSlot >= 0 {
		ks, kr := mapKey(benchSender, uint64(balSlot)), mapKey(benchRecip, uint64(balSlot))
		ibs.SetState(tok, &ks, *uint256.NewInt(1_000_000_000_000))
		ibs.SetState(tok, &kr, *uint256.NewInt(5_000_000))
	}
	rules := benchChainConfig().Rules(1)
	if err := ibs.CommitBlock(rules, state.NewPlainStateWriter(tx, tx, 1)); err != nil {
		tb.Fatal(err)
	}
	return tx
}

func callTransfer(tx kv.Tx, tok types.Address) (*state.IntraBlockState, uint64, error) {
	ibs := state.New(state.NewPlainStateReader(tx))
	cfg := &runtime.Config{
		ChainConfig: benchChainConfig(),
		Origin:      benchSender,
		BlockNumber: big.NewInt(1),
		Time:        big.NewInt(1),
		GasLimit:    benchGas,
		GasPrice:    new(uint256.Int),
		Value:       new(uint256.Int),
		State:       ibs,
	}
	_, left, err := runtime.Call(tok, transferInput(benchRecip, benchAmount), cfg)
	return ibs, benchGas - left, err
}

// discoverBalanceSlot finds the storage index of the balances mapping by
// seeding one candidate at a time and checking that a transfer lands there.
func discoverBalanceSlot(tb testing.TB, codes map[types.Hash][]byte, tok types.Address) (uint64, uint64) {
	for slot := 0; slot <= 20; slot++ {
		tx := buildState(tb, codes, tok, slot)
		ibs, gas, err := callTransfer(tx, tok)
		if err != nil {
			continue
		}
		kr := mapKey(benchRecip, uint64(slot))
		var got uint256.Int
		ibs.GetState(tok, &kr, &got)
		want := new(uint256.Int).Add(uint256.NewInt(5_000_000), benchAmount)
		if got.Eq(want) {
			return uint64(slot), gas
		}
	}
	tb.Fatalf("no balance slot in 0..20 makes a %x transfer land", tok)
	return 0, 0
}

func nativeUSDT(ibs *state.IntraBlockState, balSlot uint64) {
	var v uint256.Int
	s0, s3, s4, s10 := slotHash(0), slotHash(3), slotHash(4), slotHash(10)
	ibs.GetState(benchUSDT, &s0, &v) // paused (packed with owner)
	bl := mapKey(benchSender, 6)
	ibs.GetState(benchUSDT, &bl, &v)  // isBlackListed[sender]
	ibs.GetState(benchUSDT, &s10, &v) // deprecated
	ibs.GetState(benchUSDT, &s3, &v)  // basisPointsRate
	ibs.GetState(benchUSDT, &s4, &v)  // maximumFee
	nativeMove(ibs, benchUSDT, balSlot)
}

func nativeUSDC(ibs *state.IntraBlockState, balSlot uint64) {
	var v uint256.Int
	s1 := slotHash(1)
	ibs.GetState(benchUSDC, &usdcImplSlot, &v) // proxy implementation
	ibs.GetState(benchUSDC, &s1, &v)           // paused (packed with pauser)
	nativeMove(ibs, benchUSDC, balSlot)        // blacklist bit rides in the balance word
}

func nativeMove(ibs *state.IntraBlockState, tok types.Address, balSlot uint64) {
	ks, kr := mapKey(benchSender, balSlot), mapKey(benchRecip, balSlot)
	var bs, br uint256.Int
	ibs.GetState(tok, &ks, &bs)
	ibs.GetState(tok, &kr, &br)
	bs.Sub(&bs, benchAmount)
	br.Add(&br, benchAmount)
	ibs.SetState(tok, &ks, bs)
	ibs.SetState(tok, &kr, br)
	var from, to types.Hash
	copy(from[12:], benchSender[:])
	copy(to[12:], benchRecip[:])
	data := make([]byte, 32)
	benchAmount.WriteToSlice(data)
	ibs.AddLog(&block.Log{Address: tok, Topics: []types.Hash{transferTopic, from, to}, Data: data})
}

func init() {
	transferTopic = types.BytesToHash(crypto.Keccak256([]byte("Transfer(address,address,uint256)")))
}

// TestNativeMatchesEVM checks the native path reaches the same balances as the
// real bytecode, so the benchmark compares equal work.
func TestNativeMatchesEVM(t *testing.T) {
	codes := loadCodes(t)
	for _, tc := range []struct {
		tok    types.Address
		name   string
		native func(*state.IntraBlockState, uint64)
	}{{benchUSDT, "USDT", nativeUSDT}, {benchUSDC, "USDC", nativeUSDC}} {
		slot, gas := discoverBalanceSlot(t, codes, tc.tok)
		t.Logf("%s: balances mapping at slot %d, EVM call gas %d (before refund)", tc.name, slot, gas)
		tx := buildState(t, codes, tc.tok, int(slot))
		evm, _, err := callTransfer(tx, tc.tok)
		if err != nil {
			t.Fatalf("%s evm: %v", tc.name, err)
		}
		nat := state.New(state.NewPlainStateReader(tx))
		tc.native(nat, slot)
		for _, a := range []types.Address{benchSender, benchRecip} {
			k := mapKey(a, slot)
			var e, n uint256.Int
			evm.GetState(tc.tok, &k, &e)
			nat.GetState(tc.tok, &k, &n)
			if !e.Eq(&n) {
				t.Fatalf("%s balance of %x: evm %s native %s", tc.name, a, e.String(), n.String())
			}
		}
	}
}

func benchEVM(b *testing.B, tok types.Address) {
	codes := loadCodes(b)
	slot, _ := discoverBalanceSlot(b, codes, tok)
	tx := buildState(b, codes, tok, int(slot))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := callTransfer(tx, tok); err != nil {
			b.Fatal(err)
		}
	}
}

func benchNative(b *testing.B, tok types.Address, native func(*state.IntraBlockState, uint64)) {
	codes := loadCodes(b)
	slot, _ := discoverBalanceSlot(b, codes, tok)
	tx := buildState(b, codes, tok, int(slot))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ibs := state.New(state.NewPlainStateReader(tx))
		native(ibs, slot)
	}
}

// benchSetup is the floor both paths share per transaction in this harness:
// a fresh IntraBlockState and, for the EVM, an environment with its access
// list prepared.
func BenchmarkSetupOnly(b *testing.B) {
	codes := loadCodes(b)
	tx := buildState(b, codes, benchUSDT, 2)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ibs := state.New(state.NewPlainStateReader(tx))
		cfg := &runtime.Config{ChainConfig: benchChainConfig(), Origin: benchSender, BlockNumber: big.NewInt(1),
			Time: big.NewInt(1), GasLimit: benchGas, GasPrice: new(uint256.Int), Value: new(uint256.Int), State: ibs}
		_ = runtime.NewEnv(cfg)
	}
}

func BenchmarkUSDTTransferEVM(b *testing.B)    { benchEVM(b, benchUSDT) }
func BenchmarkUSDTTransferNative(b *testing.B) { benchNative(b, benchUSDT, nativeUSDT) }
func BenchmarkUSDCTransferEVM(b *testing.B)    { benchEVM(b, benchUSDC) }
func BenchmarkUSDCTransferNative(b *testing.B) { benchNative(b, benchUSDC, nativeUSDC) }
