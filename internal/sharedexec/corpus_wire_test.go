package sharedexec

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func corpusDescriptorForTest() CorpusDescriptor {
	return CorpusDescriptor{Version: CorpusVersion, Identity: Identity{Version: 1, Namespace: params.PerformanceNamespace, ChainID: "4205001", GenesisHash: types.Hash{1}}, GenesisFileHash: types.Hash{2}, Codec: CorpusCodec, Signature: CorpusSignature, KeyDerivation: CorpusKeyDerivation, SeedCommitment: types.Hash{3}, SenderCount: 4, NonceStart: 7, NoncesPerSender: 5, RecipientRule: CorpusRecipientRule, RecipientCount: 3, ShardCount: 2, FrameTransactions: 2}
}

func TestCorpusDescriptorBindsIdentityAlgorithmAndEveryNonce(t *testing.T) {
	descriptor := corpusDescriptorForTest()
	id, err := descriptor.ID()
	if err != nil {
		t.Fatal(err)
	}
	if id.Hex() != "0xb4c41866a66c27e87c070c3daebe2f92da861d4b5e29e432636d7e98a57ec0ac" {
		t.Fatalf("descriptor golden %s", id.Hex())
	}
	for _, change := range []func(*CorpusDescriptor){func(d *CorpusDescriptor) { d.Identity.GenesisHash[0]++ }, func(d *CorpusDescriptor) { d.NonceStart++ }, func(d *CorpusDescriptor) { d.NoncesPerSender++ }, func(d *CorpusDescriptor) { d.RecipientCount++ }, func(d *CorpusDescriptor) { d.SeedCommitment[0]++ }, func(d *CorpusDescriptor) { d.SenderCount++ }} {
		changed := descriptor
		change(&changed)
		next, err := changed.ID()
		if err != nil || next == id {
			t.Fatal("descriptor field not committed")
		}
	}
	seen := map[[2]uint64]bool{}
	for shard := uint16(0); shard < descriptor.ShardCount; shard++ {
		for seq := uint64(0); seq < 6; seq++ {
			sender, nonce, count, err := descriptor.FrameRange(shard, seq)
			if err != nil {
				t.Fatal(err)
			}
			for j := uint64(0); j < uint64(count); j++ {
				key := [2]uint64{uint64(sender), nonce + j}
				if seen[key] || sender%2 != uint32(shard) || nonce+j < 7 || nonce+j > 11 {
					t.Fatal("repeated nonce, wrong shard or mutated range")
				}
				seen[key] = true
			}
		}
	}
	if uint64(len(seen)) != descriptor.TotalTransactions() {
		t.Fatal("corpus omits sender nonce ranges")
	}
	if _, _, _, err := descriptor.FrameRange(0, 6); err == nil {
		t.Fatal("sequence beyond shard accepted")
	}
	if _, _, _, err := descriptor.FrameRange(2, 0); err == nil {
		t.Fatal("unknown shard accepted")
	}
}

