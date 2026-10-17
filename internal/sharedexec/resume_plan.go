package sharedexec

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"
	"sort"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// These offline values describe only the bounded four-frame recovery slice.
// Structural validation does not prove that an address belongs to the original
// source's logical sender index. That requires recovery from the original
// signed frames after full source hashing. Nothing here arms an owner/session,
// trusts an old ACK, or authorizes transaction dispatch.
type ResumeCanonicalAnchor struct {
	Number uint64
	Hash   types.Hash
	Root   types.Hash
}

type ResumeSender struct {
	Index   uint32
	Address types.Address
	Nonce   uint64
}

type ResumeFrame struct {
	ID               CorpusFrameID
	SenderIndex      uint32
	PayloadHash      types.Hash
	PayloadBytes     uint32
	CanonicalSkipped uint16
	NewlyAdmitted    uint16
}

// ResumePlan deliberately excludes epoch, timestamps, paths, ACKs and PlanID.
// A later session must independently verify the fixed proof/source bytes and
// the actual canonical/applied account snapshot before using a plan.
type ResumePlan struct {
	Version               uint16
	Identity              Identity
	ManifestSHA256        types.Hash
	CorpusID              types.Hash
	GenesisFileSHA256     types.Hash
	SourceProofSHA256     types.Hash
	MappingSHA256         types.Hash
	RecoveryOuterSHA256   types.Hash
	RecoverySHA256        types.Hash
	AccountVectorSHA256   types.Hash
	OriginalRestoreSHA256 types.Hash
	Canonical             ResumeCanonicalAnchor
	CanonicalNonceSum     uint64
	Senders               []ResumeSender
	Frames                []ResumeFrame
}

// ResumeMappingID binds logical order, not address-sorted vector order. Nonces
// are intentionally excluded here; the full plan ID includes every nonce.
func ResumeMappingID(senders []ResumeSender) (types.Hash, error) {
	if len(senders) == 0 || len(senders) > 12500 {
		return types.Hash{}, errors.New("resume mapping sender count invalid")
	}
	seen := make(map[types.Address]bool, len(senders))
	var out bytes.Buffer
	out.WriteString("gov5-resume-mapping-v1\x00")
	_ = binary.Write(&out, binary.LittleEndian, uint32(len(senders)))
	for i, sender := range senders {
		if sender.Index != uint32(i) || sender.Address == (types.Address{}) || seen[sender.Address] {
			return types.Hash{}, errors.New("resume mapping requires unique nonzero addresses in logical index order")
		}
		seen[sender.Address] = true
		_ = binary.Write(&out, binary.LittleEndian, sender.Index)
		out.Write(sender.Address[:])
	}
	return types.Hash(sha256.Sum256(out.Bytes())), nil
}

