package sharedexec

import (
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

// Test-only exports let external integration tests exercise Replica against the
// same real Go executor fixture without adding production fixture interfaces.
type ExecutionFixtureForTest struct {
	Chain          *internal.BlockChain
	Public, Leader *hotstuff.HotStuff
	Template       *block.Block
	Recipient      types.Address
	Keys           []blscommon.SecretKey
}

func NewExecutionFixtureForTest(t *testing.T) *ExecutionFixtureForTest {
	f := new(ExecutionFixtureForTest)
	f.Chain, f.Public, f.Leader, f.Template, f.Recipient, _ = unsignedBuildFixtureWithSet(t, 7, func(keys []blscommon.SecretKey) { f.Keys = keys })
	return f
}
func StartUnixExecutionForTest(t *testing.T, s *Server) string { return startExecutionSocket(t, s) }
