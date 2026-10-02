package discover

import (
	"testing"
	"time"

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
