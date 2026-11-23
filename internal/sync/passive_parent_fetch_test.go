package sync

import (
	"testing"
	"time"

	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/synclegacy"
)

// A pushed block names its own parent; that parent must not gain
// branch-switch authority. Here the parent is a stored sibling of the
// applied head: the legacy path re-applies it with authority (switching the
// applied branch on a peer's word), the hardened path leaves the switch to a
// consensus-named fetch.
func passiveParentFixture(t *testing.T) (*harnessNet, *harnessChain, comtypes.Hash, func()) {
	t.Helper()
	base := harnessBase(99)
	ancestor := base[len(base)-1]
	applied := harnessBlock(ancestor.Hash(), 100, 0xaa)
	sibling := harnessBlock(ancestor.Hash(), 100, 0xbb)
	chain := newHarnessChain(append(base, applied)...)
	chain.store(sibling)
	net := newHarnessNet(t, chain)
	child := harnessBlock(sibling.Hash(), 101, 0xbb)
	return net, chain, sibling.Hash(), func() { net.svc.fetchMissingParent(child.ParentHash()) }
}

func TestPushedParentFetchHasNoSwitchAuthority(t *testing.T) {
	_, chain, _, fetch := passiveParentFixture(t)
	fetch()
	time.Sleep(100 * time.Millisecond)
	if chain.switchCount() != 0 {
		t.Fatal("a parent named only by a pushed block switched the applied branch")
	}
}

func TestPushedParentFetchLegacySwitches(t *testing.T) {
	defer synclegacy.FutureAuthority.SetForTest(true)()
	_, chain, _, fetch := passiveParentFixture(t)
	fetch()
	deadline := time.Now().Add(2 * time.Second)
	for chain.switchCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if chain.switchCount() == 0 {
		t.Fatal("legacy path was expected to switch with authority (the hole this fix closes)")
	}
}

// The consensus-named fetch keeps its authority.
func TestConsensusNamedFetchKeepsSwitchAuthority(t *testing.T) {
	net, chain, sibling, _ := passiveParentFixture(t)
	net.svc.FetchBlockByHash(sibling)
	deadline := time.Now().Add(2 * time.Second)
	for chain.switchCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if chain.switchCount() == 0 {
		t.Fatal("a consensus-named fetch must still switch the applied branch")
	}
}
