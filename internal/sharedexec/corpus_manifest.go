package sharedexec

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

const (
	CorpusVersion          = 1
	CorpusCodec            = "ethereum-signed-rlp-eip155-v1"
	CorpusSignature        = "secp256k1-eip155-v1"
	CorpusKeyDerivation    = "gov5-supply-seed-hmac-sha256-v1"
	CorpusRecipientRule    = "gov5-supply-hotset-keccak-v1"
	CorpusMaxShards        = 64
	CorpusMaxFrameTxs      = 500
	CorpusMaxTxBytes       = 2048
	CorpusMaxManifestBytes = 1 << 20
	CorpusMaxTransactions  = 600_000_000
)

// CorpusDescriptor is an immutable supply description, not chain admission or
// authorization evidence. CorpusID hashes this descriptor before shard files
// exist, avoiding a circular dependency on their eventual file hashes. The
// complete manifest separately binds every finalized shard hash and byte count.
type CorpusDescriptor struct {
	Version           uint16     `json:"version"`
	Identity          Identity   `json:"identity"`
	GenesisFileHash   types.Hash `json:"genesis_file_sha256"`
	Codec             string     `json:"codec"`
	Signature         string     `json:"signature"`
	KeyDerivation     string     `json:"key_derivation"`
	SeedCommitment    types.Hash `json:"seed_commitment"`
	SenderCount       uint32     `json:"sender_count"`
	NonceStart        uint64     `json:"nonce_start"`
	NoncesPerSender   uint64     `json:"nonces_per_sender"`
	RecipientRule     string     `json:"recipient_rule"`
	RecipientCount    uint32     `json:"recipient_count"`
	ShardCount        uint16     `json:"shard_count"`
	FrameTransactions uint16     `json:"frame_transactions"`
}

func (d CorpusDescriptor) Validate() error {
	if d.Version != CorpusVersion || d.Identity.Version != 1 || d.Identity.Namespace != params.PerformanceNamespace || d.Identity.ChainID != "4205001" || d.Identity.GenesisHash == (types.Hash{}) || d.GenesisFileHash == (types.Hash{}) {
		return errors.New("corpus requires full explicit isolated chain/genesis identity")
	}
	if d.Codec != CorpusCodec || d.Signature != CorpusSignature || d.KeyDerivation != CorpusKeyDerivation || d.RecipientRule != CorpusRecipientRule || d.SeedCommitment == (types.Hash{}) {
		return errors.New("unsupported corpus codec/signature/derivation")
	}
	if d.SenderCount == 0 || d.SenderCount > 12500 || d.NoncesPerSender == 0 || d.NoncesPerSender > CorpusMaxTransactions || d.NonceStart > ^uint64(0)-d.NoncesPerSender || d.RecipientCount == 0 || d.RecipientCount > 100000 {
		return errors.New("invalid bounded corpus sender/nonce/recipient ranges")
	}
	if d.ShardCount == 0 || d.ShardCount > CorpusMaxShards || uint32(d.ShardCount) > d.SenderCount || d.FrameTransactions == 0 || d.FrameTransactions > CorpusMaxFrameTxs || uint64(d.SenderCount)*d.NoncesPerSender > CorpusMaxTransactions {
		return errors.New("invalid bounded corpus shard/frame/total sizes")
	}
	return nil
}

func (d CorpusDescriptor) ID() (types.Hash, error) {
	if err := d.Validate(); err != nil {
		return types.Hash{}, err
	}
	var encoded bytes.Buffer
	encoded.WriteString("gov5-performance-corpus-descriptor-v1\x00")
	put := func(value any) { _ = binary.Write(&encoded, binary.LittleEndian, value) }
	text := func(value string) { put(uint16(len(value))); encoded.WriteString(value) }
	put(d.Version)
	put(d.Identity.Version)
	text(d.Identity.Namespace)
	text(d.Identity.ChainID)
	encoded.Write(d.Identity.GenesisHash[:])
	encoded.Write(d.GenesisFileHash[:])
	text(d.Codec)
	text(d.Signature)
	text(d.KeyDerivation)
	encoded.Write(d.SeedCommitment[:])
	put(d.SenderCount)
	put(d.NonceStart)
	put(d.NoncesPerSender)
	text(d.RecipientRule)
	put(d.RecipientCount)
	put(d.ShardCount)
	put(d.FrameTransactions)
	return types.Hash(sha256.Sum256(encoded.Bytes())), nil
}

