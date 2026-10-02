package node

import (
	"testing"

	"github.com/holiman/uint256"
	"errors"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func TestBlockNumberOrZero(t *testing.T) {
	require.Zero(t, blockNumberOrZero(nil))

	noNumber := block.NewBlock(&block.Header{}, nil)
	require.Zero(t, blockNumberOrZero(noNumber))

	withNumber := block.NewBlock(&block.Header{Number: uint256.NewInt(42)}, nil)
	require.Equal(t, uint64(42), blockNumberOrZero(withNumber))
}

func TestNodeHealthProviderNilGuards(t *testing.T) {
	n := &Node{}
	h := &nodeHealthProvider{node: n}

	require.Zero(t, h.CurrentBlock())
	require.Zero(t, h.HighestBlock()) // p2p nil -> falls back to CurrentBlock
	require.False(t, h.IsSyncing())   // is nil
	require.Zero(t, h.PeerCount())    // p2p nil
}

func TestNodeBlockProviderNilBlockChain(t *testing.T) {
	n := &Node{}
	b := &nodeBlockProvider{node: n}
	require.Zero(t, b.CurrentBlock())
}

func TestPeerdasBlockProviderNilBlockChain(t *testing.T) {
	n := &Node{}
	p := &peerdasBlockProvider{node: n}
	hash, num := p.CurrentBlock()
	require.Equal(t, types.Hash{}, hash)
	require.Zero(t, num)
}

func TestMcpNodeBackendNilFields(t *testing.T) {
	n := &Node{}
	b := &mcpNodeBackend{node: n}

	require.Nil(t, b.BlockChain())
	require.Nil(t, b.Database())
	require.Nil(t, b.TxPool())
	require.Zero(t, b.PeerCount()) // p2p nil

	status := b.SyncProgress()
	require.NotNil(t, status)
	require.Zero(t, status.CurrentBlock)
	require.Zero(t, status.HighestBlock)
	require.False(t, status.Syncing)
}

// fakeTxsPool is a minimal common.ITxsPool implementation for exercising
// ingestPoolAdapter's pure delegation.
type fakeTxsPool struct {
	addLocalErr                               error
	addLocalCalledWith                        *transaction.Transaction
	pending, pendingAddrs, queued, queuedAddrs int
}

func (f *fakeTxsPool) Stop() error { return nil }
func (f *fakeTxsPool) Has(types.Hash) bool { return false }
func (f *fakeTxsPool) Pending(bool) map[types.Address][]*transaction.Transaction { return nil }
func (f *fakeTxsPool) GetTransaction() ([]*transaction.Transaction, error)       { return nil, nil }
func (f *fakeTxsPool) GetTx(types.Hash) *transaction.Transaction                 { return nil }
func (f *fakeTxsPool) AddRemotes([]*transaction.Transaction) []error            { return nil }
func (f *fakeTxsPool) AddLocal(tx *transaction.Transaction) error {
	f.addLocalCalledWith = tx
	return f.addLocalErr
}
func (f *fakeTxsPool) AddLocals([]*transaction.Transaction) []error { return nil }
func (f *fakeTxsPool) Stats() (int, int, int, int) {
	return f.pending, f.pendingAddrs, f.queued, f.queuedAddrs
}
func (f *fakeTxsPool) Nonce(types.Address) uint64 { return 0 }
func (f *fakeTxsPool) Content() (map[types.Address][]*transaction.Transaction, map[types.Address][]*transaction.Transaction) {
	return nil, nil
}

func TestIngestPoolAdapterDelegates(t *testing.T) {
	fake := &fakeTxsPool{pending: 1, pendingAddrs: 2, queued: 3, queuedAddrs: 4}
	a := &ingestPoolAdapter{pool: fake}

	tx := &transaction.Transaction{}
	require.NoError(t, a.AddLocal(tx))
	require.Same(t, tx, fake.addLocalCalledWith)

	p, pa, q, qa := a.Stats()
	require.Equal(t, 1, p)
	require.Equal(t, 2, pa)
	require.Equal(t, 3, q)
	require.Equal(t, 4, qa)

	errBoom := errors.New("boom")
	fake.addLocalErr = errBoom
	require.ErrorIs(t, a.AddLocal(tx), errBoom)
}

func TestMinerAdminAdapterNilGuardOnSelfEnode(t *testing.T) {
	var a *p2pAdminAdapter
	require.Equal(t, "", a.SelfEnode())
}
