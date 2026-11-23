package sync

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/synclegacy"
)

func harnessBase(to uint64) []block.IBlock {
	genesis := harnessBlock([32]byte{}, 0, 0)
	return append([]block.IBlock{genesis}, harnessExtend(genesis, to, 1)...)
}

// A peer advertising an absurd height does not wedge catch-up: honest peers
// are asked first for what they claim, the liar is asked once, fails and is
// backed off, and its claim stops steering the target. An empty answer is
// not scored (an honest n42-rs server whose execution layer lags its
// advertised committed height answers empty too).
func TestCatchUpLyingPeerPenalisedAndHonestPeersServe(t *testing.T) {
	all := harnessBase(160)
	chain := newHarnessChain(all[:101]...) // head 100
	net := newHarnessNet(t, chain)
	honest := []*harnessPeer{
		net.addPeer(160, harnessHonest, all...),
		net.addPeer(160, harnessHonest, all...),
		net.addPeer(160, harnessHonest, all...),
	}
	liar := net.addPeer(1_000_160, harnessEmpty)
	net.svc.claimQuorum.Store(3)

	net.drain(5)

	if got := chain.headNumber(); got != 160 {
		t.Fatalf("head = %d, want 160", got)
	}
	if net.badResponses(liar) != 0 {
		t.Fatal("an empty answer must back the peer off, not score it")
	}
	if st := net.svc.catchUpBook.snapshot(liar.id); !time.Now().Before(st.until) {
		t.Fatal("liar is not backed off")
	}
	if liar.askedCount() != 1 {
		t.Fatalf("liar asked %d times, want 1 (backed off after the first failure)", liar.askedCount())
	}
	for _, p := range honest {
		if net.badResponses(p) != 0 {
			t.Fatal("honest peer penalised")
		}
	}
	// The liar's claim no longer drives the target.
	if h, ok := net.svc.catchUpHighestClaim(chain.CurrentBlock().Number64()); ok {
		t.Fatalf("catch-up target still follows a backed-off liar: %d", h)
	}
	// Only one honest peer was needed for the range; the others claim no more.
	// The parallel pipeline (N42_SYNC_PIPELINE=1) spreads slots over every
	// suitable peer by design, so the count is serial-path only.
	asked := 0
	for _, p := range honest {
		asked += p.askedCount()
	}
	if !PipelineOn() && asked != 1 {
		t.Fatalf("honest peers asked %d times, want 1", asked)
	}
}

// Regression for the equal-height fork recovery the BestPeers comment
// describes: this node sits on a losing same-height sibling; the lagging
// group (backed, asked first) serves only that sibling; the winning group
// claims more and must still be asked for the SAME range, so the winning
// sibling at the start height is included and the authorized switch happens.
func TestCatchUpEqualHeightForkRecovery(t *testing.T) {
	base := harnessBase(99)
	ancestor := base[len(base)-1]
	losing := harnessBlock(ancestor.Hash(), 100, 0xaa)
	winning := append([]block.IBlock{harnessBlock(ancestor.Hash(), 100, 0xbb)}, nil...)
	winning = append(winning, harnessExtend(winning[0], 190, 0xbb)...)

	chain := newHarnessChain(append(append([]block.IBlock{}, base...), losing)...)
	net := newHarnessNet(t, chain)
	lagging := append(append([]block.IBlock{}, base...), losing)
	for i := 0; i < 3; i++ {
		net.addPeer(100, harnessHonest, lagging...)
	}
	winners := append(append([]block.IBlock{}, base...), winning...)
	w1 := net.addPeer(190, harnessHonest, winners...)
	w2 := net.addPeer(190, harnessHonest, winners...)
	net.svc.claimQuorum.Store(3)
	// The lagging group is backed (a verified commit names height 100), the
	// winning group is not (only two peers claim 190 < f+1 = 3).
	net.svc.noteVerifiedCommitTarget(100)

	net.drain(5)

	head := chain.CurrentBlock()
	if head.Number64().Uint64() != 190 || head.Hash() != winning[len(winning)-1].Hash() {
		t.Fatalf("head = %d %x, want the winning branch tip 190", head.Number64().Uint64(), head.Hash())
	}
	if chain.switches == 0 {
		t.Fatal("the losing sibling was never switched away from")
	}
	if w1.askedCount()+w2.askedCount() == 0 {
		t.Fatal("winning group never asked")
	}
	for _, p := range []*harnessPeer{w1, w2} {
		for _, r := range p.ranges {
			if r[0] > 100 {
				// only acceptable after the switch already happened
				if chain.switches == 0 {
					t.Fatalf("winning peer asked from %d, above the fork height", r[0])
				}
			}
		}
	}
}

// A malformed response is scored and the round rotates to the next peer;
// the bad peer goes to the back of the next round.
func TestCatchUpRotatesAfterInvalidResponse(t *testing.T) {
	all := harnessBase(130)
	chain := newHarnessChain(all[:101]...)
	net := newHarnessNet(t, chain)
	bad := net.addPeer(130, harnessMalformed)
	good := net.addPeer(130, harnessHonest, all...)
	net.svc.claimQuorum.Store(1)

	// Put the bad peer first explicitly (catchUpTo would reorder).
	if done, _ := net.svc.catchUpRange(rangeReq(100, 31, 1), []peer.ID{bad.id, good.id}, true, 100, 130); !done {
		t.Fatal("round not done")
	}
	if got := chain.headNumber(); got != 130 {
		t.Fatalf("head = %d, want 130 after rotating past the malformed peer", got)
	}
	if net.badResponses(bad) != 1 {
		t.Fatalf("malformed peer bad responses = %d, want 1", net.badResponses(bad))
	}
	if net.badResponses(good) != 0 {
		t.Fatal("serving peer penalised")
	}
	order := net.svc.orderCatchUpPeers([]peer.ID{bad.id, good.id}, 131)
	if len(order) != 1 || order[0] != good.id {
		t.Fatalf("order = %v, want only the good peer while the bad one is backed off", order)
	}
	// With nothing else left, a backed-off peer is still used.
	if order := net.svc.orderCatchUpPeers([]peer.ID{bad.id}, 131); len(order) != 1 {
		t.Fatal("a backed-off peer must remain usable when it is the only one")
	}
}

