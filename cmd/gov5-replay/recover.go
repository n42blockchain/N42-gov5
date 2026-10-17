package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"path/filepath"
	"sort"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

type strictRecoveryResult struct {
	Kind            string                         `json:"kind"`
	Passed          bool                           `json:"passed"`
	Error           string                         `json:"error,omitempty"`
	Identity        sharedexec.Identity            `json:"identity"`
	ManifestHash    types.Hash                     `json:"manifest_sha256"`
	RuntimeManifest string                         `json:"runtime_manifest"`
	BaselineResult  string                         `json:"baseline_result"`
	BaselineSHA256  types.Hash                     `json:"baseline_sha256"`
	Dispatched      uint64                         `json:"dispatched_transactions"`
	Initial         []sharedvalidator.StatusResult `json:"initial"`
	Final           []sharedvalidator.StatusResult `json:"final"`
	OwnerInitial    sharedexec.StrictIngressStatus `json:"owner_initial"`
	OwnerFinal      sharedexec.StrictIngressStatus `json:"owner_final"`
	Accounts        []sharedexec.CanonicalAccount  `json:"accounts"`
	NonceSum        uint64                         `json:"canonical_nonce_sum"`
	VectorSHA256    types.Hash                     `json:"account_vector_sha256"`
	ReadOnlyRetries uint64                         `json:"readonly_snapshot_retries"`
	Roots           *recoveryRootEvidence          `json:"canonical_roots,omitempty"`
	Limitation      string                         `json:"limitation"`
}

type recoveryRootEvidence struct {
	CanonicalHash   types.Hash                `json:"canonical_hash"`
	CanonicalNumber uint64                    `json:"canonical_number"`
	Roots           sharedexec.CanonicalRoots `json:"roots"`
	AccountBatches  uint64                    `json:"account_batches"`
	FinalRead       bool                      `json:"final_read"`
}

func sameRecoveryRoots(previous, current *sharedexec.CanonicalRoots) (*sharedexec.CanonicalRoots, error) {
	if current == nil || current.Root == (types.Hash{}) || current.AppliedRoot != current.Root || (previous != nil && *previous != *current) {
		return nil, errors.New("recovery canonical roots differ across batches")
	}
	value := *current
	return &value, nil
}

func recoveryNonregressing(current, previous sharedvalidator.StatusResult) bool {
	return current.Identity == previous.Identity && current.Number >= previous.Number && current.CommittedExecutedTransactions >= previous.CommittedExecutedTransactions && (current.Number != previous.Number || (current.BlockHash == previous.BlockHash && current.CommittedExecutedTransactions == previous.CommittedExecutedTransactions))
}

func recoveryOwnerNonregressing(current, previous sharedexec.StrictIngressStatus) bool {
	return current.CanonicalNumber >= previous.CanonicalNumber && (current.CanonicalNumber != previous.CanonicalNumber || current.CanonicalHash == previous.CanonicalHash)
}

func recoverySession(status sharedexec.StrictIngressStatus, epoch [16]byte, m supplyManifest) error {
	if status.Identity != m.Descriptor.Identity || status.Epoch != epoch || status.ManifestHash != m.Digest || status.NewlyAdmitted != 0 || status.CryptoVerified != 0 || status.PendingTransactions != 0 || status.ReservedTransactions != 0 || status.RetainedEncodedBytes != 0 || status.ReservedEncodedBytes != 0 {
		return errors.New("recovery owner session changed or contains transaction activity")
	}
	return nil
}

func readRecoveryAccounts(ctx context.Context, request sharedexec.CanonicalAccountsRequest, addresses []types.Address, maxNonce, expected uint64, read func(context.Context, sharedexec.CanonicalAccountsRequest) (sharedexec.CanonicalAccountsResult, error)) ([]sharedexec.CanonicalAccount, uint64, error) {
	var accounts []sharedexec.CanonicalAccount
	var sum uint64
	for start := 0; start < len(addresses); start += 1000 {
		end := start + 1000
		if end > len(addresses) {
			end = len(addresses)
		}
		request.Addresses = addresses[start:end]
		response, err := read(ctx, request)
		if err != nil {
			return nil, 0, err
		}
		if response.Identity != request.Identity || response.Epoch != request.Epoch || response.ManifestHash != request.ManifestHash || response.CanonicalHash != request.CanonicalHash || response.CanonicalNumber != request.CanonicalNumber || len(response.Accounts) != end-start {
			return nil, 0, errors.New("recovery account batch snapshot differs")
		}
		for i, account := range response.Accounts {
			balance, ok := new(big.Int).SetString(account.Balance, 10)
			if !account.Exists || account.Address != addresses[start+i] || account.Nonce > maxNonce || account.CodeHash != crypto.Keccak256Hash(nil) || !ok || balance.Sign() <= 0 || balance.BitLen() > 256 || balance.String() != account.Balance {
				return nil, 0, errors.New("recovery requires existing funded supply EOAs within nonce range")
			}
			minimum := new(big.Int).SetUint64((maxNonce - account.Nonce) * 21001)
			if balance.Cmp(minimum) < 0 || sum > expected || account.Nonce > expected-sum {
				return nil, 0, errors.New("recovery sender balance or canonical nonce total differs")
			}
			sum += account.Nonce
		}
		accounts = append(accounts, response.Accounts...)
	}
	if len(addresses) == 0 || sum != expected {
		return nil, 0, errors.New("recovery nonce vector does not match canonical count")
	}
	return accounts, sum, nil
}

