// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package training

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// mockGovernance implements DatasetGovernance for testing.
type mockGovernance struct {
	approved map[types.Hash]bool
}

func (m *mockGovernance) IsApproved(id types.Hash) bool {
	return m.approved[id]
}

// testHashes returns deterministic hashes for testing.
func testHashes() (modelHash, configHash, initWeights, finalWeights types.Hash, datasets []types.Hash) {
	modelHash = types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	configHash = types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	initWeights = types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	finalWeights = types.HexToHash("0x4444444444444444444444444444444444444444444444444444444444444444")
	datasets = []types.Hash{
		types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		types.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
	}
	return
}

// makeTrace creates a TrainingTrace with the specified number of epochs.
func makeTrace(recordID, modelHash types.Hash, epochCount int) *TrainingTrace {
	traces := make([]EpochTrace, epochCount)
	for i := 0; i < epochCount; i++ {
		traces[i] = EpochTrace{
			EpochIndex:        i,
			InputDataHash:     types.HexToHash("0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"),
			WeightsBeforeHash: types.HexToHash("0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"),
			WeightsAfterHash:  types.HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
			LossHash:          types.HexToHash("0x5555555555555555555555555555555555555555555555555555555555555555"),
			GradientHash:      types.HexToHash("0x6666666666666666666666666666666666666666666666666666666666666666"),
		}
	}
	return &TrainingTrace{
		RecordID:    recordID,
		ModelHash:   modelHash,
		EpochTraces: traces,
		StartTime:   time.Now(),
		EndTime:     time.Now(),
	}
}

func TestTrainingProver_RegisterTraining(t *testing.T) {
	modelHash, configHash, initWeights, finalWeights, datasets := testHashes()
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	gov := &mockGovernance{approved: map[types.Hash]bool{
		datasets[0]: true,
		datasets[1]: true,
	}}
	prover := NewTrainingProver(gov)

	epochCount := 5
	recordID, err := prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, epochCount, trainer)
	if err != nil {
		t.Fatalf("RegisterTraining failed: %v", err)
	}
	if recordID == (types.Hash{}) {
		t.Fatal("expected non-zero record ID")
	}

	record, found := prover.GetRecord(recordID)
	if !found {
		t.Fatal("record not found after registration")
	}
	if record.ModelHash != modelHash {
		t.Errorf("ModelHash = %s, want %s", record.ModelHash.Hex(), modelHash.Hex())
	}
	if record.ConfigHash != configHash {
		t.Errorf("ConfigHash = %s, want %s", record.ConfigHash.Hex(), configHash.Hex())
	}
	if record.InitWeightsHash != initWeights {
		t.Errorf("InitWeightsHash = %s, want %s", record.InitWeightsHash.Hex(), initWeights.Hex())
	}
	if record.FinalWeightsHash != finalWeights {
		t.Errorf("FinalWeightsHash = %s, want %s", record.FinalWeightsHash.Hex(), finalWeights.Hex())
	}
	if record.EpochCount != epochCount {
		t.Errorf("EpochCount = %d, want %d", record.EpochCount, epochCount)
	}
	if record.Trainer != trainer {
		t.Errorf("Trainer = %s, want %s", record.Trainer.Hex(), trainer.Hex())
	}
	if record.Status != TrainingPending {
		t.Errorf("Status = %s, want pending", record.Status)
	}
	if len(record.DatasetHashes) != len(datasets) {
		t.Errorf("DatasetHashes length = %d, want %d", len(record.DatasetHashes), len(datasets))
	}
	if prover.RecordCount() != 1 {
		t.Errorf("RecordCount = %d, want 1", prover.RecordCount())
	}
}

func TestTrainingProver_DuplicateRejection(t *testing.T) {
	modelHash, configHash, initWeights, finalWeights, datasets := testHashes()
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	gov := &mockGovernance{approved: map[types.Hash]bool{
		datasets[0]: true,
		datasets[1]: true,
	}}
	prover := NewTrainingProver(gov)

	_, err := prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, 5, trainer)
	if err != nil {
		t.Fatalf("first registration failed: %v", err)
	}

	// Register same training again — should fail with ErrRecordExists.
	_, err = prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, 5, trainer)
	if err == nil {
		t.Fatal("expected error on duplicate registration")
	}
	if !errors.Is(err, ErrRecordExists) {
		t.Errorf("expected ErrRecordExists, got: %v", err)
	}
}

