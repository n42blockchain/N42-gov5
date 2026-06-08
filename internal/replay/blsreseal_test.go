package replay

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestBLSResealRoundTrip(t *testing.T) {
	var seed [32]byte
	seed[0] = 0x42
	r, err := NewBLSResealer(BLSResealConfig{
		Seed:          seed,
		PoolSize:      2048,
		CommitteeSize: 128,
		RampBlocks:    1000,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Active pool ramps: block 0 -> committee size, block >= ramp -> pool size.
	if got := r.ActivePool(0); got != 128 {
		t.Fatalf("ActivePool(0) = %d, want 128", got)
	}
	if got := r.ActivePool(2000); got != 2048 {
		t.Fatalf("ActivePool(2000) = %d, want 2048", got)
	}
	if got := r.ActivePool(500); got <= 128 || got >= 2048 {
		t.Fatalf("ActivePool(500) = %d, want in (128,2048)", got)
	}

	for _, blockNum := range []uint64{1, 500, 5000} {
		bh := types.BytesToHash([]byte{byte(blockNum), 0xab, 0xcd})
		rr := types.BytesToHash([]byte{0x11, byte(blockNum)})
		ce := r.BuildCE(blockNum, bh, rr)
		if ce.SignerCount != 128 {
			t.Fatalf("block %d SignerCount=%d want 128", blockNum, ce.SignerCount)
		}
		if ce.BlockHash != bh {
			t.Fatalf("block %d BlockHash mismatch", blockNum)
		}
		ok, err := r.VerifyCE(ce)
		if err != nil || !ok {
			t.Fatalf("block %d VerifyCE failed: ok=%v err=%v", blockNum, ok, err)
		}
		// Committee determinism: identical (view, blockHash) -> identical members.
		ce2 := r.BuildCE(blockNum, bh, rr)
		if ce2.AggregateSignature != ce.AggregateSignature {
			t.Fatalf("block %d aggregate non-deterministic", blockNum)
		}
		// Tamper: wrong block hash must fail verification.
		bad := *ce
		bad.BlockHash = types.BytesToHash([]byte{0xff})
		if ok, _ := r.VerifyCE(&bad); ok {
			t.Fatalf("block %d tampered CE verified true", blockNum)
		}
	}
}
