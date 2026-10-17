package sharedexec

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"sort"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

// ResumeIngressOptions is an explicit local startup choice. Reports are content
// pins only: this owner independently scans the original source and reads its
// actual canonical/applied account state before creating any session.
type ResumeIngressOptions struct {
	ManifestRaw, GenesisRaw          []byte
	SourceRoot                       string
	PlanDocumentRaw, SourceProofRaw  []byte
	ExpectedPlanID                   types.Hash
	MaxTransactions, MaxEncodedBytes int
}

type resumeIngressSession struct {
	planID types.Hash
	plan   ResumePlan
	// Online counters exclude source-scan verification and historical prefixes.
	canonicalSkipped uint64
	receipts         map[CorpusFrameID]resumeFrameReceipt
}

// EnableResumeIngress is startup-only and mutually exclusive with legacy GSI1.
// It never trusts a serialized verification result, claimed sender or old ACK.
func (s *Server) EnableResumeIngress(ctx context.Context, options ResumeIngressOptions) error {
	if ctx == nil || ctx.Err() != nil {
		return errors.New("active resume startup context required")
	}
	if len(options.ManifestRaw) == 0 || len(options.ManifestRaw) > CorpusMaxManifestBytes || len(options.GenesisRaw) == 0 || len(options.GenesisRaw) > 16<<20 || len(options.PlanDocumentRaw) == 0 || len(options.PlanDocumentRaw) > ResumeMaxPlanDocumentBytes || len(options.SourceProofRaw) == 0 || len(options.SourceProofRaw) > 16<<20 || options.ExpectedPlanID == (types.Hash{}) || options.MaxTransactions < 1417 || options.MaxTransactions > 2_000_000 || options.MaxEncodedBytes < 1 || options.MaxEncodedBytes > 512<<20 {
		return errors.New("invalid bounded resume startup options")
	}
	// The owner retains its own immutable copies for this operation.
	options.ManifestRaw = append([]byte(nil), options.ManifestRaw...)
	options.GenesisRaw = append([]byte(nil), options.GenesisRaw...)
	options.PlanDocumentRaw = append([]byte(nil), options.PlanDocumentRaw...)
	options.SourceProofRaw = append([]byte(nil), options.SourceProofRaw...)
	document, err := DecodeResumePlanDocument(options.PlanDocumentRaw)
	if err != nil {
		return err
	}
	plan := document.Plan
	if document.PlanID != options.ExpectedPlanID || plan.Identity != s.identity {
		return errors.New("resume local expected plan/identity differs")
	}
	manifest, err := DecodeCorpusManifest(options.ManifestRaw)
	if err != nil {
		return err
	}
	if manifest.Descriptor.Identity != s.identity || types.Hash(sha256.Sum256(options.SourceProofRaw)) != plan.SourceProofSHA256 {
		return errors.New("resume manifest/source proof binding differs")
	}
	// A genesis header hash does not bind every fork or validator setting.
	// Independently bind the source's exact serialized chain configuration to
	// this owner, even when the caller bypasses the command-line loader.
	var sourceGenesis conf.Genesis
	if err := json.Unmarshal(options.GenesisRaw, &sourceGenesis); err != nil {
		return err
	}
	if sourceGenesis.Config == nil || s.chain.Config() == nil {
		return errors.New("resume source/owner chain configuration required")
	}
	sourceConfig, err := json.Marshal(sourceGenesis.Config)
	if err != nil {
		return err
	}
	ownerConfig, err := json.Marshal(s.chain.Config())
	if err != nil {
		return err
	}
	if !bytes.Equal(sourceConfig, ownerConfig) {
		return errors.New("resume source/owner chain configuration differs")
	}
	if _, err := ResumeMappingID(plan.Senders); err != nil {
		return err
	}
	if _, ok := s.chain.Engine().(*hotstuff.HotStuff); !ok || s.chain.Config().HotStuff == nil || len(s.chain.Config().HotStuff.Validators) != 7 {
		return errors.New("resume ingress requires fixed-seven public HotStuff owner")
	}
	s.mu.Lock()
	if s.closed || s.started || s.resumeArming || s.ingress != nil || len(s.jobs) != 0 || len(s.builds) != 0 || len(s.queuePlans) != 0 {
		s.mu.Unlock()
		return errors.New("resume ingress requires exclusive unused owner startup")
	}
	s.resumeArming = true
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	defer func() { s.mu.Lock(); s.resumeArming = false; s.mu.Unlock() }()
	work, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	s.execute.Lock()
	defer s.execute.Unlock()
	s.mu.Lock()
	idle := !s.closed && !s.started && s.resumeArming && s.ingress == nil && len(s.jobs) == 0 && len(s.builds) == 0 && len(s.queuePlans) == 0
	s.mu.Unlock()
	if !idle {
		return errors.New("resume ingress requires unused owner")
	}
	if err := work.Err(); err != nil {
		return err
	}
	baseline, err := s.resumeCanonicalVector(work, plan)
	if err != nil {
		return err
	}
	expected, err := ResumePlanID(plan, options.ManifestRaw, baseline)
	if err != nil || expected != options.ExpectedPlanID {
		return errors.New("resume fresh canonical vector/plan differs")
	}
	var retainedBytes uint64
	for _, frame := range plan.Frames {
		retainedBytes += uint64(frame.PayloadBytes)
	}
	if retainedBytes > uint64(options.MaxEncodedBytes) {
		return errors.New("resume bounded slice needs credits for all four original payloads")
	}
	lease, err := openResumeSourceScan(work, options.SourceRoot, options.ManifestRaw, options.GenesisRaw, baseline)
	if err != nil {
		return err
	}
	defer lease.Close()
	scan, err := lease.scan(work)
	if err != nil {
		return err
	}
	_, sourceBytes, err := EncodeResumeSourceProof(options.ManifestRaw, scan)
	if err != nil {
		return err
	}
	if !bytes.Equal(sourceBytes, options.SourceProofRaw) || scan.MappingSHA256 != plan.MappingSHA256 || !reflect.DeepEqual(scan.Senders, plan.Senders) || !reflect.DeepEqual(scan.Frames, plan.Frames) {
		return errors.New("resume owner original source reconstruction differs")
	}
	// Recompute the plan from locally recovered values rather than self-checking
	// a parsed document and mistaking that consistency for source authenticity.
	rebuilt := plan
	rebuilt.Senders, rebuilt.Frames, rebuilt.MappingSHA256 = scan.Senders, scan.Frames, scan.MappingSHA256
	rebuilt.SourceProofSHA256 = types.Hash(sha256.Sum256(sourceBytes))
	if id, err := ResumePlanID(rebuilt, options.ManifestRaw, baseline); err != nil || id != options.ExpectedPlanID {
		return errors.New("resume owner reconstructed plan differs")
	}
	after, err := s.resumeCanonicalVector(work, plan)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(after, baseline) {
		return errors.New("resume canonical state changed during source verification")
	}
	if _, err := lease.checkUnchanged(); err != nil {
		return err
	}
	q := &strictIngressQueue{descriptor: manifest.Descriptor, manifestHash: types.Hash(sha256.Sum256(options.ManifestRaw)), maxTx: options.MaxTransactions, maxBytes: options.MaxEncodedBytes, lanes: make(map[types.Address]*ingressLane), stamp: ingressStamp{hash: plan.Canonical.Hash, number: plan.Canonical.Number}, resume: &resumeIngressSession{planID: options.ExpectedPlanID, plan: rebuilt, receipts: make(map[CorpusFrameID]resumeFrameReceipt)}}
	if _, err := rand.Read(q.epoch[:]); err != nil {
		return err
	}
	if q.epoch == ([16]byte{}) {
		return errors.New("resume random epoch was zero")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.started || !s.resumeArming || work.Err() != nil || s.ctx.Err() != nil || s.ingress != nil || len(s.jobs) != 0 || len(s.builds) != 0 || len(s.queuePlans) != 0 {
		return errors.New("resume owner closed or startup canceled before publication")
	}
	s.ingress = q
	s.resumeArming = false
	return nil
}

// Caller holds execute and has not started serving. Every bounded batch is
// tied to the same actual canonical+applied hash/number/header root/tree root.
func (s *Server) resumeCanonicalVector(ctx context.Context, plan ResumePlan) ([]CanonicalAccount, error) {
	if plan.Canonical.Number == 0 || plan.Canonical.Number == ^uint64(0) || plan.Canonical.Hash == (types.Hash{}) || plan.Canonical.Root == (types.Hash{}) {
		return nil, errors.New("resume requires retained nonzero canonical anchor")
	}
	addresses := make([]types.Address, len(plan.Senders))
	for i, sender := range plan.Senders {
		addresses[i] = sender.Address
	}
	sort.Slice(addresses, func(i, j int) bool { return bytes.Compare(addresses[i][:], addresses[j][:]) < 0 })
	accounts := make([]CanonicalAccount, 0, len(addresses))
	for offset := 0; offset < len(addresses); offset += canonicalAccountsLimit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := offset + canonicalAccountsLimit
		if end > len(addresses) {
			end = len(addresses)
		}
		snapshot, err := s.chain.ReadPerformanceCanonicalAccounts(ctx, addresses[offset:end])
		if err != nil {
			return nil, err
		}
		if snapshot.Hash != plan.Canonical.Hash || snapshot.Number != plan.Canonical.Number || snapshot.Root != plan.Canonical.Root || snapshot.AppliedRoot != snapshot.Root {
			return nil, errors.New("resume actual canonical/applied root differs from plan")
		}
		for _, address := range addresses[offset:end] {
			value, ok := snapshot.Accounts[address]
			if !ok || !snapshot.Exists[address] {
				return nil, errors.New("resume funded canonical account missing")
			}
			accounts = append(accounts, CanonicalAccount{Address: address, Exists: true, Nonce: value.Nonce, Balance: value.Balance.Dec(), CodeHash: value.CodeHash})
		}
	}
	return accounts, nil
}
