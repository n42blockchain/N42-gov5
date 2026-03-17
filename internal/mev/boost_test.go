package mev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/holiman/uint256"
	"go.uber.org/zap"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

type minerBackendStub struct {
	pending block.IBlock
	value   *uint256.Int
}

func (m *minerBackendStub) PendingBlock() block.IBlock {
	return m.pending
}

func (m *minerBackendStub) SealedBlockValue() *uint256.Int {
	return m.value
}

func TestRelayClientGetHeaderChoosesHighestBid(t *testing.T) {
	lowRelay := newBidRelay(t, 10)
	defer lowRelay.Close()
	highRelay := newBidRelay(t, 25)
	defer highRelay.Close()

	client := NewRelayClient([]string{lowRelay.URL, highRelay.URL}, zap.NewNop())
	client.validatorPK = []byte{0x01, 0x02}

	bid, err := client.GetHeader(context.Background(), 12, types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), []byte{0x01, 0x02})
	if err != nil {
		t.Fatalf("GetHeader() error = %v", err)
	}
	if bid == nil {
		t.Fatal("GetHeader() bid = nil, want highest bid")
	}
	if bid.Value.Cmp(uint256.NewInt(25)) != 0 {
		t.Fatalf("bid.Value = %s, want 25", bid.Value.String())
	}
	if bid.RelayURL != highRelay.URL {
		t.Fatalf("bid.RelayURL = %q, want %q", bid.RelayURL, highRelay.URL)
	}
}

func TestRelayClientGetHeaderReturnsNoValidBidOnBadJSON(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{"))
	}))
	defer relay.Close()

	client := NewRelayClient([]string{relay.URL}, zap.NewNop())
	_, err := client.GetHeader(context.Background(), 1, types.Hash{}, []byte{0x01})
	if !errors.Is(err, ErrNoValidBid) {
		t.Fatalf("GetHeader() error = %v, want ErrNoValidBid", err)
	}
}

func TestAuctionCompareAndSelectHonorsThresholds(t *testing.T) {
	auction := NewAuction(nil, uint256.NewInt(10), zap.NewNop())

	if auction.CompareAndSelect(uint256.NewInt(5), &BuilderBid{Value: uint256.NewInt(9)}) {
		t.Fatal("CompareAndSelect() = true, want false for bid below minimum")
	}
	if auction.CompareAndSelect(uint256.NewInt(15), &BuilderBid{Value: uint256.NewInt(15)}) {
		t.Fatal("CompareAndSelect() = true, want false when relay bid does not beat local value and threshold together")
	}
	if !auction.CompareAndSelect(uint256.NewInt(12), &BuilderBid{Value: uint256.NewInt(20)}) {
		t.Fatal("CompareAndSelect() = false, want true for higher profitable relay bid")
	}
}

func TestAuctionRunAuctionFallsBackWithoutValidatorPubkey(t *testing.T) {
	relay := NewRelayClient([]string{"http://relay.invalid"}, zap.NewNop())
	auction := NewAuction(relay, nil, zap.NewNop())

	useRelay, bid := auction.RunAuction(context.Background(), 3, types.Hash{}, uint256.NewInt(1))
	if useRelay || bid != nil {
		t.Fatalf("RunAuction() = (%v, %v), want local fallback", useRelay, bid)
	}
}

func TestBuilderAPIProposeBlindedBlockReturnsBid(t *testing.T) {
	relay := newBidRelay(t, 17)
	defer relay.Close()

	client := NewRelayClient([]string{relay.URL}, zap.NewNop())
	client.validatorPK = []byte{0x01}

	api := NewBuilderAPI(client)
	bid, err := api.ProposeBlindedBlock(context.Background(), 5, types.Hash{})
	if err != nil {
		t.Fatalf("ProposeBlindedBlock() error = %v", err)
	}
	if bid == nil || bid.Value.Cmp(uint256.NewInt(17)) != 0 {
		t.Fatalf("bid = %#v, want value 17", bid)
	}
}

func TestBuilderAPILocalBlockValueDefaultsToZero(t *testing.T) {
	api := NewBuilderAPI(nil)
	if got := api.LocalBlockValue(); got.Sign() != 0 {
		t.Fatalf("LocalBlockValue() = %s, want 0", got.String())
	}

	api.SetMiner(&minerBackendStub{value: uint256.NewInt(33)})
	if got := api.LocalBlockValue(); got.Cmp(uint256.NewInt(33)) != 0 {
		t.Fatalf("LocalBlockValue() = %s, want 33", got.String())
	}
}

func newBidRelay(t *testing.T, value uint64) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("relay method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(&BuilderBid{
			Header: &block.Header{},
			Value:  uint256.NewInt(value),
			Pubkey: []byte{0x01},
		}); err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
	}))
}