func TestTrainingProver_UnapprovedDataset(t *testing.T) {
	modelHash, configHash, initWeights, finalWeights, datasets := testHashes()
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	// Only approve the first dataset, second is not approved.
	gov := &mockGovernance{approved: map[types.Hash]bool{
		datasets[0]: true,
		datasets[1]: false,
	}}
	prover := NewTrainingProver(gov)

	_, err := prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, 5, trainer)
	if err == nil {
		t.Fatal("expected error for unapproved dataset")
	}
	if !errors.Is(err, ErrDatasetNotApproved) {
		t.Errorf("expected ErrDatasetNotApproved, got: %v", err)
	}
}

func TestTrainingProver_NilGovernance(t *testing.T) {
	modelHash, configHash, initWeights, finalWeights, datasets := testHashes()
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	// nil governance — should skip approval check and succeed.
	prover := NewTrainingProver(nil)

	recordID, err := prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, 5, trainer)
	if err != nil {
		t.Fatalf("expected nil governance to skip check, got: %v", err)
	}
	if recordID == (types.Hash{}) {
		t.Fatal("expected non-zero record ID")
	}

	record, found := prover.GetRecord(recordID)
	if !found {
		t.Fatal("record not found after registration with nil governance")
	}
	if record.Status != TrainingPending {
		t.Errorf("Status = %s, want pending", record.Status)
	}
}

func TestTrainingProver_ProveTraining(t *testing.T) {
	modelHash, configHash, initWeights, finalWeights, datasets := testHashes()
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	epochCount := 3

	prover := NewTrainingProver(nil)

	recordID, err := prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, epochCount, trainer)
	if err != nil {
		t.Fatalf("RegisterTraining failed: %v", err)
	}

	trace := makeTrace(recordID, modelHash, epochCount)

	proof, err := prover.ProveTraining(recordID, trace)
	if err != nil {
		t.Fatalf("ProveTraining failed: %v", err)
	}

	// Verify proof structure.
	if proof == nil {
		t.Fatal("proof is nil")
	}
	if len(proof.PublicInputs) != 160 {
		t.Errorf("PublicInputs length = %d, want 160", len(proof.PublicInputs))
	}
	if len(proof.ProofData) == 0 {
		t.Error("ProofData is empty")
	}
	if proof.RecordID != recordID {
		t.Errorf("proof RecordID = %s, want %s", proof.RecordID.Hex(), recordID.Hex())
	}
	if proof.ModelHash != modelHash {
		t.Errorf("proof ModelHash = %s, want %s", proof.ModelHash.Hex(), modelHash.Hex())
	}
	if proof.ProofType != TrainingProofTypeGroth16 {
		t.Errorf("ProofType = %s, want %s", proof.ProofType, TrainingProofTypeGroth16)
	}

	// Verify the record status transitioned to Verified.
	record, _ := prover.GetRecord(recordID)
	if record.Status != TrainingVerified {
		t.Errorf("record Status = %s, want verified", record.Status)
	}

	// Verify proof is stored.
	storedProof, found := prover.GetProof(recordID)
	if !found {
		t.Fatal("proof not found after ProveTraining")
	}
	if storedProof.RecordID != proof.RecordID {
		t.Error("stored proof does not match returned proof")
	}
}

func TestTrainingProver_ProveTraining_WrongEpochs(t *testing.T) {
	modelHash, configHash, initWeights, finalWeights, datasets := testHashes()
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	epochCount := 5

	prover := NewTrainingProver(nil)

	recordID, err := prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, epochCount, trainer)
	if err != nil {
		t.Fatalf("RegisterTraining failed: %v", err)
	}

	// Create trace with wrong epoch count (3 instead of 5).
	trace := makeTrace(recordID, modelHash, 3)

	_, err = prover.ProveTraining(recordID, trace)
	if err == nil {
		t.Fatal("expected error for mismatched epoch count")
	}
	if !errors.Is(err, ErrTraceIncomplete) {
		t.Errorf("expected ErrTraceIncomplete, got: %v", err)
	}
}

