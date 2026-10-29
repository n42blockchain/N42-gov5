package devp2p

import (
	"testing"

	"github.com/n42blockchain/N42/internal/network/eth69"
)

// The devp2p server offers exactly eth/69 and eth/68. RLPx negotiates the
// highest common version, which is eth/69 with geth (69-72), reth and erigon
// (66-71); eth/70 and eth/71 are not offered until implemented to spec.
func TestServerAdvertisesEth69And68Only(t *testing.T) {
	protos, err := NewServer(ServerConfig{}, nil).ethProtocols(nil)
	if err != nil {
		t.Fatal(err)
	}
	type cap struct {
		name    string
		version uint
		length  uint64
	}
	want := []cap{{"eth", 69, 18}, {"eth", 68, 17}}
	if len(protos) != len(want) {
		t.Fatalf("got %d eth protocols, want %d", len(protos), len(want))
	}
	for i, p := range protos {
		got := cap{p.Name, p.Version, p.Length}
		if got != want[i] {
			t.Fatalf("protocol %d = %+v, want %+v", i, got, want[i])
		}
		// Codes at or above Length are rejected by the p2p layer, so no
		// advertised version can carry the eth/71 BAL messages.
		if uint64(eth69.GetBlockAccessListsMsg) < p.Length {
			t.Fatalf("eth/%d admits GetBlockAccessLists", p.Version)
		}
	}
}
