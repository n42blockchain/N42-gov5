package guest

import (
	"errors"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/modules/state/witness"
)

// jmtReader reads state straight from a JMT commitment.
type jmtReader struct{ c *commitment.JMTCommitment }

func (r jmtReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	val, err := r.c.Tree().Get(commitment.AccountKeyHash(addr))
	if errors.Is(err, jmt.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	acct := new(account.StateAccount)
	return acct, commitment.DecodeAccountValue(val, acct)
}

func (r jmtReader) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	val, err := r.c.Tree().Get(commitment.StorageKeyHash(addr, *key))
	if errors.Is(err, jmt.ErrNotFound) {
		return nil, nil
	}
	return val, err
}

func (jmtReader) ReadAccountCode(types.Address, types.Hash) ([]byte, error) { return nil, nil }
func (jmtReader) ReadAccountCodeSize(types.Address, types.Hash) (int, error) {
	return 0, nil
}

// Under Glamsterdam the guest reports the EIP-8037 block gas,
// max(cumulative execution gas, cumulative state gas), which the header
// carries, not the receipts' cumulative gas: a transfer creating an
// account pays execution and state gas, of which the block counts the
// larger.
func TestExecuteAmsterdamReportsBlockGas(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)

	tree := jmt.New(jmt.NewMemStore())
	jmtCommit := commitment.NewJMTCommitment(tree)
	funded := &account.StateAccount{Initialised: true}
	funded.Balance.SetUint64(1e18)
	require.NoError(t, jmtCommit.UpdateAccount(sender, funded))
	require.NoError(t, tree.Flush())
	parentRoot := jmtCommit.Root()

	const chainID = 1337
	fork := ForkConfig{
		IsHomestead: true, IsEIP150: true, IsEIP155: true, IsEIP158: true, IsByzantium: true,
		IsConstantinople: true, IsPetersburg: true, IsIstanbul: true, IsBerlin: true, IsLondon: true,
		IsShanghai: true, IsCancun: true, IsPrague: true, IsOsaka: true, IsGlamsterdam: true,
	}
	slot := uint64(3)
	header := &block.Header{
		Number:     uint256.NewInt(1),
		GasLimit:   60_000_000,
		Time:       100,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
		Coinbase:   types.Address{0xcb},
		SlotNumber: &slot,
	}
	to := types.Address{0xf0}
	tx, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID: uint256.NewInt(chainID), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(10),
		Gas: 1_000_000, To: &to, Value: uint256.NewInt(1),
	}), transaction.LatestSignerForChainID(big.NewInt(chainID)), key)
	require.NoError(t, err)

	// The accesses the witness must cover, and the figures to expect.
	tracer := witness.NewTracingReader(jmtReader{jmtCommit})
	gp := new(common.GasPool).AddGas(header.GasLimit)
	res, err := ApplyTx(fork, chainID, header, state.New(tracer), tx, 0, gp, nil, 0)
	require.NoError(t, err)
	require.False(t, res.Failed)
	require.NotEqual(t, gp.UsedAmsterdam(), res.CumulativeGas, "test needs state gas")

	gen, err := witness.NewGenerator(jmtCommit)
	require.NoError(t, err)
	bw, err := gen.Generate(parentRoot, tracer, nil)
	require.NoError(t, err)
	witnessBytes, err := witness.EncodeBinaryWitness(bw)
	require.NoError(t, err)
	txBytes := tx.MarshalCompactStorage()
	require.NotNil(t, txBytes)

	out := Execute(&GuestInput{
		ChainID:      chainID,
		BlockHeader:  header.MarshalCompact(),
		Transactions: [][]byte{txBytes},
		Witness:      witnessBytes,
		ForkConfig:   fork,
	})
	require.True(t, out.Success, out.Error)
	require.Equal(t, gp.UsedAmsterdam(), out.GasUsed)
}
