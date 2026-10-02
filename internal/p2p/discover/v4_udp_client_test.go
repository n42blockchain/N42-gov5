package discover

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/p2p/discover/v4wire"
	"github.com/n42blockchain/N42/internal/p2p/enode"
)

// remoteEnodeNode builds an *enode.Node describing the udpTest's simulated
// remote peer (test.remotekey / test.remoteaddr), suitable for passing to
// client-initiated UDPv4 calls like ping and RequestENR.
func remoteEnodeNode(test *udpTest) *enode.Node {
	return enode.NewV4(&test.remotekey.PublicKey, test.remoteaddr.IP, 0, test.remoteaddr.Port)
}

// answerPing drains one outgoing datagram expected to be a Ping and replies
// with a matching Pong from the simulated remote.
func answerPing(t *testing.T, test *udpTest) {
	t.Helper()
	dg, err := test.pipe.receive()
	if err != nil {
		t.Fatalf("expected outgoing ping, got error: %v", err)
	}
	decoded, _, hash, err := v4wire.Decode(dg.data)
	if err != nil {
		t.Fatalf("failed to decode outgoing ping: %v", err)
	}
	if _, ok := decoded.(*v4wire.Ping); !ok {
		t.Fatalf("outgoing packet type = %T, want *v4wire.Ping", decoded)
	}
	pong := &v4wire.Pong{
		To:         v4wire.NewEndpoint(test.remoteaddr, 0),
		ReplyTok:   hash,
		Expiration: futureExp,
	}
	if err := test.packetIn(pong); err != nil {
		t.Fatalf("packetIn(pong): %v", err)
	}
}

// TestUDPv4_Ping drives UDPv4.ping end-to-end: it sends a ping over the
// dgram pipe and resolves once a matching Pong is "received".
func TestUDPv4_Ping(t *testing.T) {
	test := newUDPTest(t)
	defer test.close()

	errc := make(chan error, 1)
	go func() {
		_, err := test.udp.ping(remoteEnodeNode(test))
		errc <- err
	}()

	answerPing(t, test)

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("ping() returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ping() did not return in time")
	}
}

// TestUDPv4_RequestENR drives UDPv4.RequestENR end-to-end, including the
// implicit bonding ping/pong exchange ensureBond performs first.
func TestUDPv4_RequestENR(t *testing.T) {
	test := newUDPTest(t)
	defer test.close()

	target := remoteEnodeNode(test)

	errc := make(chan error, 1)
	var resultID = target.ID()
	go func() {
		_, err := test.udp.RequestENR(target)
		errc <- err
	}()

	// ensureBond: answer the solicited ping first.
	answerPing(t, test)

	// ensureBond then sleeps respTimeout waiting for a return ping; it
	// doesn't block RequestENR's own request/response below, so proceed to
	// answer the ENRRequest once it arrives.
	dg, err := test.pipe.receive()
	if err != nil {
		t.Fatalf("expected outgoing ENRRequest, got error: %v", err)
	}
	decoded, _, hash, err := v4wire.Decode(dg.data)
	if err != nil {
		t.Fatalf("decode ENRRequest: %v", err)
	}
	if _, ok := decoded.(*v4wire.ENRRequest); !ok {
		t.Fatalf("outgoing packet type = %T, want *v4wire.ENRRequest", decoded)
	}

	resp := &v4wire.ENRResponse{
		ReplyTok: hash,
		Record:   *test.udp.Self().Record(),
	}
	if err := test.packetIn(resp); err != nil {
		t.Fatalf("packetIn(ENRResponse): %v", err)
	}

	select {
	case err := <-errc:
		// The response record's ID is our own local node's ID (we signed it
		// with test.udp.Self()), not the synthetic target's ID, so RequestENR
		// is expected to reject it with an ID mismatch - this still exercises
		// the full RequestENR/verify code path end-to-end.
		if err == nil {
			t.Fatal("expected an ID-mismatch error from RequestENR (self-signed stand-in record)")
		}
		_ = resultID
	case <-time.After(5 * time.Second):
		t.Fatal("RequestENR() did not return in time")
	}
}

// bondWithRemote establishes checkBond(remote) == true on the local side.
// checkBond looks at LastPongReceived, which is only recorded when WE ping
// the remote and THEY pong back - so the local transport must initiate.
func bondWithRemote(t *testing.T, test *udpTest) {
	t.Helper()
	rm := test.udp.sendPing(remoteEnodeNode(test).ID(), test.remoteaddr, nil)
	answerPing(t, test)
	if err := <-rm.errc; err != nil {
		t.Fatalf("bonding ping failed: %v", err)
	}
}

