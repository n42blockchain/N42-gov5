package internal

import (
	"bytes"
	"context"
	"crypto/ecdsa"
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
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/zkprover"
	"github.com/n42blockchain/N42/internal/zkprover/guest"
	"github.com/n42blockchain/N42/lib/common/fixedgas"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// The zkVM guest applies Glamsterdam transactions as the host does: the
// same transactions, run through ApplyTransaction and through guest.ApplyTx
// from the same state, give the same status and cumulative gas per
// transaction, the same EIP-8037 block gas and the same accounts. The
// transactions cover state gas for new accounts and slots, a refund, a
// creation, the calldata floor, an access list, a revert, an EIP-7702
// delegation and a call through it, a gas limit above TX_MAX_GAS_LIMIT
// (the state reservoir, kept when the frame halts), SLOTNUM, and a
// transfer whose state gas is out of reach.
func TestZKGuestMatchesHostAmsterdam(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	zero := big.NewInt(0)
	cfg := &params.ChainConfig{
		ChainID:               big.NewInt(1337),
		Consensus:             params.Faker,
		HomesteadBlock:        zero,
		TangerineWhistleBlock: zero,
		SpuriousDragonBlock:   zero,
		ByzantiumBlock:        zero,
		ConstantinopleBlock:   zero,
		PetersburgBlock:       zero,
		IstanbulBlock:         zero,
		BerlinBlock:           zero,
		LondonBlock:           zero,
		ShanghaiTime:          zero,
		CancunTime:            zero,
		PragueTime:            zero,
		OsakaTime:             zero,
		GlamsterdamTime:       zero,
	}

	// toggle flips slot 0 between 0 and 1: setting it creates a slot,
	// clearing it earns a refund.
	toggle := []byte{0x60, 0x00, 0x54, 0x15, 0x60, 0x00, 0x55, 0x00}
	reverter := []byte{0x60, 0x00, 0x60, 0x00, 0xfd}
	// slotWriter stores SLOTNUM (EIP-7843) in slot 0.
	slotWriter := []byte{0x4b, 0x60, 0x00, 0x55, 0x00}
	invalid := []byte{0xfe}
	// Initcode returning toggle as the runtime code.
	initcode := append([]byte{0x67}, toggle...)
	initcode = append(initcode, 0x60, 0x00, 0x52, 0x60, 0x08, 0x60, 0x18, 0xf3)

	toggleAddr := types.Address{0xc1}
	revertAddr := types.Address{0xc2}
	slotAddr := types.Address{0xc3}
	invalidAddr := types.Address{0xc4}
	existing := types.Address{0xe1}

	const senders = 15
	keys := make([]*ecdsa.PrivateKey, senders)
	alloc := conf.GenesisAlloc{
		toggleAddr:  {Balance: "0x0", Code: toggle},
		revertAddr:  {Balance: "0x0", Code: reverter},
		slotAddr:    {Balance: "0x0", Code: slotWriter},
		invalidAddr: {Balance: "0x0", Code: invalid},
		existing:    {Balance: "0x1"},
	}
	for i := range keys {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		keys[i] = k
		alloc[crypto.PubkeyToAddress(k.PublicKey)] = conf.GenesisAccount{Balance: "0x56bc75e2d63100000"}
	}
	authKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	authority := crypto.PubkeyToAddress(authKey.PublicKey)

	db := memdb.NewTestDB(t)
	genesis := &GenesisBlock{GenesisConfig: &conf.Genesis{
		Config:     cfg,
		Alloc:      alloc,
		GasLimit:   60_000_000,
		Difficulty: uint256.NewInt(0),
		Timestamp:  1,
		BaseFee:    uint256.NewInt(1),
	}}
	var genesisBlock *block.Block
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := genesis.Write(tx)
		genesisBlock = blk
		return err
	}))

	chainID := uint256.MustFromBig(cfg.ChainID)
	signer := transaction.LatestSignerForChainID(cfg.ChainID)
	sign := func(i int, inner transaction.TxData) *transaction.Transaction {
		tx, err := transaction.SignTx(transaction.NewTx(inner), signer, keys[i])
		require.NoError(t, err)
		return tx
	}
	dyn := func(i int, to *types.Address, gas uint64, value uint64, data []byte) *transaction.Transaction {
		return sign(i, &transaction.DynamicFeeTx{
			ChainID: chainID, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(10),
			Gas: gas, To: to, Value: uint256.NewInt(value), Data: data,
		})
	}
	fresh := types.Address{0xf0, 0x01}
	unreachable := types.Address{0xf0, 0x02}
	self := crypto.PubkeyToAddress(keys[2].PublicKey)
	calldata := bytes.Repeat([]byte{0xff}, 3000)
	auth := signAuthorization(t, authKey, &transaction.Authorization{ChainID: *chainID, Address: toggleAddr, Nonce: 0})

	txs := []*transaction.Transaction{
		dyn(0, &fresh, 1_000_000, 1, nil),
		dyn(1, &existing, 200_000, 1, nil),
		dyn(2, &self, 200_000, 1, nil),
		dyn(3, nil, 500_000, 0, initcode),
		dyn(4, &toggleAddr, 200_000, 0, nil),
		dyn(5, &toggleAddr, 200_000, 0, nil),
		sign(6, &transaction.DynamicFeeTx{
			ChainID: chainID, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(10), Gas: 400_000,
			To: &existing, Value: uint256.NewInt(0), Data: calldata,
			AccessList: transaction.AccessList{{Address: revertAddr, StorageKeys: []types.Hash{{0x02}}}},
		}),
		sign(7, &transaction.AccessListTx{
			ChainID: chainID, GasPrice: uint256.NewInt(10), Gas: 200_000, To: &existing, Value: uint256.NewInt(0),
			AccessList: transaction.AccessList{{Address: toggleAddr, StorageKeys: []types.Hash{{}, {0x01}}}},
		}),
		dyn(8, &revertAddr, 200_000, 1, nil),
		sign(9, &transaction.SetCodeTx{
			ChainID: chainID, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(10), Gas: 1_000_000,
			To: &authority, Value: uint256.NewInt(0), AuthList: transaction.AuthorizationList{auth},
		}),
		dyn(10, &authority, 200_000, 0, nil),
		dyn(11, &toggleAddr, fixedgas.MaxTxnGasLimit+2_000_000, 0, nil),
		dyn(13, &slotAddr, 200_000, 0, nil),
		dyn(14, &invalidAddr, fixedgas.MaxTxnGasLimit+2_000_000, 0, nil),
		dyn(12, &unreachable, params.TxGas, 1, nil),
	}

	slotNumber := uint64(7)
	header := &block.Header{
		ParentHash: genesisBlock.Hash(),
		Number:     uint256.NewInt(1),
		GasLimit:   genesisBlock.GasLimit(),
		Time:       genesisBlock.Time() + 12,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
		Coinbase:   types.Address{0xcb},
		SlotNumber: &slotNumber,
	}
	blockHashFunc := func(uint64) types.Hash { return types.Hash{} }
	fork := zkprover.BuildGuestForkConfig(cfg, 1, header.Time)
	require.True(t, fork.IsGlamsterdam)

	watched := []types.Address{fresh, unreachable, existing, toggleAddr, revertAddr, slotAddr, authority, header.Coinbase,
		crypto.CreateAddress(crypto.PubkeyToAddress(keys[3].PublicKey), 0)}
	for _, k := range keys {
		watched = append(watched, crypto.PubkeyToAddress(k.PublicKey))
	}

	require.NoError(t, db.View(context.Background(), func(dbtx kv.Tx) error {
		hostState := state.New(state.NewPlainStateReader(dbtx))
		guestState := state.New(state.NewPlainStateReader(dbtx))
		hostPool := new(common.GasPool).AddGas(header.GasLimit)
		guestPool := new(common.GasPool).AddGas(header.GasLimit)
		var hostCumulative, guestCumulative uint64
		sawStateGasHalt := false
		for i, txn := range txs {
			hostState.Prepare(txn.Hash(), types.Hash{}, i)
			receipt, _, err := ApplyTransaction(cfg, blockHashFunc, coreTFakerEngine{}, nil, hostPool, hostState, state.NewNoopWriter(), header, txn, &hostCumulative, vm2.Config{})
			require.NoError(t, err, "host tx %d", i)
			res, err := guest.ApplyTx(fork, cfg.ChainID.Uint64(), header, guestState, txn, i, guestPool, blockHashFunc, guestCumulative)
			require.NoError(t, err, "guest tx %d", i)
			guestCumulative = res.CumulativeGas

			require.Equal(t, receipt.Status == block.ReceiptStatusFailed, res.Failed, "status of tx %d", i)
			require.Equal(t, receipt.CumulativeGasUsed, res.CumulativeGas, "cumulative gas after tx %d", i)
			require.Equal(t, receipt.GasUsed, res.GasUsed, "gas used by tx %d", i)
			require.Equal(t, hostPool.UsedAmsterdam(), guestPool.UsedAmsterdam(), "block gas after tx %d", i)
			require.Equal(t, hostPool.Gas(), guestPool.Gas(), "execution gas left after tx %d", i)
			if i == len(txs)-1 {
				sawStateGasHalt = res.Failed
			} else {
				require.Equal(t, i == 8 || i == 13, res.Failed, "status of tx %d (the revert and the invalid opcode fail)", i)
			}
		}
		require.NotEqual(t, hostCumulative, hostPool.UsedAmsterdam(), "test needs state gas")
		require.True(t, sawStateGasHalt, "the last transfer must halt on its state gas")
		code := guestState.GetCode(authority)
		require.Equal(t, vm2.AddressToDelegation(toggleAddr), code, "the delegation must be set")

		for _, addr := range watched {
			require.Equal(t, hostState.GetBalance(addr), guestState.GetBalance(addr), "balance of %x", addr)
			require.Equal(t, hostState.GetNonce(addr), guestState.GetNonce(addr), "nonce of %x", addr)
			require.Equal(t, hostState.GetCode(addr), guestState.GetCode(addr), "code of %x", addr)
			var hostVal, guestVal uint256.Int
			hostState.GetState(addr, &types.Hash{}, &hostVal)
			guestState.GetState(addr, &types.Hash{}, &guestVal)
			require.Equal(t, hostVal, guestVal, "slot 0 of %x", addr)
		}
		var slotValue uint256.Int
		guestState.GetState(slotAddr, &types.Hash{}, &slotValue)
		require.Equal(t, slotNumber, slotValue.Uint64(), "SLOTNUM in the guest")
		require.Equal(t, hostState.IntermediateRoot(), guestState.IntermediateRoot())
		return nil
	}))
}
