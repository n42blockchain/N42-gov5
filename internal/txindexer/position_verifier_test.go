package txindexer

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

type positionTestChain struct {
	*stubChain
	hash      types.Hash
	err       error
	reads     int
	positions int
}

func (c *positionTestChain) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	c.reads++
	return c.stubChain.GetBlockByNumber(number)
}

func (c *positionTestChain) CanonicalTxHashAtPosition(blockNum, txIndex uint64) (types.Hash, error) {
	c.positions++
	return c.hash, c.err
}

type legacyPositionTestChain struct {
	*stubChain
	reads int
}

func (c *legacyPositionTestChain) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	c.reads++
	return c.stubChain.GetBlockByNumber(number)
}

func TestTxPositionVerifierOptionalDispatch(t *testing.T) {
	want := txForBlock(7).Hash()
	chain := &positionTestChain{stubChain: &stubChain{head: 7}, hash: want}
	x := &Indexer{chain: chain}
	if ok, err := x.txPositionVerifier(7, 0, want); err != nil || !ok {
		t.Fatalf("matching position = %t, %v", ok, err)
	}
	if ok, err := x.txPositionVerifier(7, 0, types.Hash{0xff}); err != nil || ok {
		t.Fatalf("mismatching position = %t, %v", ok, err)
	}
	chain.err = errors.New("position read failed")
	if ok, err := x.txPositionVerifier(7, 0, want); ok || !errors.Is(err, chain.err) {
		t.Fatalf("failed position = %t, %v", ok, err)
	}
	if chain.positions != 3 || chain.reads != 0 {
		t.Fatalf("optional dispatch: positions=%d whole-block reads=%d", chain.positions, chain.reads)
	}
}

func TestTxPositionVerifierLegacyPath(t *testing.T) {
	chain := &legacyPositionTestChain{stubChain: &stubChain{head: 7}}
	x := &Indexer{chain: chain}
	want := txForBlock(7).Hash()
	if ok, err := x.txPositionVerifier(7, 0, want); err != nil || !ok {
		t.Fatalf("matching legacy position = %t, %v", ok, err)
	}
	if ok, err := x.txPositionVerifier(7, 0, types.Hash{0xff}); err != nil || ok {
		t.Fatalf("mismatching legacy position = %t, %v", ok, err)
	}
	if ok, err := x.txPositionVerifier(7, 1, want); ok || err == nil {
		t.Fatalf("out-of-range legacy position = %t, %v", ok, err)
	}
	if chain.reads != 3 {
		t.Fatalf("legacy whole-block reads=%d, want 3", chain.reads)
	}
}
