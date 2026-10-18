package sharedexec

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"

	"github.com/n42blockchain/N42/common/types"
)

// BoundedResumeSourceScan contains reproducible audit data, not an owner arming
// capability. Frames are bounded metadata only; original payloads stay on disk.
type BoundedResumeSourceScan struct {
	Source             ResumeSourceScan
	Frames             []BoundedResumeFrame
	WhitelistSHA256    types.Hash
	SourceFrames       uint64
	SourceTransactions uint64
	SourceEncodedBytes uint64
	NewTransactions    uint64
}

// BoundedResumeWhitelistID validates complete original-frame coverage in
// deterministic (shard, sequence) order. It does not authenticate source bytes.
func BoundedResumeWhitelistID(d CorpusDescriptor, senders []ResumeSender, end uint64, frames []BoundedResumeFrame) (types.Hash, uint64, uint64, error) {
	if err := d.Validate(); err != nil {
		return types.Hash{}, 0, 0, err
	}
	count, _, limit, err := boundedResumeGeometry(d, end)
	if err != nil {
		return types.Hash{}, 0, 0, err
	}
	if len(senders) != int(d.SenderCount) || uint64(len(frames)) != count {
		return types.Hash{}, 0, 0, errors.New("bounded resume whitelist must contain the complete original prefix")
	}
	if _, err := ResumeMappingID(senders); err != nil {
		return types.Hash{}, 0, 0, err
	}
	corpusID, err := d.ID()
	if err != nil {
		return types.Hash{}, 0, 0, err
	}
	h := sha256.New()
	_, _ = h.Write([]byte("gov5/bounded-resume-whitelist/v2\x00"))
	put := func(value any) { _ = binary.Write(h, binary.BigEndian, value) }
	put(count)
	var encoded, newly uint64
	seenPayload := make(map[types.Hash]bool, len(frames))
	position := 0
	for shard := uint16(0); shard < d.ShardCount; shard++ {
		perRound := uint64((d.SenderCount-1-uint32(shard))/uint32(d.ShardCount) + 1)
		shardFrames, err := boundedResumeMul(perRound, end/500)
		if err != nil {
			return types.Hash{}, 0, 0, err
		}
		for seq := uint64(0); seq < shardFrames; seq++ {
			frame := frames[position]
			position++
			index, nonce, txs, err := d.FrameRange(shard, seq)
			if err != nil || index >= uint32(len(senders)) {
				return types.Hash{}, 0, 0, errors.New("bounded resume original frame range invalid")
			}
			sender := senders[index]
			if sender.Nonce > end {
				return types.Hash{}, 0, 0, errors.New("bounded resume canonical sender exceeds original prefix")
			}
			var skip uint64
			if sender.Nonce > nonce {
				skip = sender.Nonce - nonce
				if skip > uint64(txs) {
					skip = uint64(txs)
				}
			}
			if frame.ID != (CorpusFrameID{CorpusID: corpusID, Shard: shard, Sequence: seq}) || frame.SenderIndex != index || frame.Address != sender.Address || frame.FirstNonce != nonce || frame.Transactions != 500 || txs != 500 || nonce >= end || frame.PayloadHash == (types.Hash{}) || frame.PayloadBytes < uint32(txs)*5 || frame.PayloadBytes > uint32(txs)*106 || uint64(frame.CanonicalSkipped) != skip || uint64(frame.NewlyAdmitted) != uint64(txs)-skip {
				return types.Hash{}, 0, 0, errors.New("bounded resume whitelist range/hash/bytes/fixed canonical prefix differs")
			}
			if seenPayload[frame.PayloadHash] {
				return types.Hash{}, 0, 0, errors.New("bounded resume original frame payload repeated")
			}
			seenPayload[frame.PayloadHash] = true
			encoded, err = boundedResumeAdd(encoded, uint64(CorpusWireHeaderSize)+uint64(frame.PayloadBytes))
			if err != nil {
				return types.Hash{}, 0, 0, err
			}
			newly, err = boundedResumeAdd(newly, uint64(frame.NewlyAdmitted))
			if err != nil {
				return types.Hash{}, 0, 0, err
			}
			_, _ = h.Write(frame.ID.CorpusID[:])
			put(frame.ID.Shard)
			put(frame.ID.Sequence)
			put(frame.SenderIndex)
			_, _ = h.Write(frame.Address[:])
			put(frame.FirstNonce)
			put(frame.Transactions)
			_, _ = h.Write(frame.PayloadHash[:])
			put(frame.PayloadBytes)
			put(frame.CanonicalSkipped)
			put(frame.NewlyAdmitted)
		}
	}
	if position != len(frames) || encoded > limit {
		return types.Hash{}, 0, 0, errors.New("bounded resume whitelist exceeds exact geometry")
	}
	var digest types.Hash
	copy(digest[:], h.Sum(nil))
	return digest, encoded, newly, nil
}

