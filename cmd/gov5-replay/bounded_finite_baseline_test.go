package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

// Synthetic historical projection, not execution evidence. The public chain
// fixture is tested separately; this tests the finite report's scalar schema,
// production activity and process-order contracts before common root checks.
func boundedFiniteProjectionFixture() (resumePlanInputs, supplyManifest, strictRecoveryResult, boundedFiniteHistoricalOuter) {
	var inputs resumePlanInputs
	inputs.Recovery.Path = "/synthetic/recovery.json"
	inputs.Recovery.SHA256 = types.Hash{1}
	m := supplyManifest{Digest: types.Hash{2}}
	m.Descriptor.Identity = sharedexec.Identity{Version: 1, Namespace: "synthetic", ChainID: "4205001", GenesisHash: types.Hash{3}}
	recovery := strictRecoveryResult{}
	recovery.OwnerFinal.Epoch = [16]byte{9}
	b := false
	outer := boundedFiniteHistoricalOuter{Kind: boundedFiniteHistoricalKind, Passed: true, TryID: "0001", BaselineSHA256: (types.Hash{4}).Hex(), Recovery: inputs.Recovery.Path, RecoveryHash: inputs.Recovery.SHA256.Hex(), NonceSum: 904301, VectorSHA256: types.Hash{5}, Canonical: resumeReportAnchor{Number: 1242, Hash: types.Hash{6}, Root: types.Hash{7}}, TPSMeasured: &b, CapacityProven: &b}
	outer.Schedule.New = 200176
	outer.Journal.Frames, outer.Journal.Original, outer.Journal.New, outer.Journal.Skip = 1032, 516000, 200176, 315824
	epoch := [16]byte{8}
	outer.Preloaded = sharedexec.StrictIngressStatus{Identity: m.Descriptor.Identity, ManifestHash: m.Digest, Epoch: epoch, CanonicalNumber: 1240, CanonicalHash: types.Hash{8}, NewlyAdmitted: 200176, PendingTransactions: 200176, CryptoVerified: 516000}
	outer.Drain.Passed, outer.Drain.Count = true, outer.NonceSum
	outer.Drain.Owner = outer.Preloaded
	outer.Drain.Owner.PendingTransactions = 0
	outer.Drain.Owner.CanonicalNumber, outer.Drain.Owner.CanonicalHash = outer.Canonical.Number, outer.Canonical.Hash
	outer.Drain.Reference = sharedvalidator.StatusResult{CanonicalProgress: sharedvalidator.CanonicalProgress{Identity: m.Descriptor.Identity, Number: outer.Canonical.Number, BlockHash: outer.Canonical.Hash, CommittedExecutedTransactions: outer.NonceSum}, ProducerRunning: true, OwnerQueue: &sharedvalidator.OwnerQueueBinding{Epoch: epoch, ManifestHash: m.Digest, MaxTransactions: sharedvalidator.DefaultOwnerQueueTransactions, MaxEncodedBytes: sharedexec.MaxQueueBuildEncodedBytes}}
	outer.Preproof = boundedFiniteHistoricalProof{Passed: true, TryID: outer.TryID, BaselineSHA256: outer.BaselineSHA256}
	outer.Postproof = outer.Preproof
	outer.Postproof.Lifecycle = make(map[string]struct {
		Processes []boundedHistoricalProcess `json:"processes"`
	})
	names := []string{"finite-owner", "finite-preload"}
	for i := 0; i < 7; i++ {
		names = append(names, fmt.Sprintf("produce-v%d", i))
	}
	names = append(names, "reopen-owner")
	for i := 0; i < 7; i++ {
		names = append(names, fmt.Sprintf("reopen-v%d", i))
	}
	names = append(names, "physical-reopen-audit")
	rc := 0
	records := make([]boundedHistoricalProcess, 18)
	for i, name := range names {
		start, stop := float64(3), float64(4)
		if i == 0 {
			start, stop = 1, 5
		} else if i == 1 {
			start, stop = 1, 2
		} else if i == 9 {
			start, stop = 6, 10
		} else if i >= 10 {
			start, stop = 7, 9
		}
		records[i] = boundedHistoricalProcess{Name: name, PID: i + 1, PGID: i + 1, ReturnCode: &rc, ForcedKill: &b, Started: start, Stopped: stop}
	}
	outer.Postproof.Lifecycle["all-process-lifecycle.json"] = struct {
		Processes []boundedHistoricalProcess `json:"processes"`
	}{records}
	return inputs, m, recovery, outer
}

