package main

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
	"syscall"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

// These are audit observations, not transferable verification capabilities.
// Paths/times belong in an outer audit, never in the semantic source proof.
type resumeSourceMetadata struct {
	Path                  string
	Device, Inode, Bytes  uint64
	Links                 uint64
	UID, Mode             uint32
	ModifiedNS, ChangedNS int64
}

type resumeSourceWitness struct {
	SenderIndex      uint32                   `json:"sender_index"`
	Address          types.Address            `json:"address"`
	FirstFrame       sharedexec.CorpusFrameID `json:"first_frame"`
	PayloadHash      types.Hash               `json:"payload_sha256"`
	PayloadBytes     uint32                   `json:"payload_bytes"`
	FirstTransaction types.Hash               `json:"first_transaction_sha256"`
}

type resumeSourceScan struct {
	Senders                                 []sharedexec.ResumeSender
	MappingSHA256                           types.Hash
	Witnesses                               []resumeSourceWitness
	Frames                                  []sharedexec.ResumeFrame
	FullSourceBytes, MappingSignatureChecks uint64
	SelectedFrameSignatureChecks            uint64
	FullSourceShards                        uint16
	FilesBefore, FilesAfter                 []resumeSourceMetadata
}

func resumeSourceStat(path string, info os.FileInfo) (resumeSourceMetadata, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Size() <= 0 {
		return resumeSourceMetadata{}, errors.New("resume source requires owned regular 0600 single-link files")
	}
	return resumeSourceMetadata{Path: path, Device: uint64(stat.Dev), Inode: stat.Ino, Bytes: uint64(info.Size()), Links: stat.Nlink, UID: stat.Uid, Mode: uint32(info.Mode()), ModifiedNS: info.ModTime().UnixNano(), ChangedNS: stat.Ctim.Sec*1e9 + stat.Ctim.Nsec}, nil
}

func checkResumeSources(files []strictSource, before []resumeSourceMetadata) ([]resumeSourceMetadata, error) {
	if len(files) != len(before) {
		return nil, errors.New("resume source descriptor set changed")
	}
	after := make([]resumeSourceMetadata, len(files))
	for i, source := range files {
		if err := checkStrictSource(source); err != nil {
			return nil, err
		}
		fdInfo, err := source.file.Stat()
		if err != nil {
			return nil, err
		}
		fd, err := resumeSourceStat(before[i].Path, fdInfo)
		if err != nil {
			return nil, err
		}
		pathInfo, err := os.Lstat(before[i].Path)
		if err != nil {
			return nil, err
		}
		path, err := resumeSourceStat(before[i].Path, pathInfo)
		if err != nil || fd != before[i] || path != before[i] {
			return nil, errors.New("resume source path/descriptor metadata changed")
		}
		after[i] = path
	}
	return after, nil
}

