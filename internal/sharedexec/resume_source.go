package sharedexec

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
)

// These are audit observations, not transferable verification capabilities.
// Paths/times belong in an outer audit, never in the semantic source proof.
type ResumeSourceMetadata struct {
	Path                  string
	Device, Inode, Bytes  uint64
	Links                 uint64
	UID, Mode             uint32
	ModifiedNS, ChangedNS int64
}

type ResumeSourceWitness struct {
	SenderIndex      uint32        `json:"sender_index"`
	Address          types.Address `json:"address"`
	FirstFrame       CorpusFrameID `json:"first_frame"`
	PayloadHash      types.Hash    `json:"payload_sha256"`
	PayloadBytes     uint32        `json:"payload_bytes"`
	FirstTransaction types.Hash    `json:"first_transaction_sha256"`
}

type ResumeSourceScan struct {
	Senders                                 []ResumeSender
	MappingSHA256                           types.Hash
	Witnesses                               []ResumeSourceWitness
	Frames                                  []ResumeFrame
	FullSourceBytes, MappingSignatureChecks uint64
	SelectedFrameSignatureChecks            uint64
	FullSourceShards                        uint16
	FilesBefore, FilesAfter                 []ResumeSourceMetadata
}

func resumeSourceAccounts(genesisRaw []byte, m CorpusManifest, baseline []CanonicalAccount) (map[types.Address]CanonicalAccount, error) {
	d := m.Descriptor
	if len(genesisRaw) == 0 || len(genesisRaw) > 16<<20 || types.Hash(sha256.Sum256(genesisRaw)) != d.GenesisFileHash {
		return nil, errors.New("resume source actual genesis bytes differ")
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(genesisRaw, &genesis); err != nil {
		return nil, err
	}
	if err := ValidateCorpusGenesis(&genesis); err != nil {
		return nil, err
	}
	block, _, err := (&internal.GenesisBlock{GenesisConfig: &genesis}).ToBlock()
	if err != nil || block == nil || block.Hash() != d.Identity.GenesisHash {
		return nil, errors.New("resume source actual genesis header differs")
	}
	if len(genesis.Alloc) != int(d.SenderCount) || len(baseline) != int(d.SenderCount) {
		return nil, errors.New("resume source needs complete genesis/baseline sender sets")
	}
	byAddress := make(map[types.Address]CanonicalAccount, len(baseline))
	for i, a := range baseline {
		if i > 0 && bytes.Compare(baseline[i-1].Address[:], a.Address[:]) >= 0 {
			return nil, errors.New("resume baseline must retain strict address order")
		}
		original, exists := genesis.Alloc[a.Address]
		funds, fundsErr := uint256.FromHex(original.Balance)
		if !exists || a.Address == (types.Address{}) || !a.Exists || original.Nonce != 0 || len(original.Code) != 0 || len(original.Storage) != 0 || fundsErr != nil || funds.Cmp(uint256.NewInt(d.NoncesPerSender*21001)) < 0 || a.CodeHash != crypto.Keccak256Hash(nil) || a.Nonce > d.NoncesPerSender || len(a.Balance) > 78 {
			return nil, errors.New("resume source alloc/baseline requires funded bounded EOAs")
		}
		balance, ok := new(big.Int).SetString(a.Balance, 10)
		minimum := new(big.Int).Mul(new(big.Int).SetUint64(d.NoncesPerSender-a.Nonce), big.NewInt(21001))
		if !ok || balance.Sign() <= 0 || balance.BitLen() > 256 || balance.String() != a.Balance || balance.Cmp(minimum) < 0 {
			return nil, errors.New("resume canonical balance invalid or insufficient")
		}
		byAddress[a.Address] = a
	}
	return byAddress, nil
}

func resumeSourceSender(raw []byte, d CorpusDescriptor, signer transaction.EIP155Signer, index uint32, nonce uint64) (types.Address, error) {
	tx, err := transaction.DecodeEthereumTransaction(raw)
	if err != nil {
		return types.Address{}, err
	}
	to := CorpusRecipient(uint32((uint64(index)*d.NoncesPerSender+nonce)%uint64(d.RecipientCount)), d.RecipientCount)
	if tx.Type() != transaction.LegacyTxType || !tx.Protected() || tx.ChainId() == nil || tx.ChainId().ToBig().Cmp(signer.ChainID()) != 0 || tx.Nonce() != nonce || tx.To() == nil || *tx.To() != to || tx.Gas() != 21000 || tx.Value() == nil || !tx.Value().Eq(uint256.NewInt(1)) || tx.GasPrice() == nil || !tx.GasPrice().Eq(uint256.NewInt(1)) || len(tx.Data()) != 0 {
		return types.Address{}, errors.New("resume source original EIP155 transfer recipe differs")
	}
	// Direct recovery ignores sender hints and process-wide transaction memo.
	return signer.Sender(tx)
}

// ScanResumeSources authenticates public mapping data from the complete source.
// Its output is an audit value, never a transferable owner capability.
func ScanResumeSources(ctx context.Context, root string, manifestRaw, genesisRaw []byte, baseline []CanonicalAccount) (ResumeSourceScan, error) {
	lease, err := openResumeSourceScan(ctx, root, manifestRaw, genesisRaw, baseline)
	if err != nil {
		return ResumeSourceScan{}, err
	}
	defer lease.Close()
	return lease.scan(ctx)
}

type resumeSourceLease struct {
	manifest CorpusManifest
	files    []resumeSourceFile
	before   []ResumeSourceMetadata
	accounts map[types.Address]CanonicalAccount
}

func (l *resumeSourceLease) Close() {
	if l != nil {
		closeResumeSources(l.files)
		l.files = nil
	}
}
func (l *resumeSourceLease) checkUnchanged() ([]ResumeSourceMetadata, error) {
	if l == nil || len(l.files) == 0 {
		return nil, errors.New("resume source lease closed")
	}
	return checkResumeSources(l.files, l.before)
}

// Only internal owner startup can retain this lease through its final state gate.
func openResumeSourceScan(ctx context.Context, root string, manifestRaw, genesisRaw []byte, baseline []CanonicalAccount) (*resumeSourceLease, error) {
	if ctx == nil {
		return nil, errors.New("resume source requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m, err := DecodeCorpusManifest(manifestRaw)
	if err != nil {
		return nil, err
	}
	d := m.Descriptor
	if d.NonceStart != 0 || d.FrameTransactions != 500 || d.NoncesPerSender < 500 {
		return nil, errors.New("resume source requires bound original 500-transaction frames")
	}
	if err := resumeSourcePrivateRoot(root); err != nil {
		return nil, err
	}
	accounts, err := resumeSourceAccounts(genesisRaw, m, baseline)
	if err != nil {
		return nil, err
	}
	before := make([]ResumeSourceMetadata, len(m.Shards))
	for i, shard := range m.Shards {
		path := filepath.Join(root, shard.File)
		info, e := os.Lstat(path)
		if e != nil {
			return nil, e
		}
		before[i], err = resumeSourceStat(path, info)
		if err != nil || before[i].Bytes != shard.Bytes {
			return nil, errors.New("resume source initial file metadata differs")
		}
	}
	files, err := openResumeSources(ctx, root, m)
	if err != nil {
		return nil, err
	}
	lease := &resumeSourceLease{manifest: m, files: files, before: before, accounts: accounts}
	if _, err := lease.checkUnchanged(); err != nil {
		lease.Close()
		return nil, err
	}
	return lease, nil
}

func (l *resumeSourceLease) scanMapping(ctx context.Context) (out ResumeSourceScan, offsets []int64, err error) {
	defer func() {
		if err != nil {
			out = ResumeSourceScan{}
			offsets = nil
		}
	}()
	if ctx == nil {
		return out, nil, errors.New("resume source requires a context")
	}
	if err = ctx.Err(); err != nil {
		return out, nil, err
	}
	if _, err = l.checkUnchanged(); err != nil {
		return out, nil, err
	}
	m, files, before, accounts := l.manifest, l.files, l.before, l.accounts
	d := m.Descriptor
	chainID, ok := new(big.Int).SetString(d.Identity.ChainID, 10)
	if !ok {
		return out, nil, errors.New("resume source chain ID invalid")
	}
	signer := transaction.NewEIP155Signer(chainID)
	out.Senders = make([]ResumeSender, d.SenderCount)
	out.Witnesses = make([]ResumeSourceWitness, d.SenderCount)
	offsets = make([]int64, d.SenderCount)
	seen := make(map[types.Address]bool, d.SenderCount)
	reader := bufio.NewReaderSize(files[0].file, 8<<20)
	var scratch []byte
	for shard, source := range files {
		if _, err = source.file.Seek(0, io.SeekStart); err != nil {
			return out, nil, err
		}
		reader.Reset(source.file)
		var offset int64
		count := (d.SenderCount-1-uint32(shard))/uint32(d.ShardCount) + 1
		for sequence := uint64(0); sequence < uint64(count); sequence++ {
			if err = ctx.Err(); err != nil {
				return out, nil, err
			}
			frame, reusable, e := ReadCorpusFrame(reader, scratch)
			scratch = reusable
			if e != nil {
				return out, nil, e
			}
			index, nonce, ntx, e := d.FrameRange(uint16(shard), sequence)
			if e != nil || nonce != 0 || ntx != 500 || frame.Shard != uint16(shard) || frame.Sequence != sequence {
				return out, nil, errors.New("resume source first-round frame differs")
			}
			if err = frame.Validate(d); err != nil {
				return out, nil, err
			}
			var first []byte
			if err = WalkCorpusPayload(frame.Payload, frame.Transactions, func(raw []byte) error {
				if first == nil {
					first = raw
				}
				return nil
			}); err != nil {
				return out, nil, err
			}
			address, e := resumeSourceSender(first, d, signer, index, 0)
			if e != nil {
				return out, nil, e
			}
			account, exists := accounts[address]
			if !exists || seen[address] {
				return out, nil, errors.New("resume source signer set is not the exact unique alloc/baseline set")
			}
			seen[address] = true
			out.MappingSignatureChecks++
			out.Senders[index] = ResumeSender{Index: index, Address: address, Nonce: account.Nonce}
			out.Witnesses[index] = ResumeSourceWitness{SenderIndex: index, Address: address, FirstFrame: frame.CorpusFrameID, PayloadHash: frame.PayloadHash, PayloadBytes: uint32(len(frame.Payload)), FirstTransaction: types.Hash(sha256.Sum256(first))}
			offsets[index] = offset
			offset += int64(CorpusWireHeaderSize + len(frame.Payload))
		}
		out.FullSourceBytes += m.Shards[shard].Bytes
	}
	if len(seen) != len(accounts) {
		return out, nil, errors.New("resume source omitted a canonical sender")
	}
	out.MappingSHA256, err = ResumeMappingID(out.Senders)
	if err != nil {
		return out, nil, err
	}
	out.FilesAfter, err = checkResumeSources(files, before)
	if err != nil {
		return out, nil, err
	}
	out.FilesBefore = before
	out.FullSourceShards = uint16(len(files))
	return out, offsets, nil
}

// scan preserves the original four-frame-v1 source policy. The mapping-only
// helper does not select or authorize any prefix plan.
func (l *resumeSourceLease) scan(ctx context.Context) (out ResumeSourceScan, err error) {
	defer func() {
		if err != nil {
			out = ResumeSourceScan{}
		}
	}()
	out, offsets, err := l.scanMapping(ctx)
	if err != nil {
		return ResumeSourceScan{}, err
	}
	d, files, before := l.manifest.Descriptor, l.files, l.before
	chainID, ok := new(big.Int).SetString(d.Identity.ChainID, 10)
	if !ok {
		return ResumeSourceScan{}, errors.New("resume source chain ID invalid")
	}
	signer := transaction.NewEIP155Signer(chainID)
	var scratch []byte
	selected := make(map[uint64]uint32)
	for _, sender := range out.Senders {
		switch sender.Nonce {
		case 0, 41, 42, 500:
			if _, exists := selected[sender.Nonce]; !exists {
				selected[sender.Nonce] = sender.Index
			}
		}
	}
	if len(selected) != 4 {
		return out, errors.New("resume source lacks a required canonical nonce bucket")
	}
	indices := []uint32{selected[0], selected[41], selected[42], selected[500]}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	for _, index := range indices {
		w := out.Witnesses[index]
		section := io.NewSectionReader(files[w.FirstFrame.Shard].file, offsets[index], int64(CorpusWireHeaderSize)+int64(w.PayloadBytes))
		frame, reusable, e := ReadCorpusFrame(section, scratch)
		scratch = reusable
		if e != nil {
			return out, e
		}
		if frame.CorpusFrameID != w.FirstFrame || frame.PayloadHash != w.PayloadHash || uint32(len(frame.Payload)) != w.PayloadBytes {
			return out, errors.New("resume source selected frame changed")
		}
		if err = frame.Validate(d); err != nil {
			return out, err
		}
		var nonce uint64
		err = WalkCorpusPayload(frame.Payload, frame.Transactions, func(raw []byte) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			address, e := resumeSourceSender(raw, d, signer, index, nonce)
			if e != nil {
				return fmt.Errorf("resume selected original signature/nonce differs: %w", e)
			}
			if address != out.Senders[index].Address {
				return errors.New("resume selected original sender differs")
			}
			nonce++
			out.SelectedFrameSignatureChecks++
			return nil
		})
		if err != nil {
			return out, err
		}
		skip := uint16(out.Senders[index].Nonce)
		out.Frames = append(out.Frames, ResumeFrame{ID: frame.CorpusFrameID, SenderIndex: index, PayloadHash: frame.PayloadHash, PayloadBytes: uint32(len(frame.Payload)), CanonicalSkipped: skip, NewlyAdmitted: 500 - skip})
	}
	out.FilesAfter, err = checkResumeSources(files, before)
	if err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	out.FilesBefore = before
	out.FullSourceShards = uint16(len(files))
	return out, nil
}
