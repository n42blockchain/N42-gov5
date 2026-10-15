package sharedexec

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

func TestUnsignedBuildViewTwoRootMatchesRealHotStuffImport(t *testing.T) {
	buildAndImportViewTwo(t)
}

func buildAndImportViewTwo(t *testing.T) (*Server, *Client, *block.Block) {
	t.Helper()
	ctx := context.Background()
	f := NewExecutionFixtureForTest(t)
	cfg := f.Chain.Config()
	leader := hotstuff.New(cfg.HotStuff, cfg)
	defer leader.Close()
	set := f.Public.Engine().ValidatorSetForView(2)
	index := hotstuff.LeaderForView(2, set)
	address, err := set.GetAddress(index)
	if err != nil {
		t.Fatal(err)
	}
	leader.Authorize(address, f.Keys[int(index)])
	if err := leader.InitEngineFromConfig(); err != nil {
		t.Fatal(err)
	}
	leader.Engine().RestoreState(2, hotstuff.GenesisQC(), hotstuff.GenesisQC(), 0)
	header := block.CopyHeader(f.Template.Header().(*block.Header))
	if err := leader.Prepare(f.Chain, header); err != nil {
		t.Fatal(err)
	}
	s, err := New(f.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(startExecutionSocket(t, s), s.Identity())
	t.Cleanup(c.Close)
	built, err := c.Build(ctx, buildRequest(t, s.Identity(), block.NewBlock(header, f.Template.Transactions()).(*block.Block)))
	if err != nil {
		t.Fatal(err)
	}
	var candidate block.Block
	if err := candidate.Unmarshal(built.BlockRLP); err != nil {
		t.Fatal(err)
	}
	sealed := make(chan block.IBlock, 1)
	if err := leader.Seal(f.Chain, &candidate, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	b := (<-sealed).(*block.Block)
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := c.Import(ctx, ImportRequest{Identity: s.Identity(), BlockHash: b.Hash(), BlockRLP: raw}); err != nil || !result.Durable || result.Result != built.Result {
		t.Fatalf("actual future-view block import: %+v %v", result, err)
	}
	return s, c, b
}