// ScanBoundedResumeSources hashes all original shards before recovering the
// public mapping or enumerating the prefix. Online ingress still checks every
// original transaction signature, including canonical-skipped transactions.
func ScanBoundedResumeSources(ctx context.Context, root string, manifestRaw, genesisRaw []byte, baseline []CanonicalAccount, end uint64) (BoundedResumeSourceScan, error) {
	m, err := DecodeCorpusManifest(manifestRaw)
	if err != nil {
		return BoundedResumeSourceScan{}, err
	}
	if _, _, _, err := boundedResumeGeometry(m.Descriptor, end); err != nil {
		return BoundedResumeSourceScan{}, err
	}
	lease, err := openResumeSourceScan(ctx, root, manifestRaw, genesisRaw, baseline)
	if err != nil {
		return BoundedResumeSourceScan{}, err
	}
	defer lease.Close()
	return lease.scanBounded(ctx, end)
}

func (l *resumeSourceLease) scanBounded(ctx context.Context, end uint64) (out BoundedResumeSourceScan, err error) {
	defer func() {
		if err != nil {
			out = BoundedResumeSourceScan{}
		}
	}()
	if l == nil || len(l.files) == 0 {
		return out, errors.New("bounded resume source lease closed")
	}
	d := l.manifest.Descriptor
	count, total, _, err := boundedResumeGeometry(d, end)
	if err != nil {
		return out, err
	}
	out.Source, _, err = l.scanMapping(ctx)
	if err != nil {
		return out, err
	}
	out.Frames = make([]BoundedResumeFrame, 0, count)
	reader := bufio.NewReaderSize(l.files[0].file, 8<<20)
	var scratch []byte
	for shard, source := range l.files {
		if _, err = source.file.Seek(0, io.SeekStart); err != nil {
			return out, err
		}
		reader.Reset(source.file)
		perRound := uint64((d.SenderCount-1-uint32(shard))/uint32(d.ShardCount) + 1)
		shardFrames, e := boundedResumeMul(perRound, end/500)
		if e != nil {
			return out, e
		}
		for sequence := uint64(0); sequence < shardFrames; sequence++ {
			if err = ctx.Err(); err != nil {
				return out, err
			}
			frame, reusable, e := ReadCorpusFrame(reader, scratch)
			scratch = reusable
			if e != nil {
				return out, e
			}
			index, nonce, txs, e := d.FrameRange(uint16(shard), sequence)
			if e != nil || frame.Shard != uint16(shard) || frame.Sequence != sequence || txs != 500 || frame.Transactions != 500 || nonce >= end {
				return out, errors.New("bounded resume source original prefix frame differs")
			}
			if err = frame.Validate(d); err != nil {
				return out, err
			}
			if err = WalkCorpusPayload(frame.Payload, frame.Transactions, func(raw []byte) error {
				if len(raw) > 102 {
					return errors.New("bounded resume original transaction exceeds recipe byte cap")
				}
				return nil
			}); err != nil {
				return out, err
			}
			sender := out.Source.Senders[index]
			if sender.Nonce > end {
				return out, errors.New("bounded resume baseline exceeds source prefix")
			}
			var skip uint64
			if sender.Nonce > nonce {
				skip = sender.Nonce - nonce
				if skip > uint64(txs) {
					skip = uint64(txs)
				}
			}
			out.Frames = append(out.Frames, BoundedResumeFrame{ID: frame.CorpusFrameID, SenderIndex: index, Address: sender.Address, FirstNonce: nonce, Transactions: txs, PayloadHash: frame.PayloadHash, PayloadBytes: uint32(len(frame.Payload)), CanonicalSkipped: uint16(skip), NewlyAdmitted: txs - uint16(skip)})
		}
	}
	out.WhitelistSHA256, out.SourceEncodedBytes, out.NewTransactions, err = BoundedResumeWhitelistID(d, out.Source.Senders, end, out.Frames)
	if err != nil {
		return out, err
	}
	out.Source.FilesAfter, err = l.checkUnchanged()
	if err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	out.SourceFrames, out.SourceTransactions = count, total
	return out, nil
}
