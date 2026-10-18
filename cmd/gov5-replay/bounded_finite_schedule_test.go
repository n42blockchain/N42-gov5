package main

import (
	"reflect"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/params"
)

// Synthetic metadata only. Actual source inclusion and online signatures remain
// the shared scanner and GSR2 owner's responsibility; this is no arming proof.
func finiteScheduleFixture(t *testing.T) (sharedexec.CorpusDescriptor, sharedexec.BoundedResumePlan, []sharedexec.BoundedResumeFrame) {
	t.Helper()
	d := sharedexec.CorpusDescriptor{Version: 1, Identity: sharedexec.Identity{Version: 1, Namespace: params.PerformanceNamespace, ChainID: "4205001", GenesisHash: types.Hash{1}}, GenesisFileHash: types.Hash{2}, Codec: sharedexec.CorpusCodec, Signature: sharedexec.CorpusSignature, KeyDerivation: sharedexec.CorpusKeyDerivation, SeedCommitment: types.Hash{3}, SenderCount: 4, NoncesPerSender: 1000, RecipientRule: sharedexec.CorpusRecipientRule, RecipientCount: 4, ShardCount: 2, FrameTransactions: 500}
	corpus, err := d.ID()
	if err != nil {
		t.Fatal(err)
	}
	p := sharedexec.BoundedResumePlan{NonceEndExclusive: 1000, CanonicalNonceSum: 1583, MaxSourceFrames: 8, MaxSourceTransactions: 4000, MaxSourceEncodedBytes: 424672}
	for i, nonce := range []uint64{0, 41, 542, 1000} {
		p.Senders = append(p.Senders, sharedexec.ResumeSender{Index: uint32(i), Address: types.Address{19: byte(4 - i)}, Nonce: nonce})
	}
	var frames []sharedexec.BoundedResumeFrame
	for shard := uint16(0); shard < d.ShardCount; shard++ {
		for sequence := uint64(0); sequence < 4; sequence++ {
			index, nonce, count, err := d.FrameRange(shard, sequence)
			if err != nil {
				t.Fatal(err)
			}
			sender := p.Senders[index]
			var skip uint64
			if sender.Nonce > nonce {
				skip = sender.Nonce - nonce
				if skip > 500 {
					skip = 500
				}
			}
			frames = append(frames, sharedexec.BoundedResumeFrame{ID: sharedexec.CorpusFrameID{CorpusID: corpus, Shard: shard, Sequence: sequence}, SenderIndex: index, Address: sender.Address, FirstNonce: nonce, Transactions: count, PayloadHash: types.Hash{byte(len(frames) + 1)}, PayloadBytes: 52000, CanonicalSkipped: uint16(skip), NewlyAdmitted: count - uint16(skip)})
		}
	}
	p.WhitelistSHA256, p.SourceEncodedBytes, p.MaxNewTransactions, err = sharedexec.BoundedResumeWhitelistID(d, p.Senders, p.NonceEndExclusive, frames)
	if err != nil {
		t.Fatal(err)
	}
	return d, p, frames
}

func TestBoundedFiniteScheduleRoundRobinWholeFramesAndStableDigest(t *testing.T) {
	d, p, frames := finiteScheduleFixture(t)
	id := types.Hash{9}
	s, err := selectBoundedFiniteSchedule(d, p, id, frames, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if s.NewlyAdmitted != 1459 || s.CanonicalSkipped != 1041 || s.OriginalTransactions != 2500 || s.EncodedBytes != 260420 || s.RetainedBytes != 156000 || !reflect.DeepEqual(s.FramesPerShard, []uint64{3, 2}) || !reflect.DeepEqual(s.SenderNew, []uint64{1000, 459, 0, 0}) {
		t.Fatalf("finite original-prefix selection differs: %+v", s)
	}
	for i, f := range s.Frames {
		if f.ID.Shard != uint16(i%2) || f.ID.Sequence != uint64(i/2) || f.Transactions != 500 {
			t.Fatal("finite schedule reordered or truncated an original frame")
		}
	}
	if again, err := selectBoundedFiniteSchedule(d, p, id, frames, 1000); err != nil || !reflect.DeepEqual(s, again) || s.Digest == (types.Hash{}) {
		t.Fatal("same input changed finite schedule", err)
	}
	changed := s
	changed.PlanID[0]++
	if boundedFiniteScheduleID(changed) == s.Digest {
		t.Fatal("schedule digest did not bind the full-prefix plan")
	}
	changed = s
	changed.Frames = append([]sharedexec.BoundedResumeFrame(nil), s.Frames...)
	changed.Frames[0].PayloadHash[0]++
	if boundedFiniteScheduleID(changed) == s.Digest {
		t.Fatal("schedule digest did not bind exact original frame payload")
	}
}

func TestBoundedFiniteScheduleRejectsCapsAndCompleteWhitelistMutations(t *testing.T) {
	d, p, frames := finiteScheduleFixture(t)
	for _, target := range []uint64{0, 2418, finitePhaseTargetNew + 1} {
		if _, err := selectBoundedFiniteSchedule(d, p, types.Hash{9}, frames, target); err == nil {
			t.Fatalf("invalid or unavailable target accepted: %d", target)
		}
	}
	for _, change := range []func(*sharedexec.BoundedResumePlan, []sharedexec.BoundedResumeFrame){
		func(p *sharedexec.BoundedResumePlan, _ []sharedexec.BoundedResumeFrame) { p.CanonicalNonceSum++ },
		func(p *sharedexec.BoundedResumePlan, _ []sharedexec.BoundedResumeFrame) {
			p.CanonicalNonceSum = ^uint64(0)
		},
		func(p *sharedexec.BoundedResumePlan, _ []sharedexec.BoundedResumeFrame) { p.MaxSourceTransactions-- },
		func(p *sharedexec.BoundedResumePlan, _ []sharedexec.BoundedResumeFrame) { p.MaxSourceEncodedBytes = 1 },
		func(_ *sharedexec.BoundedResumePlan, f []sharedexec.BoundedResumeFrame) {
			f[7].PayloadHash = types.Hash{}
		},
		func(_ *sharedexec.BoundedResumePlan, f []sharedexec.BoundedResumeFrame) { f[1], f[2] = f[2], f[1] },
	} {
		copyPlan, copyFrames := p, append([]sharedexec.BoundedResumeFrame(nil), frames...)
		change(&copyPlan, copyFrames)
		if _, err := selectBoundedFiniteSchedule(d, copyPlan, types.Hash{9}, copyFrames, 1000); err == nil {
			t.Fatal("invalid complete original whitelist or budget accepted")
		}
	}
}