// ValidateResumePlanStructure checks bounded metadata and its relationship to
// an already audited account vector. It neither reads source files nor verifies
// proof reports. A consistent permutation of the public mapping can pass this
// function: the future original-signature source scanner must reject it.
func ValidateResumePlanStructure(p ResumePlan, manifestRaw []byte, baseline []CanonicalAccount) error {
	m, err := DecodeCorpusManifest(manifestRaw)
	if err != nil {
		return err
	}
	d := m.Descriptor
	if p.Version != 1 || p.Identity != d.Identity || p.ManifestSHA256 != types.Hash(sha256.Sum256(manifestRaw)) || p.CorpusID != m.CorpusID || p.GenesisFileSHA256 != d.GenesisFileHash {
		return errors.New("resume plan manifest/chain/genesis binding differs")
	}
	if d.NonceStart != 0 || d.FrameTransactions != 500 || d.NoncesPerSender < 500 || p.Canonical.Number == 0 || p.Canonical.Number == ^uint64(0) || p.Canonical.Hash == (types.Hash{}) || p.Canonical.Root == (types.Hash{}) {
		return errors.New("resume v1 requires original 500-transaction frames and a retained canonical anchor")
	}
	for _, hash := range []types.Hash{p.ManifestSHA256, p.CorpusID, p.GenesisFileSHA256, p.SourceProofSHA256, p.MappingSHA256, p.RecoveryOuterSHA256, p.RecoverySHA256, p.AccountVectorSHA256, p.OriginalRestoreSHA256} {
		if hash == (types.Hash{}) {
			return errors.New("resume plan proof binding missing")
		}
	}
	if len(p.Senders) != int(d.SenderCount) || len(baseline) != int(d.SenderCount) {
		return errors.New("resume plan requires the complete sender/account vector")
	}
	mapping, err := ResumeMappingID(p.Senders)
	if err != nil || mapping != p.MappingSHA256 {
		return errors.New("resume plan public mapping digest differs")
	}
	byAddress := make(map[types.Address]CanonicalAccount, len(baseline))
	var sum uint64
	for i, account := range baseline {
		if i > 0 && bytes.Compare(baseline[i-1].Address[:], account.Address[:]) >= 0 {
			return errors.New("resume account vector must retain strict address order")
		}
		if !account.Exists || account.Address == (types.Address{}) || account.CodeHash != crypto.Keccak256Hash(nil) || account.Nonce > d.NoncesPerSender || len(account.Balance) == 0 || len(account.Balance) > 78 {
			return errors.New("resume requires existing bounded supply EOAs")
		}
		balance, ok := new(big.Int).SetString(account.Balance, 10)
		if !ok || balance.Sign() <= 0 || balance.BitLen() > 256 || balance.String() != account.Balance {
			return errors.New("resume balance must be canonical positive uint256 decimal")
		}
		// This is the same minimum transfer recipe used by the canonical
		// recovery audit, not a substitute for validating each original tx.
		minimum := new(big.Int).Mul(new(big.Int).SetUint64(d.NoncesPerSender-account.Nonce), big.NewInt(21001))
		if balance.Cmp(minimum) < 0 || sum > ^uint64(0)-account.Nonce {
			return errors.New("resume sender funds or nonce sum invalid")
		}
		sum += account.Nonce
		byAddress[account.Address] = account
	}
	if sum != p.CanonicalNonceSum {
		return errors.New("resume canonical nonce sum differs")
	}
	// JSON is used only for the existing recovery vector's digest contract.
	// PlanID below uses fixed-width binary encoding, never JSON or map order.
	vector, err := json.Marshal(baseline)
	if err != nil || types.Hash(sha256.Sum256(vector)) != p.AccountVectorSHA256 {
		return errors.New("resume canonical account vector digest differs")
	}
	selected := map[uint64]uint32{}
	for _, sender := range p.Senders {
		account, ok := byAddress[sender.Address]
		if !ok || sender.Nonce != account.Nonce {
			return errors.New("resume mapping must join the canonical vector by address")
		}
		switch sender.Nonce {
		case 0, 41, 42, 500:
			if _, exists := selected[sender.Nonce]; !exists {
				selected[sender.Nonce] = sender.Index
			}
		}
	}
	if len(selected) != 4 || len(p.Frames) != 4 {
		return errors.New("resume v1 requires the four canonical nonce buckets")
	}
	indices := []uint32{selected[0], selected[41], selected[42], selected[500]}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	var skipped, admitted uint64
	for i, frame := range p.Frames {
		sender, nonce, count, err := d.FrameRange(frame.ID.Shard, frame.ID.Sequence)
		if err != nil || frame.ID.CorpusID != p.CorpusID || frame.SenderIndex != indices[i] || sender != frame.SenderIndex || nonce != 0 || count != 500 || frame.PayloadHash == (types.Hash{}) || frame.PayloadBytes < uint32(count)*5 || frame.PayloadBytes > CorpusMaxFramePayload {
			return errors.New("resume v1 frame whitelist/range/order invalid")
		}
		expectedSkip := p.Senders[sender].Nonce
		if expectedSkip > uint64(count) {
			expectedSkip = uint64(count)
		}
		if uint64(frame.CanonicalSkipped) != expectedSkip || uint64(frame.NewlyAdmitted) != uint64(count)-expectedSkip {
			return errors.New("resume frame must preserve the exact canonical prefix")
		}
		skipped += uint64(frame.CanonicalSkipped)
		admitted += uint64(frame.NewlyAdmitted)
	}
	if skipped != 583 || admitted != 1417 {
		return errors.New("resume v1 bounded slice counts differ")
	}
	return nil
}

// ResumePlanID hashes every offline binding in declaration order. Runtime
// epochs and receipts are separate and cannot silently change this plan.
func ResumePlanID(p ResumePlan, manifestRaw []byte, baseline []CanonicalAccount) (types.Hash, error) {
	if err := ValidateResumePlanStructure(p, manifestRaw, baseline); err != nil {
		return types.Hash{}, err
	}
	var out bytes.Buffer
	out.WriteString("gov5-resume-plan-v1\x00")
	put := func(value any) { _ = binary.Write(&out, binary.LittleEndian, value) }
	text := func(value string) { put(uint16(len(value))); out.WriteString(value) }
	put(p.Version)
	put(p.Identity.Version)
	text(p.Identity.Namespace)
	text(p.Identity.ChainID)
	out.Write(p.Identity.GenesisHash[:])
	for _, hash := range []types.Hash{p.ManifestSHA256, p.CorpusID, p.GenesisFileSHA256, p.SourceProofSHA256, p.MappingSHA256, p.RecoveryOuterSHA256, p.RecoverySHA256, p.AccountVectorSHA256, p.OriginalRestoreSHA256} {
		out.Write(hash[:])
	}
	put(p.Canonical.Number)
	out.Write(p.Canonical.Hash[:])
	out.Write(p.Canonical.Root[:])
	put(p.CanonicalNonceSum)
	put(uint32(len(p.Senders)))
	for _, sender := range p.Senders {
		put(sender.Index)
		out.Write(sender.Address[:])
		put(sender.Nonce)
	}
	put(uint16(len(p.Frames)))
	for _, frame := range p.Frames {
		out.Write(frame.ID.CorpusID[:])
		put(frame.ID.Shard)
		put(frame.ID.Sequence)
		put(frame.SenderIndex)
		out.Write(frame.PayloadHash[:])
		put(frame.PayloadBytes)
		put(frame.CanonicalSkipped)
		put(frame.NewlyAdmitted)
	}
	return types.Hash(sha256.Sum256(out.Bytes())), nil
}
