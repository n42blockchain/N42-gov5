package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

// Same versioned derivation as the supply corpus. Only fresh queue fixtures use
// it; the original functional fixture and its stored keys remain unchanged.
func queueFixtureKey(seed []byte, index uint32) (*ecdsa.PrivateKey, error) {
	if len(seed) != 32 {
		return nil, errors.New("queue fixture requires a 32-byte seed")
	}
	var encoded [8]byte
	binary.LittleEndian.PutUint32(encoded[:4], index)
	for retry := uint32(0); retry < 1024; retry++ {
		binary.LittleEndian.PutUint32(encoded[4:], retry)
		h := hmac.New(sha256.New, seed)
		h.Write([]byte(sharedexec.CorpusKeyDerivation))
		h.Write(encoded[:])
		if key, err := crypto.ToECDSA(h.Sum(nil)); err == nil {
			return key, nil
		}
	}
	return nil, errors.New("queue fixture seed scalar derivation failed")
}
func queueRecipient(index uint32) types.Address {
	var encoded [4]byte
	binary.LittleEndian.PutUint32(encoded[:], index)
	return types.BytesToAddress(crypto.Keccak256([]byte(sharedexec.CorpusRecipientRule), encoded[:])[12:])
}
func prepareQueueFixture(m manifest, g *conf.Genesis, seed []byte, keys []string) error {
	if len(keys) != 4 || len(seed) != 32 {
		return errors.New("queue fixture requires four fresh senders")
	}
	d := sharedexec.CorpusDescriptor{Version: sharedexec.CorpusVersion, Identity: m.Identity, GenesisFileHash: types.HexToHash("0x" + m.GenesisSHA256), Codec: sharedexec.CorpusCodec, Signature: sharedexec.CorpusSignature, KeyDerivation: sharedexec.CorpusKeyDerivation, SeedCommitment: types.Hash(sha256.Sum256(seed)), SenderCount: 4, NoncesPerSender: 2, RecipientRule: sharedexec.CorpusRecipientRule, RecipientCount: 4, ShardCount: 4, FrameTransactions: 2}
	id, err := d.ID()
	if err != nil {
		return err
	}
	corpus := sharedexec.CorpusManifest{Descriptor: d, CorpusID: id}
	dir := filepath.Join(m.Root, "load", "queue")
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	recipients := map[types.Address]bool{}
	for i, encoded := range keys {
		key, err := queueFixtureKey(seed, uint32(i))
		if err != nil {
			return err
		}
		if hex.EncodeToString(crypto.FromECDSA(key)) != encoded {
			return errors.New("persisted queue key differs from seed derivation")
		}
		sender := crypto.PubkeyToAddress(key.PublicKey)
		funded, ok := g.Alloc[sender]
		if !ok || funded.Nonce != 0 {
			return errors.New("queue sender must have funded genesis nonce zero")
		}
		to := queueRecipient(uint32(i))
		if recipients[to] || to == (types.Address{}) {
			return errors.New("invalid queue recipient set")
		}
		if _, ok := g.Alloc[to]; ok {
			return errors.New("queue recipient overlaps sender")
		}
		recipients[to] = true
		for _, validator := range m.Validators {
			if strings.EqualFold(to.Hex(), validator.Address) {
				return errors.New("queue recipient overlaps validator")
			}
		}
		var raws [][]byte
		signer := transaction.NewEIP155Signer(g.Config.ChainID)
		for nonce := uint64(0); nonce < 2; nonce++ {
			to = queueRecipient(uint32((uint64(i)*d.NoncesPerSender + nonce - d.NonceStart) % uint64(d.RecipientCount)))
			tx, err := transaction.SignNewTx(key, signer, &transaction.LegacyTx{Nonce: nonce, To: &to, Value: uint256.NewInt(1), Gas: 21000, GasPrice: uint256.NewInt(1)})
			if err != nil {
				return err
			}
			got, err := signer.Sender(tx)
			if err != nil || got != sender {
				return errors.New("queue transaction direct signature verification failed")
			}
			raw, err := tx.EthEncoded()
			if err != nil {
				return err
			}
			raws = append(raws, raw)
		}
		frame, err := sharedexec.MakeCorpusFrame(sharedexec.CorpusFrameID{CorpusID: id, Shard: uint16(i)}, raws)
		if err != nil {
			return err
		}
		var wire bytes.Buffer
		if err := sharedexec.WriteCorpusFrame(&wire, frame); err != nil {
			return err
		}
		name := fmt.Sprintf("shard-%02d.gcf", i)
		if err := writePrivate(filepath.Join(dir, name), wire.Bytes()); err != nil {
			return err
		}
		corpus.Shards = append(corpus.Shards, sharedexec.CorpusShard{Index: uint16(i), File: name, Transactions: 2, Frames: 1, Bytes: uint64(wire.Len()), SHA256: types.Hash(sha256.Sum256(wire.Bytes()))})
	}
	if err := corpus.Validate(); err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(dir, "manifest.json"), corpus); err != nil {
		return err
	}
	configs := filepath.Join(m.Root, "queue-configs")
	if err := os.Mkdir(configs, 0700); err != nil {
		return err
	}
	for _, v := range m.Validators {
		var cfg map[string]any
		if err := readFixtureJSON(v.Config, &cfg); err != nil {
			return err
		}
		node, ok := cfg["node"].(map[string]any)
		if !ok {
			return errors.New("invalid original node fixture")
		}
		node["performance_owner_queue"] = true
		node["performance_queue_max_transactions"] = sharedvalidator.DefaultOwnerQueueTransactions
		node["performance_queue_max_encoded_bytes"] = sharedexec.MaxQueueBuildEncodedBytes
		if err := writePrivate(filepath.Join(configs, fmt.Sprintf("v%d.json", v.Index)), cfg); err != nil {
			return err
		}
	}
	return nil
}