func TestCorpusWireRoundTripAndDescriptorGates(t *testing.T) {
	descriptor := corpusDescriptorForTest()
	id, _ := descriptor.ID()
	frame, err := MakeCorpusFrame(CorpusFrameID{CorpusID: id, Shard: 1, Sequence: 0}, [][]byte{{1, 2}, {3, 4, 5}})
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	if err := WriteCorpusFrame(&buffer, frame); err != nil {
		t.Fatal(err)
	}
	decoded, scratch, err := ReadCorpusFrame(&buffer, make([]byte, 0, 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(descriptor); err != nil {
		t.Fatal(err)
	}
	if decoded.CorpusFrameID != frame.CorpusFrameID || decoded.Transactions != 2 || !bytes.Equal(decoded.Payload, frame.Payload) || len(scratch) != len(frame.Payload) || buffer.Len() != 0 {
		t.Fatal("wire changed frame identity/payload")
	}
	decoded.Sequence = 4
	if err := decoded.Validate(descriptor); err == nil {
		t.Fatal("wrong count for last nonce chunk")
	}
	decoded = frame
	decoded.CorpusID[0] ^= 1
	if err := decoded.Validate(descriptor); err == nil {
		t.Fatal("cross-corpus frame")
	}
	decoded = frame
	decoded.Payload = append(bytes.Clone(frame.Payload), 0)
	decoded.PayloadHash = types.Hash(sha256.Sum256(decoded.Payload))
	if err := decoded.Validate(descriptor); err == nil {
		t.Fatal("trailing payload")
	}
}

func TestCorpusWireBoundsLengthsBeforeAllocationOrPayloadRead(t *testing.T) {
	descriptor := corpusDescriptorForTest()
	id, _ := descriptor.ID()
	frame, err := MakeCorpusFrame(CorpusFrameID{CorpusID: id}, [][]byte{{1}, {2}})
	if err != nil {
		t.Fatal(err)
	}
	header, err := frame.Header()
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func([]byte){func(h []byte) { h[3] = '2' }, func(h []byte) { binary.LittleEndian.PutUint16(h[36:38], 64) }, func(h []byte) { binary.LittleEndian.PutUint16(h[46:48], 0) }, func(h []byte) { binary.LittleEndian.PutUint16(h[46:48], 501) }, func(h []byte) { binary.LittleEndian.PutUint32(h[48:52], ^uint32(0)) }, func(h []byte) { binary.LittleEndian.PutUint32(h[48:52], CorpusMaxFramePayload+1) }, func(h []byte) { binary.LittleEndian.PutUint32(h[48:52], 1) }} {
		invalid := bytes.Clone(header[:])
		change(invalid)
		reader := bytes.NewReader(append(invalid, 0x99))
		_, scratch, err := ReadCorpusFrame(reader, make([]byte, 0, 16))
		if err == nil || reader.Len() != 1 || cap(scratch) != 16 {
			t.Fatal("invalid header read/allocated payload")
		}
	}
	var encoded bytes.Buffer
	if err := WriteCorpusFrame(&encoded, frame); err != nil {
		t.Fatal(err)
	}
	raw := encoded.Bytes()
	raw[len(raw)-1] ^= 1
	if _, _, err := ReadCorpusFrame(bytes.NewReader(raw), nil); err == nil {
		t.Fatal("tampered payload digest")
	}
	if _, _, err := ReadCorpusFrame(bytes.NewReader(header[:10]), nil); err != io.ErrUnexpectedEOF {
		t.Fatalf("truncated header %v", err)
	}
}

func TestCorpusManifestBindsBoundedShardMetadata(t *testing.T) {
	descriptor := corpusDescriptorForTest()
	id, _ := descriptor.ID()
	manifest := CorpusManifest{Descriptor: descriptor, CorpusID: id}
	for i := uint16(0); i < 2; i++ {
		manifest.Shards = append(manifest.Shards, CorpusShard{Index: i, File: []string{"shard-00.gcf", "shard-01.gcf"}[i], Transactions: 10, Frames: 6, Bytes: 6*CorpusWireHeaderSize + 10*5, SHA256: types.Hash{byte(i + 1)}})
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCorpusManifest(raw); err != nil {
		t.Fatal(err)
	}
	manifest.Shards[0].File = "../escape"
	raw, _ = json.Marshal(manifest)
	if _, err := DecodeCorpusManifest(raw); err == nil {
		t.Fatal("unbound/escaping shard name")
	}
	manifest.Shards[0].File = "shard-00.gcf"
	manifest.Shards[0].Transactions++
	raw, _ = json.Marshal(manifest)
	if _, err := DecodeCorpusManifest(raw); err == nil {
		t.Fatal("wrong shard transaction count")
	}
	if _, err := DecodeCorpusManifest(bytes.Repeat([]byte{' '}, CorpusMaxManifestBytes+1)); err == nil {
		t.Fatal("unbounded manifest")
	}
}