func TestTrainingProver_VerifyTrainingProof(t *testing.T) {
	modelHash, configHash, initWeights, finalWeights, datasets := testHashes()
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	epochCount := 3

	prover := NewTrainingProver(nil)

	recordID, err := prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, epochCount, trainer)
	if err != nil {
		t.Fatalf("RegisterTraining failed: %v", err)
	}

	trace := makeTrace(recordID, modelHash, epochCount)
	proof, err := prover.ProveTraining(recordID, trace)
	if err != nil {
		t.Fatalf("ProveTraining failed: %v", err)
	}

	valid, err := prover.VerifyTrainingProof(proof)
	if err != nil {
		t.Fatalf("VerifyTrainingProof failed: %v", err)
	}
	if !valid {
		t.Error("expected proof to be valid")
	}
}

func TestTrainingProver_VerifyInvalidProof(t *testing.T) {
	prover := NewTrainingProver(nil)

	// Test nil proof.
	_, err := prover.VerifyTrainingProof(nil)
	if !errors.Is(err, ErrProofNil) {
		t.Errorf("nil proof: expected ErrProofNil, got: %v", err)
	}

	// Test empty proof data.
	_, err = prover.VerifyTrainingProof(&TrainingProof{
		ProofData:    nil,
		PublicInputs: make([]byte, 160),
	})
	if !errors.Is(err, ErrProofDataEmpty) {
		t.Errorf("empty proof data: expected ErrProofDataEmpty, got: %v", err)
	}

	// Test wrong public inputs length.
	_, err = prover.VerifyTrainingProof(&TrainingProof{
		ProofData:    []byte{0x01, 0x02, 0x03},
		PublicInputs: make([]byte, 100), // wrong length, should be 160
	})
	if !errors.Is(err, ErrPublicInputsLength) {
		t.Errorf("wrong public inputs length: expected ErrPublicInputsLength, got: %v", err)
	}
}

func TestTrainingVerifier_Verify(t *testing.T) {
	modelHash, configHash, initWeights, finalWeights, datasets := testHashes()
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	epochCount := 3

	prover := NewTrainingProver(nil)
	verifier := NewTrainingVerifier()

	recordID, err := prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, epochCount, trainer)
	if err != nil {
		t.Fatalf("RegisterTraining failed: %v", err)
	}

	trace := makeTrace(recordID, modelHash, epochCount)
	proof, err := prover.ProveTraining(recordID, trace)
	if err != nil {
		t.Fatalf("ProveTraining failed: %v", err)
	}

	valid, err := verifier.VerifyTrainingProof(proof)
	if err != nil {
		t.Fatalf("TrainingVerifier.VerifyTrainingProof failed: %v", err)
	}
	if !valid {
		t.Error("expected proof to be valid via TrainingVerifier")
	}
}

func TestTrainingVerifier_Stats(t *testing.T) {
	modelHash, configHash, initWeights, finalWeights, datasets := testHashes()
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	epochCount := 3

	prover := NewTrainingProver(nil)
	verifier := NewTrainingVerifier()

	recordID, err := prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, epochCount, trainer)
	if err != nil {
		t.Fatalf("RegisterTraining failed: %v", err)
	}

	trace := makeTrace(recordID, modelHash, epochCount)
	proof, err := prover.ProveTraining(recordID, trace)
	if err != nil {
		t.Fatalf("ProveTraining failed: %v", err)
	}

	// Verify a valid proof twice.
	for i := 0; i < 2; i++ {
		valid, vErr := verifier.VerifyTrainingProof(proof)
		if vErr != nil || !valid {
			t.Fatalf("verification %d failed: valid=%v, err=%v", i, valid, vErr)
		}
	}

	// Fail with nil proof.
	verifier.VerifyTrainingProof(nil)

	// Fail with empty proof data.
	verifier.VerifyTrainingProof(&TrainingProof{
		ProofData:    nil,
		PublicInputs: make([]byte, 160),
	})

	// Fail with wrong public inputs length.
	verifier.VerifyTrainingProof(&TrainingProof{
		ProofData:    []byte{0x01},
		PublicInputs: make([]byte, 10),
	})

	verified, failed := verifier.Stats()
	if verified != 2 {
		t.Errorf("verified = %d, want 2", verified)
	}
	if failed != 3 {
		t.Errorf("failed = %d, want 3", failed)
	}
}

