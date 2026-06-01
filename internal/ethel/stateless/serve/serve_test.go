package serve

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestByteLimiter(t *testing.T) {
	clk := time.Unix(1000, 0)
	bl := NewByteLimiter(100, 1000, 16) // 100 B/s, burst 1000
	bl.SetClock(func() time.Time { return clk })

	ip := "1.2.3.4"
	if !bl.Allow(ip, 1000) {
		t.Fatal("should allow up to burst")
	}
	if bl.Allow(ip, 1) {
		t.Fatal("should deny when bucket empty")
	}
	clk = clk.Add(5 * time.Second) // +500 bytes
	if !bl.Allow(ip, 500) {
		t.Fatal("should allow after refill")
	}
	if bl.Allow(ip, 1) {
		t.Fatal("should deny after consuming refill")
	}
	if !bl.Allow("5.6.7.8", 1000) {
		t.Fatal("a different IP has its own bucket")
	}
	if bl.Allow("9.9.9.9", 2000) {
		t.Fatal("cost over burst must be denied")
	}
}

func TestByteLimiterEviction(t *testing.T) {
	bl := NewByteLimiter(1, 1, 4) // maxIPs = 4
	for i := 0; i < 20; i++ {
		bl.Allow(fmt.Sprintf("ip-%d", i), 0)
	}
	if tr := bl.Tracked(); tr > 4 {
		t.Fatalf("tracked %d > maxIPs 4", tr)
	}
}

type stubBE struct{ head uint64 }

func (s *stubBE) Head() (uint64, types.Hash, uint64, error) {
	return s.head, types.Hash{}, s.head - s.head%5, nil
}
func (s *stubBE) HeaderRLP(n uint64) ([]byte, error) {
	if n > s.head {
		return nil, errors.New("gap")
	}
	return make([]byte, 600), nil
}
func (s *stubBE) BodyRLP(n uint64) ([]byte, error)        { return make([]byte, 200), nil }
func (s *stubBE) Witness(n uint64) ([]byte, error)        { return make([]byte, 7000), nil }
func (s *stubBE) Anchor(n uint64) ([]byte, error)         { return make([]byte, 1000), nil }
func (s *stubBE) Code(h types.Hash) ([]byte, error)       { return make([]byte, 100), nil }
func (s *stubBE) AccountProof(types.Address, []types.Hash) ([]byte, error) {
	return nil, ErrNotSupported
}

func TestServiceCaps(t *testing.T) {
	svc := NewService(&stubBE{head: 1000}, DefaultCaps(), nil)

	if _, err := svc.GetHeaders("ip", 0, 257); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("over MaxHeaders: want ErrCapExceeded, got %v", err)
	}
	hs, err := svc.GetHeaders("ip", 0, 10)
	if err != nil || len(hs) != 10 {
		t.Fatalf("within cap: err=%v len=%d", err, len(hs))
	}
	// stops at tip: from 995, count 20 -> 995..1000 = 6 headers
	hs, err = svc.GetHeaders("ip", 995, 20)
	if err != nil || len(hs) != 6 {
		t.Fatalf("tip stop: err=%v len=%d (want 6)", err, len(hs))
	}
	hashes := make([]types.Hash, 65)
	if _, err := svc.GetCode("ip", hashes); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("over MaxCodeHashes: want ErrCapExceeded, got %v", err)
	}
}

func TestServiceBandwidth(t *testing.T) {
	bl := NewByteLimiter(0, 10000, 16) // burst 10 KB, no refill in-test
	clk := time.Unix(1, 0)
	bl.SetClock(func() time.Time { return clk })
	svc := NewService(&stubBE{head: 1000}, DefaultCaps(), bl)

	if _, err := svc.GetWitness("ip", 1); err != nil { // 7000B -> 3000 left
		t.Fatalf("first witness: %v", err)
	}
	if _, err := svc.GetWitness("ip", 2); !errors.Is(err, ErrRateLimited) { // 7000 > 3000
		t.Fatalf("second witness: want ErrRateLimited, got %v", err)
	}
	if _, err := svc.GetWitness("other", 1); err != nil { // fresh bucket
		t.Fatalf("other IP witness: %v", err)
	}
}
