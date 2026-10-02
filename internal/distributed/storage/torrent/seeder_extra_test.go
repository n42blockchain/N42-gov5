package torrent

import (
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

// TestSeederSeed_AlreadySeedingReturnsExistingEntry covers Seed's
// already-seeding fast path, which must return the existing entry without
// ever calling bridge.SeedContent (and therefore without needing a real
// torrent client).
func TestSeederSeed_AlreadySeedingReturnsExistingEntry(t *testing.T) {
	bridge := &Bridge{mappings: make(map[[32]byte]*HashMapping)}
	seeder := NewSeeder(bridge)

	var ch [32]byte
	ch[0] = 0xCC
	existing := &SeedEntry{
		ContentHash:  ch,
		InfoHash:     metainfo.Hash{0x01},
		Name:         "already-seeded.bin",
		Size:         42,
		SeedingSince: time.Now(),
	}
	seeder.seeds[ch] = existing

	got, err := seeder.Seed(ch, "irrelevant-new-name.bin", []byte("data"), 0)
	if err != nil {
		t.Fatalf("Seed() error = %v, want nil for already-seeding content", err)
	}
	if got != existing {
		t.Fatalf("Seed() returned %+v, want the existing entry %+v", got, existing)
	}
	if seeder.SeedCount() != 1 {
		t.Fatalf("SeedCount() = %d, want 1 (no duplicate added)", seeder.SeedCount())
	}
}
