package sharedexec

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
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
	f.Chain, f.Public, f.Leader, f.Template, f.Recipient, _ = unsignedBuildFixtureWithSet(t, 7, func(keys []blscommon.SecretKey) { f.Keys = keys }, func(g *conf.Genesis) { g.BaseFee = uint256.NewInt(1) })
	return f
}
func StartUnixExecutionForTest(t *testing.T, s *Server) string { return startExecutionSocket(t, s) }

// Strict fixture exports only test data for the real producer integration.
type StrictIngressFixtureForTest struct {
	ExecutionFixtureForTest
	Server   *Server
	Frames   [2]CorpusFrame
	Senders  [2]types.Address
	Manifest []byte
}

func NewStrictIngressFixtureForTest(t *testing.T) *StrictIngressFixtureForTest {
	f := newStrictIngressFixture(t, 32, 1<<20)
	return &StrictIngressFixtureForTest{ExecutionFixtureForTest: ExecutionFixtureForTest{Chain: f.chain, Public: f.chain.Engine().(*hotstuff.HotStuff), Leader: f.leader, Template: f.template, Recipient: f.recipient, Keys: f.keys}, Server: f.server, Frames: f.frames, Senders: f.from, Manifest: append([]byte(nil), f.manifest...)}
}
func StartUnixStrictIngressForTest(t *testing.T, s *Server) string {
	return startStrictIngressSocket(t, s)
}
