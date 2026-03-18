package params

import (
	"math/big"
	"testing"
)

func TestBlobScheduleDefaultsFollowForks(t *testing.T) {
	cfg := &ChainConfig{
		PragueTime: big.NewInt(10),
		OsakaTime:  big.NewInt(20),
		BPO1Time:   big.NewInt(30),
		BPO2Time:   big.NewInt(40),
		BPO3Time:   big.NewInt(50),
		BPO4Time:   big.NewInt(60),
		BPO5Time:   big.NewInt(70),
	}

	if got := cfg.BlobMaxBlobsPerBlock(0); got != 6 {
		t.Fatalf("BlobMaxBlobsPerBlock(0) = %d, want 6", got)
	}
	if got := cfg.BlobMaxBlobsPerBlock(10); got != 9 {
		t.Fatalf("BlobMaxBlobsPerBlock(10) = %d, want 9", got)
	}
	if got := cfg.BlobMaxBlobsPerBlock(20); got != 9 {
		t.Fatalf("BlobMaxBlobsPerBlock(20) = %d, want 9", got)
	}
	if got := cfg.BlobMaxBlobsPerBlock(30); got != 15 {
		t.Fatalf("BlobMaxBlobsPerBlock(30) = %d, want 15", got)
	}
	if got := cfg.BlobMaxBlobsPerBlock(40); got != 21 {
		t.Fatalf("BlobMaxBlobsPerBlock(40) = %d, want 21", got)
	}
	if got := cfg.BlobMaxBlobsPerBlock(50); got != 32 {
		t.Fatalf("BlobMaxBlobsPerBlock(50) = %d, want 32", got)
	}
	if got := cfg.BlobMaxBlobsPerBlock(60); got != 21 {
		t.Fatalf("BlobMaxBlobsPerBlock(60) = %d, want 21", got)
	}
	if got := cfg.BlobMaxBlobsPerBlock(70); got != 21 {
		t.Fatalf("BlobMaxBlobsPerBlock(70) = %d, want 21", got)
	}
}

func TestBlobScheduleOverridesAndBPOFallback(t *testing.T) {
	cfg := &ChainConfig{
		PragueTime: big.NewInt(0),
		OsakaTime:  big.NewInt(0),
		BPO1Time:   big.NewInt(15),
		BlobSchedule: &BlobSchedule{
			BPO1: &BlobConfig{
				Target:                uint64Ptr(12),
				Max:                   uint64Ptr(18),
				BaseFeeUpdateFraction: uint64Ptr(999),
			},
		},
	}

	if got := cfg.BlobTargetBlobsPerBlock(15); got != 12 {
		t.Fatalf("BlobTargetBlobsPerBlock(15) = %d, want 12", got)
	}
	if got := cfg.BlobMaxBlobsPerBlock(15); got != 18 {
		t.Fatalf("BlobMaxBlobsPerBlock(15) = %d, want 18", got)
	}
	if got := cfg.BlobBaseFeeUpdateFraction(15); got != 999 {
		t.Fatalf("BlobBaseFeeUpdateFraction(15) = %d, want 999", got)
	}
}

func TestCalcExcessBlobGasUsesActiveTarget(t *testing.T) {
	cfg := &ChainConfig{
		PragueTime: big.NewInt(0),
		OsakaTime:  big.NewInt(20),
	}

	parentExcess := uint64(0)
	parentBlobGasUsed := uint64(9 * BlobTxBlobGasPerBlob)

	if got := cfg.CalcExcessBlobGas(parentExcess, parentBlobGasUsed, 10); got != 3*BlobTxBlobGasPerBlob {
		t.Fatalf("CalcExcessBlobGas(prague) = %d, want %d", got, 3*BlobTxBlobGasPerBlob)
	}
	if got := cfg.CalcExcessBlobGas(parentExcess, parentBlobGasUsed, 20); got != 3*BlobTxBlobGasPerBlob {
		t.Fatalf("CalcExcessBlobGas(osaka) = %d, want %d", got, 3*BlobTxBlobGasPerBlob)
	}
}
