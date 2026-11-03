package mobileverify

import (
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/params"
)

// The miner runs the whole phone loop on its own against the real server:
// it waits for the epoch to commit its registration, verifies the head
// once its packet is there, counts a head that does not re-execute as
// failed, and counts the verified block certified once the window closes.
func TestMinerEndToEnd(t *testing.T) {
	const number = 88
	root := internal.ReceiptsRootFor(params.MainnetChainConfig, number, nil)
	hash, encoded := executablePacket(t, number, root)
	badHash, badEncoded := executablePacket(t, number+1, types.Hash{0xba, 0xd0})

	reg := NewRegistry()
	cache := NewPacketCache(8)
	cache.Put(hash, number, encoded)
	cache.Put(badHash, number+1, badEncoded)
	packets := NewPacketService(cache, nil, "/test")
	store := NewCertStore(8)
	windows := NewWindowManager(reg, fixedLookup(map[types.Hash]uint64{hash: number, badHash: number + 1}), 60*time.Millisecond, store)
	defer windows.Stop()
	srv := NewHTTPServer("127.0.0.1:0", reg, packets, windows, store)
	var bad atomic.Bool
	srv.SetHead(func() (uint64, types.Hash, bool) {
		if bad.Load() {
			return number + 1, badHash, true
		}
		return number, hash, true
	})
	ts := httptest.NewServer(srv.srv.Handler)
	defer ts.Close()

	k, err := evmsdk.GenerateMobileBLSKey()
	if err != nil {
		t.Fatal(err)
	}
	m, err := evmsdk.NewMobileVerifyMiner(ts.URL+"/", k.PrivHex, "mainnet")
	if err != nil {
		t.Fatal(err)
	}
	if m.PubkeyHex() != k.PubHex {
		t.Fatalf("pubkey %s, want %s", m.PubkeyHex(), k.PubHex)
	}
	if err := m.SetIntervalsMs(5, 5, 100); err != nil {
		t.Fatal(err)
	}
	type status struct {
		Running     bool    `json:"running"`
		Phase       string  `json:"phase"`
		DeviceIndex *uint32 `json:"deviceIndex"`
		Verified    int     `json:"verified"`
		Failed      int     `json:"failed"`
		Certified   int     `json:"certified"`
		LastSigners int     `json:"lastSigners"`
		LastBlock   *struct {
			BlockNumber  uint64 `json:"blockNumber"`
			ReceiptsRoot string `json:"receiptsRoot"`
		} `json:"lastBlock"`
	}
	read := func() status {
		var st status
		if err := json.Unmarshal([]byte(m.Status()), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	waitFor := func(what string, cond func(status) bool) status {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			st := read()
			if cond(st) {
				return st
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s: %s", what, m.Status())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	m.Start()
	defer m.Stop()
	waitFor("the provisional registration", func(st status) bool { return st.Phase == evmsdk.MinerPhaseRegistering })
	if st := read(); st.Verified != 0 {
		t.Fatalf("verified before the epoch committed: %+v", st)
	}
	reg.CommitEpoch()

	st := waitFor("the certificate", func(st status) bool { return st.Certified == 1 })
	if st.Verified != 1 || st.LastBlock == nil || st.LastBlock.BlockNumber != number || st.LastBlock.ReceiptsRoot != root.Hex() || st.LastSigners != 1 {
		t.Fatalf("status %s", m.Status())
	}
	var pubkey [48]byte
	pub, err := hex.DecodeString(k.PubHex)
	if err != nil {
		t.Fatal(err)
	}
	copy(pubkey[:], pub)
	if idx, ok := reg.Lookup(pubkey); !ok || st.DeviceIndex == nil || *st.DeviceIndex != idx {
		t.Fatalf("device index %v, registry %d (%v)", st.DeviceIndex, idx, ok)
	}

	bad.Store(true)
	st = waitFor("the bad head to fail", func(st status) bool { return st.Failed == 1 })
	if st.Verified != 1 || st.Phase != evmsdk.MinerPhaseMining {
		t.Fatalf("status %s", m.Status())
	}

	m.Stop()
	if st := read(); st.Running || st.Phase != evmsdk.MinerPhaseIdle {
		t.Fatalf("after Stop: %s", m.Status())
	}
}

// A miner started with the wrong chain name re-executes the head under
// that chain's rules, which yields another receipts root: every attempt
// counts as failed, nothing reaches the server, and the miner keeps
// running instead of giving up.
func TestMinerOnTheWrongChainFailsWithoutSubmitting(t *testing.T) {
	const number = 88
	root := internal.ReceiptsRootFor(params.MainnetChainConfig, number, nil)
	hash, encoded := executablePacket(t, number, root)
	next, nextEncoded := executablePacket(t, number+1, root)

	reg := NewRegistry()
	cache := NewPacketCache(8)
	cache.Put(hash, number, encoded)
	cache.Put(next, number+1, nextEncoded)
	store := NewCertStore(8)
	windows := NewWindowManager(reg, fixedLookup(map[types.Hash]uint64{hash: number, next: number + 1}), 20*time.Millisecond, store)
	defer windows.Stop()
	srv := NewHTTPServer("127.0.0.1:0", reg, NewPacketService(cache, nil, "/test"), windows, store)
	var advanced atomic.Bool
	srv.SetHead(func() (uint64, types.Hash, bool) {
		if advanced.Load() {
			return number + 1, next, true
		}
		return number, hash, true
	})
	ts := httptest.NewServer(srv.srv.Handler)
	defer ts.Close()

	k, err := evmsdk.GenerateMobileBLSKey()
	if err != nil {
		t.Fatal(err)
	}
	m, err := evmsdk.NewMobileVerifyMiner(ts.URL, k.PrivHex, "eth-sepolia")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetIntervalsMs(5, 5, 50); err != nil {
		t.Fatal(err)
	}
	type status struct {
		Running   bool   `json:"running"`
		Phase     string `json:"phase"`
		Verified  int    `json:"verified"`
		Failed    int    `json:"failed"`
		LastError string `json:"lastError"`
	}
	waitFor := func(what string, cond func(status) bool) status {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var st status
			if err := json.Unmarshal([]byte(m.Status()), &st); err != nil {
				t.Fatal(err)
			}
			if cond(st) {
				return st
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s: %s", what, m.Status())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	m.Start()
	defer m.Stop()
	waitFor("the provisional registration", func(st status) bool { return st.Phase == evmsdk.MinerPhaseRegistering })
	reg.CommitEpoch()
	waitFor("the first head to fail", func(st status) bool { return st.Failed == 1 })
	advanced.Store(true)
	st := waitFor("the next head to fail", func(st status) bool { return st.Failed == 2 })
	if st.Verified != 0 || !st.Running || st.Phase != evmsdk.MinerPhaseMining || st.LastError == "" {
		t.Fatalf("status %s", m.Status())
	}
	// Both windows have closed by now; neither block got a certificate.
	time.Sleep(100 * time.Millisecond)
	if store.Len() != 0 {
		t.Fatalf("cert blocks = %d, want none", store.Len())
	}
}