// Validate complete immutable shard files before any network admission. This
// checker only supports the fresh, eight-transaction functional fixture.
func readQueueCorpus(m manifest) (sharedexec.CorpusManifest, types.Hash, []sharedexec.CorpusFrame, error) {
	path := filepath.Join(m.Root, "load", "queue", "manifest.json")
	var corpus sharedexec.CorpusManifest
	if err := readFixtureJSON(path, &corpus); err != nil {
		return corpus, types.Hash{}, nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return corpus, types.Hash{}, nil, err
	}
	if _, err := sharedexec.DecodeCorpusManifest(raw); err != nil {
		return corpus, types.Hash{}, nil, err
	}
	d := corpus.Descriptor
	if d.Identity != m.Identity || d.GenesisFileHash != types.HexToHash("0x"+m.GenesisSHA256) || d.SenderCount != 4 || d.NoncesPerSender != 2 || d.NonceStart != 0 || d.ShardCount != 4 || d.FrameTransactions != 2 {
		return corpus, types.Hash{}, nil, errors.New("unexpected queue functional corpus")
	}
	frames := make([]sharedexec.CorpusFrame, 0, 4)
	for _, shard := range corpus.Shards {
		path := filepath.Join(m.Root, "load", "queue", shard.File)
		info, err := os.Lstat(path)
		if err != nil {
			return corpus, types.Hash{}, nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 8192 || uint64(info.Size()) != shard.Bytes {
			return corpus, types.Hash{}, nil, errors.New("queue shard bounded size mismatch")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return corpus, types.Hash{}, nil, err
		}
		if types.Hash(sha256.Sum256(raw)) != shard.SHA256 {
			return corpus, types.Hash{}, nil, errors.New("queue shard SHA mismatch")
		}
		reader := bytes.NewReader(raw)
		frame, _, err := sharedexec.ReadCorpusFrame(reader, nil)
		if err != nil {
			return corpus, types.Hash{}, nil, err
		}
		if err := frame.Validate(d); err != nil {
			return corpus, types.Hash{}, nil, err
		}
		if frame.Shard != shard.Index || frame.Sequence != 0 || reader.Len() != 0 {
			return corpus, types.Hash{}, nil, errors.New("queue shard frame mismatch")
		}
		frames = append(frames, frame)
	}
	return corpus, types.Hash(sha256.Sum256(raw)), frames, nil
}
func queueEmpty(s sharedexec.StrictIngressStatus) bool {
	return !s.Reconciling && queueCreditsEmpty(s)
}
func queueCreditsEmpty(s sharedexec.StrictIngressStatus) bool {
	return s.PendingTransactions == 0 && s.ReservedTransactions == 0 && s.RetainedEncodedBytes == 0 && s.ReservedEncodedBytes == 0
}

func waitQueueRecovery(ctx context.Context, manifest types.Hash, probe func(context.Context) (sharedexec.StrictIngressStatus, error)) (sharedexec.StrictIngressStatus, error) {
	work, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var epoch [16]byte
	for {
		status, err := probe(work)
		if err != nil {
			return status, err
		}
		if epoch == ([16]byte{}) {
			epoch = status.Epoch
		}
		if epoch == ([16]byte{}) || status.Epoch != epoch || status.ManifestHash != manifest || status.NewlyAdmitted != 0 || status.CryptoVerified != 0 || !queueCreditsEmpty(status) {
			return status, errors.New("zero-admission owner recovery binding changed; never blindly replay nonce zero")
		}
		if !status.Reconciling {
			return status, nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-work.Done():
			timer.Stop()
			return status, fmt.Errorf("owner recovery blocked at canonical %d/%s; no transactions sent: %w", status.CanonicalNumber, status.CanonicalHash, work.Err())
		case <-timer.C:
		}
	}
}

type queueAdmissionRetries struct {
	Busy     int `json:"busy"`
	Snapshot int `json:"snapshot"`
}

// Only typed, known zero-admission outcomes permit another send of this exact
// frame on the same connection. A missing final ACK is never retried.
func admitQueueFrame(ctx context.Context, frame sharedexec.CorpusFrame, timeout time.Duration, admit func(context.Context, sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error)) (sharedexec.StrictIngressAck, queueAdmissionRetries, error) {
	work, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var retries queueAdmissionRetries
	for {
		ack, err := admit(work, frame)
		if err == nil {
			return ack, retries, nil
		}
		if !errors.Is(err, sharedexec.ErrIngressCreditsExhausted) && !errors.Is(err, sharedexec.ErrIngressSnapshotChanged) {
			return ack, retries, err
		}
		if ack.NewlyAdmitted != 0 || ack.Duplicates != 0 {
			return ack, retries, errors.New("retry carried nonzero admission; refusing replay")
		}
		if errors.Is(err, sharedexec.ErrIngressCreditsExhausted) {
			retries.Busy++
		} else {
			retries.Snapshot++
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-work.Done():
			timer.Stop()
			return ack, retries, fmt.Errorf("bounded zero-admission retry stopped: %w", work.Err())
		case <-timer.C:
		}
	}
}

func privateQueueSocket(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return errors.New("queue socket must exist without aliases")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		return errors.New("queue socket must be private and owned")
	}
	return nil
}

