package internal

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// beaconTestChainConfig returns a Cancun chain config with the given consensus.
func beaconTestChainConfig(consensus params.ConsensusType) *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(1),
		Consensus:             consensus,
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
	}
}

func beaconTestHeader() *block.Header {
	return &block.Header{
		Number:     uint256.NewInt(1),
		Time:       1_000,
		GasLimit:   30_000_000,
		BaseFee:    uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
	}
}

// beaconRingSlots returns the EIP-4788 timestamp and root slots for ts.
func beaconRingSlots(ts uint64) (types.Hash, types.Hash) {
	var tsSlot, rootSlot types.Hash
	uint256.NewInt(ts % beaconRootsHistoryBufferLen).WriteToSlice(tsSlot[:])
	uint256.NewInt(ts%beaconRootsHistoryBufferLen + beaconRootsHistoryBufferLen).WriteToSlice(rootSlot[:])
	return tsSlot, rootSlot
}

func beaconSlot(ibs *state.IntraBlockState, slot types.Hash) uint256.Int {
	var v uint256.Int
	ibs.GetState(params.BeaconRootsAddress, &slot, &v)
	return v
}

func newBeaconTestIBS(t *testing.T) *state.IntraBlockState {
	t.Helper()
	db := memdb.NewTestDB(t)
	txDb := memdb.BeginRw(t, db)
	return state.New(state.NewPlainState(txDb, 1))
}

// On an Ethereum chain EIP-4788 is a system call into whatever code sits at
// the beacon-roots address (geth ProcessBeaconBlockRoot): the code decides
// what gets stored, the client does not write the ring buffer itself.
func TestProcessBeaconBlockRootEthChainRunsContractCode(t *testing.T) {
	t.Parallel()
	cfg := beaconTestChainConfig(params.EtHashConsensus)
	header := beaconTestHeader()
	root := types.HexToHash("0xbeef")

	ibs := newBeaconTestIBS(t)
	ibs.CreateAccount(params.BeaconRootsAddress, true)
	// PUSH1 0x2a PUSH1 0x00 SSTORE STOP: stores 42 at slot 0 and nothing else.
	ibs.SetCode(params.BeaconRootsAddress, []byte{0x60, 0x2a, 0x60, 0x00, 0x55, 0x00})

	require.NoError(t, ProcessBeaconBlockRoot(&root, cfg, ibs, header, nil))

	got := beaconSlot(ibs, types.Hash{})
	require.Equal(t, uint64(42), got.Uint64(), "contract code must run")
	tsSlot, rootSlot := beaconRingSlots(header.Time)
	tsVal, rootVal := beaconSlot(ibs, tsSlot), beaconSlot(ibs, rootSlot)
	require.True(t, tsVal.IsZero(), "ring buffer must not be written directly")
	require.True(t, rootVal.IsZero(), "ring buffer must not be written directly")
}

// With no code at the beacon-roots address the system call does nothing.
func TestProcessBeaconBlockRootEthChainNoCodeWritesNothing(t *testing.T) {
	t.Parallel()
	cfg := beaconTestChainConfig(params.EtHashConsensus)
	header := beaconTestHeader()
	root := types.HexToHash("0xbeef")
	tsSlot, rootSlot := beaconRingSlots(header.Time)

	t.Run("absent", func(t *testing.T) {
		ibs := newBeaconTestIBS(t)
		require.NoError(t, ProcessBeaconBlockRoot(&root, cfg, ibs, header, nil))
		require.False(t, ibs.Exist(params.BeaconRootsAddress))
		require.False(t, ibs.Exist(state.SystemAddress))
		tsVal, rootVal := beaconSlot(ibs, tsSlot), beaconSlot(ibs, rootSlot)
		require.True(t, tsVal.IsZero())
		require.True(t, rootVal.IsZero())
	})

	t.Run("codeless account", func(t *testing.T) {
		ibs := newBeaconTestIBS(t)
		ibs.CreateAccount(params.BeaconRootsAddress, false)
		ibs.SetNonce(params.BeaconRootsAddress, 1)
		require.NoError(t, ProcessBeaconBlockRoot(&root, cfg, ibs, header, nil))
		tsVal, rootVal := beaconSlot(ibs, tsSlot), beaconSlot(ibs, rootSlot)
		require.True(t, tsVal.IsZero())
		require.True(t, rootVal.IsZero())
		require.Equal(t, uint64(1), ibs.GetNonce(params.BeaconRootsAddress))
		require.False(t, ibs.Exist(state.SystemAddress))
	})
}

