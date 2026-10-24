package sharedexec

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

// BoundedResumeIngressOptions selects an independent explicit GSR2 session.
// The public library supports bounded fixtures; the production command-line
// profile additionally fixes the 6000-sender nonce[0,10000) campaign geometry.
type BoundedResumeIngressOptions ResumeIngressOptions

type boundedResumeSession struct {
	plan                                                             BoundedResumePlan
	frames                                                           map[CorpusFrameID]BoundedResumeFrame
	shards                                                           map[uint16]bool
	admittedFrames, admittedTransactions, admittedBytes, admittedNew uint64
	reservedFrames, reservedTransactions, reservedBytes, reservedNew uint64
}

func boundedResumeStatePlan(p BoundedResumePlan) ResumePlan {
	return ResumePlan{Version: 2, Identity: p.Identity, ManifestSHA256: p.ManifestSHA256, CorpusID: p.CorpusID, GenesisFileSHA256: p.GenesisFileSHA256, SourceProofSHA256: p.SourceProofSHA256, MappingSHA256: p.MappingSHA256, RecoveryOuterSHA256: p.RecoveryOuterSHA256, RecoverySHA256: p.RecoverySHA256, AccountVectorSHA256: p.AccountVectorSHA256, OriginalRestoreSHA256: p.OriginalRestoreSHA256, Canonical: p.Canonical, CanonicalNonceSum: p.CanonicalNonceSum, Senders: p.Senders}
}

// EnableBoundedResumeIngress independently authenticates local source bytes and
// the entire actual canonical snapshot before atomically publishing a GSR2 epoch.
func (s *Server) EnableBoundedResumeIngress(ctx context.Context, options BoundedResumeIngressOptions) error {
	if ctx == nil || ctx.Err() != nil {
		return errors.New("active resume startup context required")
	}
	if len(options.ManifestRaw) == 0 || len(options.ManifestRaw) > CorpusMaxManifestBytes || len(options.GenesisRaw) == 0 || len(options.GenesisRaw) > 16<<20 || len(options.PlanDocumentRaw) == 0 || len(options.PlanDocumentRaw) > ResumeMaxPlanDocumentBytes || len(options.SourceProofRaw) == 0 || len(options.SourceProofRaw) > 16<<20 || options.ExpectedPlanID == (types.Hash{}) || options.MaxTransactions < 1 || options.MaxTransactions > 2_000_000 || options.MaxEncodedBytes < 1 || options.MaxEncodedBytes > 512<<20 {
		return errors.New("invalid bounded resume startup options")
	}
	// The owner retains its own immutable copies for this operation.
	options.ManifestRaw = append([]byte(nil), options.ManifestRaw...)
	options.GenesisRaw = append([]byte(nil), options.GenesisRaw...)
	options.PlanDocumentRaw = append([]byte(nil), options.PlanDocumentRaw...)
	options.SourceProofRaw = append([]byte(nil), options.SourceProofRaw...)
	document, err := DecodeBoundedResumePlanDocument(options.PlanDocumentRaw)
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
	statePlan := boundedResumeStatePlan(plan)
	baseline, err := s.resumeCanonicalVector(work, statePlan)
	if err != nil {
		return err
	}
	expected, err := BoundedResumePlanID(plan, options.ManifestRaw, baseline)
	if err != nil || expected != options.ExpectedPlanID {
		return errors.New("resume fresh canonical vector/plan differs")
	}
	lease, err := openResumeSourceScan(work, options.SourceRoot, options.ManifestRaw, options.GenesisRaw, baseline)
	if err != nil {
		return err
	}
	defer lease.Close()
	scan, err := lease.scanBounded(work, plan.NonceEndExclusive)
	if err != nil {
		return err
	}
	_, sourceBytes, err := EncodeResumeSourceProof(options.ManifestRaw, scan.Source)
	if err != nil {
		return err
	}
	if !bytes.Equal(sourceBytes, options.SourceProofRaw) || scan.Source.MappingSHA256 != plan.MappingSHA256 || !reflect.DeepEqual(scan.Source.Senders, plan.Senders) || scan.WhitelistSHA256 != plan.WhitelistSHA256 || scan.SourceFrames != plan.MaxSourceFrames || scan.SourceTransactions != plan.MaxSourceTransactions || scan.SourceEncodedBytes != plan.SourceEncodedBytes || scan.NewTransactions != plan.MaxNewTransactions {
		return errors.New("resume owner original source reconstruction differs")
	}
	// Recompute the plan from locally recovered values rather than self-checking
	// a parsed document and mistaking that consistency for source authenticity.
	rebuilt := plan
	rebuilt.Senders, rebuilt.MappingSHA256 = scan.Source.Senders, scan.Source.MappingSHA256
	rebuilt.WhitelistSHA256 = scan.WhitelistSHA256
	rebuilt.SourceEncodedBytes = scan.SourceEncodedBytes
	rebuilt.MaxSourceFrames, rebuilt.MaxSourceTransactions, rebuilt.MaxNewTransactions = scan.SourceFrames, scan.SourceTransactions, scan.NewTransactions
	rebuilt.SourceProofSHA256 = types.Hash(sha256.Sum256(sourceBytes))
	if id, err := BoundedResumePlanID(rebuilt, options.ManifestRaw, baseline); err != nil || id != options.ExpectedPlanID {
		return errors.New("resume owner reconstructed plan differs")
	}
	after, err := s.resumeCanonicalVector(work, statePlan)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(after, baseline) {
		return errors.New("resume canonical state changed during source verification")
	}
	if _, err := lease.checkUnchanged(); err != nil {
		return err
	}
	whitelist := make(map[CorpusFrameID]BoundedResumeFrame, len(scan.Frames))
	shards := make(map[uint16]bool, manifest.Descriptor.ShardCount)
	for _, frame := range scan.Frames {
		if int(frame.PayloadBytes) > options.MaxEncodedBytes {
			return errors.New("bounded resume queue needs one full original payload byte credit")
		}
		whitelist[frame.ID] = frame
		shards[frame.ID.Shard] = true
	}
	window := &boundedResumeSession{plan: rebuilt, frames: whitelist, shards: shards}
	q := &strictIngressQueue{descriptor: manifest.Descriptor, manifestHash: types.Hash(sha256.Sum256(options.ManifestRaw)), maxTx: options.MaxTransactions, maxBytes: options.MaxEncodedBytes, lanes: make(map[types.Address]*ingressLane), stamp: ingressStamp{hash: plan.Canonical.Hash, number: plan.Canonical.Number}, resume: &resumeIngressSession{planID: options.ExpectedPlanID, plan: statePlan, receipts: make(map[CorpusFrameID]resumeFrameReceipt), window: window}}
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
	var diagnostics *resumeDiagnostics
	if options.DiagnosticsJSONL != "" {
		diagnostics, err = newResumeDiagnostics(options.DiagnosticsJSONL)
		if err != nil {
			return err
		}
	}
	s.ingress = q
	s.resumeArming = false
	if diagnostics != nil {
		s.resumeDiagnostics = diagnostics
		s.resumeObserverWG.Add(1)
		go diagnostics.run(s, q)
	}
	return nil
}
