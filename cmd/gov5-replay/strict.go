package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

type strictObservation struct {
	ElapsedSeconds  float64                        `json:"elapsed_seconds"`
	Canonical       sharedvalidator.StatusResult   `json:"single_validator_canonical"`
	Owner           sharedexec.StrictIngressStatus `json:"owner"`
	Stream          strictStreamSample             `json:"stream"`
	OtherValidators []sharedvalidator.StatusResult `json:"validators,omitempty"`
}
type strictRunResult struct {
	Kind                         string                         `json:"kind"`
	Passed                       bool                           `json:"passed"`
	Error                        string                         `json:"error,omitempty"`
	Identity                     sharedexec.Identity            `json:"identity"`
	ManifestSHA256               string                         `json:"manifest_sha256"`
	RuntimeManifest              string                         `json:"runtime_manifest"`
	OfferedTPS                   uint64                         `json:"offered_tps"`
	ExecutionTrust               string                         `json:"execution_trust"`
	BuildThenImportExecutesTwice bool                           `json:"build_then_import_executes_twice"`
	FullSourceIntegrityVerified  bool                           `json:"full_source_integrity_verified"`
	SourceIntegritySeconds       float64                        `json:"source_integrity_seconds"`
	SourceExhausted              bool                           `json:"source_exhausted"`
	CorpusAvailableThroughWindow bool                           `json:"corpus_available_through_window"`
	ActualWindowSeconds          float64                        `json:"actual_window_seconds"`
	CanonicalWindowDelta         uint64                         `json:"canonical_window_delta"`
	CanonicalTPS                 *float64                       `json:"canonical_committed_executed_tps,omitempty"`
	Initial                      []sharedvalidator.StatusResult `json:"initial"`
	WindowStart                  *strictObservation             `json:"window_start,omitempty"`
	WindowEnd                    *strictObservation             `json:"window_end,omitempty"`
	Samples                      []strictObservation            `json:"samples"`
	Final                        []sharedvalidator.StatusResult `json:"final"`
	OwnerInitial                 sharedexec.StrictIngressStatus `json:"owner_initial"`
	OwnerFinal                   sharedexec.StrictIngressStatus `json:"owner_final"`
	Stream                       strictStreamSample             `json:"stream_final"`
	Prefix                       []strictShardProgress          `json:"consumed_prefixes"`
}

