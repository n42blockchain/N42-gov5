package torrent

import (
	"testing"
)

// mockSeeder creates a Seeder with a mock bridge for testing.
// Since Seeder.Seed calls bridge.SeedContent which needs a real client,
// we test the non-I/O methods directly.

func TestSeederLifecycle(t *testing.T) {
	bridge := &Bridge{mappings: make(map[[32]byte]*HashMapping)}
	seeder := NewSeeder(bridge)

	seeder.Start()
	defer seeder.Stop()

	if seeder.SeedCount() != 0 {
		t.Fatal("expected 0 seeds")
	}

	seeds := seeder.ListSeeds()
	if len(seeds) != 0 {
		t.Fatal("expected empty seed list")
	}

	var ch [32]byte
	if seeder.IsSeeding(ch) {
		t.Fatal("should not be seeding")
	}
}

func TestSeederUnseedNonExistent(t *testing.T) {
	bridge := &Bridge{mappings: make(map[[32]byte]*HashMapping)}
	seeder := NewSeeder(bridge)

	// Should not panic
	seeder.Unseed([32]byte{0xAA})
}

func TestSeederContextCancellation(t *testing.T) {
	bridge := &Bridge{mappings: make(map[[32]byte]*HashMapping)}
	seeder := NewSeeder(bridge)

	seeder.Start()
	seeder.Stop()

	// After stop, context should be cancelled
	select {
	case <-seeder.ctx.Done():
		// expected
	default:
		t.Fatal("context should be done after Stop")
	}
}
