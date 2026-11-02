package publicrpc

import (
	"strings"
	"testing"

	"github.com/n42blockchain/N42/internal/ethel/rpccaps"
)

// A body above the peek limit is documented to pass through ungated, so the
// downstream rpc server, whose own limit is larger, must see all of it.
func TestCapabilityGateForwardsOversizedBodyIntact(t *testing.T) {
	next := &recordingHandler{}
	gate := newCapabilityGate(rpccaps.M1, nil, next)

	payload := `{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":["` +
		strings.Repeat("a", maxGateBody) + `"]}`
	rec := post(gate, payload)

	if !next.called {
		t.Fatalf("oversized body did not reach the rpc server: %s", rec.Body.String())
	}
	if len(next.body) != len(payload) {
		t.Fatalf("rpc server received %d bytes of a %d byte body", len(next.body), len(payload))
	}
	if next.body != payload {
		t.Fatal("rpc server received a different body")
	}
}

// The largest body the gate can still parse keeps being gated.
func TestCapabilityGatePeeksBodyAtTheLimit(t *testing.T) {
	const prefix = `{"jsonrpc":"2.0","id":1,"method":"eth_getTransactionByHash","params":["0x00"],"pad":"`
	const suffix = `"}`
	next := &recordingHandler{}
	gate := newCapabilityGate(rpccaps.M1, nil, next)

	post(gate, prefix+strings.Repeat("a", maxGateBody-len(prefix)-len(suffix))+suffix)

	if next.called {
		t.Fatal("request at the peek limit bypassed the capability gate")
	}
}
