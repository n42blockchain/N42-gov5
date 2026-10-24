package sharedexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

type boundedExecutionFixture struct {
	*resumeExecutionFixture
	prefix    BoundedResumePlan
	prefixID  types.Hash
	prefixRaw []byte
	selected  []BoundedResumeFrame
	original  map[CorpusFrameID]CorpusFrame
}

func newBoundedExecutionFixture(t *testing.T) *boundedExecutionFixture {
	t.Helper()
	f := &boundedExecutionFixture{resumeExecutionFixture: newResumeExecutionFixture(t), original: make(map[CorpusFrameID]CorpusFrame)}
	scan, err := ScanBoundedResumeSources(context.Background(), f.sourceRoot, f.manifestRaw, f.genesisRaw, f.baseline, 1000)
	if err != nil {
		t.Fatal(err)
	}
	_, proof, err := EncodeResumeSourceProof(f.manifestRaw, scan.Source)
	if err != nil || !bytes.Equal(proof, f.proofRaw) {
		t.Fatal("v2 owner fixture source proof changed original mapping", err)
	}
	f.selected = scan.Frames
	p := f.plan
	f.prefix = BoundedResumePlan{Version: 2, Identity: p.Identity, ManifestSHA256: p.ManifestSHA256, CorpusID: p.CorpusID, GenesisFileSHA256: p.GenesisFileSHA256, SourceProofSHA256: types.Hash(sha256.Sum256(proof)), MappingSHA256: scan.Source.MappingSHA256, RecoveryOuterSHA256: p.RecoveryOuterSHA256, RecoverySHA256: p.RecoverySHA256, AccountVectorSHA256: p.AccountVectorSHA256, OriginalRestoreSHA256: p.OriginalRestoreSHA256, Canonical: p.Canonical, CanonicalNonceSum: p.CanonicalNonceSum, Senders: scan.Source.Senders, NonceEndExclusive: 1000, MaxSourceFrames: scan.SourceFrames, MaxSourceTransactions: scan.SourceTransactions, MaxNewTransactions: scan.NewTransactions, SourceEncodedBytes: scan.SourceEncodedBytes, WhitelistSHA256: scan.WhitelistSHA256}
	_, _, f.prefix.MaxSourceEncodedBytes, err = boundedResumeGeometry(f.descriptor, 1000)
	if err != nil {
		t.Fatal(err)
	}
	f.prefixID, err = BoundedResumePlanID(f.prefix, f.manifestRaw, f.baseline)
	if err != nil {
		t.Fatal(err)
	}
	f.prefixRaw, err = json.Marshal(BoundedResumePlanDocument{Kind: "gov5-bounded-resume-prefix-plan-v2", Plan: f.prefix, PlanID: f.prefixID})
	if err != nil {
		t.Fatal(err)
	}
	for shard := uint16(0); shard < f.descriptor.ShardCount; shard++ {
		raw, err := os.ReadFile(filepath.Join(f.sourceRoot, fmt.Sprintf("shard-%02d.gcf", shard)))
		if err != nil {
			t.Fatal(err)
		}
		reader := bytes.NewReader(raw)
		for reader.Len() > 0 {
			frame, _, err := ReadCorpusFrame(reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			f.original[frame.CorpusFrameID] = frame
		}
	}
	return f
}

func (f *boundedExecutionFixture) prefixOptions() BoundedResumeIngressOptions {
	return BoundedResumeIngressOptions{ManifestRaw: f.manifestRaw, GenesisRaw: f.genesisRaw, SourceRoot: f.sourceRoot, PlanDocumentRaw: f.prefixRaw, SourceProofRaw: f.proofRaw, ExpectedPlanID: f.prefixID, MaxTransactions: 2000, MaxEncodedBytes: 1 << 20}
}

func TestBoundedResumeDiagnosticsExplicitOwnerLifecycle(t *testing.T) {
	f := newBoundedExecutionFixture(t)
	path := filepath.Join(t.TempDir(), "bounded-observer.jsonl")
	options := f.prefixOptions()
	options.DiagnosticsJSONL = path
	if err := f.server.EnableBoundedResumeIngress(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	status, err := f.server.StrictIngressStatus()
	if err != nil || f.server.resumeDiagnostics == nil {
		t.Fatal("bounded observer not installed", err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Count(raw, []byte{'\n'}) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bounded observer did not emit periodic samples")
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.closeOwner()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	if len(rows) < 3 {
		t.Fatal("bounded observer missed periodic or terminal samples")
	}
	for i, row := range rows {
		var sample struct {
			Epoch            [16]byte `json:"epoch"`
			TerminalComplete bool     `json:"terminal_complete"`
		}
		if err := json.Unmarshal(row, &sample); err != nil || sample.Epoch != status.Epoch || sample.TerminalComplete != (i == len(rows)-1) {
			t.Fatalf("bounded observer sample %d invalid: %v", i, err)
		}
	}
	var final struct {
		Complete bool `json:"complete"`
	}
	statusRaw, err := os.ReadFile(path + ".status.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(statusRaw, &final); err != nil || !final.Complete {
		t.Fatalf("bounded observer did not close cleanly: %v", err)
	}
}

func TestBoundedResumeDiagnosticsExistingFileFailsWithoutPublication(t *testing.T) {
	f := newBoundedExecutionFixture(t)
	path := filepath.Join(t.TempDir(), "existing.jsonl")
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	options := f.prefixOptions()
	options.DiagnosticsJSONL = path
	if err := f.server.EnableBoundedResumeIngress(context.Background(), options); err == nil || f.server.ingress != nil || f.server.resumeDiagnostics != nil {
		t.Fatalf("existing observer file did not fail closed: %v", err)
	}
	if actual, err := os.ReadFile(path); err != nil || string(actual) != "existing" {
		t.Fatalf("observer rejection modified existing file: %q, %v", actual, err)
	}
	options.DiagnosticsJSONL = ""
	if err := f.server.EnableBoundedResumeIngress(context.Background(), options); err != nil || f.server.ingress == nil || f.server.resumeDiagnostics != nil {
		t.Fatalf("default-off bounded ingress failed after observer rejection: %v", err)
	}
}

func TestFourFrameResumeRejectsBoundedDiagnosticsPath(t *testing.T) {
	var server Server
	if err := server.EnableResumeIngress(context.Background(), ResumeIngressOptions{DiagnosticsJSONL: "/tmp/not-created.jsonl"}); err == nil {
		t.Fatal("four-frame resume accepted a bounded-only observer path")
	}
}

func executeBoundedFixtureQueue(t *testing.T, f *resumeExecutionFixture, view hotstuff.ViewNumber, expected int) *block.Block {
	t.Helper()
	f.leader.Engine().RestoreState(view, f.lastQC, f.lastQC, 0)
	parent := f.chain.CurrentBlock()
	header := &block.Header{ParentHash: parent.Hash(), Number: uint256.NewInt(parent.Number64().Uint64() + 1), GasLimit: f.genesis.GasLimit, BaseFee: uint256.NewInt(1)}
	if err := f.leader.Prepare(f.chain, header); err != nil {
		t.Fatal(err)
	}
	header.TxHash = block.TxRootAt(nil, header.Time)
	raw, err := block.NewBlock(header, nil).(*block.Block).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.server.StrictIngressStatus()
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(startExecutionSocket(t, f.server), f.server.Identity())
	defer client.Close()
	built, err := client.BuildFromQueue(context.Background(), QueueBuildRequest{Identity: f.server.Identity(), Epoch: status.Epoch, PreparedHash: crypto.Keccak256Hash(raw), PreparedRLP: raw, MaxTransactions: expected, MaxEncodedBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	var candidate block.Block
	if err := candidate.Unmarshal(built.BlockRLP); err != nil || len(candidate.Transactions()) != expected {
		t.Fatal("actual queue build suffix count differs", err)
	}
	return f.sealImportBuilt(t, built.BuildResult)
}

func TestBoundedResumeIngressTwoRoundsRealQCNonrefundableCapsAndPhysicalReopen(t *testing.T) {
	f := newBoundedExecutionFixture(t)
	ctx := context.Background()
	if err := f.server.EnableBoundedResumeIngress(ctx, f.prefixOptions()); err != nil {
		t.Fatal(err)
	}
	status, _ := f.server.StrictIngressStatus()
	socket := startStrictIngressSocket(t, f.server)
	if c, err := DialResumeIngress(ctx, socket, f.server.Identity(), status.ManifestHash, f.prefixID, status.Epoch, 0); err == nil {
		_ = c.Close()
		t.Fatal("GSR1 implicitly upgraded to the bounded prefix protocol")
	}
	if c, err := DialStrictIngress(ctx, socket, f.server.Identity(), status.ManifestHash, 0); err == nil {
		_ = c.Close()
		t.Fatal("GSI1 implicitly upgraded to the bounded prefix protocol")
	}
	clients := make(map[uint16]*ResumeIngressClient)
	for shard := uint16(0); shard < 2; shard++ {
		callerPlan := f.prefix
		callerPlan.Senders = append([]ResumeSender(nil), f.prefix.Senders...)
		client, err := DialBoundedResumeIngress(ctx, socket, callerPlan, f.prefixID, status.Epoch, shard)
		if err != nil {
			t.Fatal(err)
		}
		clients[shard] = client
		defer client.Close()
		callerPlan.Senders[0].Nonce = 999
		if client.boundedPlan.Senders[0].Nonce != f.prefix.Senders[0].Nonce {
			t.Fatal("client fixed canonical prefix aliases caller memory")
		}
	}
	var newTotal int
	for round := uint64(0); round < 2; round++ {
		for _, selected := range f.selected {
			if selected.FirstNonce != round*500 {
				continue
			}
			frame := f.original[selected.ID]
			before, _ := f.server.StrictIngressStatus()
			if _, err := f.server.AdmitResumeFrame(ctx, f.server.Identity(), f.prefixID, status.Epoch, frame); err == nil {
				t.Fatal("v1 direct API admitted a v2 original frame")
			}
			ack, err := clients[frame.Shard].AdmitBounded(ctx, frame, selected)
			if err != nil || ack.Duplicate || ack.NewlyAdmitted != int(selected.NewlyAdmitted) || ack.Lifetime == nil {
				t.Fatalf("original prefix ACK %+v %v", ack, err)
			}
			newTotal += ack.NewlyAdmitted
			if selected.NewlyAdmitted == 0 && (ack.Status.PendingTransactions != before.PendingTransactions || ack.Status.RetainedEncodedBytes != before.RetainedEncodedBytes || ack.Status.CryptoVerified != before.CryptoVerified+500) {
				t.Fatal("all-skipped original frame retained queue credit or skipped signature checks")
			}
		}
		expected := 1417
		if round == 1 {
			expected = 2000
		}
		view := hotstuff.ViewNumber(8 + 7*round)
		b := executeBoundedFixtureQueue(t, f.resumeExecutionFixture, view, expected)
		f.commit(t, b, view, 4)
		f.commit(t, b, view, 5)
		q := f.server.ingress
		q.mu.Lock()
		life := q.resume.window.status()
		q.mu.Unlock()
		drained, _ := f.server.StrictIngressStatus()
		if drained.PendingTransactions != 0 || drained.RetainedEncodedBytes != 0 || life.SourceFrames != (round+1)*4 || life.SourceTransactions != (round+1)*2000 || life.NewTransactions != uint64(newTotal) || life.ReservedFrames != 0 || life.ReservedTransactions != 0 || life.ReservedEncodedBytes != 0 || life.ReservedNew != 0 {
			t.Fatalf("commit refunded lifetime caps or leaked credits %+v %+v", drained, life)
		}
	}
	selected := f.selected[0]
	ack, err := clients[selected.ID.Shard].AdmitBounded(ctx, f.original[selected.ID], selected)
	if err != nil || !ack.Duplicate || ack.NewlyAdmitted != 0 || ack.Lifetime.SourceFrames != 8 || ack.Lifetime.SourceTransactions != 4000 || ack.Lifetime.NewTransactions != 3417 || ack.Lifetime.SourceEncodedBytes != f.prefix.SourceEncodedBytes {
		t.Fatalf("committed receipt recharged full lifetime cap %+v %v", ack, err)
	}
	if newTotal != 3417 {
		t.Fatal("historical583 skip entered the new count")
	}
	for _, client := range clients {
		_ = client.Close()
	}
	f.closeOwner()
	f.open(t)
	if err := f.server.EnableBoundedResumeIngress(ctx, f.prefixOptions()); err == nil {
		t.Fatal("old arming snapshot accepted after real canonical progress")
	}
	snapshot, err := f.chain.ReadPerformanceCanonicalAccounts(ctx, f.senders[:])
	if err != nil || snapshot.Number != 3 || snapshot.Root == (types.Hash{}) || snapshot.Root != snapshot.AppliedRoot {
		t.Fatal("physical prefix reopen root differs", err)
	}
	var sum uint64
	for _, sender := range f.senders {
		account := snapshot.Accounts[sender]
		if account.Nonce != 1000 {
			t.Fatal("second original frame did not survive physical reopen")
		}
		for _, previous := range f.baseline {
			if previous.Address == sender {
				expected, _ := uint256.FromDecimal(previous.Balance)
				expected.Sub(expected, uint256.NewInt((1000-previous.Nonce)*21001))
				if !account.Balance.Eq(expected) || !snapshot.Exists[sender] || account.CodeHash != previous.CodeHash {
					t.Fatal("physical prefix sender balance/code/exists delta differs")
				}
			}
		}
		sum += account.Nonce
	}
	if sum != 4000 {
		t.Fatal("physical prefix total differs")
	}
}

func TestBoundedResumeKnownZeroCreditBusyLeavesLifetimeUnchanged(t *testing.T) {
	f := newBoundedExecutionFixture(t)
	options := f.prefixOptions()
	options.MaxTransactions = 500
	if err := f.server.EnableBoundedResumeIngress(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	status, _ := f.server.StrictIngressStatus()
	var accepted, blocked BoundedResumeFrame
	for _, selected := range f.selected {
		if selected.FirstNonce == 0 && selected.SenderIndex == 0 {
			accepted = selected
		}
		if selected.FirstNonce == 0 && selected.SenderIndex == 1 {
			blocked = selected
		}
	}
	if _, err := f.server.AdmitBoundedResumeFrame(context.Background(), f.server.Identity(), f.prefixID, status.Epoch, f.original[accepted.ID]); err != nil {
		t.Fatal(err)
	}
	q := f.server.ingress
	before := *q.resume.window.status()
	if _, err := f.server.AdmitBoundedResumeFrame(context.Background(), f.server.Identity(), f.prefixID, status.Epoch, f.original[blocked.ID]); !errors.Is(err, ErrIngressCreditsExhausted) {
		t.Fatal("full queue did not return a typed known-zero Busy", err)
	}
	after := *q.resume.window.status()
	if before != after || after.SourceFrames != 1 || after.SourceTransactions != 500 || after.NewTransactions != 500 || after.ReservedFrames != 0 {
		t.Fatal("known-zero Busy charged a unique original frame")
	}
}

func TestBoundedResumeLegacyOwnerCannotUpgradeToGSR2(t *testing.T) {
	f := newBoundedExecutionFixture(t)
	if err := f.server.EnableResumeIngress(context.Background(), f.options()); err != nil {
		t.Fatal(err)
	}
	status, _ := f.server.StrictIngressStatus()
	socket := startStrictIngressSocket(t, f.server)
	if client, err := DialBoundedResumeIngress(context.Background(), socket, f.prefix, f.prefixID, status.Epoch, 0); err == nil {
		_ = client.Close()
		t.Fatal("old four-frame owner implicitly accepted GSR2")
	}
	if _, err := f.server.AdmitBoundedResumeFrame(context.Background(), f.server.Identity(), f.planID, status.Epoch, f.frames[0]); err == nil {
		t.Fatal("bounded direct API implicitly upgraded a four-frame session")
	}
}

// Test-only source construction deliberately binds a wrong sender at nonce5
// in a skipped prefix. Full source hashing and the nonce0 mapping remain valid;
// only real online authentication of the entire original frame can reject it.
func poisonBoundedFixtureSkippedSignature(t *testing.T, f *boundedExecutionFixture, sender uint32) BoundedResumeFrame {
	t.Helper()
	var selected BoundedResumeFrame
	for _, candidate := range f.selected {
		if candidate.SenderIndex == sender && candidate.FirstNonce == 0 {
			selected = candidate
		}
	}
	frame := f.original[selected.ID]
	var raws [][]byte
	if err := WalkCorpusPayload(frame.Payload, frame.Transactions, func(raw []byte) error {
		raws = append(raws, append([]byte(nil), raw...))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	to := CorpusRecipient(uint32(uint64(sender)*f.descriptor.NoncesPerSender+5), f.descriptor.RecipientCount)
	wrong, err := transaction.SignNewTx(f.private[(sender+1)%4], transaction.NewEIP155Signer(f.genesis.Config.ChainID), &transaction.LegacyTx{Nonce: 5, To: &to, Gas: 21000, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	raws[5], err = wrong.EthEncoded()
	if err != nil {
		t.Fatal(err)
	}
	frame, err = MakeCorpusFrame(selected.ID, raws)
	if err != nil {
		t.Fatal(err)
	}
	f.original[selected.ID] = frame
	m, err := DecodeCorpusManifest(f.manifestRaw)
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	for seq := uint64(0); seq < m.Shards[selected.ID.Shard].Frames; seq++ {
		id := CorpusFrameID{CorpusID: m.CorpusID, Shard: selected.ID.Shard, Sequence: seq}
		if err := WriteCorpusFrame(&encoded, f.original[id]); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.sourceRoot, m.Shards[selected.ID.Shard].File), encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	m.Shards[selected.ID.Shard].Bytes = uint64(encoded.Len())
	m.Shards[selected.ID.Shard].SHA256 = types.Hash(sha256.Sum256(encoded.Bytes()))
	f.manifestRaw, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	scan, err := ScanBoundedResumeSources(context.Background(), f.sourceRoot, f.manifestRaw, f.genesisRaw, f.baseline, 1000)
	if err != nil {
		t.Fatal("offline nonce0 mapping should pass before online skipped-signature rejection", err)
	}
	_, f.proofRaw, err = EncodeResumeSourceProof(f.manifestRaw, scan.Source)
	if err != nil {
		t.Fatal(err)
	}
	f.prefix.ManifestSHA256 = types.Hash(sha256.Sum256(f.manifestRaw))
	f.prefix.SourceProofSHA256 = types.Hash(sha256.Sum256(f.proofRaw))
	f.prefix.SourceEncodedBytes, f.prefix.WhitelistSHA256 = scan.SourceEncodedBytes, scan.WhitelistSHA256
	f.prefixID, err = BoundedResumePlanID(f.prefix, f.manifestRaw, f.baseline)
	if err != nil {
		t.Fatal(err)
	}
	f.prefixRaw, err = json.Marshal(BoundedResumePlanDocument{Kind: "gov5-bounded-resume-prefix-plan-v2", Plan: f.prefix, PlanID: f.prefixID})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range scan.Frames {
		if candidate.ID == selected.ID {
			return candidate
		}
	}
	t.Fatal("original selected frame disappeared")
	return BoundedResumeFrame{}
}

func TestBoundedResumeAuthenticatesSkippedSignaturesBeforeAnyPublication(t *testing.T) {
	for _, sender := range []uint32{1, 3} {
		t.Run(fmt.Sprint(sender), func(t *testing.T) {
			f := newBoundedExecutionFixture(t)
			selected := poisonBoundedFixtureSkippedSignature(t, f, sender)
			if err := f.server.EnableBoundedResumeIngress(context.Background(), f.prefixOptions()); err != nil {
				t.Fatal(err)
			}
			status, _ := f.server.StrictIngressStatus()
			if _, err := f.server.AdmitBoundedResumeFrame(context.Background(), f.server.Identity(), f.prefixID, status.Epoch, f.original[selected.ID]); err == nil {
				t.Fatal("skipped original signature bypassed online authentication")
			}
			q := f.server.ingress
			q.mu.Lock()
			life := q.resume.window.status()
			receipts, lanes := len(q.resume.receipts), len(q.lanes)
			q.mu.Unlock()
			after, _ := f.server.StrictIngressStatus()
			if after.NewlyAdmitted != 0 || after.PendingTransactions != 0 || after.RetainedEncodedBytes != 0 || after.ReservedTransactions != 0 || after.ReservedEncodedBytes != 0 || life.SourceFrames != 0 || life.SourceTransactions != 0 || life.NewTransactions != 0 || life.ReservedFrames != 0 || life.ReservedTransactions != 0 || life.ReservedEncodedBytes != 0 || life.ReservedNew != 0 || receipts != 0 || lanes != 0 {
				t.Fatal("bad skipped signature left a receipt, lane or charged lifetime budget")
			}
		})
	}
}

func TestBoundedResumeIngressLostPublishedFinalAckNeverReplays(t *testing.T) {
	f := newBoundedExecutionFixture(t)
	ctx := context.Background()
	if err := f.server.EnableBoundedResumeIngress(ctx, f.prefixOptions()); err != nil {
		t.Fatal(err)
	}
	status, _ := f.server.StrictIngressStatus()
	upstream := startStrictIngressSocket(t, f.server)
	proxyPath := filepath.Join(t.TempDir(), "ack-loss.sock")
	proxy, err := listenPrivateExecutionUnix(proxyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	done := make(chan error, 1)
	go func() {
		front, err := proxy.Accept()
		if err != nil {
			done <- err
			return
		}
		defer front.Close()
		back, err := net.Dial("unix", upstream)
		if err != nil {
			done <- err
			return
		}
		defer back.Close()
		_ = front.SetDeadline(time.Now().Add(10 * time.Second))
		_ = back.SetDeadline(time.Now().Add(10 * time.Second))
		run := func() error {
			var magic [4]byte
			if _, err := io.ReadFull(front, magic[:]); err != nil {
				return err
			}
			if err := writeIngressBytes(back, magic[:]); err != nil {
				return err
			}
			var hello resumeHello
			if err := readResumeMetadata(front, &hello); err != nil {
				return err
			}
			if err := writeIngressMetadata(back, hello); err != nil {
				return err
			}
			var reply resumeReply
			if err := readResumeMetadata(back, &reply); err != nil {
				return err
			}
			if err := writeIngressMetadata(front, reply); err != nil {
				return err
			}
			var header [CorpusWireHeaderSize]byte
			if _, err := io.ReadFull(front, header[:]); err != nil {
				return err
			}
			if err := writeIngressBytes(back, header[:]); err != nil {
				return err
			}
			if err := readResumeMetadata(back, &reply); err != nil {
				return err
			}
			if reply.Kind != "grant" {
				return fmt.Errorf("expected grant got %s", reply.Kind)
			}
			if err := writeIngressMetadata(front, reply); err != nil {
				return err
			}
			body := make([]byte, f.selected[0].PayloadBytes)
			if _, err := io.ReadFull(front, body); err != nil {
				return err
			}
			if err := writeIngressBytes(back, body); err != nil {
				return err
			}
			if err := readResumeMetadata(back, &reply); err != nil {
				return err
			}
			if reply.Kind != "admitted" || reply.Ack.NewlyAdmitted != 500 {
				return errors.New("owner did not actually publish before ACK loss")
			}
			return nil
		}
		done <- run()
	}()
	client, err := DialBoundedResumeIngress(ctx, proxyPath, f.prefix, f.prefixID, status.Epoch, f.frames[0].Shard)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	selected := f.selected[0]
	if _, err := client.AdmitBounded(ctx, f.frames[0], selected); !errors.Is(err, ErrIngressUnknownOutcome) {
		t.Fatalf("lost published ACK %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := client.AdmitBounded(ctx, f.frames[0], selected); !errors.Is(err, ErrIngressUnknownOutcome) {
		t.Fatal("poisoned connection replayed", err)
	}
	q := f.server.ingress
	q.mu.Lock()
	life := q.resume.window.status()
	q.mu.Unlock()
	if life.SourceFrames != 1 || life.SourceTransactions != 500 || life.NewTransactions != 500 || life.ReservedFrames != 0 || life.ReservedNew != 0 {
		t.Fatal("unknown outcome refunded or replayed lifetime budget")
	}
	actual, _ := f.server.StrictIngressStatus()
	if actual.NewlyAdmitted != 500 || actual.PendingTransactions != 500 || actual.CryptoVerified != 500 || actual.ReservedTransactions != 0 || actual.ReservedEncodedBytes != 0 || len(f.server.ingress.resume.receipts) != 1 {
		t.Fatalf("unknown outcome was replayed or revoked %+v", actual)
	}
}

func TestBoundedResumeIngressConcurrentSameFrameAndStaleStampReleaseLifetime(t *testing.T) {
	f := newBoundedExecutionFixture(t)
	ctx := context.Background()
	if err := f.server.EnableBoundedResumeIngress(ctx, f.prefixOptions()); err != nil {
		t.Fatal(err)
	}
	status, _ := f.server.StrictIngressStatus()
	frame := f.frames[1]
	// Reserve twice before either publication, precisely the concurrent first
	// request boundary. No source/identity validation is replaced by this seam.
	first, err := f.server.reserveResumeIngress(f.server.ingress, f.prefixID, status.Epoch, frame, len(frame.Payload))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.server.reserveResumeIngress(f.server.ingress, f.prefixID, status.Epoch, frame, len(frame.Payload))
	if err != nil {
		first.release()
		t.Fatal(err)
	}
	defer first.release()
	defer second.release()
	type result struct {
		ack ResumeIngressAck
		err error
	}
	results := make(chan result, 2)
	for _, reservation := range []*resumeReservation{first, second} {
		go func(r *resumeReservation) {
			ack, err := f.server.verifyReservedResume(ctx, f.server.ingress, frame, r)
			results <- result{ack, err}
		}(reservation)
	}
	newTotal, duplicates := 0, 0
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		newTotal += got.ack.NewlyAdmitted
		if got.ack.Duplicate {
			duplicates++
			if got.ack.NewlyAdmitted != 0 || got.ack.PriorAdmission != 459 {
				t.Fatalf("duplicate receipt %+v", got.ack)
			}
		}
	}
	if newTotal != 459 || duplicates != 1 {
		t.Fatal("concurrent frame published twice")
	}
	after, _ := f.server.StrictIngressStatus()
	if after.NewlyAdmitted != 459 || after.PendingTransactions != 459 || after.ReservedTransactions != 0 || after.ReservedEncodedBytes != 0 || after.RetainedEncodedBytes != len(frame.Payload) || after.CryptoVerified != 1000 || f.server.ingress.resume.canonicalSkipped != 41 || len(f.server.ingress.resume.receipts) != 1 {
		t.Fatalf("concurrent publication leaked credits/counters %+v", after)
	}
	// A changed generation may become clean before old verification returns.
	// The stale publication must still return exact zero and release both budgets.
	freshFrame := f.frames[2]
	reservation, err := f.server.reserveResumeIngress(f.server.ingress, f.prefixID, status.Epoch, freshFrame, len(freshFrame.Payload))
	if err != nil {
		t.Fatal(err)
	}
	f.server.ingress.mu.Lock()
	f.server.ingress.stamp.generation++
	f.server.ingress.mu.Unlock()
	_, err = f.server.verifyReservedResume(ctx, f.server.ingress, freshFrame, reservation)
	reservation.release()
	if !errors.Is(err, ErrIngressSnapshotChanged) {
		t.Fatalf("stale canonical generation %v", err)
	}
	q := f.server.ingress
	q.mu.Lock()
	life := q.resume.window.status()
	q.mu.Unlock()
	if life.SourceFrames != 1 || life.SourceTransactions != 500 || life.NewTransactions != 459 || life.ReservedFrames != 0 || life.ReservedTransactions != 0 || life.ReservedEncodedBytes != 0 || life.ReservedNew != 0 {
		t.Fatalf("concurrent duplicate or stale stamp changed life budget %+v", life)
	}
	final, _ := f.server.StrictIngressStatus()
	if final.NewlyAdmitted != after.NewlyAdmitted || final.PendingTransactions != after.PendingTransactions || final.RetainedEncodedBytes != after.RetainedEncodedBytes || final.ReservedTransactions != 0 || final.ReservedEncodedBytes != 0 || len(f.server.ingress.resume.receipts) != 1 {
		t.Fatal("stale frame partially published")
	}
}