func TestTrainingProver_RecordLifecycle(t *testing.T) {
	modelHash, configHash, initWeights, finalWeights, datasets := testHashes()
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	epochCount := 4

	gov := &mockGovernance{approved: map[types.Hash]bool{
		datasets[0]: true,
		datasets[1]: true,
	}}
	prover := NewTrainingProver(gov)
	verifier := NewTrainingVerifier()

	// Phase 1: Register.
	recordID, err := prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, epochCount, trainer)
	if err != nil {
		t.Fatalf("RegisterTraining failed: %v", err)
	}

	record, _ := prover.GetRecord(recordID)
	if record.Status != TrainingPending {
		t.Fatalf("after register: Status = %s, want pending", record.Status)
	}

	// Phase 2: Prove.
	trace := makeTrace(recordID, modelHash, epochCount)
	proof, err := prover.ProveTraining(recordID, trace)
	if err != nil {
		t.Fatalf("ProveTraining failed: %v", err)
	}

	record, _ = prover.GetRecord(recordID)
	if record.Status != TrainingVerified {
		t.Fatalf("after prove: Status = %s, want verified", record.Status)
	}

	// Phase 3: Verify via prover.
	valid, err := prover.VerifyTrainingProof(proof)
	if err != nil {
		t.Fatalf("prover.VerifyTrainingProof failed: %v", err)
	}
	if !valid {
		t.Error("prover verification: expected valid proof")
	}

	// Phase 4: Verify via verifier.
	valid, err = verifier.VerifyTrainingProof(proof)
	if err != nil {
		t.Fatalf("verifier.VerifyTrainingProof failed: %v", err)
	}
	if !valid {
		t.Error("verifier verification: expected valid proof")
	}

	// Phase 5: Verify stored proof matches.
	storedProof, found := prover.GetProof(recordID)
	if !found {
		t.Fatal("stored proof not found")
	}
	if storedProof.RecordID != recordID {
		t.Error("stored proof RecordID mismatch")
	}
	if len(storedProof.ProofData) != len(proof.ProofData) {
		t.Error("stored proof data length mismatch")
	}

	vCount, fCount := verifier.Stats()
	if vCount != 1 {
		t.Errorf("verifier verified count = %d, want 1", vCount)
	}
	if fCount != 0 {
		t.Errorf("verifier failed count = %d, want 0", fCount)
	}
}

// TestTrainingProver_EmptyDatasets verifies that registering with an empty
// dataset slice returns ErrNoDatasets.
func TestTrainingProver_EmptyDatasets(t *testing.T) {
	prover := NewTrainingProver(nil)
	modelHash, configHash, initWeights, finalWeights, _ := testHashes()
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	_, err := prover.RegisterTraining(modelHash, nil, configHash, initWeights, finalWeights, 5, trainer)
	if !errors.Is(err, ErrNoDatasets) {
		t.Fatalf("nil datasets: expected ErrNoDatasets, got: %v", err)
	}

	_, err = prover.RegisterTraining(modelHash, []types.Hash{}, configHash, initWeights, finalWeights, 5, trainer)
	if !errors.Is(err, ErrNoDatasets) {
		t.Fatalf("empty datasets: expected ErrNoDatasets, got: %v", err)
	}
}

// TestTrainingProver_NilTrace verifies that ProveTraining with a nil trace
// returns ErrNilTrace.
func TestTrainingProver_NilTrace(t *testing.T) {
	modelHash, configHash, initWeights, finalWeights, datasets := testHashes()
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	prover := NewTrainingProver(nil)
	recordID, err := prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, 3, trainer)
	if err != nil {
		t.Fatalf("RegisterTraining failed: %v", err)
	}

	_, err = prover.ProveTraining(recordID, nil)
	if !errors.Is(err, ErrNilTrace) {
		t.Fatalf("nil trace: expected ErrNilTrace, got: %v", err)
	}

	// Ensure the record is still in Pending status after the nil trace error.
	record, _ := prover.GetRecord(recordID)
	if record.Status != TrainingPending {
		t.Errorf("Status after nil trace: got %s, want pending", record.Status)
	}
}