func (d CorpusDescriptor) TotalTransactions() uint64 {
	return uint64(d.SenderCount) * d.NoncesPerSender
}

// A shard visits its disjoint sender indices in order for each nonce chunk.
// Thus a frame has one sender, with consecutive original signed nonces. With
// >=32 senders per shard, a 32-frame FIFO window never overlaps one sender.
func (d CorpusDescriptor) FrameRange(shard uint16, sequence uint64) (sender uint32, nonce uint64, count uint16, err error) {
	if err = d.Validate(); err != nil {
		return
	}
	if shard >= d.ShardCount {
		err = errors.New("corpus shard outside descriptor")
		return
	}
	senders := uint64((d.SenderCount-1-uint32(shard))/uint32(d.ShardCount) + 1)
	round := sequence / senders
	local := sequence % senders
	if round >= (d.NoncesPerSender+uint64(d.FrameTransactions)-1)/uint64(d.FrameTransactions) {
		err = errors.New("corpus frame sequence outside descriptor")
		return
	}
	sender = uint32(shard) + uint32(local)*uint32(d.ShardCount)
	nonce = d.NonceStart + round*uint64(d.FrameTransactions)
	remaining := d.NoncesPerSender - round*uint64(d.FrameTransactions)
	count = d.FrameTransactions
	if remaining < uint64(count) {
		count = uint16(remaining)
	}
	return
}

type CorpusShard struct {
	Index        uint16     `json:"index"`
	File         string     `json:"file"`
	Transactions uint64     `json:"transactions"`
	Frames       uint64     `json:"frames"`
	Bytes        uint64     `json:"bytes"`
	SHA256       types.Hash `json:"sha256"`
}
type CorpusManifest struct {
	Descriptor CorpusDescriptor `json:"descriptor"`
	CorpusID   types.Hash       `json:"corpus_id"`
	Shards     []CorpusShard    `json:"shards"`
}

func (m CorpusManifest) Validate() error {
	id, err := m.Descriptor.ID()
	if err != nil {
		return err
	}
	if id != m.CorpusID || len(m.Shards) != int(m.Descriptor.ShardCount) {
		return errors.New("corpus descriptor/manifest binding mismatch")
	}
	for i, shard := range m.Shards {
		senders := uint64((m.Descriptor.SenderCount-1-uint32(i))/uint32(m.Descriptor.ShardCount) + 1)
		expectedTx := senders * m.Descriptor.NoncesPerSender
		expectedFrames := senders * ((m.Descriptor.NoncesPerSender + uint64(m.Descriptor.FrameTransactions) - 1) / uint64(m.Descriptor.FrameTransactions))
		if shard.Index != uint16(i) || shard.File != fmt.Sprintf("shard-%02d.gcf", i) || shard.Transactions != expectedTx || shard.Frames != expectedFrames || shard.SHA256 == (types.Hash{}) || shard.Bytes < expectedFrames*CorpusWireHeaderSize+expectedTx*5 || shard.Bytes > expectedFrames*CorpusWireHeaderSize+expectedTx*(4+CorpusMaxTxBytes) {
			return errors.New("invalid corpus shard identity/count/size/hash")
		}
	}
	return nil
}

func DecodeCorpusManifest(raw []byte) (CorpusManifest, error) {
	var manifest CorpusManifest
	if len(raw) == 0 || len(raw) > CorpusMaxManifestBytes {
		return manifest, errors.New("corpus manifest size invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return CorpusManifest{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return CorpusManifest{}, errors.New("one corpus manifest required")
	}
	if err := manifest.Validate(); err != nil {
		return CorpusManifest{}, err
	}
	return manifest, nil
}
