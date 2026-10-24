package main

import (
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

const boundedQ21HistoricalKind = "gov5-q21-diagnostic-2m4-window-physical-reopen-sol-accepted-v1"

// A previously accepted bounded run is a historical starting point, never a
// new throughput claim. The existing boundedHistoricalBaseline still verifies
// the physical reopen and current owner arming checks the live retained state.
type boundedQ21HistoricalOuter struct {
	Kind           string                         `json:"kind"`
	Passed         bool                           `json:"passed"`
	Error          string                         `json:"error"`
	DiagnosticOnly *bool                          `json:"diagnostic_only"`
	TPSMeasured    *bool                          `json:"tps_measured"`
	CapacityProven *bool                          `json:"capacity_5m_proven"`
	TryID          string                         `json:"try_id"`
	BaselineSHA256 string                         `json:"baseline_sha256"`
	Recovery       string                         `json:"recovery_result"`
	RecoveryHash   string                         `json:"recovery_sha256"`
	NonceSum       uint64                         `json:"canonical_nonce_sum"`
	VectorSHA256   types.Hash                     `json:"account_vector_sha256"`
	Canonical      resumeReportAnchor             `json:"canonical"`
	OwnerFinal     sharedexec.StrictIngressStatus `json:"owner_final"`
	Live           struct {
		Owner        sharedexec.StrictIngressStatus `json:"owner"`
		Validators   []sharedvalidator.StatusResult `json:"validators"`
		Accounts     []sharedexec.CanonicalAccount  `json:"accounts"`
		NonceSum     uint64                         `json:"canonical_nonce_sum"`
		VectorSHA256 types.Hash                     `json:"account_vector_sha256"`
		Roots        *recoveryRootEvidence          `json:"canonical_roots"`
	} `json:"live_quiescent_snapshot"`
	Preproof  boundedQ21HistoricalProof `json:"preproof"`
	Postproof boundedQ21HistoricalProof `json:"postproof"`
}

type boundedQ21HistoricalProof struct {
	Kind           string                               `json:"kind"`
	Passed         bool                                 `json:"passed"`
	TryID          string                               `json:"try_id"`
	BaselineSHA256 string                               `json:"baseline_sha256"`
	Lifecycle      map[string]boundedQ21LifecycleRecord `json:"lifecycle"`
}

type boundedQ21LifecycleRecord struct {
	Processes    []boundedHistoricalProcess `json:"processes"`
	CleanupClean *bool                      `json:"cleanup_clean"`
}

func decodeBoundedQ21HistoricalOuter(inputs resumePlanInputs, m supplyManifest, recovery strictRecoveryResult, raw []byte) (boundedHistoricalProjection, error) {
	fail := func() (boundedHistoricalProjection, error) {
		return boundedHistoricalProjection{}, errors.New("accepted Q21 historical proof differs")
	}
	var outer boundedQ21HistoricalOuter
	if err := resumeDecodeJSON(raw, &outer, false); err != nil {
		return boundedHistoricalProjection{}, err
	}
	if outer.Kind != boundedQ21HistoricalKind || !outer.Passed || outer.Error != "" || outer.DiagnosticOnly == nil || !*outer.DiagnosticOnly || outer.TPSMeasured == nil || !*outer.TPSMeasured || outer.CapacityProven == nil || *outer.CapacityProven || outer.Recovery != inputs.Recovery.Path || outer.TryID == "" {
		return fail()
	}
	recoveryHash, err := resumeHashText(outer.RecoveryHash)
	if err != nil || recoveryHash != inputs.Recovery.SHA256 {
		return fail()
	}
	baselineHash, err := resumeHashText(outer.BaselineSHA256)
	if err != nil || baselineHash == (types.Hash{}) {
		return fail()
	}
	for i, proof := range []boundedQ21HistoricalProof{outer.Preproof, outer.Postproof} {
		kind := "same-claim-bounded-window-preproof"
		if i == 1 {
			kind = "same-claim-bounded-window-postproof"
		}
		if proof.Kind != kind || !proof.Passed || proof.TryID != outer.TryID || proof.BaselineSHA256 != outer.BaselineSHA256 {
			return fail()
		}
	}
	epoch := outer.OwnerFinal.Epoch
	if epoch == ([16]byte{}) || epoch == recovery.OwnerFinal.Epoch || !boundedHistoricalOwnerBinding(outer.OwnerFinal, epoch, m) || !strictQueueEmpty(outer.OwnerFinal) || outer.OwnerFinal.CanonicalNumber != outer.Canonical.Number || outer.OwnerFinal.CanonicalHash != outer.Canonical.Hash || !reflect.DeepEqual(outer.Live.Owner, outer.OwnerFinal) {
		return fail()
	}
	if outer.NonceSum == 0 || outer.NonceSum != recovery.NonceSum || outer.VectorSHA256 != recovery.VectorSHA256 || outer.Live.NonceSum != outer.NonceSum || outer.Live.VectorSHA256 != outer.VectorSHA256 || !reflect.DeepEqual(outer.Live.Accounts, recovery.Accounts) || !reflect.DeepEqual(outer.Live.Roots, recovery.Roots) || outer.Live.Roots == nil || outer.Live.Roots.CanonicalNumber != outer.Canonical.Number || outer.Live.Roots.CanonicalHash != outer.Canonical.Hash || outer.Live.Roots.Roots.Root != outer.Canonical.Root || outer.Live.Roots.Roots.AppliedRoot != outer.Canonical.Root || !outer.Live.Roots.FinalRead || outer.Live.Roots.AccountBatches != uint64((len(recovery.Accounts)+999)/1000) {
		return fail()
	}
	if len(outer.Live.Validators) != 7 {
		return fail()
	}
	for _, validator := range outer.Live.Validators {
		if strictValidatorBinding(validator, epoch, m) != nil || !validator.ProducerRunning || !validator.RecoveryOnly || !validator.DrainAcknowledged || validator.QueuedTransactions != 0 || validator.Number != outer.Canonical.Number || validator.BlockHash != outer.Canonical.Hash || validator.CommittedExecutedTransactions != outer.NonceSum {
			return fail()
		}
	}
	if !validBoundedQ21Lifecycle(outer.Postproof.Lifecycle) {
		return fail()
	}
	return boundedHistoricalProjection{outer.NonceSum, outer.VectorSHA256, outer.Canonical}, nil
}

func validBoundedQ21Lifecycle(parts map[string]boundedQ21LifecycleRecord) bool {
	all, ok := parts["all-process-lifecycle.json"]
	if !ok || all.CleanupClean == nil || !*all.CleanupClean || len(all.Processes) != 18 || len(parts) != 3 {
		return false
	}
	names := []string{"bounded-owner", "bounded-window"}
	for i := 0; i < 7; i++ {
		names = append(names, fmt.Sprintf("produce-v%d", i))
	}
	names = append(names, "reopen-owner")
	for i := 0; i < 7; i++ {
		names = append(names, fmt.Sprintf("reopen-v%d", i))
	}
	names = append(names, "physical-reopen-audit")
	seen := make(map[int]bool, 18)
	for i, process := range all.Processes {
		if process.Name != names[i] || process.PID <= 0 || process.PGID != process.PID || seen[process.PID] || process.ReturnCode == nil || *process.ReturnCode != 0 || process.ForcedKill == nil || *process.ForcedKill || math.IsNaN(process.Started) || math.IsInf(process.Started, 0) || math.IsNaN(process.Stopped) || math.IsInf(process.Stopped, 0) || process.Started <= 0 || process.Stopped < process.Started {
			return false
		}
		seen[process.PID] = true
	}
	for _, i := range []int{1, 2, 3, 4, 5, 6, 7, 8} {
		if all.Processes[i].Started < all.Processes[0].Started || all.Processes[i].Stopped > all.Processes[0].Stopped || all.Processes[i].Stopped > all.Processes[9].Started {
			return false
		}
	}
	if all.Processes[0].Stopped > all.Processes[9].Started {
		return false
	}
	for i := 10; i <= 16; i++ {
		if all.Processes[i].Started < all.Processes[9].Started || all.Processes[i].Started > all.Processes[17].Started || all.Processes[i].Stopped < all.Processes[17].Stopped || all.Processes[i].Stopped > all.Processes[9].Stopped {
			return false
		}
	}
	if all.Processes[17].Started < all.Processes[9].Started || all.Processes[17].Stopped > all.Processes[9].Stopped {
		return false
	}
	for name, subset := range map[string][]boundedHistoricalProcess{
		"production-process-lifecycle.json": append([]boundedHistoricalProcess{all.Processes[0]}, all.Processes[2:9]...),
		"reopen-process-lifecycle.json":     all.Processes[9:17],
	} {
		part, ok := parts[name]
		if !ok || part.CleanupClean == nil || !*part.CleanupClean || !reflect.DeepEqual(part.Processes, subset) {
			return false
		}
	}
	return true
}