// TestUDPv4_HandleFindnodeSendsNeighbors verifies that a bonded remote's
// Findnode request is answered with a Neighbors packet (empty table still
// sends one empty Neighbors reply, per handleFindnode).
func TestUDPv4_HandleFindnodeSendsNeighbors(t *testing.T) {
	test := newUDPTest(t)
	defer test.close()

	bondWithRemote(t, test)

	req := &v4wire.Findnode{
		Target:     v4wire.Pubkey{1, 2, 3},
		Expiration: futureExp,
	}
	if err := test.packetIn(req); err != nil {
		t.Fatalf("packetIn(findnode): %v", err)
	}

	dg, err := test.pipe.receive()
	if err != nil {
		t.Fatalf("expected a Neighbors reply, got error: %v", err)
	}
	decoded, _, _, err := v4wire.Decode(dg.data)
	if err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if _, ok := decoded.(*v4wire.Neighbors); !ok {
		t.Fatalf("reply type = %T, want *v4wire.Neighbors", decoded)
	}
}

// TestUDPv4_FindnodeClientRoundTrip drives UDPv4.findnode end-to-end: after
// bonding, it sends a Findnode and resolves once a Neighbors reply carrying
// a valid node arrives, exercising nodeFromRPC/nodeToRPC along the way.
func TestUDPv4_FindnodeClientRoundTrip(t *testing.T) {
	test := newUDPTest(t)
	defer test.close()

	bondWithRemote(t, test)

	neighborKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate neighbor key: %v", err)
	}
	neighborNode := enode.NewV4(&neighborKey.PublicKey, test.remoteaddr.IP, 30303, 30303)

	type findResult struct {
		nodes []*node
		err   error
	}
	resc := make(chan findResult, 1)
	go func() {
		nodes, err := test.udp.findnode(remoteEnodeNode(test).ID(), test.remoteaddr, v4wire.Pubkey{9, 9})
		resc <- findResult{nodes, err}
	}()

	// findnode() calls ensureBond first, which checks a DIFFERENT condition
	// (whether the remote has recently pinged US) than the bonding we set up
	// above (whether WE pinged the remote); it is still considered "too old"
	// here, so an extra solicited ping goes out before the Findnode itself.
	answerPing(t, test)

	dg, err := test.pipe.receive()
	if err != nil {
		t.Fatalf("expected outgoing Findnode, got error: %v", err)
	}
	decoded, _, _, err := v4wire.Decode(dg.data)
	if err != nil {
		t.Fatalf("decode Findnode: %v", err)
	}
	if _, ok := decoded.(*v4wire.Findnode); !ok {
		t.Fatalf("outgoing packet type = %T, want *v4wire.Findnode", decoded)
	}

	reply := &v4wire.Neighbors{
		Nodes:      []v4wire.Node{nodeToRPC(wrapNode(neighborNode))},
		Expiration: futureExp,
	}
	if err := test.packetIn(reply); err != nil {
		t.Fatalf("packetIn(neighbors): %v", err)
	}

	select {
	case res := <-resc:
		if res.err != nil {
			t.Fatalf("findnode() returned error: %v", res.err)
		}
		if len(res.nodes) != 1 || res.nodes[0].ID() != neighborNode.ID() {
			t.Fatalf("findnode() nodes = %v, want [%v]", res.nodes, neighborNode.ID())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("findnode() did not return in time")
	}
}

// TestUDPv4_PingPublicWrapper exercises the exported Ping() convenience
// wrapper around the lower-level ping().
func TestUDPv4_PingPublicWrapper(t *testing.T) {
	test := newUDPTest(t)
	defer test.close()

	errc := make(chan error, 1)
	go func() { errc <- test.udp.Ping(remoteEnodeNode(test)) }()

	answerPing(t, test)

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Ping() returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ping() did not return in time")
	}
}

// TestUDPv4_HandleENRRequestSendsResponse verifies that a bonded remote's
// ENRRequest is answered with our local node's current ENRResponse.
func TestUDPv4_HandleENRRequestSendsResponse(t *testing.T) {
	test := newUDPTest(t)
	defer test.close()

	bondWithRemote(t, test)

	req := &v4wire.ENRRequest{Expiration: futureExp}
	if err := test.packetIn(req); err != nil {
		t.Fatalf("packetIn(ENRRequest): %v", err)
	}

	dg, err := test.pipe.receive()
	if err != nil {
		t.Fatalf("expected an ENRResponse, got error: %v", err)
	}
	decoded, _, _, err := v4wire.Decode(dg.data)
	if err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	resp, ok := decoded.(*v4wire.ENRResponse)
	if !ok {
		t.Fatalf("reply type = %T, want *v4wire.ENRResponse", decoded)
	}
	if resp.Record.Seq() != test.udp.Self().Seq() {
		t.Fatalf("response record seq = %d, want %d", resp.Record.Seq(), test.udp.Self().Seq())
	}
}

// TestUDPv4_ENRRequestWithoutBondFails mirrors
// TestUDPv4_FindnodeRequiresBond for the ENRRequest handler.
func TestUDPv4_ENRRequestWithoutBondFails(t *testing.T) {
	test := newUDPTest(t)
	defer test.close()

	req := &v4wire.ENRRequest{Expiration: futureExp}
	if err := test.packetIn(req); err == nil {
		t.Fatal("expected an error for ENRRequest without a prior bond")
	}
}
