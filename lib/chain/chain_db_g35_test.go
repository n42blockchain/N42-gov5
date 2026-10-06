/*
   Tests for chain_db.go's kv.Getter-backed accessors using a minimal
   in-memory fake Getter.
*/

package chain

import (
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
)

// g35FakeGetter is a minimal kv.Getter backed by an in-memory map.
type g35FakeGetter struct {
	store map[string][]byte
}

func newG35FakeGetter() *g35FakeGetter { return &g35FakeGetter{store: map[string][]byte{}} }

func (g *g35FakeGetter) key(table string, k []byte) string { return table + "/" + string(k) }

func (g *g35FakeGetter) Has(table string, key []byte) (bool, error) {
	_, ok := g.store[g.key(table, key)]
	return ok, nil
}
func (g *g35FakeGetter) GetOne(table string, key []byte) ([]byte, error) {
	return g.store[g.key(table, key)], nil
}
func (g *g35FakeGetter) ForEach(table string, fromPrefix []byte, walker func(k, v []byte) error) error {
	return nil
}
func (g *g35FakeGetter) ForPrefix(table string, prefix []byte, walker func(k, v []byte) error) error {
	return nil
}
func (g *g35FakeGetter) ForAmount(table string, prefix []byte, amount uint32, walker func(k, v []byte) error) error {
	return nil
}
func (g *g35FakeGetter) put(table string, k, v []byte) { g.store[g.key(table, k)] = v }

func TestG35CanonicalHash(t *testing.T) {
	g := newG35FakeGetter()
	if h, err := CanonicalHash(g, 0, nil); err != nil || h != nil {
		t.Fatalf("expected nil hash for missing entry, got %v err=%v", h, err)
	}

	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, 0)
	g.put(kv.HeaderCanonical, key, []byte("hash0"))
	h, err := CanonicalHash(g, 0, nil)
	if err != nil {
		t.Fatalf("CanonicalHash: %v", err)
	}
	if string(h) != "hash0" {
		t.Fatalf("expected hash0, got %s", h)
	}
}

func TestG35GetConfig(t *testing.T) {
	g := newG35FakeGetter()
	cfg, err := GetConfig(g, nil)
	if err != nil {
		t.Fatalf("GetConfig with no canonical hash: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config when genesis hash is missing")
	}

	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, 0)
	g.put(kv.HeaderCanonical, key, []byte("genesis-hash"))

	// no config stored yet.
	cfg, err = GetConfig(g, nil)
	if err != nil {
		t.Fatalf("GetConfig with no stored config: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config when ConfigTable entry is missing")
	}

	data, _ := json.Marshal(&Config{ChainID: bi(7)})
	g.put(kv.ConfigTable, []byte("genesis-hash"), data)
	cfg, err = GetConfig(g, nil)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg == nil || cfg.ChainID.Int64() != 7 {
		t.Fatalf("expected decoded config with ChainID 7, got %v", cfg)
	}

	// invalid JSON must error.
	g.put(kv.ConfigTable, []byte("genesis-hash"), []byte("{not json"))
	if _, err := GetConfig(g, nil); err == nil {
		t.Fatalf("expected error decoding invalid JSON config")
	}
}

func TestG35HeadHeaderHash(t *testing.T) {
	g := newG35FakeGetter()
	h, err := HeadHeaderHash(g)
	if err != nil {
		t.Fatalf("HeadHeaderHash: %v", err)
	}
	if h != nil {
		t.Fatalf("expected nil for missing head header hash")
	}

	g.put(kv.HeadHeaderKey, []byte(kv.HeadHeaderKey), []byte("head-hash"))
	h, err = HeadHeaderHash(g)
	if err != nil {
		t.Fatalf("HeadHeaderHash: %v", err)
	}
	if string(h) != "head-hash" {
		t.Fatalf("expected head-hash, got %s", h)
	}
}

func TestG35HeaderNumber(t *testing.T) {
	g := newG35FakeGetter()
	n, err := HeaderNumber(g, []byte("missing"))
	if err != nil {
		t.Fatalf("HeaderNumber: %v", err)
	}
	if n != nil {
		t.Fatalf("expected nil for missing header number")
	}

	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, 42)
	g.put(kv.HeaderNumber, []byte("h1"), buf)
	n, err = HeaderNumber(g, []byte("h1"))
	if err != nil {
		t.Fatalf("HeaderNumber: %v", err)
	}
	if n == nil || *n != 42 {
		t.Fatalf("expected 42, got %v", n)
	}

	// wrong-length value errors.
	g.put(kv.HeaderNumber, []byte("h2"), []byte{1, 2, 3})
	if _, err := HeaderNumber(g, []byte("h2")); err == nil {
		t.Fatalf("expected error for wrong-length header number value")
	}
}

func TestG35CurrentBlockNumber(t *testing.T) {
	g := newG35FakeGetter()
	n, err := CurrentBlockNumber(g)
	if err != nil {
		t.Fatalf("CurrentBlockNumber with no head: %v", err)
	}
	if n != nil {
		t.Fatalf("expected nil current block number when head header hash missing")
	}

	g.put(kv.HeadHeaderKey, []byte(kv.HeadHeaderKey), []byte("head-hash"))
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, 99)
	g.put(kv.HeaderNumber, []byte("head-hash"), buf)

	n, err = CurrentBlockNumber(g)
	if err != nil {
		t.Fatalf("CurrentBlockNumber: %v", err)
	}
	if n == nil || *n != 99 {
		t.Fatalf("expected 99, got %v", n)
	}
}
