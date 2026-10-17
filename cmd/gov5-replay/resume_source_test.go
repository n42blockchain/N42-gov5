package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/params"
)

type resumeSourceFixture struct {
	root     string
	manifest supplyManifest
	genesis  []byte
	baseline []sharedexec.CanonicalAccount
	keys     []*ecdsa.PrivateKey
}

// Fixture signing is test-only. Production scanning never reads a seed or key.
// This account vector is synthetic, not evidence of canonical state execution.
func makeResumeSourceFixture(t *testing.T) resumeSourceFixture {
	t.Helper()
	f := resumeSourceFixture{root: t.TempDir(), keys: make([]*ecdsa.PrivateKey, 4)}
	seed := make([]byte, 32)
	for candidate := 1; ; candidate++ {
		seed[0] = byte(candidate)
		addresses := make([]types.Address, 4)
		for i := range f.keys {
			key, err := supplyKey(seed, uint32(i))
			if err != nil {
				t.Fatal(err)
			}
			f.keys[i], addresses[i] = key, crypto.PubkeyToAddress(key.PublicKey)
		}
		if !sort.SliceIsSorted(addresses, func(i, j int) bool { return bytes.Compare(addresses[i][:], addresses[j][:]) < 0 }) {
			break
		}
		if candidate == 255 {
			t.Fatal("could not choose non-address-ordered public fixture")
		}
	}
	raw, err := os.ReadFile("../../scripts/gov5-perf/isolated-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(raw, &genesis); err != nil {
		t.Fatal(err)
	}
	genesis.Config.HotStuff.Validators = nil
	for i := 0; i < 7; i++ {
		key, err := bls.RandKey()
		if err != nil {
			t.Fatal(err)
		}
		pub := key.PublicKey().Marshal()
		genesis.Config.HotStuff.Validators = append(genesis.Config.HotStuff.Validators, params.HotStuffValidatorConfig{Address: types.BytesToAddress(crypto.Keccak256(pub)[12:]).Hex(), BLSKey: "0x" + hex.EncodeToString(pub)})
	}
	genesis.Alloc = make(conf.GenesisAlloc)
	prefixes := []uint64{0, 41, 42, 500}
	for i, key := range f.keys {
		address := crypto.PubkeyToAddress(key.PublicKey)
		genesis.Alloc[address] = conf.GenesisAccount{Balance: "0x100000000000000000"}
		balance, _ := uint256.FromHex(genesis.Alloc[address].Balance)
		balance.Sub(balance, uint256.NewInt(prefixes[i]*21001))
		f.baseline = append(f.baseline, sharedexec.CanonicalAccount{Address: address, Exists: true, Nonce: prefixes[i], Balance: balance.ToBig().String(), CodeHash: crypto.Keccak256Hash(nil)})
	}
	sort.Slice(f.baseline, func(i, j int) bool { return bytes.Compare(f.baseline[i].Address[:], f.baseline[j].Address[:]) < 0 })
	encoded, err := json.Marshal(&genesis)
	if err != nil {
		t.Fatal(err)
	}
	// Match prepareCorpus's actual file codec: Genesis accepts hex bytes,
	// while the default []byte JSON encoder would emit base64 extraData.
	var document map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	document["extraData"], err = json.Marshal("0x" + hex.EncodeToString(genesis.ExtraData))
	if err != nil {
		t.Fatal(err)
	}
	f.genesis, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(f.genesis, &genesis); err != nil {
		t.Fatal("actual fixture genesis bytes do not decode:", err)
	}
	b, _, err := (&internal.GenesisBlock{GenesisConfig: &genesis}).ToBlock()
	if err != nil {
		t.Fatal(err)
	}
	d := sharedexec.CorpusDescriptor{Version: 1, Identity: sharedexec.Identity{Version: 1, Namespace: params.PerformanceNamespace, ChainID: "4205001", GenesisHash: b.Hash()}, GenesisFileHash: types.Hash(sha256.Sum256(f.genesis)), Codec: sharedexec.CorpusCodec, Signature: sharedexec.CorpusSignature, KeyDerivation: sharedexec.CorpusKeyDerivation, SeedCommitment: types.Hash(sha256.Sum256(seed)), SenderCount: 4, NoncesPerSender: 1000, RecipientRule: sharedexec.CorpusRecipientRule, RecipientCount: 17, ShardCount: 2, FrameTransactions: 500}
	id, err := d.ID()
	if err != nil {
		t.Fatal(err)
	}
	m := sharedexec.CorpusManifest{Descriptor: d, CorpusID: id}
	for shard := uint16(0); shard < 2; shard++ {
		meta, err := prepareShard(context.Background(), f.root, d, id, shard, f.keys, new(prepareCost))
		if err != nil {
			t.Fatal(err)
		}
		m.Shards = append(m.Shards, meta)
	}
	if err := os.WriteFile(filepath.Join(f.root, "genesis.json"), f.genesis, 0600); err != nil {
		t.Fatal(err)
	}
	f.manifest = persistResumeTestManifest(t, f.root, m)
	return f
}

