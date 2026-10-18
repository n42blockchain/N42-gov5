package main

import (
	"errors"
	"fmt"
	"math"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

const boundedFiniteHistoricalKind = "gov5-finite-phase-diagnostic-physical-reopen-sol-accepted-v1"

// Only historical metadata is normalized here. Source authentication and fresh
// canonical state are independently checked by scanning and owner arming.
type boundedHistoricalProjection struct {
	NonceSum     uint64
	VectorSHA256 types.Hash
	Canonical    resumeReportAnchor
}

type boundedFiniteHistoricalOuter struct {
	Kind           string             `json:"kind"`
	Passed         bool               `json:"passed"`
	Error          string             `json:"error"`
	TryID          string             `json:"try_id"`
	BaselineSHA256 string             `json:"baseline_sha256"`
	Recovery       string             `json:"recovery_result"`
	RecoveryHash   string             `json:"recovery_sha256"`
	NonceSum       uint64             `json:"canonical_nonce_sum"`
	VectorSHA256   types.Hash         `json:"account_vector_sha256"`
	Canonical      resumeReportAnchor `json:"canonical"`
	TPSMeasured    *bool              `json:"tps_measured"`
	CapacityProven *bool              `json:"capacity_5m_proven"`
	Schedule       struct {
		New uint64 `json:"newly_admitted"`
	} `json:"schedule"`
	Journal struct {
		Frames   uint64 `json:"frames"`
		Original uint64 `json:"original"`
		New      uint64 `json:"new"`
		Skip     uint64 `json:"skip"`
	} `json:"journal_totals"`
	Preloaded sharedexec.StrictIngressStatus `json:"preloaded_owner"`
	Drain     struct {
		Passed    bool                           `json:"passed"`
		Count     uint64                         `json:"canonical_count"`
		Owner     sharedexec.StrictIngressStatus `json:"owner"`
		Reference sharedvalidator.StatusResult   `json:"reference"`
	} `json:"drain"`
	Preproof  boundedFiniteHistoricalProof `json:"preproof"`
	Postproof boundedFiniteHistoricalProof `json:"postproof"`
}

type boundedFiniteHistoricalProof struct {
	Passed         bool   `json:"passed"`
	TryID          string `json:"try_id"`
	BaselineSHA256 string `json:"baseline_sha256"`
	Lifecycle      map[string]struct {
		Processes []boundedHistoricalProcess `json:"processes"`
	} `json:"lifecycle"`
}

type boundedHistoricalProcess struct {
	Name       string  `json:"name"`
	PID        int     `json:"pid"`
	PGID       int     `json:"pgid"`
	ReturnCode *int    `json:"return_code"`
	ForcedKill *bool   `json:"forced_kill"`
	Started    float64 `json:"started_unix"`
	Stopped    float64 `json:"stopped_unix"`
}

func decodeBoundedHistoricalOuter(inputs resumePlanInputs, m supplyManifest, recovery strictRecoveryResult, raw []byte) (boundedHistoricalProjection, error) {
	fail := func() (boundedHistoricalProjection, error) {
		return boundedHistoricalProjection{}, errors.New("bounded historical outer metadata differs")
	}
	var header struct {
		Kind string `json:"kind"`
	}
	if err := resumeDecodeJSON(raw, &header, false); err != nil {
		return boundedHistoricalProjection{}, err
	}
	switch header.Kind {
	case "gov5-post-resume-double-physical-reopen-accepted-not-tps", "gov5-post-bounded-double-physical-reopen-accepted-not-tps":
		var outer boundedHistoricalOuter
		if err := resumeDecodeJSON(raw, &outer, false); err != nil {
			return boundedHistoricalProjection{}, err
		}
		if !outer.Passed || outer.Error != "" || outer.Dispatched != 0 || outer.TPSMeasured || len(outer.Recoveries) != 2 || len(outer.RecoveryHashes) != 2 || outer.Recoveries[1] != inputs.Recovery.Path {
			return fail()
		}
		hash, err := resumeHashText(outer.RecoveryHashes[1])
		if err != nil || hash != inputs.Recovery.SHA256 {
			return fail()
		}
		return boundedHistoricalProjection{outer.NonceSum, outer.VectorSHA256, outer.Canonical}, nil
	case boundedFiniteHistoricalKind:
		var outer boundedFiniteHistoricalOuter
		if err := resumeDecodeJSON(raw, &outer, false); err != nil {
			return boundedHistoricalProjection{}, err
		}
		if !outer.Passed || outer.Error != "" || outer.TPSMeasured == nil || *outer.TPSMeasured || outer.CapacityProven == nil || *outer.CapacityProven || outer.Recovery != inputs.Recovery.Path {
			return fail()
		}
		hash, err := resumeHashText(outer.RecoveryHash)
		if err != nil || hash != inputs.Recovery.SHA256 {
			return fail()
		}
		baselineHash, err := resumeHashText(outer.BaselineSHA256)
		if err != nil || baselineHash == (types.Hash{}) || outer.TryID == "" {
			return fail()
		}
		j := outer.Journal
		if outer.Schedule.New < 200000 || outer.Schedule.New > 200499 || j.New != outer.Schedule.New || outer.Preloaded.NewlyAdmitted != j.New || j.New > math.MaxUint64-j.Skip || j.New+j.Skip != j.Original || j.Frames > math.MaxUint64/500 || j.Frames*500 != j.Original || outer.NonceSum < j.New {
			return fail()
		}
		epoch := outer.Preloaded.Epoch
		if epoch == ([16]byte{}) || epoch == recovery.OwnerFinal.Epoch || !boundedHistoricalOwnerBinding(outer.Preloaded, epoch, m) || outer.Preloaded.Reconciling || outer.Preloaded.PendingTransactions != int(j.New) || outer.Preloaded.ReservedTransactions != 0 || outer.Preloaded.ReservedEncodedBytes != 0 || outer.Preloaded.CanonicalNumber >= outer.Canonical.Number {
			return fail()
		}
		d := outer.Drain
		if !d.Passed || d.Count != outer.NonceSum || !boundedHistoricalOwnerBinding(d.Owner, epoch, m) || !strictQueueEmpty(d.Owner) || d.Owner.NewlyAdmitted != j.New || !boundedHistoricalHeadAtOrBefore(d.Owner.CanonicalNumber, d.Owner.CanonicalHash, outer.Canonical) || strictValidatorBinding(d.Reference, epoch, m) != nil || !d.Reference.ProducerRunning || d.Reference.QueuedTransactions != 0 || !boundedHistoricalHeadAtOrBefore(d.Reference.Number, d.Reference.BlockHash, outer.Canonical) || d.Reference.CommittedExecutedTransactions != outer.NonceSum {
			return fail()
		}
		for _, proof := range []boundedFiniteHistoricalProof{outer.Preproof, outer.Postproof} {
			if !proof.Passed || proof.TryID != outer.TryID || proof.BaselineSHA256 != outer.BaselineSHA256 {
				return fail()
			}
		}
		if err := validateBoundedFiniteHistoricalLifecycle(outer.Postproof.Lifecycle["all-process-lifecycle.json"].Processes); err != nil {
			return fail()
		}
		return boundedHistoricalProjection{outer.NonceSum, outer.VectorSHA256, outer.Canonical}, nil
	default:
		return fail()
	}
}

func validateBoundedFiniteHistoricalLifecycle(records []boundedHistoricalProcess) error {
	names := []string{"finite-owner", "finite-preload"}
	for i := 0; i < 7; i++ {
		names = append(names, fmt.Sprintf("produce-v%d", i))
	}
	names = append(names, "reopen-owner")
	for i := 0; i < 7; i++ {
		names = append(names, fmt.Sprintf("reopen-v%d", i))
	}
	names = append(names, "physical-reopen-audit")
	if len(records) != len(names) {
		return errors.New("finite historical process set differs")
	}
	seen := make(map[int]bool, len(records))
	for i, r := range records {
		if r.Name != names[i] || r.PID <= 0 || r.PGID != r.PID || seen[r.PID] || r.ReturnCode == nil || *r.ReturnCode != 0 || r.ForcedKill == nil || *r.ForcedKill || math.IsNaN(r.Started) || math.IsInf(r.Started, 0) || math.IsNaN(r.Stopped) || math.IsInf(r.Stopped, 0) || r.Started <= 0 || r.Stopped < r.Started {
			return errors.New("finite historical process cleanup differs")
		}
		seen[r.PID] = true
	}
	for i := 2; i < 9; i++ {
		if records[0].Started > records[i].Started || records[1].Stopped > records[i].Started || records[i].Stopped > records[0].Stopped || records[i].Stopped > records[9].Started {
			return errors.New("finite preload/production/reopen order differs")
		}
	}
	for i := 10; i < 17; i++ {
		if records[9].Started > records[i].Started || records[i].Started > records[17].Started || records[17].Stopped > records[i].Stopped || records[i].Stopped > records[9].Stopped {
			return errors.New("finite readonly audit/reopen lifetime differs")
		}
	}
	if records[0].Started > records[1].Started || records[1].Stopped > records[0].Stopped || records[9].Started > records[17].Started || records[0].Stopped > records[9].Started || records[17].Stopped > records[9].Stopped {
		return errors.New("finite owner/reopen order differs")
	}
	return nil
}

func boundedHistoricalOwnerBinding(s sharedexec.StrictIngressStatus, epoch [16]byte, m supplyManifest) bool {
	return s.Identity == m.Descriptor.Identity && s.Epoch == epoch && s.ManifestHash == m.Digest
}

func boundedHistoricalHeadAtOrBefore(number uint64, hash types.Hash, final resumeReportAnchor) bool {
	return number > 0 && hash != (types.Hash{}) && (number < final.Number || (number == final.Number && hash == final.Hash))
}