// runStrictRecovery never opens an admission transport or submits transactions.
// It audits one retained failed run after external, producer-disabled QC replay.
// This is state reconciliation evidence, not permission to replay unknown ACKs.
func runStrictRecovery(parent context.Context, root, runtimePath, attempt, baselinePath string) error {
	return runStrictRecoveryWithRoots(parent, root, runtimePath, attempt, baselinePath, false)
}

func runStrictRecoveryWithRoots(parent context.Context, root, runtimePath, attempt, baselinePath string, requireRoots bool) (returnErr error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !stringsHasPrefixPath(root, supplyRoot) {
		return errors.New("explicit isolated recovery corpus required")
	}
	if err := strictPrivatePath(root, true, false); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	m, err := loadSupplyManifest(root)
	if err != nil {
		return err
	}
	runtime, err := loadStrictRuntime(runtimePath, root, strictRuntimeBase, m)
	if err != nil {
		return err
	}
	if filepath.Base(baselinePath) != "result.json" || !stringsHasPrefixPath(baselinePath, filepath.Join(runtime.Root, "strict-attempts")) {
		return errors.New("recovery baseline must be a retained failed result in this runtime")
	}
	raw, err := strictMetadata(baselinePath, 16<<20)
	if err != nil {
		return err
	}
	var baseline strictRunResult
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&baseline); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF || baseline.Passed || baseline.Error == "" || !baseline.FullSourceIntegrityVerified || baseline.Kind != "strict-owner-queue-canonical-observation-not-capacity-promotion" || baseline.Identity != runtime.Identity || baseline.ManifestSHA256 != m.Digest.Hex() || baseline.RuntimeManifest != runtimePath || baseline.OwnerFinal.Identity != runtime.Identity || baseline.OwnerFinal.ManifestHash != m.Digest || len(baseline.Final) != 7 {
		return errors.New("recovery baseline identity/failure evidence differs")
	}
	for _, status := range baseline.Final {
		if status.Identity != runtime.Identity || status.Number == 0 || status.CommittedExecutedTransactions == 0 {
			return errors.New("recovery requires retained nonzero canonical observations")
		}
	}
	genesisRaw, err := strictMetadata(runtime.Genesis, 16<<20)
	if err != nil {
		return err
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(genesisRaw, &genesis); err != nil {
		return err
	}
	if len(genesis.Alloc) != int(m.Descriptor.SenderCount) || m.Descriptor.NonceStart != 0 {
		return errors.New("recovery source must allocate exactly the nonce-zero sender set")
	}
	addresses := make([]types.Address, 0, len(genesis.Alloc))
	for address, account := range genesis.Alloc {
		if account.Nonce != 0 || len(account.Code) != 0 || len(account.Storage) != 0 {
			return errors.New("recovery source sender is not an EOA at nonce zero")
		}
		addresses = append(addresses, address)
	}
	sort.Slice(addresses, func(i, j int) bool { return bytes.Compare(addresses[i][:], addresses[j][:]) < 0 })
	dir, err := strictAttempt(runtime.Root, attempt)
	if err != nil {
		return err
	}
	result := strictRecoveryResult{Kind: "strict-owner-queue-recovery-no-dispatch-not-tps", Identity: runtime.Identity, ManifestHash: m.Digest, RuntimeManifest: runtimePath, BaselineResult: baselinePath, BaselineSHA256: types.Hash(sha256.Sum256(raw)), Limitation: "trusted canonical nonce snapshot; old volatile/unknown ACKs are not replay-authorized"}
	defer func() {
		if returnErr != nil {
			result.Error = returnErr.Error()
		}
		returnErr = errors.Join(returnErr, exclusiveJSON(filepath.Join(dir, "recovery.json"), result))
	}()
	if err := strictPrivatePath(runtime.OwnerSocket, false, true); err != nil {
		return err
	}
	owner := sharedexec.NewClient(runtime.OwnerSocket, runtime.Identity)
	defer owner.Close()
	status, err := owner.StrictIngressStatus(ctx)
	if err != nil {
		return err
	}
	if status.Epoch == baseline.OwnerFinal.Epoch || status.Epoch == ([16]byte{}) {
		return errors.New("recovery requires a new empty owner epoch")
	}
	epoch := status.Epoch
	result.OwnerInitial = status
	if !recoveryOwnerNonregressing(status, baseline.OwnerFinal) {
		return errors.New("recovery owner canonical evidence regressed")
	}
	previousOwner := status
	clients := make([]*sharedvalidator.APIClient, 7)
	for i, validator := range runtime.Validators {
		if err := strictPrivatePath(validator.APISocket, false, true); err != nil {
			return err
		}
		clients[i] = sharedvalidator.NewAPIClient(validator.APISocket, runtime.Identity)
		defer clients[i].Close()
	}
	previous := append([]sharedvalidator.StatusResult(nil), baseline.Final...)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("bounded read-only recovery did not converge: %w", err)
		}
		statuses, err := strictStatuses(ctx, clients, m, epoch)
		if err != nil {
			return err
		}
		if result.Initial == nil {
			result.Initial = statuses
		}
		for i, s := range statuses {
			if s.ProducerRunning || !recoveryNonregressing(s, previous[i]) {
				return errors.New("recovery producer active or canonical evidence regressed")
			}
		}
		previous = statuses
		status, err = owner.StrictIngressStatus(ctx)
		if err != nil {
			return err
		}
		if err := recoverySession(status, epoch, m); err != nil {
			return err
		}
		if !recoveryOwnerNonregressing(status, previousOwner) {
			return errors.New("recovery owner canonical evidence regressed")
		}
		previousOwner = status
		result.Final, result.OwnerFinal = statuses, status
		ready := strictQueueEmpty(status)
		for _, s := range statuses {
			ready = ready && s.BlockHash == status.CanonicalHash && s.Number == status.CanonicalNumber && s.CommittedExecutedTransactions == statuses[0].CommittedExecutedTransactions
		}
		if ready {
			request := sharedexec.CanonicalAccountsRequest{Identity: runtime.Identity, Epoch: epoch, ManifestHash: m.Digest, CanonicalHash: status.CanonicalHash, CanonicalNumber: status.CanonicalNumber}
			request.IncludeRoots = requireRoots
			var roots *sharedexec.CanonicalRoots
			var batches uint64
			read := func(ctx context.Context, query sharedexec.CanonicalAccountsRequest) (sharedexec.CanonicalAccountsResult, error) {
				response, err := owner.CanonicalAccounts(ctx, query)
				if err != nil || !requireRoots {
					return response, err
				}
				roots, err = sameRecoveryRoots(roots, response.Roots)
				if err != nil {
					return sharedexec.CanonicalAccountsResult{}, err
				}
				batches++
				return response, nil
			}
			accounts, sum, err := readRecoveryAccounts(ctx, request, addresses, m.Descriptor.NoncesPerSender, statuses[0].CommittedExecutedTransactions, read)
			if err != nil && !errors.Is(err, sharedexec.ErrIngressSnapshotChanged) {
				return err
			}
			if err == nil {
				if requireRoots {
					request.Addresses = addresses[:1]
					if _, err := read(ctx, request); err != nil {
						return err
					}
				}
				finalOwner, err := owner.StrictIngressStatus(ctx)
				if err != nil {
					return err
				}
				if err := recoverySession(finalOwner, epoch, m); err != nil {
					return err
				}
				if strictQueueEmpty(finalOwner) && finalOwner.CanonicalHash == request.CanonicalHash && finalOwner.CanonicalNumber == request.CanonicalNumber {
					if requireRoots {
						finalStatuses, err := strictStatuses(ctx, clients, m, epoch)
						if err != nil {
							return err
						}
						for _, current := range finalStatuses {
							if current.ProducerRunning || current.Number != request.CanonicalNumber || current.BlockHash != request.CanonicalHash || current.CommittedExecutedTransactions != sum {
								return errors.New("recovery validators changed during final root read")
							}
						}
						result.Final = finalStatuses
						result.Roots = &recoveryRootEvidence{CanonicalHash: request.CanonicalHash, CanonicalNumber: request.CanonicalNumber, Roots: *roots, AccountBatches: batches - 1, FinalRead: true}
					}
					result.Accounts, result.NonceSum, result.OwnerFinal = accounts, sum, finalOwner
					encoded, _ := json.Marshal(accounts)
					result.VectorSHA256 = types.Hash(sha256.Sum256(encoded))
					result.Passed = true
					return nil
				}
			}
		}
		result.ReadOnlyRetries++
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
}
