package internal

import (
	"fmt"
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
	"github.com/n42blockchain/N42/internal/consensus"
	vm "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

type transferTestEngine struct {
	consensus.Engine
	author types.Address
}

func (transferTestEngine) Type() params.ConsensusType                    { return params.HotStuffConsensus }
func (e transferTestEngine) Author(block.IHeader) (types.Address, error) { return e.author, nil }

type transferTestReader struct {
	state.StateReader
	accounts map[types.Address]*account.StateAccount
}

func (r transferTestReader) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	if data := r.accounts[a]; data != nil {
		return data.SelfCopy(), nil
	}
	return nil, nil
}

func transferFixture(t *testing.T, price uint64) (*params.ChainConfig, *block.Header, transaction.Transactions, transferTestReader, []types.Address) {
	t.Helper()
	return transferFixtureN(t, price, 4)
}

func transferFixtureN(t *testing.T, price uint64, count int) (*params.ChainConfig, *block.Header, transaction.Transactions, transferTestReader, []types.Address) {
	t.Helper()
	cfg := testStateTransitionChainConfig()
	header := &block.Header{Number: uint256.NewInt(1), Time: 1, GasLimit: uint64(count) * 21000, GasUsed: uint64(count) * 21000, BaseFee: uint256.NewInt(7), Difficulty: uint256.NewInt(0), Coinbase: types.Address{18: 1}}
	reader := transferTestReader{accounts: make(map[types.Address]*account.StateAccount)}
	txs := make(transaction.Transactions, count)
	var keys []types.Address
	for lane := 0; lane < 2; lane++ {
		key, err := crypto.GenerateKey()
		require.NoError(t, err)
		from := crypto.PubkeyToAddress(key.PublicKey)
		to := types.Address{18: 2, 19: byte(lane)}
		data := account.NewAccount()
		data.Initialised = true
		data.Balance.SetUint64(1000000000)
		reader.accounts[from] = &data
		keys = append(keys, from, to)
		for n := 0; n < count/2; n++ {
			raw := transaction.NewTransaction(uint64(n), from, &to, uint256.NewInt(1), 21000, uint256.NewInt(price), nil)
			txs[2*n+lane], err = transaction.SignTx(raw, transaction.NewLondonSigner(big.NewInt(1)), key)
			require.NoError(t, err)
			txs[2*n+lane].SetFrom(from)
		}
	}
	return cfg, header, txs, reader, keys
}

func executeTransfersSerial(t *testing.T, cfg *params.ChainConfig, engine consensus.Engine, h *block.Header, txs transaction.Transactions, s *state.IntraBlockState) block.Receipts {
	t.Helper()
	gp := new(common.GasPool).AddGas(h.GasLimit)
	var gas uint64
	var receipts block.Receipts
	for i, tx := range txs {
		s.Prepare(tx.Hash(), types.Hash{}, i)
		r, _, err := ApplyTransaction(cfg, func(uint64) types.Hash { return types.Hash{} }, engine, nil, gp, s, state.NewNoopWriter(), h, tx, &gas, vm.Config{})
		require.NoError(t, err)
		receipts = append(receipts, r)
	}
	return receipts
}

func TestSpeculateTransfersMatchesSerial(t *testing.T) {
	for _, mode := range []string{"tip", "zero-tip", "collector", "aliased-collector", "prefunded"} {
		t.Run(mode, func(t *testing.T) {
			price := uint64(9)
			if mode == "zero-tip" {
				price = 7
			}
			cfg, h, txs, reader, keys := transferFixture(t, price)
			engine := transferTestEngine{author: types.Address{18: 3}}
			keys = append(keys, engine.author)
			if mode == "collector" || mode == "aliased-collector" {
				collector := types.Address{18: 4}
				if mode == "aliased-collector" {
					collector = engine.author
				}
				cfg.Eip1559FeeCollector = &collector
				cfg.Eip1559FeeCollectorTransition = big.NewInt(0)
				keys = append(keys, collector)
			}
			if mode == "prefunded" {
				data := account.NewAccount()
				data.Initialised = true
				data.Balance.SetUint64(19)
				reader.accounts[engine.author] = &data
			}
			s, serial := state.New(reader), state.New(reader)
			// Simulate a block-start credit to an endpoint. Workers must use it.
			for _, live := range []*state.IntraBlockState{s, serial} {
				live.AddBalance(keys[1], uint256.NewInt(11))
				require.NoError(t, live.FinalizeTx(cfg.RulesWithTimestamp(1, 1), state.NewNoopWriter()))
			}
			before, err := s.SnapshotAccounts(keys)
			require.NoError(t, err)
			got, err := speculateTransfers(cfg, engine, h, types.Hash{}, txs, s, 2)
			require.NoError(t, err)
			require.NotNil(t, got)
			wantReceipts := executeTransfersSerial(t, cfg, engine, h, txs, serial)
			require.Equal(t, wantReceipts, got.receipts)
			wantDirty := serial.DirtyAddresses()
			require.Len(t, got.accounts, len(wantDirty))
			want, err := serial.SnapshotAccounts(wantDirty)
			require.NoError(t, err)
			for _, a := range wantDirty {
				data, err := want.ReadAccountData(a)
				require.NoError(t, err)
				actual, ok := got.accounts[a]
				require.True(t, ok)
				require.Equal(t, data, actual)
			}
			after, err := s.SnapshotAccounts(keys)
			require.NoError(t, err)
			for _, a := range keys {
				old, err := before.ReadAccountData(a)
				require.NoError(t, err)
				now, err := after.ReadAccountData(a)
				require.NoError(t, err)
				require.Equal(t, old, now, "speculation changed original state")
			}
			merged, err := s.MergeEOAAccounts(got.base, got.accounts, cfg.RulesWithTimestamp(1, 1))
			require.NoError(t, err)
			require.True(t, merged)
			actualWrites, expectedWrites := newTransferCommitCapture(), newTransferCommitCapture()
			require.NoError(t, s.CommitBlock(cfg.RulesWithTimestamp(1, 1), actualWrites))
			require.NoError(t, serial.CommitBlock(cfg.RulesWithTimestamp(1, 1), expectedWrites))
			require.Equal(t, expectedWrites, actualWrites)
		})
	}
}

