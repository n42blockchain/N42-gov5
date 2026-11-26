package api

import (
	"testing"

	"github.com/n42blockchain/N42/common/block"
)

// ExplorerBenchChain exposes the heavy explorer fixture to the external
// test package (tracers live in a package that imports this one).
func ExplorerBenchChain(tb testing.TB) (*API, []*block.Block) {
	fx := explorerGetFixture(tb)
	return explorerColdAPI(tb, fx), fx.Blocks
}
