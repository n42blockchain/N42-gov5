package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"
)

// serveRangeForTest runs one writeBodiesRange call over [start, end] on a
// chain whose committed head is head and whose canonical table also holds a
// leftover row at head+1, and returns the number of blocks streamed.
func serveRangeForTest(t *testing.T, head, start, end uint64) (int, error) {
	t.Helper()
	svc, fp := newRangeTestService(t, head)
	var blocks []*syncBlockStub
	for n := uint64(1); n <= head+1; n++ {
		blocks = append(blocks, &syncBlockStub{number: uint256.NewInt(n)})
	}
	svc.cfg.chain = newRangeChainStub(head, blocks...)
	client, server := connectedStreamServer(t, fp)
	t.Cleanup(func() { server.Close(); client.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := client.Read(buf); err != nil {
				return
			}
		}
	}()
	reply := &rangeReply{}
	err := svc.writeBodiesRange(context.Background(), uint256.NewInt(start), uint256.NewInt(end), 1, reply, server)
	return reply.blocks, err
}

// Profile 1 (standard §3): ranges are committed-only. A canonical row above
// the committed head (a leftover a commit has not truncated yet) is not
// served; the committed prefix is, and the reply ends cleanly.
func TestBodiesByRangeServesCommittedOnlyOnProfile1(t *testing.T) {
	setProfile1ForTest(t, true)
	served, err := serveRangeForTest(t, 2, 1, 3)
	if served != 2 {
		t.Fatalf("served %d blocks, want the 2 committed ones", served)
	}
	if !errors.Is(err, errRangeTruncated) {
		t.Fatalf("err = %v, want a clean truncation at the committed head", err)
	}
}

// A range that starts above the committed head serves nothing.
func TestBodiesByRangeAboveCommittedHeadServesNothingOnProfile1(t *testing.T) {
	setProfile1ForTest(t, true)
	served, err := serveRangeForTest(t, 2, 3, 3)
	if served != 0 || err == nil {
		t.Fatalf("served %d blocks, err %v; want none and a miss", served, err)
	}
}

// Legacy chains are unchanged: every stored canonical row is served.
func TestBodiesByRangeLegacyChainUnchanged(t *testing.T) {
	setProfile1ForTest(t, false)
	served, err := serveRangeForTest(t, 2, 1, 3)
	if served != 3 || err != nil {
		t.Fatalf("served %d blocks, err %v; want 3 and no error", served, err)
	}
}