func strictAttempt(root, name string) (string, error) {
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`).MatchString(name) {
		return "", errors.New("strict attempt requires a new lowercase basename")
	}
	parent := filepath.Join(root, "strict-attempts")
	if err := os.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err := strictPrivatePath(parent, true, false); err != nil {
		return "", err
	}
	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0700); err != nil {
		return "", err
	}
	return path, nil
}
func strictStatuses(ctx context.Context, clients []*sharedvalidator.APIClient, m supplyManifest, epoch [16]byte) ([]sharedvalidator.StatusResult, error) {
	statuses := make([]sharedvalidator.StatusResult, len(clients))
	failures := make([]error, len(clients))
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Add(1)
		go func(i int, c *sharedvalidator.APIClient) {
			defer wg.Done()
			probe, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			statuses[i], failures[i] = c.Status(probe)
			if failures[i] == nil {
				failures[i] = strictValidatorBinding(statuses[i], epoch, m)
			}
		}(i, c)
	}
	wg.Wait()
	for _, err := range failures {
		if err != nil {
			return statuses, err
		}
	}
	return statuses, nil
}
func strictWindowRate(start, end time.Time, before, after uint64) (float64, error) {
	if !end.After(start) || after < before {
		return 0, errors.New("strict canonical counter/time regressed")
	}
	return float64(after-before) / end.Sub(start).Seconds(), nil
}
func strictDrainMatches(statuses []sharedvalidator.StatusResult, owner sharedexec.StrictIngressStatus, expected uint64) bool {
	if len(statuses) != 7 || !strictQueueEmpty(owner) || owner.NewlyAdmitted != expected {
		return false
	}
	for _, s := range statuses {
		if s.CommittedExecutedTransactions != expected || s.BlockHash != owner.CanonicalHash || s.Number != owner.CanonicalNumber {
			return false
		}
	}
	return true
}

// runStrict uses genuinely signed, immutable corpus prefixes over GSI1. It is
// separate from raw qualification and never counts ACKs as canonical execution.
// Its caller must hold the exclusive BOX run claim for source hashing and IO.
func runStrict(parent context.Context, root, runtimePath, attempt string, offered uint64) (returnErr error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !stringsHasPrefixPath(root, supplyRoot) || offered == 0 || offered > 20_000_000 {
		return errors.New("explicit isolated corpus and offered rate 1..20M required")
	}
	if err := strictPrivatePath(root, true, false); err != nil {
		return err
	}
	ctx, cancelWork := context.WithTimeout(parent, 120*time.Second)
	defer cancelWork()
	m, err := loadSupplyManifest(root)
	if err != nil {
		return err
	}
	runtime, err := loadStrictRuntime(runtimePath, root, strictRuntimeBase, m)
	if err != nil {
		return err
	}
	dir, err := strictAttempt(runtime.Root, attempt)
	if err != nil {
		return err
	}
	result := strictRunResult{Kind: "strict-owner-queue-canonical-observation-not-capacity-promotion", Identity: m.Descriptor.Identity, ManifestSHA256: m.Digest.Hex(), RuntimeManifest: runtimePath, OfferedTPS: offered, ExecutionTrust: "accepted-trusted-execution-source", BuildThenImportExecutesTwice: true}
	defer func() {
		if returnErr != nil {
			result.Error = returnErr.Error()
		}
		returnErr = errors.Join(returnErr, exclusiveJSON(filepath.Join(dir, "result.json"), result))
	}()
	beforeIntegrity := time.Now()
	sources, err := openStrictSources(ctx, root, m)
	if err != nil {
		return err
	}
	defer closeStrictSources(sources)
	result.SourceIntegritySeconds = time.Since(beforeIntegrity).Seconds()
	result.FullSourceIntegrityVerified = true
	for _, path := range []string{runtime.OwnerSocket, filepath.Join(runtime.OwnerDir, "execution-rpc", "ingress.sock")} {
		if err := strictPrivatePath(path, false, true); err != nil {
			return err
		}
	}
	owner := sharedexec.NewClient(runtime.OwnerSocket, runtime.Identity)
	defer owner.Close()
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	ownerInitial, err := owner.StrictIngressStatus(probe)
	cancel()
	if err != nil {
		return err
	}
	result.OwnerInitial = ownerInitial
	if ownerInitial.ManifestHash != m.Digest || ownerInitial.NewlyAdmitted != 0 || ownerInitial.CryptoVerified != 0 || !strictQueueEmpty(ownerInitial) {
		return errors.New("strict replay requires a fresh empty owner session; no nonce-zero resume")
	}
	epoch := ownerInitial.Epoch
	clients := make([]*sharedvalidator.APIClient, 7)
	defer func() {
		for _, c := range clients {
			if c != nil {
				c.Close()
			}
		}
	}()
	for i, v := range runtime.Validators {
		if err := strictPrivatePath(v.APISocket, false, true); err != nil {
			return err
		}
		clients[i] = sharedvalidator.NewAPIClient(v.APISocket, runtime.Identity)
	}
	initial, err := strictStatuses(ctx, clients, m, epoch)
	if err != nil {
		return err
	}
	result.Initial = initial
	for _, s := range initial {
		if s.CommittedExecutedTransactions != 0 {
			return errors.New("strict replay requires seven fresh zero canonical counters")
		}
	}
	admitted := make([]strictAdmitter, len(m.Shards))
	defer func() {
		for _, c := range admitted {
			if c != nil {
				_ = c.Close()
			}
		}
	}()
	for i := range m.Shards {
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		c, err := sharedexec.DialStrictIngress(probe, filepath.Join(runtime.OwnerDir, "execution-rpc", "ingress.sock"), runtime.Identity, m.Digest, uint16(i))
		cancel()
		if err != nil {
			return err
		}
		admitted[i] = c
		if c.Epoch() != epoch {
			return errors.New("owner epoch changed before dispatch")
		}
	}
	dispatch, stopDispatch := context.WithCancel(ctx)
	defer stopDispatch()
	var firstError error
	var errorMu sync.Mutex
	recordError := func(err error) {
		if err == nil {
			return
		}
		errorMu.Lock()
		if firstError == nil {
			firstError = err
		}
		errorMu.Unlock()
		stopDispatch()
	}
	counters := new(strictStreamCounters)
	ready := make(chan error, len(m.Shards))
	start := make(chan struct{})
	pacer := &supplyPacer{rate: offered}
	progress := make([]strictShardProgress, len(m.Shards))
	var wg sync.WaitGroup
	for i := range m.Shards {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			recordError(strictPumpShard(ctx, dispatch, sources[i], m, uint16(i), admitted[i], epoch, pacer, counters, ready, start, &progress[i]))
		}(i)
	}
	for range m.Shards {
		if err := <-ready; err != nil {
			recordError(err)
		}
	}
	pacer.start = time.Now()
	close(start)
	observe := func() (strictObservation, error) {
		var out strictObservation
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		s, err := clients[0].Status(probe)
		cancel()
		if err != nil {
			return out, err
		}
		if err := strictValidatorBinding(s, epoch, m); err != nil {
			return out, err
		}
		probe, cancel = context.WithTimeout(ctx, 2*time.Second)
		o, err := owner.StrictIngressStatus(probe)
		cancel()
		if err != nil {
			return out, err
		}
		if o.Epoch != epoch || o.ManifestHash != m.Digest {
			return out, errors.New("strict owner epoch/manifest changed")
		}
		return strictObservation{ElapsedSeconds: time.Since(pacer.start).Seconds(), Canonical: s, Owner: o, Stream: counters.sample()}, nil
	}
	waitUntil := func(deadline time.Time) error {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		lastPeers := time.Now()
		for time.Now().Before(deadline) {
			timer := time.NewTimer(time.Until(deadline))
			select {
			case <-dispatch.Done():
				timer.Stop()
				return dispatch.Err()
			case <-timer.C:
				return nil
			case <-ticker.C:
				timer.Stop()
				sample, err := observe()
				if err != nil {
					return err
				}
				if time.Since(lastPeers) >= time.Second {
					sample.OtherValidators, err = strictStatuses(ctx, clients, m, epoch)
					if err != nil {
						return err
					}
					lastPeers = time.Now()
				}
				result.Samples = append(result.Samples, sample)
			}
		}
		return nil
	}
	var begin, end strictObservation
	var windowStart, windowEnd time.Time
	if err := waitUntil(pacer.start.Add(4 * time.Second)); err != nil {
		recordError(err)
	} else {
		// Enclose the actual canonical read boundaries conservatively. The end
		// read is taken before its timestamp; drain never enters this numerator.
		windowStart = time.Now()
		begin, err = observe()
		if err != nil {
			recordError(err)
		} else {
			result.WindowStart = &begin
			if err := waitUntil(windowStart.Add(20 * time.Second)); err != nil {
				recordError(err)
			} else {
				stopDispatch()
				end, err = observe()
				windowEnd = time.Now()
				if err != nil {
					recordError(err)
				} else {
					result.WindowEnd = &end
				}
			}
		}
	}
	stopDispatch()
	wg.Wait()
	result.Stream = counters.sample()
	result.Prefix = progress
	for _, p := range progress {
		result.SourceExhausted = result.SourceExhausted || p.Exhausted
	}
	for _, source := range sources {
		if err := checkStrictSource(source); err != nil {
			recordError(err)
		}
	}
	errorMu.Lock()
	failure := firstError
	errorMu.Unlock()
	if result.WindowEnd != nil {
		result.ActualWindowSeconds = windowEnd.Sub(windowStart).Seconds()
		if end.Canonical.CommittedExecutedTransactions < begin.Canonical.CommittedExecutedTransactions {
			recordError(errors.New("canonical window counter regressed"))
		} else {
			result.CanonicalWindowDelta = end.Canonical.CommittedExecutedTransactions - begin.Canonical.CommittedExecutedTransactions
		}
		firstExhausted := counters.firstExhaustedNanos.Load()
		result.CorpusAvailableThroughWindow = strictCorpusAvailable(end.Stream.Admitted, m.Descriptor.TotalTransactions(), time.Duration(firstExhausted), windowEnd.Sub(pacer.start))
	}
	// Every in-flight final ACK has settled before drain. Only canonical counters
	// plus independently released owner credits prove this prefix completed.
	drainCtx, cancelDrain := context.WithTimeout(ctx, 30*time.Second)
	defer cancelDrain()
	for {
		statuses, err := strictStatuses(drainCtx, clients, m, epoch)
		if err != nil {
			recordError(err)
			break
		}
		probe, cancel := context.WithTimeout(drainCtx, 2*time.Second)
		current, err := owner.StrictIngressStatus(probe)
		cancel()
		if err != nil {
			recordError(err)
			break
		}
		if current.Epoch != epoch || current.ManifestHash != m.Digest {
			recordError(errors.New("owner epoch changed during drain"))
			break
		}
		result.Final = statuses
		result.OwnerFinal = current
		if strictDrainMatches(statuses, current, result.Stream.Admitted) {
			break
		}
		if drainCtx.Err() != nil {
			recordError(errors.New("strict bounded canonical/credit drain did not converge"))
			break
		}
		select {
		case <-drainCtx.Done():
			recordError(errors.New("strict bounded canonical/credit drain did not converge"))
		case <-time.After(100 * time.Millisecond):
		}
		if drainCtx.Err() != nil {
			break
		}
	}
	errorMu.Lock()
	if firstError != nil {
		failure = firstError
	}
	errorMu.Unlock()
	if failure != nil {
		return failure
	}
	if result.WindowEnd == nil || result.ActualWindowSeconds < 20 || !result.CorpusAvailableThroughWindow {
		return errors.New("strict window incomplete or unique corpus exhausted")
	}
	rate, err := strictWindowRate(windowStart, windowEnd, begin.Canonical.CommittedExecutedTransactions, end.Canonical.CommittedExecutedTransactions)
	if err != nil {
		return err
	}
	result.CanonicalTPS = &rate
	result.Passed = true
	return nil
}
func stringsHasPrefixPath(path, base string) bool {
	return len(path) > len(base) && path[:len(base)+1] == base+string(filepath.Separator)
}

// End-of-run ACKs and EOFs may settle after the measured window. Compare the
// first per-shard exhaustion with that window, not with the final worker state.
func strictCorpusAvailable(windowAdmitted, total uint64, firstExhausted, windowEnd time.Duration) bool {
	return windowAdmitted < total && (firstExhausted == 0 || firstExhausted > windowEnd)
}