func resumeSourceAccounts(genesisRaw []byte, m supplyManifest, baseline []sharedexec.CanonicalAccount) (map[types.Address]sharedexec.CanonicalAccount, error) {
	d := m.Descriptor
	if len(genesisRaw) == 0 || len(genesisRaw) > 16<<20 || types.Hash(sha256.Sum256(genesisRaw)) != d.GenesisFileHash {
		return nil, errors.New("resume source actual genesis bytes differ")
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(genesisRaw, &genesis); err != nil {
		return nil, err
	}
	if err := validateSupplyGenesis(&genesis); err != nil {
		return nil, err
	}
	block, _, err := (&internal.GenesisBlock{GenesisConfig: &genesis}).ToBlock()
	if err != nil || block == nil || block.Hash() != d.Identity.GenesisHash {
		return nil, errors.New("resume source actual genesis header differs")
	}
	if len(genesis.Alloc) != int(d.SenderCount) || len(baseline) != int(d.SenderCount) {
		return nil, errors.New("resume source needs complete genesis/baseline sender sets")
	}
	byAddress := make(map[types.Address]sharedexec.CanonicalAccount, len(baseline))
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

func resumeSourceSender(raw []byte, d sharedexec.CorpusDescriptor, signer transaction.EIP155Signer, index uint32, nonce uint64) (types.Address, error) {
	tx, err := transaction.DecodeEthereumTransaction(raw)
	if err != nil {
		return types.Address{}, err
	}
	to := supplyRecipient(uint32((uint64(index)*d.NoncesPerSender+nonce)%uint64(d.RecipientCount)), d.RecipientCount)
	if tx.Type() != transaction.LegacyTxType || !tx.Protected() || tx.ChainId() == nil || tx.ChainId().ToBig().Cmp(signer.ChainID()) != 0 || tx.Nonce() != nonce || tx.To() == nil || *tx.To() != to || tx.Gas() != 21000 || tx.Value() == nil || !tx.Value().Eq(uint256.NewInt(1)) || tx.GasPrice() == nil || !tx.GasPrice().Eq(uint256.NewInt(1)) || len(tx.Data()) != 0 {
		return types.Address{}, errors.New("resume source original EIP155 transfer recipe differs")
	}
	// Direct recovery ignores sender hints and process-wide transaction memo.
	return signer.Sender(tx)
}

// scanResumeSources hashes every complete shard before any signature recovery.
// Its ordinary output binds source contents; it does not arm an owner/session.
// No source seed, private key, chain database or network is accessed here.
func scanResumeSources(ctx context.Context, root string, m supplyManifest, genesisRaw []byte, baseline []sharedexec.CanonicalAccount) (out resumeSourceScan, err error) {
	defer func() {
		if err != nil {
			out = resumeSourceScan{}
		}
	}()
	if ctx == nil {
		return out, errors.New("resume source requires a context")
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if err = m.CorpusManifest.Validate(); err != nil {
		return out, err
	}
	d := m.Descriptor
	if m.Digest == (types.Hash{}) || d.NonceStart != 0 || d.FrameTransactions != 500 || d.NoncesPerSender < 500 {
		return out, errors.New("resume source requires bound original 500-transaction frames")
	}
	if err = strictPrivatePath(root, true, false); err != nil {
		return out, err
	}
	accounts, err := resumeSourceAccounts(genesisRaw, m, baseline)
	if err != nil {
		return out, err
	}
	before := make([]resumeSourceMetadata, len(m.Shards))
	for i, shard := range m.Shards {
		path := filepath.Join(root, shard.File)
		info, e := os.Lstat(path)
		if e != nil {
			return out, e
		}
		before[i], err = resumeSourceStat(path, info)
		if err != nil || before[i].Bytes != shard.Bytes {
			return out, errors.New("resume source initial file metadata differs")
		}
	}
	files, err := openStrictSources(ctx, root, m)
	if err != nil {
		return out, err
	}
	defer closeStrictSources(files)
	if _, err = checkResumeSources(files, before); err != nil {
		return out, err
	}
	chainID, ok := new(big.Int).SetString(d.Identity.ChainID, 10)
	if !ok {
		return out, errors.New("resume source chain ID invalid")
	}
	signer := transaction.NewEIP155Signer(chainID)
	out.Senders = make([]sharedexec.ResumeSender, d.SenderCount)
	out.Witnesses = make([]resumeSourceWitness, d.SenderCount)
	offsets := make([]int64, d.SenderCount)
	seen := make(map[types.Address]bool, d.SenderCount)
	reader := bufio.NewReaderSize(files[0].file, 8<<20)
	var scratch []byte
	for shard, source := range files {
		reader.Reset(source.file)
		var offset int64
		count := (d.SenderCount-1-uint32(shard))/uint32(d.ShardCount) + 1
		for sequence := uint64(0); sequence < uint64(count); sequence++ {
			if err = ctx.Err(); err != nil {
				return out, err
			}
			frame, reusable, e := sharedexec.ReadCorpusFrame(reader, scratch)
			scratch = reusable
			if e != nil {
				return out, e
			}
			index, nonce, ntx, e := d.FrameRange(uint16(shard), sequence)
			if e != nil || nonce != 0 || ntx != 500 || frame.Shard != uint16(shard) || frame.Sequence != sequence {
				return out, errors.New("resume source first-round frame differs")
			}
			if err = frame.Validate(d); err != nil {
				return out, err
			}
			var first []byte
			if err = sharedexec.WalkCorpusPayload(frame.Payload, frame.Transactions, func(raw []byte) error {
				if first == nil {
					first = raw
				}
				return nil
			}); err != nil {
				return out, err
			}
			address, e := resumeSourceSender(first, d, signer, index, 0)
			if e != nil {
				return out, e
			}
			account, exists := accounts[address]
			if !exists || seen[address] {
				return out, errors.New("resume source signer set is not the exact unique alloc/baseline set")
			}
			seen[address] = true
			out.MappingSignatureChecks++
			out.Senders[index] = sharedexec.ResumeSender{Index: index, Address: address, Nonce: account.Nonce}
			out.Witnesses[index] = resumeSourceWitness{SenderIndex: index, Address: address, FirstFrame: frame.CorpusFrameID, PayloadHash: frame.PayloadHash, PayloadBytes: uint32(len(frame.Payload)), FirstTransaction: types.Hash(sha256.Sum256(first))}
			offsets[index] = offset
			offset += int64(sharedexec.CorpusWireHeaderSize + len(frame.Payload))
		}
		out.FullSourceBytes += m.Shards[shard].Bytes
	}
	if len(seen) != len(accounts) {
		return out, errors.New("resume source omitted a canonical sender")
	}
	out.MappingSHA256, err = sharedexec.ResumeMappingID(out.Senders)
	if err != nil {
		return out, err
	}
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
		section := io.NewSectionReader(files[w.FirstFrame.Shard].file, offsets[index], int64(sharedexec.CorpusWireHeaderSize)+int64(w.PayloadBytes))
		frame, reusable, e := sharedexec.ReadCorpusFrame(section, scratch)
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
		err = sharedexec.WalkCorpusPayload(frame.Payload, frame.Transactions, func(raw []byte) error {
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
		out.Frames = append(out.Frames, sharedexec.ResumeFrame{ID: frame.CorpusFrameID, SenderIndex: index, PayloadHash: frame.PayloadHash, PayloadBytes: uint32(len(frame.Payload)), CanonicalSkipped: skip, NewlyAdmitted: 500 - skip})
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