// An empty answer for blocks beyond the peer's own claim is no evidence.
func TestCatchUpNoPenaltyBeyondClaim(t *testing.T) {
	all := harnessBase(120)
	chain := newHarnessChain(all[:101]...)
	net := newHarnessNet(t, chain)
	stale := net.addPeer(90, harnessEmpty) // stale claim below our head
	net.svc.catchUpTo(120, []peer.ID{stale.id})
	if net.badResponses(stale) != 0 {
		t.Fatal("peer penalised for blocks above its own claim")
	}
	if st := net.svc.catchUpBook.snapshot(stale.id); st.failures != 0 {
		t.Fatal("peer backed off for blocks above its own claim")
	}
}

func TestCatchUpTimeoutFromObservedLatency(t *testing.T) {
	if got := catchUpTimeout(catchUpPeerState{}, 100); got != catchUpTimeoutMax {
		t.Fatalf("no samples: %v, want %v", got, catchUpTimeoutMax)
	}
	fast := catchUpPeerState{samples: 3, perBlock: time.Millisecond}
	if got := catchUpTimeout(fast, 10); got != catchUpTimeoutMin {
		t.Fatalf("fast peer, 10 blocks: %v, want the %v floor", got, catchUpTimeoutMin)
	}
	if got := catchUpTimeout(fast, 1000); got != catchUpTimeoutBase+4*time.Second {
		t.Fatalf("fast peer, 1000 blocks: %v, want %v", got, catchUpTimeoutBase+4*time.Second)
	}
	slow := catchUpPeerState{samples: 3, perBlock: 200 * time.Millisecond}
	if got := catchUpTimeout(slow, 1024); got != catchUpTimeoutMax {
		t.Fatalf("slow peer: %v, want the %v cap", got, catchUpTimeoutMax)
	}
}

func TestCatchUpBackoffGrowsAndResets(t *testing.T) {
	var b catchUpPeerBook
	pid := peer.ID("p")
	now := time.Now()
	b.fail(pid, now)
	first := b.snapshot(pid).until.Sub(now)
	b.fail(pid, now)
	second := b.snapshot(pid).until.Sub(now)
	if first != catchUpBackoffBase || second != 2*catchUpBackoffBase {
		t.Fatalf("back-off %v then %v, want %v then %v", first, second, catchUpBackoffBase, 2*catchUpBackoffBase)
	}
	for i := 0; i < 40; i++ {
		b.fail(pid, now)
	}
	if d := b.snapshot(pid).until.Sub(now); d != catchUpBackoffMax {
		t.Fatalf("back-off %v, want the %v cap", d, catchUpBackoffMax)
	}
	b.served(pid, 10, 100*time.Millisecond)
	if st := b.snapshot(pid); st.failures != 0 || !st.until.IsZero() || st.perBlock != 10*time.Millisecond {
		t.Fatalf("after serving: %+v", st)
	}
}

// The escape hatch restores the first-five, unscored behaviour.
func TestCatchUpPeerSelectLegacySwitch(t *testing.T) {
	defer synclegacy.PeerSelect.SetForTest(true)()
	all := harnessBase(120)
	chain := newHarnessChain(all[:101]...)
	net := newHarnessNet(t, chain)
	for i := 0; i < 7; i++ {
		net.addPeer(120, harnessEmpty)
	}
	if got := len(net.svc.catchUpPeerSet()); got != 5 {
		t.Fatalf("legacy peer set = %d, want 5", got)
	}
	net.svc.catchUpTo(120, net.svc.catchUpPeerSet())
	for _, p := range net.peers {
		if net.badResponses(p) != 0 {
			t.Fatal("legacy path scored a peer")
		}
	}
}

// Refusals that are legitimate for the other clients on this wire (n42-rs
// answers "server busy" when its serve slots are full, or refuses a peer
// without a status) back the peer off but never reach the peer scorer, whose
// IsBad verdict the connection gater would enforce.
func TestCatchUpRefusalIsNotScored(t *testing.T) {
	all := harnessBase(160)
	chain := newHarnessChain(all[:101]...)
	net := newHarnessNet(t, chain)
	busy := net.addPeer(160, harnessRefuse)
	net.svc.claimQuorum.Store(1)
	net.drain(3)
	if busy.askedCount() == 0 {
		t.Fatal("busy peer never asked; the test exercises nothing")
	}
	if net.badResponses(busy) != 0 {
		t.Fatalf("a refusal was scored: %d", net.badResponses(busy))
	}
	if st := net.svc.catchUpBook.snapshot(busy.id); !time.Now().Before(st.until) {
		t.Fatal("refusing peer not backed off")
	}
	// An honest peer arriving later still closes the gap.
	net.addPeer(160, harnessHonest, all...)
	net.drain(3)
	if got := chain.headNumber(); got != 160 {
		t.Fatalf("head = %d, want 160", got)
	}
}