func TestBoundedResumeFiniteHistoricalScalarProjectionAndLifecycle(t *testing.T) {
	inputs, m, recovery, outer := boundedFiniteProjectionFixture()
	raw, err := json.Marshal(outer)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := decodeBoundedHistoricalOuter(inputs, m, recovery, raw)
	if err != nil || projected.Canonical != outer.Canonical || projected.NonceSum != 904301 || projected.VectorSHA256 != outer.VectorSHA256 {
		t.Fatal("complete finite metadata rejected", err)
	}

	// Stopping producers may leave a later stable empty block with the same
	// committed count; the physical reopen must bind that later final anchor.
	later := outer
	later.Canonical.Number++
	later.Canonical.Hash = types.Hash{33}
	laterRaw, err := json.Marshal(later)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeBoundedHistoricalOuter(inputs, m, recovery, laterRaw); err != nil {
		t.Fatal("later stable empty head rejected", err)
	}
	// Unlike a no-dispatch audit, the finite outer has real admitted work. Its
	// separately parsed inner recovery remains subject to dispatch==0 below.
	var old boundedHistoricalOuter
	if err := resumeDecodeJSON(raw, &old, false); err == nil {
		t.Fatal("scalar recovery hash silently accepted as old list schema")
	}
	for _, change := range []func(map[string]any){

		func(v map[string]any) { v["canonical"].(map[string]any)["number"] = 1241 },
		func(v map[string]any) { v["canonical"].(map[string]any)["hash"] = (types.Hash{55}).Hex() },
		func(v map[string]any) { v["kind"] = "unknown" },
		func(v map[string]any) { delete(v, "tps_measured") },
		func(v map[string]any) { v["tps_measured"] = true },
		func(v map[string]any) { delete(v, "capacity_5m_proven") },
		func(v map[string]any) { v["capacity_5m_proven"] = true },
		func(v map[string]any) { v["recovery_sha256"] = []string{outer.RecoveryHash} },
		func(v map[string]any) { v["recovery_sha256"] = (types.Hash{99}).Hex() },
		func(v map[string]any) { v["recovery_result"] = "wrong-path" },
		func(v map[string]any) { v["journal_totals"].(map[string]any)["new"] = 200177 },
		func(v map[string]any) { v["journal_totals"].(map[string]any)["frames"] = 1033 },
		func(v map[string]any) { v["preloaded_owner"].(map[string]any)["NewlyAdmitted"] = 200177 },
		func(v map[string]any) { v["drain"].(map[string]any)["canonical_count"] = 904302 },
		func(v map[string]any) { v["preproof"].(map[string]any)["baseline_sha256"] = (types.Hash{99}).Hex() },
		func(v map[string]any) { v["postproof"].(map[string]any)["try_id"] = "different" },
	} {
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		change(v)
		changed, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeBoundedHistoricalOuter(inputs, m, recovery, changed); err == nil {
			t.Fatal("finite metadata mutation accepted", string(changed))
		}
	}
	for _, change := range []func([]boundedHistoricalProcess) []boundedHistoricalProcess{
		func(r []boundedHistoricalProcess) []boundedHistoricalProcess { return r[:17] },
		func(r []boundedHistoricalProcess) []boundedHistoricalProcess { r[0].ReturnCode = nil; return r },
		func(r []boundedHistoricalProcess) []boundedHistoricalProcess { x := 1; r[0].ReturnCode = &x; return r },
		func(r []boundedHistoricalProcess) []boundedHistoricalProcess { r[0].ForcedKill = nil; return r },
		func(r []boundedHistoricalProcess) []boundedHistoricalProcess {
			x := true
			r[0].ForcedKill = &x
			return r
		},
		func(r []boundedHistoricalProcess) []boundedHistoricalProcess { r[1].PID = r[0].PID; return r },
		func(r []boundedHistoricalProcess) []boundedHistoricalProcess { r[2].Started = 1; return r },
		func(r []boundedHistoricalProcess) []boundedHistoricalProcess { r[0].Stopped = 7; return r },

		func(r []boundedHistoricalProcess) []boundedHistoricalProcess {
			r[17].Started = 2
			r[17].Stopped = 3
			return r
		},
		func(r []boundedHistoricalProcess) []boundedHistoricalProcess { r[10].Stopped = 8; return r },
		func(r []boundedHistoricalProcess) []boundedHistoricalProcess { r[0].Started = 2; return r },
		func(r []boundedHistoricalProcess) []boundedHistoricalProcess { r[17].Stopped = 11; return r },
	} {
		records := append([]boundedHistoricalProcess(nil), outer.Postproof.Lifecycle["all-process-lifecycle.json"].Processes...)
		if err := validateBoundedFiniteHistoricalLifecycle(change(records)); err == nil {
			t.Fatal("unclean or overlapping finite lifecycle accepted")
		}
	}
}
