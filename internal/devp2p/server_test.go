package devp2p

import (
	"testing"

	n42types "github.com/n42blockchain/N42/common/types"
)

func TestNewServerAppliesDefaults(t *testing.T) {
	s := NewServer(ServerConfig{}, nil)
	if s.cfg.MaxPeers != 200 {
		t.Fatalf("MaxPeers = %d, want 200", s.cfg.MaxPeers)
	}
	if s.cfg.ListenAddr != ":30303" {
		t.Fatalf("ListenAddr = %q, want :30303", s.cfg.ListenAddr)
	}
}

func TestNewServerKeepsExplicitConfig(t *testing.T) {
	s := NewServer(ServerConfig{MaxPeers: 7, ListenAddr: ":1234"}, nil)
	if s.cfg.MaxPeers != 7 {
		t.Fatalf("MaxPeers = %d, want 7", s.cfg.MaxPeers)
	}
	if s.cfg.ListenAddr != ":1234" {
		t.Fatalf("ListenAddr = %q, want :1234", s.cfg.ListenAddr)
	}
}

func TestNewServerHonorsMaxPeersEnvOverride(t *testing.T) {
	t.Setenv("N42_MAX_PEERS", "55")
	s := NewServer(ServerConfig{}, nil)
	if s.cfg.MaxPeers != 55 {
		t.Fatalf("MaxPeers = %d, want 55 from env override", s.cfg.MaxPeers)
	}
}

func TestNewServerIgnoresInvalidMaxPeersEnv(t *testing.T) {
	t.Setenv("N42_MAX_PEERS", "not-a-number")
	s := NewServer(ServerConfig{}, nil)
	if s.cfg.MaxPeers != 200 {
		t.Fatalf("MaxPeers = %d, want default 200 when env is invalid", s.cfg.MaxPeers)
	}
}

func TestNewServerIgnoresNonPositiveMaxPeersEnv(t *testing.T) {
	t.Setenv("N42_MAX_PEERS", "0")
	s := NewServer(ServerConfig{}, nil)
	if s.cfg.MaxPeers != 200 {
		t.Fatalf("MaxPeers = %d, want default 200 when env is non-positive", s.cfg.MaxPeers)
	}
}

func TestServerStopIsNoopBeforeStart(t *testing.T) {
	s := NewServer(ServerConfig{}, nil)
	// Must not panic even though srv/dialMix were never initialized.
	s.Stop()
}

func TestServerSelfPeerCountNilBeforeStart(t *testing.T) {
	s := NewServer(ServerConfig{}, nil)
	if s.Self() != nil {
		t.Fatal("Self() should be nil before Start")
	}
	if s.PeerCount() != 0 {
		t.Fatal("PeerCount() should be 0 before Start")
	}
}

func TestServerAddPeerRemovePeerErrorBeforeStart(t *testing.T) {
	s := NewServer(ServerConfig{}, nil)
	if err := s.AddPeer("enode://foo"); err == nil {
		t.Fatal("expected error adding peer before server started")
	}
	if err := s.RemovePeer("enode://foo"); err == nil {
		t.Fatal("expected error removing peer before server started")
	}
}

func TestServerAddPeerNilReceiver(t *testing.T) {
	var s *Server
	if err := s.AddPeer("enode://foo"); err == nil {
		t.Fatal("expected error on nil *Server")
	}
	if err := s.RemovePeer("enode://foo"); err == nil {
		t.Fatal("expected error on nil *Server")
	}
}

func TestDialCandidatesWithoutGenesisReturnsEmptyMix(t *testing.T) {
	s := NewServer(ServerConfig{}, nil)
	it := s.dialCandidates()
	if it == nil {
		t.Fatal("dialCandidates() returned nil iterator")
	}
	if s.dialMix == nil {
		t.Fatal("dialCandidates() did not populate dialMix")
	}
	s.Stop()
}

func TestDialCandidatesWithKnownMainnetGenesisAddsDNSSource(t *testing.T) {
	// The canonical Ethereum mainnet genesis hash resolves via
	// gethparams.KnownDNSNetwork — exercises the DNS-source-added branch.
	mainnetGenesis := n42types.HexToHash("0xd4e56740f876aef8c010b86a40d5f56745a118d0906a34e69aec8c0db1cb8fa3")
	s := NewServer(ServerConfig{Genesis: mainnetGenesis}, nil)
	it := s.dialCandidates()
	if it == nil {
		t.Fatal("dialCandidates() returned nil iterator")
	}
	s.Stop()
}

func TestDialCandidatesWithExplicitDiscoveryURLs(t *testing.T) {
	s := NewServer(ServerConfig{DiscoveryURLs: []string{"not a valid enrtree url"}}, nil)
	it := s.dialCandidates()
	if it == nil {
		t.Fatal("dialCandidates() returned nil iterator even on bad URL (should fall back to table-only)")
	}
	s.Stop()
}

func TestAddTableSourcesNoopWithoutDialMixOrServer(t *testing.T) {
	s := NewServer(ServerConfig{}, nil)
	// Neither dialMix nor srv set yet; must not panic.
	s.addTableSources()

	s.dialCandidates()
	// dialMix set but srv nil; must still not panic.
	s.addTableSources()
	s.Stop()
}

