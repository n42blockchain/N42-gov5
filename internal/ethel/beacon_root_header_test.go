package ethel

import (
	"context"
	"strings"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// cancunMainnetHeader is a post-merge mainnet header between Cancun and Prague.
func cancunMainnetHeader(root *types.Hash) *block.Header {
	return &block.Header{
		Number:           uint256.NewInt(19_500_000),
		GasLimit:         30_000_000,
		Difficulty:       uint256.NewInt(0),
		Time:             1_710_338_135 + 12,
		BaseFee:          uint256.NewInt(7),
		ParentBeaconRoot: root,
	}
}

// geth rejects a Cancun header without parentBeaconBlockRoot at header
// verification (consensus/beacon: "header is missing beaconRoot").
func TestEthReplayEngineVerifyHeaderRejectsMissingBeaconRoot(t *testing.T) {
	engine := NewEthReplayEngine(params.EthereumMainnetChainConfig)
	root := types.HexToHash("0x01")

	if err := engine.VerifyHeader(nil, cancunMainnetHeader(nil), false); err == nil || !strings.Contains(err.Error(), "beacon") {
		t.Fatalf("VerifyHeader(Cancun, no root) = %v, want missing beacon root error", err)
	}
	if err := engine.VerifyHeader(nil, cancunMainnetHeader(&root), false); err != nil {
		t.Fatalf("VerifyHeader(Cancun, root) = %v, want nil", err)
	}
	preCancun := &block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(1), Time: 1_438_269_988}
	if err := engine.VerifyHeader(nil, preCancun, false); err != nil {
		t.Fatalf("VerifyHeader(pre-Cancun, no root) = %v, want nil", err)
	}

	_, results := engine.VerifyHeaders(nil, []block.IHeader{cancunMainnetHeader(&root), cancunMainnetHeader(nil)}, nil)
	if err := <-results; err != nil {
		t.Fatalf("VerifyHeaders[0] = %v, want nil", err)
	}
	if err := <-results; err == nil {
		t.Fatal("VerifyHeaders[1] (no root) = nil, want error")
	}
}

// The eth-el import paths (devp2p, batch replay, witness replay) execute
// through ProcessBlock: a Cancun block without a beacon root must be an
// error, not a silently skipped EIP-4788 call.
func TestProcessBlockRejectsMissingBeaconRootAfterCancun(t *testing.T) {
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	blockHashFunc := func(uint64) types.Hash { return types.Hash{} }

	ibs := state.New(state.NewPlainStateReader(tx))
	_, err = ProcessBlock(chainCfg, engine, cancunMainnetHeader(nil), []*transaction.Transaction{}, nil, nil, ibs, blockHashFunc, nil)
	if err == nil || !strings.Contains(err.Error(), "beacon") {
		t.Fatalf("ProcessBlock(Cancun, no root) = %v, want missing beacon root error", err)
	}

	root := types.HexToHash("0x01")
	ibs = state.New(state.NewPlainStateReader(tx))
	if _, err := ProcessBlock(chainCfg, engine, cancunMainnetHeader(&root), []*transaction.Transaction{}, nil, nil, ibs, blockHashFunc, nil); err != nil {
		t.Fatalf("ProcessBlock(Cancun, root) = %v, want nil", err)
	}
}
