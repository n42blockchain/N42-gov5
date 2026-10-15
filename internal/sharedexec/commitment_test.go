package sharedexec

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

func TestImportRejectsSignedZeroExecutionCommitments(t *testing.T) {
	chain, _, leader, template, _, _ := unsignedBuildFixture(t)
	server, err := New(chain, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	result, err := server.Build(context.Background(), buildRequest(t, server.Identity(), template))
	if err != nil {
		t.Fatal(err)
	}
	var built block.Block
	if err := built.Unmarshal(result.BlockRLP); err != nil {
		t.Fatal(err)
	}
	for _, zeroRoot := range []bool{true, false} {
		header := built.Header().(*block.Header)
		if zeroRoot {
			header.Root = types.Hash{}
		} else {
			header.ReceiptHash = types.Hash{}
		}
		unsigned := built.WithSeal(header)
		sealed := make(chan block.IBlock, 1)
		if err := leader.Seal(chain, unsigned, sealed, make(chan struct{})); err != nil {
			t.Fatal(err)
		}
		b := (<-sealed).(*block.Block)
		raw, err := b.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if result, err := server.Import(context.Background(), ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw}); err == nil || result.Durable {
			t.Fatal("signed unset commitment was mistaken for verified execution")
		}
		if _, err := chain.InsertChain([]block.IBlock{b}); err == nil {
			t.Fatal("performance BC batch import bypassed commitment guard")
		}
	}
	if server.ImportCalls() != 0 || chain.CurrentBlock().Hash() != chain.GenesisBlock().Hash() {
		t.Fatal("rejected commitments touched execution/canonical")
	}
}
