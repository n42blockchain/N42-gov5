package replay

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func preludeTestDeltas() (*P1AllocDelta, *P1AllocDelta) {
	d1 := NewP1AllocDelta()
	d2 := NewP1AllocDelta()
	for i := 0; i < 200; i++ {
		a := types.Address{0xa0, byte(i >> 8), byte(i)}
		d1.AddPrecreate(a, uint64(i+1))
	}
	// Addresses in both files sum; a topup-only address.
	d2.AddTopup(types.Address{0xa0, 0, 5}, uint256.NewInt(1_000_000), 7)
	d2.AddTopup(types.Address{0x01}, new(uint256.Int).Mul(uint256.NewInt(509), uint256.NewInt(1e18)), 3)
	return d1, d2
}

func TestP1PreludeTransfersSumAndSort(t *testing.T) {
	d1, d2 := preludeTestDeltas()
	tr := P1PreludeTransfers(d1, d2)
	require.Len(t, tr, 201)
	for i := 1; i < len(tr); i++ {
		require.Less(t, string(tr[i-1].To[:]), string(tr[i].To[:]))
	}
	require.Equal(t, types.Address{0x01}, tr[0].To)
	for _, x := range tr {
		switch x.To {
		case types.Address{0xa0, 0, 5}:
			require.Equal(t, uint64(1_000_001), x.Wei.Uint64())
		case types.Address{0x01}:
			require.Equal(t, "509000000000000000000", x.Wei.Dec())
		default:
			require.Equal(t, uint64(1), x.Wei.Uint64())
		}
	}
	// Order of the files does not matter.
	require.Equal(t, tr, P1PreludeTransfers(d2, d1))
	// Zero entries are skipped.
	z := NewP1AllocDelta()
	z.entry(types.Address{0x02}, 1)
	require.Empty(t, P1PreludeTransfers(z))
}

func TestP1PreludeGasAndFee(t *testing.T) {
	require.Equal(t, uint64(params.TxGas)+vm2.NewAccountStateGas, P1PreludeGasPerTx())
	require.Equal(t, uint64(2_000_000_000), P1PreludeMaxFee(nil).Uint64())
	require.Equal(t, uint64(14), P1PreludeMaxFee(uint256.NewInt(7)).Uint64())
	g := P1PreludeGasPerTx()
	require.Equal(t, int(15_000_000/g), P1PreludeTxsPerBlock(30_000_000, g))

	tr := []P1PreludeTransfer{{To: types.Address{1}, Wei: *uint256.NewInt(5)}, {To: types.Address{2}, Wei: *uint256.NewInt(1)}}
	got := P1PreludeFunderBalance(tr, 100, uint256.NewInt(3))
	// 6 + 2*100*3*1.1
	require.Equal(t, uint64(6+600+60), got.Uint64())
}

func TestBuildP1PreludeTxsDeterministic(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	funder := crypto.PubkeyToAddress(key.PublicKey)
	d1, d2 := preludeTestDeltas()
	tr := P1PreludeTransfers(d1, d2)
	gas, fee := P1PreludeGasPerTx(), P1PreludeMaxFee(uint256.NewInt(1e9))
	chainID := big.NewInt(94)
	a, err := BuildP1PreludeTxs(chainID, key, tr, gas, fee)
	require.NoError(t, err)
	b, err := BuildP1PreludeTxs(chainID, key, tr, gas, fee)
	require.NoError(t, err)
	require.Len(t, a, len(tr))
	signer := transaction.LatestSignerForChainID(chainID)
	for i := range a {
		require.Equal(t, a[i].Hash(), b[i].Hash(), "tx %d not deterministic", i)
		require.Equal(t, uint64(i), a[i].Nonce())
		require.Equal(t, tr[i].To, *a[i].To())
		require.Equal(t, tr[i].Wei.Dec(), a[i].Value().Dec())
		require.Equal(t, gas, a[i].Gas())
		require.Equal(t, uint64(1), a[i].GasTipCap().Uint64())
		require.Equal(t, fee.Uint64(), a[i].GasFeeCap().Uint64())
		require.Equal(t, uint8(transaction.DynamicFeeTxType), a[i].Type())
		from, err := transaction.Sender(signer, a[i])
		require.NoError(t, err)
		require.Equal(t, funder, from)
	}
	require.Equal(t, P1PreludeDigest(funder, gas, fee, tr), P1PreludeDigest(funder, gas, fee, P1PreludeTransfers(d2, d1)))
	require.NotEqual(t, P1PreludeDigest(funder, gas, fee, tr), P1PreludeDigest(funder, gas, fee, tr[1:]))

	// The funder must not be a delta address.
	bad := append([]P1PreludeTransfer{{To: funder, Wei: *uint256.NewInt(1)}}, tr...)
	_, err = BuildP1PreludeTxs(chainID, key, bad, gas, fee)
	require.Error(t, err)
}