func persistResumeTestManifest(t *testing.T, root string, m sharedexec.CorpusManifest) supplyManifest {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	parsed, err := parseSupplyManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestResumeSourceRecoversOriginalLogicalMappingAfterAllShardHashes(t *testing.T) {
	f := makeResumeSourceFixture(t)
	got, err := scanResumeSources(context.Background(), f.root, f.manifest, f.genesis, f.baseline)
	if err != nil {
		t.Fatal(err)
	}
	if got.MappingSignatureChecks != 4 || got.SelectedFrameSignatureChecks != 2000 || got.FullSourceShards != 2 || !reflect.DeepEqual(got.FilesBefore, got.FilesAfter) {
		t.Fatalf("source scan counts/identity: %+v", got)
	}
	var total, skipped, newly uint64
	for _, shard := range f.manifest.Shards {
		total += shard.Bytes
	}
	if got.FullSourceBytes != total || len(got.Senders) != 4 || len(got.Witnesses) != 4 || len(got.Frames) != 4 {
		t.Fatal("incomplete source proof material")
	}
	for i, key := range f.keys {
		if got.Senders[i].Index != uint32(i) || got.Senders[i].Address != crypto.PubkeyToAddress(key.PublicKey) || got.Witnesses[i].Address != got.Senders[i].Address || got.Witnesses[i].FirstTransaction == (types.Hash{}) {
			t.Fatal("logical index was inferred from address order")
		}
	}
	for _, frame := range got.Frames {
		skipped += uint64(frame.CanonicalSkipped)
		newly += uint64(frame.NewlyAdmitted)
	}
	if skipped != 583 || newly != 1417 || got.Frames[1].NewlyAdmitted != 459 || got.Frames[2].NewlyAdmitted != 458 || got.Frames[3].NewlyAdmitted != 0 {
		t.Fatal("original partial prefixes were cropped/rounded")
	}
}

func rewriteResumeTestFrame(t *testing.T, f resumeSourceFixture, m supplyManifest, logical uint32, edit func(*sharedexec.CorpusFrame, [][]byte)) supplyManifest {
	t.Helper()
	m.Shards = append([]sharedexec.CorpusShard(nil), m.Shards...)
	shard := uint16(logical % uint32(m.Descriptor.ShardCount))
	path := filepath.Join(f.root, m.Shards[shard].File)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(raw)
	var output bytes.Buffer
	for seq := uint64(0); seq < m.Shards[shard].Frames; seq++ {
		frame, _, err := sharedexec.ReadCorpusFrame(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		index, nonce, _, err := m.Descriptor.FrameRange(shard, seq)
		if err != nil {
			t.Fatal(err)
		}
		if index == logical && nonce == 0 {
			var txs [][]byte
			if err := sharedexec.WalkCorpusPayload(frame.Payload, frame.Transactions, func(wire []byte) error { txs = append(txs, append([]byte(nil), wire...)); return nil }); err != nil {
				t.Fatal(err)
			}
			edit(&frame, txs)
			frame, err = sharedexec.MakeCorpusFrame(frame.CorpusFrameID, txs)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := sharedexec.WriteCorpusFrame(&output, frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, output.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	m.Shards[shard].Bytes = uint64(output.Len())
	m.Shards[shard].SHA256 = types.Hash(sha256.Sum256(output.Bytes()))
	return persistResumeTestManifest(t, f.root, m.CorpusManifest)
}

func resumeTestSignedWire(t *testing.T, f resumeSourceFixture, logical uint32, nonce uint64, key *ecdsa.PrivateKey, signer transaction.Signer) []byte {
	t.Helper()
	d := f.manifest.Descriptor
	to := supplyRecipient(uint32((uint64(logical)*d.NoncesPerSender+nonce)%uint64(d.RecipientCount)), d.RecipientCount)
	tx, err := transaction.SignNewTx(key, signer, &transaction.LegacyTx{Nonce: nonce, To: &to, Value: uint256.NewInt(1), Gas: 21000, GasPrice: uint256.NewInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tx.EthEncoded()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestResumeSourceRejectsChangedOrUnauthorizedOriginalBytes(t *testing.T) {
	f := makeResumeSourceFixture(t)
	originals := make(map[string][]byte)
	for _, shard := range f.manifest.Shards {
		path := filepath.Join(f.root, shard.File)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		originals[path] = raw
	}
	cases := []struct {
		name string
		edit func(*testing.T) (supplyManifest, []byte, []sharedexec.CanonicalAccount, context.Context)
	}{
		{"unselected tail corruption with original manifest", func(t *testing.T) (supplyManifest, []byte, []sharedexec.CanonicalAccount, context.Context) {
			path := filepath.Join(f.root, f.manifest.Shards[1].File)
			raw := append([]byte(nil), originals[path]...)
			raw[len(raw)-1] ^= 1
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			return f.manifest, f.genesis, f.baseline, context.Background()
		}},
		{"wrong genesis bytes", func(t *testing.T) (supplyManifest, []byte, []sharedexec.CanonicalAccount, context.Context) {
			return f.manifest, append(append([]byte(nil), f.genesis...), ' '), f.baseline, context.Background()
		}},
		{"missing baseline sender", func(t *testing.T) (supplyManifest, []byte, []sharedexec.CanonicalAccount, context.Context) {
			return f.manifest, f.genesis, f.baseline[:3], context.Background()
		}},
		{"wrong baseline order", func(t *testing.T) (supplyManifest, []byte, []sharedexec.CanonicalAccount, context.Context) {
			b := append([]sharedexec.CanonicalAccount(nil), f.baseline...)
			b[0], b[1] = b[1], b[0]
			return f.manifest, f.genesis, b, context.Background()
		}},
		{"cancelled", func(t *testing.T) (supplyManifest, []byte, []sharedexec.CanonicalAccount, context.Context) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return f.manifest, f.genesis, f.baseline, ctx
		}},
	}
	for _, tc := range []struct {
		name     string
		logical  uint32
		at       int
		nonce    uint64
		wrongKey bool
		chain    int64
		frameID  bool
	}{
		{"mapping nonce not zero", 0, 0, 1, false, 4205001, false},
		{"wrong chain", 0, 0, 0, false, 4205002, false},
		{"duplicate mapped signer", 2, 0, 0, true, 4205001, false},
		{"bad selected final signer", 0, 499, 499, true, 4205001, false},
		{"skipped prefix wrong signer", 1, 10, 10, true, 4205001, false},
		{"all skipped frame wrong signer", 3, 499, 499, true, 4205001, false},
		{"selected nonce gap", 0, 499, 500, false, 4205001, false},
		{"wrong frame ID", 0, 0, 0, false, 4205001, true},
	} {
		tc := tc
		cases = append(cases, struct {
			name string
			edit func(*testing.T) (supplyManifest, []byte, []sharedexec.CanonicalAccount, context.Context)
		}{tc.name, func(t *testing.T) (supplyManifest, []byte, []sharedexec.CanonicalAccount, context.Context) {
			m := rewriteResumeTestFrame(t, f, f.manifest, tc.logical, func(frame *sharedexec.CorpusFrame, txs [][]byte) {
				if tc.frameID {
					frame.Sequence++
					return
				}
				key := f.keys[tc.logical]
				if tc.wrongKey {
					key = f.keys[(tc.logical+2)%4]
				}
				if tc.name == "duplicate mapped signer" {
					key = f.keys[0]
				}
				txs[tc.at] = resumeTestSignedWire(t, f, tc.logical, tc.nonce, key, transaction.NewEIP155Signer(big.NewInt(tc.chain)))
			})
			return m, f.genesis, f.baseline, context.Background()
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for path, raw := range originals {
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			m, g, b, ctx := tc.edit(t)
			got, err := scanResumeSources(ctx, f.root, m, g, b)
			if err == nil || !reflect.DeepEqual(got, resumeSourceScan{}) {
				t.Fatalf("bad source returned partial success: %+v %v", got, err)
			}
			if strings.Contains(tc.name, "unselected tail") && !strings.Contains(err.Error(), "full source shard integrity") {
				t.Fatal("mapping ran before all shard hashes", err)
			}
		})
	}
}

func TestResumeSourceMetadataRejectsAliasesAndRestoredModificationTime(t *testing.T) {
	f := makeResumeSourceFixture(t)
	for _, name := range []string{"path replaced", "write restored mtime", "symlink", "hardlink"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(f.root, f.manifest.Shards[0].File)
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			before, err := resumeSourceStat(path, info)
			if err != nil {
				t.Fatal(err)
			}
			files, err := openStrictSources(context.Background(), f.root, f.manifest)
			if err != nil {
				t.Fatal(err)
			}
			defer closeStrictSources(files)
			all := make([]resumeSourceMetadata, len(files))
			all[0] = before
			for i := 1; i < len(files); i++ {
				p := filepath.Join(f.root, f.manifest.Shards[i].File)
				info, err := os.Lstat(p)
				if err != nil {
					t.Fatal(err)
				}
				all[i], err = resumeSourceStat(p, info)
				if err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "path replaced", "symlink":
				saved := path + ".saved"
				if err := os.Rename(path, saved); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(path); _ = os.Rename(saved, path) })
				if name == "symlink" {
					err = os.Symlink(saved, path)
				} else {
					err = os.WriteFile(path, raw, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				alias := path + ".alias"
				if err := os.Link(path, alias); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(alias) })
			case "write restored mtime":
				time.Sleep(2 * time.Millisecond)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := checkResumeSources(files, all); err == nil {
				t.Fatal("changed path/ctime/link identity accepted")
			}
		})
	}
}