// recordingReader is an in-memory StateReader that logs every account and
// storage read in order -- the same reads the eth-el witness stream records
// (code reads are not part of that stream).
type recordingReader struct {
	accounts map[types.Address]*account.StateAccount
	codes    map[types.Hash][]byte
	log      []string
}

func (r *recordingReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	r.log = append(r.log, fmt.Sprintf("acct %x", addr))
	if a, ok := r.accounts[addr]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, nil
}

func (r *recordingReader) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	r.log = append(r.log, fmt.Sprintf("slot %x %x", addr, *key))
	return nil, nil
}

func (r *recordingReader) ReadAccountCode(_ types.Address, codeHash types.Hash) ([]byte, error) {
	return r.codes[codeHash], nil
}

func (r *recordingReader) ReadAccountCodeSize(_ types.Address, codeHash types.Hash) (int, error) {
	return len(r.codes[codeHash]), nil
}

func newCanonicalBeaconReader() *recordingReader {
	codeHash := crypto.Keccak256Hash(params.BeaconRootsCode)
	acc := account.NewAccount()
	acc.Initialised = true
	acc.Nonce = 1
	acc.CodeHash = codeHash
	return &recordingReader{
		accounts: map[types.Address]*account.StateAccount{params.BeaconRootsAddress: &acc},
		codes:    map[types.Hash][]byte{codeHash: params.BeaconRootsCode},
	}
}

// With the canonical contract the system call stores exactly what the legacy
// direct write stored, reads state in the same order (so recorded eth-el
// witness streams stay valid) and leaves the system address untouched.
func TestProcessBeaconBlockRootEthChainCanonicalContractMatchesDirectWrite(t *testing.T) {
	t.Parallel()
	header := beaconTestHeader()
	root := types.HexToHash("0x4caae98182a2860dfdb84e1796d50c4f1d73821b26d7e9b40071cb5236395096")
	tsSlot, rootSlot := beaconRingSlots(header.Time)

	run := func(consensus params.ConsensusType) (*state.IntraBlockState, []string) {
		reader := newCanonicalBeaconReader()
		ibs := state.New(reader)
		require.NoError(t, ProcessBeaconBlockRoot(&root, beaconTestChainConfig(consensus), ibs, header, nil))
		return ibs, reader.log
	}

	ethIBS, ethReads := run(params.EtHashConsensus)
	nativeIBS, nativeReads := run(params.HotStuffConsensus)

	for name, ibs := range map[string]*state.IntraBlockState{"eth": ethIBS, "native": nativeIBS} {
		tsVal, rootVal := beaconSlot(ibs, tsSlot), beaconSlot(ibs, rootSlot)
		require.Equal(t, header.Time, tsVal.Uint64(), name)
		require.Equal(t, root, types.Hash(rootVal.Bytes32()), name)
		require.Equal(t, uint64(1), ibs.GetNonce(params.BeaconRootsAddress), name)
	}
	require.Equal(t, nativeReads, ethReads, "system call must read state in the direct-write order")
	require.False(t, ethIBS.Exist(state.SystemAddress), "system address must not be touched")
	addrWarm := ethIBS.AddressInAccessList(params.BeaconRootsAddress)
	_, slotWarm := ethIBS.SlotInAccessList(params.BeaconRootsAddress, rootSlot)
	require.False(t, addrWarm || slotWarm, "system call warmth must not leak into the first transaction")
}

// N42 native chains keep the direct ring-buffer write (their state roots and
// mobile read logs were produced with it), even without contract code.
func TestProcessBeaconBlockRootNativeChainKeepsDirectWrite(t *testing.T) {
	t.Parallel()
	header := beaconTestHeader()
	root := types.HexToHash("0xbeef")
	tsSlot, rootSlot := beaconRingSlots(header.Time)

	for _, consensus := range []params.ConsensusType{params.HotStuffConsensus, params.AposConsensu} {
		ibs := newBeaconTestIBS(t)
		ibs.CreateAccount(params.BeaconRootsAddress, true)
		ibs.SetCode(params.BeaconRootsAddress, []byte{0x60, 0x2a, 0x60, 0x00, 0x55, 0x00})
		require.NoError(t, ProcessBeaconBlockRoot(&root, beaconTestChainConfig(consensus), ibs, header, nil))
		tsVal, rootVal := beaconSlot(ibs, tsSlot), beaconSlot(ibs, rootSlot)
		require.Equal(t, header.Time, tsVal.Uint64(), consensus)
		require.Equal(t, root, types.Hash(rootVal.Bytes32()), consensus)
		zero := beaconSlot(ibs, types.Hash{})
		require.True(t, zero.IsZero(), "native chains must not run the contract code")
	}
}