func queueSmokeFixture(ctx context.Context, path, base string) error {
	root := filepath.Dir(path)
	if !strings.HasPrefix(root, base+string(filepath.Separator)) || path != filepath.Join(root, "run.json") {
		return errors.New("explicit dedicated queue fixture required")
	}
	var m manifest
	if err := readFixtureJSON(path, &m); err != nil {
		return err
	}
	if m.SchemaVersion != 1 || m.Root != root || len(m.Validators) != 7 || m.OwnerSocket != filepath.Join(root, "e", "execution-rpc", "execution.sock") || m.Genesis != filepath.Join(root, "genesis.json") || m.Transactions != "ethereum-secp256k1" || m.GasPrice != "1" {
		return errors.New("invalid queue topology")
	}
	resultPath := filepath.Join(root, "queue-smoke-result.json")
	if _, err := os.Lstat(resultPath); !os.IsNotExist(err) {
		return errors.New("queue result is never overwritten")
	}
	var g conf.Genesis
	if err := readFixtureJSON(m.Genesis, &g); err != nil {
		return err
	}
	genesisRaw, err := os.ReadFile(m.Genesis)
	if err != nil {
		return err
	}
	if types.Hash(sha256.Sum256(genesisRaw)) != types.HexToHash("0x"+m.GenesisSHA256) || g.Config == nil || g.Config.Performance == nil || g.Config.ChainID == nil {
		return errors.New("queue genesis file binding invalid")
	}
	genesis, _, err := (&internal.GenesisBlock{GenesisConfig: &g}).ToBlock()
	if err != nil {
		return err
	}
	if m.Identity != (sharedexec.Identity{Version: 1, Namespace: g.Config.Performance.NetworkNamespace, ChainID: g.Config.ChainID.String(), GenesisHash: genesis.Hash()}) {
		return errors.New("queue actual genesis identity mismatch")
	}
	_, digest, frames, err := readQueueCorpus(m)
	if err != nil {
		return err
	}
	if err := privateQueueSocket(m.OwnerSocket); err != nil {
		return err
	}
	if err := privateQueueSocket(filepath.Join(root, "e", "execution-rpc", "ingress.sock")); err != nil {
		return err
	}
	owner := sharedexec.NewClient(m.OwnerSocket, m.Identity)
	defer owner.Close()
	recoveryStart := time.Now()
	before, err := waitQueueRecovery(ctx, digest, owner.StrictIngressStatus)
	recoveryWait := time.Since(recoveryStart)
	if err != nil {
		return err
	}
	if before.ManifestHash != digest || before.NewlyAdmitted != 0 || before.CryptoVerified != 0 || !queueEmpty(before) {
		return errors.New("fresh empty owner queue required; never blindly replay nonce zero")
	}
	clients := make([]*sharedvalidator.APIClient, 7)
	defer func() {
		for _, c := range clients {
			if c != nil {
				c.Close()
			}
		}
	}()
	initial := make([]sharedvalidator.StatusResult, 7)
	for i, v := range m.Validators {
		if v.Index != i || v.APISocket != filepath.Join(root, fmt.Sprintf("v%d", i), "validator-rpc", "validator.sock") {
			return errors.New("queue validator socket mismatch")
		}
		if err := privateQueueSocket(v.APISocket); err != nil {
			return err
		}
		clients[i] = sharedvalidator.NewAPIClient(v.APISocket, m.Identity)
	}
	// Existing empty-block history is allowed, but never infer unused nonces
	// from an empty volatile owner queue alone. Establish one common zero-count
	// canonical head on all seven metadata stores and the owner before sending.
	for {
		for i, c := range clients {
			initial[i], err = c.Status(ctx)
			if err != nil {
				return err
			}
			b := initial[i].OwnerQueue
			if initial[i].CommittedExecutedTransactions != 0 || b == nil || b.Epoch != before.Epoch || b.ManifestHash != digest {
				return errors.New("zero-count validators with identical owner binding required; refusing nonce-zero replay")
			}
		}
		current, err := owner.StrictIngressStatus(ctx)
		if err != nil {
			return err
		}
		if current.Epoch != before.Epoch || current.ManifestHash != digest || current.NewlyAdmitted != 0 || current.CryptoVerified != 0 || !queueCreditsEmpty(current) {
			return errors.New("owner changed before zero-count canonical baseline")
		}
		converged := !current.Reconciling
		for _, s := range initial {
			if s.CanonicalProgress.Number != current.CanonicalNumber || s.CanonicalProgress.BlockHash != current.CanonicalHash {
				converged = false
			}
		}
		if converged {
			before = current
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	var acks []sharedexec.StrictIngressAck
	var admissionRetries []queueAdmissionRetries
	for _, frame := range frames {
		c, err := sharedexec.DialStrictIngress(ctx, filepath.Join(root, "e", "execution-rpc", "ingress.sock"), m.Identity, digest, frame.Shard)
		if err != nil {
			return err
		}
		if c.Epoch() != before.Epoch {
			c.Close()
			return errors.New("owner epoch changed before first frame send")
		}
		ack, retries, err := admitQueueFrame(ctx, frame, 5*time.Second, c.Admit)
		c.Close()
		if err != nil {
			return fmt.Errorf("queue admission stopped without replay: %w", err)
		}
		if ack.Status.Epoch != before.Epoch || ack.NewlyAdmitted != 2 || ack.Duplicates != 0 {
			return errors.New("fresh queue frame did not admit exactly two new transactions")
		}
		acks = append(acks, ack)
		admissionRetries = append(admissionRetries, retries)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		final := make([]sharedvalidator.StatusResult, 7)
		converged := true
		for i, c := range clients {
			final[i], err = c.Status(ctx)
			if err != nil {
				return err
			}
			if final[i].CommittedExecutedTransactions != 8 || (i > 0 && final[i].CanonicalProgress != final[0].CanonicalProgress) {
				converged = false
			}
		}
		after, err := owner.StrictIngressStatus(ctx)
		if err != nil {
			return err
		}
		if after.Epoch != before.Epoch || after.ManifestHash != digest {
			return errors.New("owner volatile epoch changed during queue check")
		}
		if converged && queueEmpty(after) && after.NewlyAdmitted == 8 && after.CryptoVerified >= 8 && after.CanonicalHash == final[0].CanonicalProgress.BlockHash && after.CanonicalNumber == final[0].CanonicalProgress.Number {
			return writePrivate(resultPath, map[string]any{"kind": "owner-queue-functional-check-not-tps", "identity": m.Identity, "genesis_sha256": m.GenesisSHA256, "manifest_sha256": digest, "unique_committed_executed_transactions": 8, "initial": initial, "final": final, "owner_initial": before, "owner_final": after, "admission_acks": acks, "admission_retries": admissionRetries, "startup_recovery_wait_ns": recoveryWait.Nanoseconds(), "crypto_verified_counts_attempts": true, "execution_trust": "accepted-trusted-execution-source", "build_then_import_executes_twice": true})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
