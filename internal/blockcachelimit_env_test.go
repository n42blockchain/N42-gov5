package internal

import (
	"os"
	"testing"
)

func TestBlockCacheLimitEnv(t *testing.T) {
	if got := envInt("N42_BLOCK_CACHE_BLOCKS_SMOKE", 512); got != 512 {
		t.Fatalf("unset: got %d", got)
	}
	os.Setenv("N42_BLOCK_CACHE_BLOCKS_SMOKE", "4")
	if got := envInt("N42_BLOCK_CACHE_BLOCKS_SMOKE", 512); got != 4 {
		t.Fatalf("set to 4: got %d", got)
	}
	os.Setenv("N42_BLOCK_CACHE_BLOCKS_SMOKE", "0")
	if got := envInt("N42_BLOCK_CACHE_BLOCKS_SMOKE", 512); got != 512 {
		t.Fatalf("zero must fall back: got %d", got)
	}
	os.Setenv("N42_BLOCK_CACHE_BLOCKS_SMOKE", "nonsense")
	if got := envInt("N42_BLOCK_CACHE_BLOCKS_SMOKE", 512); got != 512 {
		t.Fatalf("garbage must fall back: got %d", got)
	}
	os.Unsetenv("N42_BLOCK_CACHE_BLOCKS_SMOKE")
}
