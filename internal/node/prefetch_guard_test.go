package node

import (
	"strings"
	"testing"

	"github.com/n42blockchain/N42/lib/kv/layered"
)

// A nil cache is what layered.ExtractCache returns on every non-layered DB,
// which is the default. Prefetch there is pure cost: its workers race the
// executor's MDBX cursor to populate a cache that does not exist.
func TestPrefetchRefusedWithoutSharedCache(t *testing.T) {
	err := checkPrefetchHasCache(nil)
	if err == nil {
		t.Fatal("prefetch must be refused when there is no shared cache to populate")
	}
	// The message has to name the way out, or an operator hitting it at
	// startup has a refusal and no next step.
	for _, want := range []string{"layered_db.enable", "prefetch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q so the operator knows what to change; got: %v", want, err)
		}
	}
}

func TestPrefetchAllowedWithSharedCache(t *testing.T) {
	cache := layered.NewShardedCache(16, 1024)
	if cache == nil {
		t.Skip("NewShardedCache returned nil; signature changed")
	}
	if err := checkPrefetchHasCache(cache); err != nil {
		t.Fatalf("prefetch must be allowed when a shared cache exists: %v", err)
	}
}