func TestPackP1Prelude(t *testing.T) {
	txs := make([]*transaction.Transaction, 10)
	for i := range txs {
		txs[i] = transaction.NewTx(&transaction.DynamicFeeTx{Nonce: uint64(i), Value: new(uint256.Int)})
	}
	blocks := PackP1Prelude(txs, 4)
	require.Len(t, blocks, 3)
	require.Len(t, blocks[0], 4)
	require.Len(t, blocks[2], 2)
	n := uint64(0)
	for _, b := range blocks {
		for _, tx := range b {
			require.Equal(t, n, tx.Nonce())
			n++
		}
	}
	require.Empty(t, PackP1Prelude(nil, 4))
}

// One packed prelude block executes under Amsterdam rules: every transfer
// into an empty account succeeds, the recipients hold exactly their delta,
// and the block's gas figure stays at or below the gas target, so the base
// fee does not rise.
func TestP1PreludeBlockExecutes(t *testing.T) {
	modules.N42Init()
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })

	zero := big.NewInt(0)
	cfg := &params.ChainConfig{
		ChainID: big.NewInt(94), Consensus: params.Faker,
		HomesteadBlock: zero, TangerineWhistleBlock: zero, SpuriousDragonBlock: zero,
		ByzantiumBlock: zero, ConstantinopleBlock: zero, PetersburgBlock: zero,
		IstanbulBlock: zero, BerlinBlock: zero, LondonBlock: zero,
		ShanghaiTime: zero, CancunTime: zero, PragueTime: zero, OsakaTime: zero, GlamsterdamTime: zero,
	}
	const gasLimit = 30_000_000
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	funder := crypto.PubkeyToAddress(key.PublicKey)
	d1, d2 := preludeTestDeltas()
	tr := P1PreludeTransfers(d1, d2)
	baseFee := uint256.NewInt(1e9)
	gas, fee := P1PreludeGasPerTx(), P1PreludeMaxFee(baseFee)
	bal := P1PreludeFunderBalance(tr, gas, fee)

	coinbase := types.Address{0xcb}
	db := memdb.NewTestDB(t)
	genesis := &internal.GenesisBlock{GenesisConfig: &conf.Genesis{
		Config: cfg, GasLimit: gasLimit, Difficulty: uint256.NewInt(0), Timestamp: 1, BaseFee: baseFee,
		Alloc: conf.GenesisAlloc{funder: {Balance: bal.Dec()}},
	}}
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		_, _, err := genesis.Write(tx)
		return err
	}))
	txs, err := BuildP1PreludeTxs(cfg.ChainID, key, tr, gas, fee)
	require.NoError(t, err)
	blocks := PackP1Prelude(txs, P1PreludeTxsPerBlock(gasLimit, gas))
	require.Greater(t, len(blocks), 1)

	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		ibs := state.New(state.NewPlainStateReader(tx))
		for bn, btxs := range blocks {
			header := &block.Header{Coinbase: coinbase, Number: uint256.NewInt(uint64(bn + 1)), GasLimit: gasLimit, Time: uint64(bn + 2),
				BaseFee: baseFee, Difficulty: uint256.NewInt(0)}
			gp := new(common.GasPool).AddGas(gasLimit)
			var cum uint64
			for i, txn := range btxs {
				ibs.Prepare(txn.Hash(), types.Hash{}, i)
				r, _, err := internal.ApplyTransaction(cfg, func(uint64) types.Hash { return types.Hash{} }, nil, &header.Coinbase,
					gp, ibs, state.NewNoopWriter(), header, txn, &cum, vm2.Config{})
				require.NoError(t, err, "block %d tx %d", bn, i)
				require.Equal(t, block.ReceiptStatusSuccessful, r.Status)
			}
			require.LessOrEqual(t, internal.HeaderGasUsed(cfg, header, gp, cum), uint64(gasLimit/params.ElasticityMultiplier))
		}
		for _, x := range tr {
			require.Equal(t, x.Wei.Dec(), ibs.GetBalance(x.To).Dec(), "balance of %s", x.To.Hex())
		}
		require.Equal(t, uint64(len(tr)), ibs.GetNonce(funder))
		// The tip keeps the (initially empty) coinbase non-empty.
		require.False(t, ibs.Empty(coinbase))
		return nil
	}))
}
