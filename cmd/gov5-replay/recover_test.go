package main

import (
	"context"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

func recoveryTestBatch(request sharedexec.CanonicalAccountsRequest) sharedexec.CanonicalAccountsResult {
	r := sharedexec.CanonicalAccountsResult{Identity: request.Identity, Epoch: request.Epoch, ManifestHash: request.ManifestHash, CanonicalHash: request.CanonicalHash, CanonicalNumber: request.CanonicalNumber}
	for _, address := range request.Addresses {
		r.Accounts = append(r.Accounts, sharedexec.CanonicalAccount{Address: address, Exists: true, Nonce: 1, Balance: "1000000000", CodeHash: crypto.Keccak256Hash(nil)})
	}
	return r
}

func TestRecoveryAccountsRequireOneCompleteCanonicalVector(t *testing.T) {
	addresses := make([]types.Address, 1001)
	for i := range addresses {
		addresses[i] = types.BytesToAddress([]byte{byte(i >> 8), byte(i), 1})
	}
	request := sharedexec.CanonicalAccountsRequest{Epoch: [16]byte{1}, CanonicalHash: types.Hash{1}, CanonicalNumber: 3}
	for _, kind := range []string{"complete", "changed-second-batch", "snapshot-conflict"} {
		calls := 0
		read := func(_ context.Context, r sharedexec.CanonicalAccountsRequest) (sharedexec.CanonicalAccountsResult, error) {
			calls++
			if r.CanonicalHash != request.CanonicalHash || r.CanonicalNumber != 3 || r.Epoch != request.Epoch || len(r.Addresses) > 1000 {
				t.Fatal("batch did not pin full snapshot")
			}
			out := recoveryTestBatch(r)
			if calls == 2 {
				if kind == "changed-second-batch" {
					out.CanonicalHash[0] ^= 1
				}
				if kind == "snapshot-conflict" {
					return out, sharedexec.ErrIngressSnapshotChanged
				}
			}
			return out, nil
		}
		got, sum, err := readRecoveryAccounts(context.Background(), request, addresses, 3, 1001, read)
		if calls != 2 {
			t.Fatalf("batch count %d", calls)
		}
		if kind == "complete" {
			if err != nil || len(got) != 1001 || sum != 1001 {
				t.Fatalf("complete vector %d %d %v", len(got), sum, err)
			}
		} else if err == nil || len(got) != 0 || sum != 0 {
			t.Fatalf("mixed/partial vector escaped %s: %d %d %v", kind, len(got), sum, err)
		}
		if kind == "snapshot-conflict" && !errors.Is(err, sharedexec.ErrIngressSnapshotChanged) {
			t.Fatal("lost bounded read retry classification")
		}
	}
}

func TestRecoveryAccountsRejectInvalidOrUnfundedSenders(t *testing.T) {
	request := sharedexec.CanonicalAccountsRequest{}
	addresses := []types.Address{{1}}
	for _, mutate := range []func(*sharedexec.CanonicalAccountsResult){
		func(r *sharedexec.CanonicalAccountsResult) { r.Accounts[0].Exists = false },
		func(r *sharedexec.CanonicalAccountsResult) { r.Accounts[0].Address = types.Address{2} },
		func(r *sharedexec.CanonicalAccountsResult) { r.Accounts[0].CodeHash = types.Hash{2} },
		func(r *sharedexec.CanonicalAccountsResult) { r.Accounts[0].Nonce = 4 },
		func(r *sharedexec.CanonicalAccountsResult) { r.Accounts[0].Balance = "1" },
		func(r *sharedexec.CanonicalAccountsResult) { r.Accounts[0].Balance = "-1" },
		func(r *sharedexec.CanonicalAccountsResult) { r.Accounts[0].Nonce = 0 },
	} {
		read := func(_ context.Context, request sharedexec.CanonicalAccountsRequest) (sharedexec.CanonicalAccountsResult, error) {
			out := recoveryTestBatch(request)
			mutate(&out)
			return out, nil
		}
		if got, sum, err := readRecoveryAccounts(context.Background(), request, addresses, 3, 1, read); err == nil || got != nil || sum != 0 {
			t.Fatalf("invalid vector accepted: %+v %d %v", got, sum, err)
		}
	}
}

func TestRecoveryNonregressionAndEmptySession(t *testing.T) {
	owner := sharedexec.StrictIngressStatus{CanonicalNumber: 254, CanonicalHash: types.Hash{2}}
	if recoveryOwnerNonregressing(sharedexec.StrictIngressStatus{CanonicalNumber: 253, CanonicalHash: types.Hash{1}}, owner) || recoveryOwnerNonregressing(sharedexec.StrictIngressStatus{CanonicalNumber: 254, CanonicalHash: types.Hash{1}}, owner) {
		t.Fatal("owner ahead of last validator observation was allowed to regress")
	}
	previous := sharedvalidator.StatusResult{CanonicalProgress: sharedvalidator.CanonicalProgress{Number: 253, BlockHash: types.Hash{1}, CommittedExecutedTransactions: 47500}}
	current := previous
	current.Number, current.BlockHash, current.CommittedExecutedTransactions = 254, types.Hash{2}, 210500
	if !recoveryNonregressing(current, previous) {
		t.Fatal("late valid commit was treated as replay")
	}
	drift := previous
	drift.CommittedExecutedTransactions++
	if recoveryNonregressing(drift, previous) {
		t.Fatal("same canonical block changed transaction count")
	}
	for _, bad := range []sharedvalidator.StatusResult{{CanonicalProgress: sharedvalidator.CanonicalProgress{Number: 252, BlockHash: types.Hash{1}, CommittedExecutedTransactions: 47500}}, {CanonicalProgress: sharedvalidator.CanonicalProgress{Number: 253, BlockHash: types.Hash{2}, CommittedExecutedTransactions: 47500}}, {CanonicalProgress: sharedvalidator.CanonicalProgress{Number: 254, BlockHash: types.Hash{2}, CommittedExecutedTransactions: 47000}}} {
		if recoveryNonregressing(bad, previous) {
			t.Fatal("regressing height/hash/count accepted")
		}
	}
	var m supplyManifest
	m.Descriptor.Identity = previous.Identity
	m.Digest = types.Hash{1}
	status := sharedexec.StrictIngressStatus{Identity: previous.Identity, Epoch: [16]byte{1}, ManifestHash: m.Digest, Reconciling: true}
	if err := recoverySession(status, status.Epoch, m); err != nil {
		t.Fatal("bootstrap gate should wait without dispatch", err)
	}
	for _, change := range []func(*sharedexec.StrictIngressStatus){func(s *sharedexec.StrictIngressStatus) { s.Epoch[0] ^= 1 }, func(s *sharedexec.StrictIngressStatus) { s.ManifestHash[0] ^= 1 }, func(s *sharedexec.StrictIngressStatus) { s.NewlyAdmitted = 1 }, func(s *sharedexec.StrictIngressStatus) { s.CryptoVerified = 1 }, func(s *sharedexec.StrictIngressStatus) { s.PendingTransactions = 1 }, func(s *sharedexec.StrictIngressStatus) { s.RetainedEncodedBytes = 1 }, func(s *sharedexec.StrictIngressStatus) { s.ReservedTransactions = 1 }, func(s *sharedexec.StrictIngressStatus) { s.ReservedEncodedBytes = 1 }} {
		bad := status
		change(&bad)
		if recoverySession(bad, status.Epoch, m) == nil {
			t.Fatal("new epoch/transaction activity was silently accepted")
		}
	}
}