// TestTrainingProver_ProveAlreadyVerified verifies that calling ProveTraining
// on an already-verified record returns ErrRecordNotPending.
func TestTrainingProver_ProveAlreadyVerified(t *testing.T) {
	modelHash, configHash, initWeights, finalWeights, datasets := testHashes()
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	epochCount := 3

	prover := NewTrainingProver(nil)
	recordID, err := prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, epochCount, trainer)
	if err != nil {
		t.Fatalf("RegisterTraining failed: %v", err)
	}

	// First prove should succeed.
	trace := makeTrace(recordID, modelHash, epochCount)
	_, err = prover.ProveTraining(recordID, trace)
	if err != nil {
		t.Fatalf("first ProveTraining failed: %v", err)
	}

	// Second prove should fail because the record is now Verified.
	trace2 := makeTrace(recordID, modelHash, epochCount)
	_, err = prover.ProveTraining(recordID, trace2)
	if !errors.Is(err, ErrRecordNotPending) {
		t.Fatalf("second ProveTraining: expected ErrRecordNotPending, got: %v", err)
	}
}

// TestTrainingProver_EpochCountExceeded verifies that registering with
// epochCount > 100000 returns ErrEpochCountExceeded.
func TestTrainingProver_EpochCountExceeded(t *testing.T) {
	modelHash, configHash, initWeights, finalWeights, datasets := testHashes()
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	prover := NewTrainingProver(nil)

	_, err := prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, 100001, trainer)
	if !errors.Is(err, ErrEpochCountExceeded) {
		t.Fatalf("epochCount=100001: expected ErrEpochCountExceeded, got: %v", err)
	}

	// Exactly 100000 should succeed.
	_, err = prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, 100000, trainer)
	if err != nil {
		t.Fatalf("epochCount=100000: unexpected error: %v", err)
	}
}

// TestTrainingProver_ConcurrentRegistration verifies that concurrent
// registrations from multiple goroutines are race-free and all unique
// registrations are recorded.
func TestTrainingProver_ConcurrentRegistration(t *testing.T) {
	prover := NewTrainingProver(nil)
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	const numRegistrations = 10
	var wg sync.WaitGroup
	errs := make([]error, numRegistrations)
	recordIDs := make([]types.Hash, numRegistrations)

	for i := 0; i < numRegistrations; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Each goroutine uses a unique model hash to produce a unique record ID.
			modelHash := types.BytesToHash([]byte{byte(idx), 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31})
			dataset := types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			configHash := types.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
			initW := types.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
			finalW := types.HexToHash("0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
			recordIDs[idx], errs[idx] = prover.RegisterTraining(modelHash, []types.Hash{dataset}, configHash, initW, finalW, 5, trainer)
		}(i)
	}
	wg.Wait()

	successCount := 0
	for i, err := range errs {
		if err != nil {
			t.Errorf("RegisterTraining[%d]: unexpected error: %v", i, err)
		} else {
			successCount++
		}
	}

	if prover.RecordCount() != successCount {
		t.Errorf("RecordCount = %d, want %d", prover.RecordCount(), successCount)
	}
}

// TestTrainingProver_DatasetHashesCopied verifies that RegisterTraining copies
// the dataset hashes slice, so mutating the original does not affect the stored
// record.
func TestTrainingProver_DatasetHashesCopied(t *testing.T) {
	prover := NewTrainingProver(nil)
	trainer := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	modelHash, configHash, initWeights, finalWeights, _ := testHashes()

	originalHash := types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	mutatedHash := types.HexToHash("0x9999999999999999999999999999999999999999999999999999999999999999")

	datasets := []types.Hash{originalHash}
	recordID, err := prover.RegisterTraining(modelHash, datasets, configHash, initWeights, finalWeights, 5, trainer)
	if err != nil {
		t.Fatalf("RegisterTraining failed: %v", err)
	}

	// Mutate the original slice.
	datasets[0] = mutatedHash

	// The stored record should still have the original hash.
	record, found := prover.GetRecord(recordID)
	if !found {
		t.Fatal("record not found")
	}
	if len(record.DatasetHashes) != 1 {
		t.Fatalf("DatasetHashes length = %d, want 1", len(record.DatasetHashes))
	}
	if record.DatasetHashes[0] != originalHash {
		t.Errorf("DatasetHashes[0] = %s, want %s (mutation leaked)", record.DatasetHashes[0].Hex(), originalHash.Hex())
	}
}