func TestSpeculateTransfersFallback(t *testing.T) {
	for _, mode := range []string{"nonce", "balance", "contract", "precompile", "fee-endpoint", "fee-overflow", "gas"} {
		t.Run(mode, func(t *testing.T) {
			cfg, h, txs, reader, keys := transferFixture(t, 9)
			engine := transferTestEngine{author: types.Address{18: 3}}
			s := state.New(reader)
			switch mode {
			case "nonce":
				s.SetNonce(keys[0], 1)
			case "balance":
				s.SetBalance(keys[0], uint256.NewInt(0))
			case "contract":
				s.SetCode(keys[1], []byte{0})
			case "precompile":
				engine.author = keys[1]
				keys[1] = types.Address{19: 4}
				txs[0] = transaction.NewTransaction(0, keys[0], &keys[1], uint256.NewInt(1), 21000, uint256.NewInt(9), nil)
			case "fee-endpoint":
				engine.author = keys[1]
			case "fee-overflow":
				s.SetBalance(engine.author, new(uint256.Int).SetAllOne())
			case "gas":
				h.GasLimit--
			}
			keys = append(keys, engine.author)
			before, err := s.SnapshotAccounts(keys)
			require.NoError(t, err)
			got, err := speculateTransfers(cfg, engine, h, types.Hash{}, txs, s, 2)
			require.NoError(t, err)
			require.Nil(t, got)
			after, err := s.SnapshotAccounts(keys)
			require.NoError(t, err)
			for _, a := range keys {
				old, _ := before.ReadAccountData(a)
				now, _ := after.ReadAccountData(a)
				require.Equal(t, old, now)
			}
		})
	}
}

func TestTransferComponentsBridge(t *testing.T) {
	a, b, c, d := types.Address{19: 1}, types.Address{19: 2}, types.Address{19: 3}, types.Address{19: 4}
	require.Equal(t, [][]int{{0, 1, 2}}, transferComponents([][2]types.Address{{a, b}, {c, d}, {b, c}}))
	require.Equal(t, [][]int{{0, 2}, {1}}, transferComponents([][2]types.Address{{a, b}, {c, d}, {b, a}}))
}

type transferAccountWrite struct {
	Address           types.Address
	Original, Current []byte
	Deleted           bool
}
type transferCommitCapture struct {
	rows  []transferAccountWrite
	wipes []types.Address
}

func newTransferCommitCapture() *transferCommitCapture { return &transferCommitCapture{} }
func encodeTransferAccount(a *account.StateAccount) []byte {
	if a == nil {
		return nil
	}
	return a.MarshalV2()
}
func (c *transferCommitCapture) UpdateAccountData(a types.Address, original, current *account.StateAccount) error {
	c.rows = append(c.rows, transferAccountWrite{a, encodeTransferAccount(original), encodeTransferAccount(current), false})
	return nil
}
func (c *transferCommitCapture) DeleteAccount(a types.Address, original *account.StateAccount) error {
	c.rows = append(c.rows, transferAccountWrite{a, encodeTransferAccount(original), nil, true})
	return nil
}
func (c *transferCommitCapture) CreateContract(a types.Address) error {
	c.wipes = append(c.wipes, a)
	return nil
}
func (*transferCommitCapture) UpdateAccountCode(types.Address, types.Hash, []byte) error {
	return fmt.Errorf("unexpected code write")
}
func (*transferCommitCapture) WriteAccountStorage(types.Address, types.Hash, uint256.Int, uint256.Int) error {
	return fmt.Errorf("unexpected storage write")
}
